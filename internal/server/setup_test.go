package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/store"
)

// newSetupUI は、パスワードが無い UI と、セットアップコード（ファイルに書いたもの）を作る。
func newSetupUI(t *testing.T) (http.Handler, *store.Store, *SetupCode) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	code, err := NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "setup-code.txt")
	if err := os.WriteFile(file, []byte(code+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sc := &SetupCode{Code: code, File: file}
	ui := &UI{
		Sessions: st, Passwords: st, Tokens: st, Jobs: st, Setup: sc,
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   time.Now,
	}
	return Handler(Health{LastPoll: time.Now}, ui), st, sc
}

func setupBody(code, password string) string {
	b, _ := json.Marshal(map[string]string{"code": code, "password": password})
	return string(b)
}

func sessionCookie(rec interface{ Result() *http.Response }) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestNewSetupCode(t *testing.T) {
	re := regexp.MustCompile(`^[` + setupAlphabet + `]{4}-[` + setupAlphabet + `]{4}$`)
	seen := map[string]bool{}
	for range 20 {
		c, err := NewSetupCode()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(c) {
			t.Errorf("形が違う: %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 19 {
		t.Errorf("同じコードが続く: %v", seen)
	}
}

// セットアップコードを入れてパスワードを決めると、ログインした状態になり、ファイルが消え、コードは無効になる。
func TestSetupDecidesPassword(t *testing.T) {
	h, st, sc := newSetupUI(t)
	if rec := do(h, "POST", "/api/login", `{"password":"secret"}`); rec.Code != 503 {
		t.Errorf("決める前の login = %d、want 503", rec.Code)
	}
	// 小文字・区切りなし・空白入りでも通る。
	in := " " + strings.ToLower(strings.ReplaceAll(sc.Code, "-", "")) + " "
	rec := do(h, "POST", "/api/setup", setupBody(in, "secret"))
	if rec.Code != 200 {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("setup の応答に cookie が無い")
	}
	if rec := do(h, "GET", "/api/me", "", c); rec.Code != 200 {
		t.Errorf("決めた後の /api/me = %d", rec.Code)
	}
	if _, err := os.Stat(sc.File); !os.IsNotExist(err) {
		t.Errorf("セットアップコードのファイルが残る: %v", err)
	}
	hash, err := st.PasswordHash(context.Background())
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("secret")) != nil {
		t.Errorf("DB の hash = %q, %v", hash, err)
	}
	// コードは無効（もう一度は決められない）。
	if rec := do(h, "POST", "/api/setup", setupBody(sc.Code, "other")); rec.Code != 409 {
		t.Errorf("2 回目の setup = %d、want 409", rec.Code)
	}
	if rec := do(h, "POST", "/api/login", `{"password":"secret"}`); rec.Code != 200 {
		t.Errorf("決めたパスワードの login = %d", rec.Code)
	}
}

// セットアップコードを 5 回続けて間違えたら、正しいコードも受け付けない。
func TestSetupLocksAfterFailures(t *testing.T) {
	h, st, sc := newSetupUI(t)
	for i := 1; i <= SetupMaxFailures; i++ {
		rec := do(h, "POST", "/api/setup", setupBody("WRONG-CODE", "secret"))
		if rec.Code != 401 {
			t.Fatalf("%d 回目 = %d %s、want 401", i, rec.Code, rec.Body)
		}
		var body struct {
			Remaining int  `json:"remaining"`
			Locked    bool `json:"locked"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Remaining != SetupMaxFailures-i || body.Locked != (i == SetupMaxFailures) {
			t.Errorf("%d 回目の応答 = %s", i, rec.Body)
		}
	}
	rec := do(h, "POST", "/api/setup", setupBody(sc.Code, "secret"))
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), `"locked":true`) {
		t.Errorf("締めた後の正しいコード = %d %s、want 429", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/me", ""); rec.Code != 503 || !strings.Contains(rec.Body.String(), `"locked":true`) {
		t.Errorf("締めた後の /api/me = %d %s", rec.Code, rec.Body)
	}
	if hash, _ := st.PasswordHash(context.Background()); hash != "" {
		t.Errorf("パスワードが決まった: %q", hash)
	}
	if _, err := os.Stat(sc.File); err != nil {
		t.Errorf("ファイルは残るはず: %v", err)
	}
}

// 間違いの数は続けて間違えた数で、途中で正しく入れれば決まる（4 回までなら締めない）。
func TestSetupBelowLimit(t *testing.T) {
	h, _, sc := newSetupUI(t)
	for range SetupMaxFailures - 1 {
		do(h, "POST", "/api/setup", setupBody("WRONG-CODE", "secret"))
	}
	if rec := do(h, "GET", "/api/me", ""); !strings.Contains(rec.Body.String(), `"locked":false`) {
		t.Errorf("/api/me = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/setup", setupBody(sc.Code, "secret")); rec.Code != 200 {
		t.Errorf("4 回間違えた後の正しいコード = %d %s", rec.Code, rec.Body)
	}
}

// パスワードが空・長すぎる・JSON が壊れているときは 400 で、間違いには数えない。
func TestSetupBadRequest(t *testing.T) {
	h, _, sc := newSetupUI(t)
	for _, body := range []string{setupBody(sc.Code, ""), setupBody(sc.Code, strings.Repeat("a", 73)), "not json"} {
		if rec := do(h, "POST", "/api/setup", body); rec.Code != 400 {
			t.Errorf("%q = %d、want 400", body, rec.Code)
		}
	}
	if sc.Locked() || sc.failures != 0 {
		t.Errorf("間違いに数えた: %d", sc.failures)
	}
}

// セットアップコードが無い（パスワードが決まった後に起動した）ときは、決められない。
func TestSetupWithoutCode(t *testing.T) {
	h, _, _ := newUI(t, "secret")
	if rec := do(h, "POST", "/api/setup", setupBody("ABCD-EFGH", "x")); rec.Code != 409 {
		t.Errorf("setup = %d、want 409", rec.Code)
	}
}

func TestChangePassword(t *testing.T) {
	h, st, _ := newUI(t, "secret")
	if rec := do(h, "PUT", "/api/password", `{"current":"secret","password":"new"}`); rec.Code != 401 {
		t.Errorf("ログインしていない = %d、want 401", rec.Code)
	}
	c := login(t, h)
	if rec := do(h, "PUT", "/api/password", `{"current":"wrong","password":"new"}`, c); rec.Code != 403 {
		t.Errorf("今のパスワードが違う = %d、want 403", rec.Code)
	}
	if rec := do(h, "PUT", "/api/password", `{"current":"secret","password":""}`, c); rec.Code != 400 {
		t.Errorf("新しいパスワードが空 = %d、want 400", rec.Code)
	}
	if rec := do(h, "PUT", "/api/password", `{"current":"secret","password":"new"}`, c); rec.Code != 200 {
		t.Fatalf("変える = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/login", `{"password":"secret"}`); rec.Code != 401 {
		t.Errorf("前のパスワード = %d、want 401", rec.Code)
	}
	if rec := do(h, "POST", "/api/login", `{"password":"new"}`); rec.Code != 200 {
		t.Errorf("新しいパスワード = %d、want 200", rec.Code)
	}
	// 今のセッションはそのまま使える。
	if rec := do(h, "GET", "/api/me", "", c); rec.Code != 200 {
		t.Errorf("変えた後の /api/me = %d", rec.Code)
	}
	if hash, _ := st.PasswordHash(context.Background()); bcrypt.CompareHashAndPassword([]byte(hash), []byte("new")) != nil {
		t.Errorf("DB の hash が新しいパスワードでない")
	}
}
