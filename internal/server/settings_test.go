package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
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

// newSettingsUI は、設定の API と通知の API を立てる（設定は本物の app.Settings で、DB と鍵のファイルは一時フォルダ）。
func newSettingsUI(t *testing.T) (http.Handler, *app.Settings, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &app.Settings{Store: st, Secrets: secret.Load(filepath.Join(dir, config.KeyFile)),
		Build: func(c config.Config) app.Runtime { return app.Runtime{Config: c} }}
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ui := &UI{
		Sessions: st, Tokens: st, Notifications: st, Settings: s, Backups: st, Passwords: passwords(string(b)),
		// Settings があれば、通知先の種類と会員種別は設定の写しから読む（下の値は使わない）。
		NotifyChannel: "slack", Membership: domain.Membership("double"),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	return h, s, login(t, h)
}

func TestSettingsRequireLogin(t *testing.T) {
	h, _, _ := newSettingsUI(t)
	for _, method := range []string{"GET", "PATCH"} {
		if rec := do(h, method, "/api/settings", `{}`); rec.Code != 401 {
			t.Errorf("%s /api/settings = %d、want 401", method, rec.Code)
		}
	}
}

// GET は秘密の値を返さず、設定済みかどうかだけを返す。PATCH は書いた項目だけを変え、すぐに効く（通知先の種類も変わる）。
func TestSettingsGetPatch(t *testing.T) {
	h, s, cookie := newSettingsUI(t)
	var v config.View
	rec := do(h, "GET", "/api/settings", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	decode(t, rec.Body.Bytes(), &v)
	if v.LibrarySet || *v.Settings.Library != "" || *v.Settings.Notify.Channel != config.ChannelNone || v.UnreadableSecrets == nil ||
		len(v.Secrets) != len(config.SecretNames) || v.Secrets[config.SecretDiscordWebhook].Set {
		t.Fatalf("GET（何も保存していない） = %s", rec.Body)
	}

	lib := filepath.Join(t.TempDir(), "library")
	body := `{"library":` + quoteJSON(lib) + `,"keyword_max_pending":3,"notify":{"channel":"discord","discord_webhook":"https://discord.invalid/TOPSECRET"}}`
	rec = do(h, "PATCH", "/api/settings", body, cookie)
	if rec.Code != 200 {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Fatalf("答えに秘密の値が出た: %s", rec.Body)
	}
	decode(t, rec.Body.Bytes(), &v)
	if !v.LibrarySet || *v.Settings.Library != lib || *v.Settings.KeywordMaxPending != 3 || *v.Settings.MaxAttemptsFree != domain.DefaultMaxAttemptsFree ||
		!v.Secrets[config.SecretDiscordWebhook].Set || v.Secrets[config.SecretDiscordWebhook].Unreadable {
		t.Fatalf("PATCH の答え = %s", rec.Body)
	}
	if fi, err := os.Stat(lib); err != nil || !fi.IsDir() {
		t.Fatalf("保存先のフォルダを作っていない: %v", err)
	}
	if c := s.Current(); c.DiscordWebhook != "https://discord.invalid/TOPSECRET" {
		t.Fatalf("秘密を当てていない: %+v", c)
	}
	rec = do(h, "GET", "/api/settings", "", cookie)
	if strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Fatalf("GET に秘密の値が出た: %s", rec.Body)
	}
	// 通知先の種類は、起動したときの値ではなく、設定の写しから読む。
	var status notificationStatusBody
	decode(t, do(h, "GET", "/api/notifications/status", "", cookie).Body.Bytes(), &status)
	if status.Channel != config.ChannelDiscord {
		t.Fatalf("通知先の種類 = %q", status.Channel)
	}

	// 秘密の空の文字列は消す。
	rec = do(h, "PATCH", "/api/settings", `{"notify":{"channel":"none","discord_webhook":""}}`, cookie)
	if rec.Code != 200 {
		t.Fatalf("PATCH（消す） = %d %s", rec.Code, rec.Body)
	}
	decode(t, rec.Body.Bytes(), &v)
	if v.Secrets[config.SecretDiscordWebhook].Set || s.Current().DiscordWebhook != "" || *v.Settings.Library != lib {
		t.Fatalf("消した後 = %s", rec.Body)
	}
}

// 誤りが 1 つでもあれば 400 にして、何も当てない。
func TestSettingsPatchErrors(t *testing.T) {
	h, s, cookie := newSettingsUI(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ body, want string }{
		"知らないキー":         {`{"libary":"x"}`, "知らないキー"},
		"知らない入れ子のキー":     {`{"notify":{"webhook":"x"}}`, "知らないキー"},
		"JSON の誤り":       {`{`, "JSON が読めない"},
		"値の誤り":           {`{"keyword_max_pending":3,"muxer":"go"}`, "muxer"},
		"書けない保存先":        {`{"keyword_max_pending":3,"library":` + quoteJSON(filepath.Join(file, "lib")) + `}`, "保存先"},
		"会員種別":           {`{"membership":"areafree"}`, "未対応"},
		"書けないバックアップの取る先": {`{"keyword_max_pending":3,"backup_dir":` + quoteJSON(filepath.Join(file, "bk")) + `}`, "バックアップの取る先"},
		"相対のバックアップの取る先":  {`{"backup_dir":"bk"}`, "絶対パス"},
		"残す数":            {`{"backup_keep":0}`, "backup_keep"},
		"channel の書き忘れ":  {`{"notify":{"discord_webhook":"https://discord.invalid/TOPSECRET"}}`, "notify.channel を書いて"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(h, "PATCH", "/api/settings", c.body, cookie)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.want) || strings.Contains(rec.Body.String(), "TOPSECRET") {
				t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
			}
			if cur := s.Current(); cur.KeywordMaxPending != domain.DefaultKeywordMaxPending || cur.DiscordWebhook != "" || cur.Library != "" || cur.BackupDir != "" || cur.BackupKeep != config.DefaultBackupKeep {
				t.Fatalf("誤りがあるのに当てた: %+v", cur)
			}
		})
	}
}

func quoteJSON(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
