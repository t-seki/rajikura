package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/server"
	"github.com/t-seki/rajikura/internal/store"
)

// openBackup は、バックアップのファイルを DB として開く。
func openBackup(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("バックアップ %s を開けない: %v", path, err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// backup -o は写しを書く。-o を省くと、今のフォルダに rajikura-<日時>.db で書く。一時のファイルは残さない。
func TestBackupCommand(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	r := domain.Show{Name: "テスト番組", CheckTitle: true,
		Weekly: []domain.Weekly{domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: 20})}}
	if err := api.Store.PutShow(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.Store.CreateSession(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := api.run(t, "backup", "x"); code != 2 || !strings.Contains(stderr, "使い方: rajikura backup [-o <file>]") {
		t.Errorf("backup の引数が多い: %d %q", code, stderr)
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "backup.db")
	code, stdout, stderr := api.run(t, "backup", "-o", out)
	if code != 0 || !strings.Contains(stdout, "バックアップを "+out+" に書いた") {
		t.Fatalf("backup -o: %d %q %q", code, stdout, stderr)
	}
	// 書き先にファイルがあれば置き換える。
	if code, _, stderr := api.run(t, "backup", "-o", out); code != 0 {
		t.Fatalf("backup -o（2 回目）: %d %q", code, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("書き先のフォルダのファイル = %v、want backup.db だけ", entries)
	}
	st := openBackup(t, out)
	if rs, err := st.ListShows(ctx); err != nil || len(rs) != 1 || rs[0].Name != "テスト番組" {
		t.Errorf("バックアップの番組 = %+v, %v", rs, err)
	}

	// -o を省くと、今のフォルダに日本時間の日時の名前で書く。
	cwd := t.TempDir()
	t.Chdir(cwd)
	old := backupNow
	backupNow = func() time.Time { return time.Date(2026, 10, 18, 3, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { backupNow = old })
	if code, stdout, stderr := api.run(t, "backup"); code != 0 || !strings.Contains(stdout, "rajikura-20261018-120405.db") {
		t.Fatalf("backup: %d %q %q", code, stdout, stderr)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 1 || entries[0].Name() != "rajikura-20261018-120405.db" {
		t.Fatalf("今のフォルダのファイル = %v, %v", entries, err)
	}
	openBackup(t, filepath.Join(cwd, "rajikura-20261018-120405.db"))
}

// serve が断ったら、ファイルを残さずに終わる。
func TestBackupCommandRejected(t *testing.T) {
	api := newTestAPI(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "backup.db")
	code, _, stderr := runCmd(t, context.Background(), "-url", api.URL, "-token", "rjk_wrong", "backup", "-o", out)
	if code != 1 || !strings.Contains(stderr, "API トークンを受け付けない") {
		t.Errorf("トークンが違う backup: %d %q", code, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("断られたのにファイルが残った: %v", entries)
	}
}

// loginCookie は serve にパスワード secret でログインし、セッションの cookie を返す。
func loginCookie(t *testing.T, url string) *http.Cookie {
	t.Helper()
	resp, err := http.Post(url+"/api/login", "application/json", strings.NewReader(`{"password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == server.SessionCookie {
			return c
		}
	}
	t.Fatalf("ログインの cookie が無い（%d）", resp.StatusCode)
	return nil
}

// meStatus は、cookie で GET /api/me を呼んだときの状態。
func meStatus(t *testing.T, url string, c *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url+"/api/me", nil)
	req.Header.Set("Cookie", c.Name+"="+c.Value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// 動いている serve から backup で取った写しを、別のデータのフォルダに rajikura.db として置くと、serve が起動し、番組・設定・
// パスワード・API トークンが戻る。ログインのセッションは戻らない（ログインし直す）。秘密は、鍵のファイルも置けば戻り、
// 置かなければ復号できない（入れ直す）。
func TestServeFromBackup(t *testing.T) {
	isolateClient(t)
	day := domain.BroadcastDayOf(time.Now()).AddDays(3)
	useFakeRadiko(t, fakeGuide(t, day))
	ctx := context.Background()

	data := filepath.Join(t.TempDir(), "data")
	addr, logs, stop := startServe(t, "-data", data, "serve", "-listen", "127.0.0.1:0")
	url := "http://" + addr
	setupPassword(t, url, data, logs())
	cookie := loginCookie(t, url)
	if s := meStatus(t, url, cookie); s != http.StatusOK {
		t.Fatalf("ログインした後の GET /api/me = %d", s)
	}
	st, err := store.Open(ctx, filepath.Join(data, config.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateAPIToken(ctx, "テスト", time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	cli := func(url string, args ...string) (int, string, string) {
		return runCmd(t, ctx, append([]string{"-url", url, "-token", token}, args...)...)
	}

	lib := filepath.Join(t.TempDir(), "library")
	libJSON, _ := json.Marshal(lib)
	settings := `{"library":` + string(libJSON) + `,"keyword_max_pending":3,"notify":{"channel":"discord","discord_webhook":"https://discord.invalid/api/webhooks/1/TOPSECRET"}}`
	if code, body := callAPI(t, url, token, http.MethodPatch, "/api/settings", settings, nil); code != http.StatusOK {
		t.Fatalf("PATCH /api/settings: %d %s", code, body)
	}
	show := fmt.Sprintf(`{"name":"テスト番組","weekly":[{"station":"TBS","weekday":%d,"start":"20:00"}]}`, int(day.Weekday()))
	if code, body := callAPI(t, url, token, http.MethodPost, "/api/shows", show, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/shows: %d %s", code, body)
	}

	// serve を止めずに取る。
	backup := filepath.Join(t.TempDir(), "backup.db")
	if code, out, stderr := cli(url, "backup", "-o", backup); code != 0 || !strings.Contains(out, "バックアップを") {
		t.Fatalf("backup: %d %q %q", code, out, stderr)
	}
	if code := stop(); code != 0 {
		t.Fatalf("serve の終了コード = %d: %s", code, logs())
	}
	key, err := os.ReadFile(filepath.Join(data, config.KeyFile))
	if err != nil {
		t.Fatal(err)
	}

	restore := func(t *testing.T, withKey bool) (url string, stop func() int) {
		t.Helper()
		dir := t.TempDir()
		b, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.DBFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if withKey {
			if err := os.WriteFile(filepath.Join(dir, config.KeyFile), key, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		addr, logs, stop := startServe(t, "serve", "-data", dir, "-listen", "127.0.0.1:0")
		// パスワードは戻るので、セットアップコードは作らない。
		if _, err := os.Stat(filepath.Join(dir, SetupCodeFile)); !os.IsNotExist(err) {
			t.Errorf("戻した DB で setup-code.txt ができた: %v（%s）", err, logs())
		}
		return "http://" + addr, stop
	}

	for _, withKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("鍵を置く=%v", withKey), func(t *testing.T) {
			url, stop := restore(t, withKey)
			// ログインのセッションは戻らない。パスワードは戻るので、ログインし直せる。
			if s := meStatus(t, url, cookie); s != http.StatusUnauthorized {
				t.Errorf("戻した後の前のセッションで GET /api/me = %d、want 401", s)
			}
			if s := meStatus(t, url, loginCookie(t, url)); s != http.StatusOK {
				t.Errorf("ログインし直した後の GET /api/me = %d", s)
			}
			// 番組と設定は戻る（API トークンも戻るので、同じトークンで呼べる）。
			var shows []struct {
				Name   string `json:"name"`
				Weekly []struct {
					Start string `json:"start"`
				} `json:"weekly"`
			}
			code, body := callAPI(t, url, token, http.MethodGet, "/api/shows", "", &shows)
			if code != http.StatusOK || len(shows) != 1 || shows[0].Name != "テスト番組" ||
				len(shows[0].Weekly) != 1 || shows[0].Weekly[0].Start != "20:00" {
				t.Errorf("戻した後の GET /api/shows: %d %s", code, body)
			}
			var v config.View
			code, body = callAPI(t, url, token, http.MethodGet, "/api/settings", "", &v)
			secret := v.Secrets[config.SecretDiscordWebhook]
			// 鍵を置かなければ、秘密は DB にあるが復号できない。置けば復号できる。
			if code != http.StatusOK || v.Settings.Library == nil || *v.Settings.Library != lib ||
				v.Settings.KeywordMaxPending == nil || *v.Settings.KeywordMaxPending != 3 ||
				v.Settings.Notify == nil || *v.Settings.Notify.Channel != config.ChannelDiscord ||
				!secret.Set || secret.Unreadable == withKey {
				t.Errorf("戻した後の GET /api/settings: %d %s", code, body)
			}
			if code := stop(); code != 0 {
				t.Fatalf("戻した serve の終了コード = %d", code)
			}
		})
	}
}
