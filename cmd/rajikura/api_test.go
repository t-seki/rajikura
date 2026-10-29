package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/t-seki/rajikura/internal/client"
	"github.com/t-seki/rajikura/internal/server"
	"github.com/t-seki/rajikura/internal/store"
)

// isolateClient は、CLI が手元の環境変数と設定ファイル（client.toml）を読まないようにする。
// 設定ファイルを置くフォルダの親を一時フォルダにして返す。
func isolateClient(t *testing.T) string {
	t.Helper()
	t.Setenv(client.EnvURL, "")
	t.Setenv(client.EnvToken, "")
	dir := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = old })
	return dir
}

// testAPI は serve の HTTP API の代わり（一時の DB と、本物の server.UI）。展開はしない。
type testAPI struct {
	URL   string
	Token string
	Store *store.Store
	// DB は DB のファイルのパス（store を通さずに値を書き換えるテストが使う）。
	DB string
}

// newTestAPI は API を立て、API トークンを発行する。CLI の環境は isolateClient で切り離す。
func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	isolateClient(t)
	db := filepath.Join(t.TempDir(), "rajikura.db")
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	setStorePassword(t, st, "secret")
	ui := &server.UI{
		Sessions: st, Passwords: st, Shows: st, Onces: st, Keywords: st, Jobs: st, Tokens: st, Backups: st,
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
	}
	srv := httptest.NewServer(server.Handler(server.Health{LastPoll: time.Now}, ui))
	t.Cleanup(srv.Close)
	_, token, err := st.CreateAPIToken(context.Background(), "テスト", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &testAPI{URL: srv.URL, Token: token, Store: st, DB: db}
}

// run は CLI を -url と -token 付きで流す。
func (a *testAPI) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runCmd(t, context.Background(), append([]string{"-url", a.URL, "-token", a.Token}, args...)...)
}

// callAPI は serve の API を API トークンで呼び（body は JSON。空なら送らない）、状態と答えの本文を返す。
// out が nil でなく、答えが 2xx なら、答えを out に読む。
func callAPI(t *testing.T, url, token, method, path, body string, out any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s の答えを読めない: %v（%s）", method, path, err, b)
		}
	}
	return resp.StatusCode, string(b)
}
