package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

func TestBackupFileName(t *testing.T) {
	// 日本時間で名前を付ける（UTC の 2026-10-18 15:04:05 は日本時間の 10-19 00:04:05）。
	got := BackupFileName(time.Date(2026, 10, 18, 15, 4, 5, 0, time.UTC))
	if got != "rajikura-20261019-000405.db" {
		t.Errorf("BackupFileName = %q", got)
	}
	if !regexp.MustCompile(`^rajikura-\d{8}-\d{6}\.db$`).MatchString(BackupFileName(time.Now())) {
		t.Errorf("BackupFileName(now) = %q", BackupFileName(time.Now()))
	}
}

// バックアップは番組・設定・API トークンを含み、ログインのセッションを含まない。写しは 1 つのファイルで開ける。
func TestBackup(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	r := domain.Show{Name: "テスト番組", CheckTitle: true,
		Weekly: []domain.Weekly{domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: 20})}}
	if err := s.PutShow(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, `{"library":"/library"}`, map[string][]byte{"notify.discord_webhook": []byte("暗号化した値")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasswordHash(ctx, "$2a$10$hash"); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateAPIToken(ctx, "テスト", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := s.CreateSession(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	// 写しの横に WAL などのファイルは残らない。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("写しのフォルダのファイル = %v、want backup.db だけ", entries)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("SQLite format 3\x00")) {
		t.Fatalf("写しが SQLite のファイルでない: %q", b[:16])
	}
	if bytes.Contains(b, []byte(session)) {
		t.Error("写しのファイルにログインのセッションの ID が残っている")
	}

	c, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rs, err := c.ListShows(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "テスト番組" || len(rs[0].Weekly) != 1 {
		t.Errorf("写しの番組 = %+v, %v", rs, err)
	}
	body, secrets, err := c.Settings(ctx)
	if err != nil || body != `{"library":"/library"}` || string(secrets["notify.discord_webhook"]) != "暗号化した値" {
		t.Errorf("写しの設定 = %q %q, %v", body, secrets, err)
	}
	if hash, err := c.PasswordHash(ctx); err != nil || hash != "$2a$10$hash" {
		t.Errorf("写しのパスワードの hash = %q, %v", hash, err)
	}
	if ok, err := c.APITokenValid(ctx, token, time.Now()); err != nil || !ok {
		t.Errorf("写しで API トークンが使えない: %v %v", ok, err)
	}
	if ok, err := c.SessionValid(ctx, session, time.Now()); err != nil || ok {
		t.Errorf("写しでログインのセッションが有効: %v %v", ok, err)
	}
	var n int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("写しの sessions の行 = %d, %v", n, err)
	}
	// 元の DB のセッションは消えない。
	if ok, err := s.SessionValid(ctx, session, time.Now()); err != nil || !ok {
		t.Errorf("元の DB のセッションが無効になった: %v %v", ok, err)
	}
}

// 書き先にファイルがあれば、上書きせずにエラーにする。
func TestBackupExistingFile(t *testing.T) {
	s, _ := openTemp(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(path, []byte("前のファイル"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(context.Background(), path); err == nil {
		t.Fatal("書き先があるのにエラーにならない")
	}
	if b, _ := os.ReadFile(path); string(b) != "前のファイル" {
		t.Errorf("書き先が変わった: %q", b)
	}
}
