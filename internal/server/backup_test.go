package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/secret"
	"github.com/t-seki/rajikura/internal/store"
)

// GET /api/backup は VACUUM INTO で作った写しを返す。写しは番組・設定を含み、sessions の表が空で、秘密の鍵を含まない。
// 一時のフォルダに置いた写しは、返した後に消える。
func TestBackupAPI(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	keyPath := filepath.Join(dir, config.KeyFile)
	st, err := store.Open(ctx, filepath.Join(dir, "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &app.Settings{Store: st, Secrets: secret.Load(keyPath), Build: func(c config.Config) app.Runtime { return app.Runtime{Config: c} }}
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, config.Patch{Notify: &config.NotifyPatch{Channel: sp(config.ChannelDiscord), DiscordWebhook: sp("https://discord.invalid/TOPSECRET")}}); err != nil {
		t.Fatal(err)
	}
	r := domain.Show{Name: "テスト番組", CheckTitle: true,
		Weekly: []domain.Weekly{domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: 20})}}
	if err := st.PutShow(ctx, r); err != nil {
		t.Fatal(err)
	}
	b, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	ui := &UI{
		Sessions: st, Tokens: st, Settings: s, Backups: st, BackupTempDir: tmp, Passwords: passwords(string(b)),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return time.Date(2026, 10, 18, 3, 4, 5, 0, time.UTC) },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)

	if rec := do(h, "GET", "/api/backup", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("ログインせずに GET /api/backup = %d、want 401", rec.Code)
	}
	cookie := login(t, h)
	rec := do(h, "GET", "/api/backup", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/backup = %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="rajikura-20261018-120405.db"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.sqlite3" {
		t.Errorf("Content-Type = %q", got)
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("SQLite format 3\x00")) {
		t.Fatalf("答えが SQLite のファイルでない")
	}
	if bytes.Contains(body, []byte(cookie.Value)) {
		t.Error("写しにログインのセッションの ID が入っている")
	}
	// 鍵を含まない（鍵のファイルの中身も、鍵そのものも写しに無い）。秘密は暗号化したまま入る。
	keyText, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyText)))
	if err != nil || len(key) == 0 {
		t.Fatalf("鍵のファイルを読めない: %v", err)
	}
	if bytes.Contains(body, bytes.TrimSpace(keyText)) || bytes.Contains(body, key) || bytes.Contains(body, []byte("TOPSECRET")) {
		t.Error("写しに鍵か、平文の秘密が入っている")
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Errorf("一時のフォルダに写しが残った: %v %v", entries, err)
	}

	path := filepath.Join(t.TempDir(), "rajikura.db")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if rs, err := c.ListShows(ctx); err != nil || len(rs) != 1 || rs[0].Name != "テスト番組" {
		t.Errorf("写しの番組 = %+v, %v", rs, err)
	}
	if ok, err := c.SessionValid(ctx, cookie.Value, time.Now()); err != nil || ok {
		t.Errorf("写しでログインのセッションが有効: %v %v", ok, err)
	}
	if settingsBody, secrets, err := c.Settings(ctx); err != nil || !strings.Contains(settingsBody, "discord") || len(secrets[config.SecretDiscordWebhook]) == 0 {
		t.Errorf("写しの設定 = %q %v, %v", settingsBody, secrets, err)
	}
	if !regexp.MustCompile(`^rajikura-\d{8}-\d{6}\.db$`).MatchString(store.BackupFileName(time.Now())) {
		t.Error("BackupFileName の形が違う")
	}
}

type failingBackups struct{ path string }

func (f *failingBackups) Backup(ctx context.Context, path string) error {
	f.path = path
	os.WriteFile(path, []byte("作りかけ"), 0o600)
	return errors.New("ディスクがいっぱい")
}

// 写しを作れなければ 500 を返し、一時のフォルダを残さない。
func TestBackupAPIFails(t *testing.T) {
	b, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tmp := t.TempDir()
	fb := &failingBackups{}
	ui := &UI{Sessions: st, Backups: fb, BackupTempDir: tmp, Passwords: passwords(string(b)), Files: fstest.MapFS{"index.html": {Data: []byte("x")}}}
	h := Handler(Health{LastPoll: time.Now}, ui)
	rec := do(h, "GET", "/api/backup", "", login(t, h))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "内部エラー") {
		t.Errorf("GET /api/backup = %d %s、want 500", rec.Code, rec.Body)
	}
	if fb.path == "" {
		t.Fatal("Backup が呼ばれていない")
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Errorf("一時のフォルダが残った: %v %v", entries, err)
	}
}

func sp(s string) *string { return &s }

func (f *failingBackups) LastBackupAt(ctx context.Context) (time.Time, error) {
	return time.Time{}, nil
}
