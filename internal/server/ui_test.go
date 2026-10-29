package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/store"
)

func newUI(t *testing.T, password string) (http.Handler, *store.Store, *time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if password != "" {
		b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetPasswordHash(context.Background(), string(b)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ui := &UI{
		Sessions:  st,
		Passwords: st,
		Tokens:    st,
		Version:   "v9.9.9",
		Files: fstest.MapFS{
			"index.html":    {Data: []byte("<p>index</p>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
		Now: func() time.Time { return now },
	}
	return Handler(Health{LastPoll: time.Now}, ui), st, &now
}

func do(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	// httptest の既定の Host（example.com）は Host の検査で断られるので、ブラウザで localhost を開いた形にする。
	req.Host = "localhost:8080"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := do(h, "POST", "/api/login", `{"password":"secret"}`)
	if rec.Code != 200 {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatal("cookie が無い")
	return nil
}

func TestLoginLogout(t *testing.T) {
	h, _, now := newUI(t, "secret")
	if rec := do(h, "GET", "/api/me", ""); rec.Code != 401 {
		t.Errorf("セッション無しの /api/me = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/login", `{"password":"wrong"}`); rec.Code != 401 {
		t.Errorf("違うパスワード = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/login", `not json`); rec.Code != 400 {
		t.Errorf("壊れた JSON = %d", rec.Code)
	}

	c := login(t, h)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie の属性 = %+v", c)
	}
	if want := now.Add(30 * 24 * time.Hour); c.Expires.Sub(want).Abs() > time.Second || c.MaxAge != 30*24*3600 {
		t.Errorf("期限 = %v / MaxAge %d、want 30 日", c.Expires, c.MaxAge)
	}
	if rec := do(h, "GET", "/api/me", "", c); rec.Code != 200 {
		t.Errorf("ログイン後の /api/me = %d", rec.Code)
	} else if got := rec.Body.String(); !strings.Contains(got, `"version":"v9.9.9"`) {
		t.Errorf("/api/me の応答に version が無い: %s", got)
	}
	if rec := do(h, "GET", "/api/unknown", "", c); rec.Code != 404 {
		t.Errorf("知らない /api/ = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/unknown", ""); rec.Code != 401 {
		t.Errorf("セッション無しの知らない /api/ = %d", rec.Code)
	}

	rec := do(h, "POST", "/api/logout", "", c)
	if rec.Code != 200 {
		t.Fatalf("logout = %d", rec.Code)
	}
	var cleared bool
	for _, cc := range rec.Result().Cookies() {
		if cc.Name == SessionCookie && cc.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout が cookie を消さない")
	}
	if rec := do(h, "GET", "/api/me", "", c); rec.Code != 401 {
		t.Errorf("logout 後の /api/me = %d", rec.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	h, st, now := newUI(t, "secret")
	c := login(t, h)
	*now = now.Add(30*24*time.Hour - time.Minute)
	if rec := do(h, "GET", "/api/me", "", c); rec.Code != 200 {
		t.Errorf("期限の直前 = %d", rec.Code)
	}
	*now = now.Add(2 * time.Minute)
	if rec := do(h, "GET", "/api/me", "", c); rec.Code != 401 {
		t.Errorf("期限切れ = %d", rec.Code)
	}
	// 期限切れは DB からも消える（時刻を戻しても有効に戻らない）。
	if ok, err := st.SessionValid(context.Background(), c.Value, time.Now()); err != nil || ok {
		t.Errorf("期限切れのセッションが残る: %v %v", ok, err)
	}
}

func TestBrokenPasswordHash(t *testing.T) {
	var buf bytes.Buffer
	ui := &UI{Passwords: passwords("not-a-bcrypt-hash"), Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Log: slog.New(slog.NewTextHandler(&buf, nil))}
	h := Handler(Health{LastPoll: time.Now}, ui)
	if !strings.Contains(buf.String(), "bcrypt の hash として読めない") {
		t.Errorf("警告のログが無い: %q", buf.String())
	}
	if rec := do(h, "POST", "/api/login", `{"password":"x"}`); rec.Code != 401 {
		t.Errorf("login = %d、want 401", rec.Code)
	}
}

// パスワードが無い間は、SPA と /healthz は返し、/api/ は 503（中身に setup: true）を返す。API トークンがあっても通さない。
func TestNoPasswordHash(t *testing.T) {
	h, st, _ := newUI(t, "")
	_, token, err := st.CreateAPIToken(context.Background(), "cli", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ method, path string }{
		{"GET", "/api/me"}, {"POST", "/api/login"}, {"POST", "/api/logout"}, {"GET", "/api/unknown"}, {"PUT", "/api/password"},
	} {
		rec := do(h, p.method, p.path, `{"password":""}`)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"setup":true`) {
			t.Errorf("%s %s = %d %s、want 503 と setup", p.method, p.path, rec.Code, rec.Body)
		}
		if rec := doBearer(h, p.method, p.path, `{"password":""}`, token); rec.Code != 503 {
			t.Errorf("トークン付きの %s %s = %d、want 503", p.method, p.path, rec.Code)
		}
	}
	for _, p := range []string{"/", "/shows", "/healthz"} {
		if rec := do(h, "GET", p, ""); rec.Code != 200 {
			t.Errorf("GET %s = %d、want 200", p, rec.Code)
		}
	}
}

// passwords はテスト用の、メモリに置くパスワードの hash。
func passwords(hash string) Passwords { return &memPasswords{hash: hash} }

type memPasswords struct {
	mu   sync.Mutex
	hash string
}

func (m *memPasswords) PasswordHash(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hash, nil
}

func (m *memPasswords) SetPasswordHash(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hash = hash
	return nil
}

func TestSPA(t *testing.T) {
	h, _, _ := newUI(t, "secret")
	for _, p := range []string{"/", "/shows", "/shows/1/edit"} {
		rec := do(h, "GET", p, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<p>index</p>") {
			t.Errorf("GET %s = %d %q", p, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "GET", "/assets/app.js", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("assets = %d %q", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/", ""); rec.Code != 405 {
		t.Errorf("POST / = %d", rec.Code)
	}
	if rec := do(h, "GET", "/healthz", ""); rec.Code != 200 {
		t.Errorf("/healthz = %d", rec.Code)
	}
}
