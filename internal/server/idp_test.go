package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/secret"
	"github.com/t-seki/rajikura/internal/store"
)

// 偽の IdP（discovery・JWKS・token の口を持つ httptest のサーバー）と、テストで作る鍵で署名した ID トークンで、
// IdP のログインを確かめる。外のネットワークには出ない。認可の画面（authorization_endpoint）はテストがブラウザと IdP の代わりに演じる。

// fakeIdP は偽の IdP。
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu sync.Mutex
	// codes は、発行した code → 払い出す ID トークンの中身と PKCE の challenge。
	codes map[string]fakeCode
	// tokenRequests は token の口に来た要求（client の認証の形を確かめる）。
	tokenRequests []*http.Request
	tokenForms    []url.Values
}

type fakeCode struct {
	claims    map[string]any
	challenge string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, codes: map[string]fakeCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := f.srv.URL
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss,
			"authorization_endpoint":                iss + "/authorize",
			"token_endpoint":                        iss + "/token",
			"jwks_uri":                              iss + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.tokenRequests = append(f.tokenRequests, r.Clone(context.Background()))
		f.tokenForms = append(f.tokenForms, r.PostForm)
		c, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": f.sign(t, c.claims),
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) issuer() string { return f.srv.URL }

func (f *fakeIdP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// authorize は、IdP の認可の画面で利用者 sub がログインしたことにして、code を発行する。authURL は rajikura が返した IdP へ移る URL。
// tweak は ID トークンの中身を変える（nil なら変えない）。返すのは rajikura の戻り先へ付ける code と state。
func (f *fakeIdP) authorize(t *testing.T, authURL, sub string, tweak func(map[string]any)) (code, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authURL, f.srv.URL+"/authorize?") {
		t.Fatalf("IdP へ移る URL = %s", authURL)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("PKCE が無い: %s", authURL)
	}
	if q.Get("scope") != "openid profile email" {
		t.Errorf("scope = %q、want openid profile email", q.Get("scope"))
	}
	if q.Get("redirect_uri") != testWebURL+"/api/idp/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	now := time.Now()
	claims := map[string]any{
		"iss": f.issuer(), "sub": sub, "aud": q.Get("client_id"), "nonce": q.Get("nonce"),
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "email": sub + "@example.com", "preferred_username": sub + "-name",
	}
	if tweak != nil {
		tweak(claims)
	}
	code = "code-" + q.Get("state")
	f.mu.Lock()
	f.codes[code] = fakeCode{claims: claims, challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	return code, q.Get("state")
}

const (
	testWebHost = "rajikura.test"
	testWebURL  = "https://" + testWebHost
)

// idpEnv は IdP のログインを試す UI。
type idpEnv struct {
	h     http.Handler
	st    *store.Store
	idp   *fakeIdP
	token string
	logs  *bytes.Buffer
}

func newIdPEnv(t *testing.T) *idpEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPasswordHash(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateAPIToken(context.Background(), "cli", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeIdP(t)
	logs := &bytes.Buffer{}
	ui := &UI{
		Sessions: st, Passwords: st, Tokens: st,
		IdP: st, IdPSecrets: secret.Load(filepath.Join(dir, "rajikura.key")), IdPClient: f.srv.Client(),
		Files:        fstest.MapFS{"index.html": {Data: []byte("<p>index</p>")}},
		Log:          slog.New(slog.NewTextHandler(logs, nil)),
		AllowedHosts: []string{testWebHost},
	}
	return &idpEnv{h: Handler(Health{LastPoll: time.Now}, ui), st: st, idp: f, token: token, logs: logs}
}

// req は host を Host にして要求を送る（ブラウザが同じオリジンから送った形）。
func (e *idpEnv) req(method, path, host, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *idpEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := e.req("POST", "/api/login", testWebHost, `{"password":"secret"}`)
	if c := sessionCookie(rec); rec.Code == 200 && c != nil {
		return c
	}
	t.Fatalf("login = %d %s", rec.Code, rec.Body)
	return nil
}

func (e *idpEnv) configure(t *testing.T, sess *http.Cookie, body string) {
	t.Helper()
	if rec := e.req("PUT", "/api/idp", testWebHost, body, sess); rec.Code != 200 {
		t.Fatalf("PUT /api/idp = %d %s", rec.Code, rec.Body)
	}
}

func (e *idpEnv) defaultConfig() string {
	return `{"issuer":"` + e.idp.issuer() + `","client_id":"rajikura","web_url":"` + testWebURL + `/","client_secret":"s3cret"}`
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}

// callback は、IdP から戻ってきた top-level の移動（ほかのサイトからの GET）を送る。
func (e *idpEnv) callback(code, state string, stateCookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/idp/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), nil)
	r.Host = testWebHost
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	if stateCookie != nil {
		r.AddCookie(stateCookie)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// link は sub を紐づける（パスワードでログインした画面から）。
func (e *idpEnv) link(t *testing.T, sess *http.Cookie, sub string) {
	t.Helper()
	e.linkTweak(t, sess, sub, nil)
}

// linkTweak は link で、ID トークンの中身を tweak で変える。
func (e *idpEnv) linkTweak(t *testing.T, sess *http.Cookie, sub string, tweak func(map[string]any)) {
	t.Helper()
	rec := e.req("POST", "/api/idp/link", testWebHost, `{"password":"secret"}`, sess)
	if rec.Code != 200 {
		t.Fatalf("POST /api/idp/link = %d %s", rec.Code, rec.Body)
	}
	var resp struct{ URL string }
	json.Unmarshal(rec.Body.Bytes(), &resp)
	code, state := e.idp.authorize(t, resp.URL, sub, tweak)
	cb := e.callback(code, state, cookieNamed(rec, idpStateCookie))
	if cb.Code != http.StatusFound || cb.Header().Get("Location") != "/settings?idp_linked=1" {
		t.Fatalf("紐づけの戻り = %d %s", cb.Code, cb.Header().Get("Location"))
	}
}

// startLogin は IdP のログインを始め、IdP へ移る URL と state の cookie を返す。
func (e *idpEnv) startLogin(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	rec := e.req("GET", "/api/idp/login", testWebHost, "")
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /api/idp/login = %d %s", rec.Code, rec.Body)
	}
	c := cookieNamed(rec, idpStateCookie)
	if c == nil {
		t.Fatal("state の cookie が無い")
	}
	if c.SameSite != http.SameSiteLaxMode || !c.HttpOnly || !c.Secure {
		t.Errorf("state の cookie = %+v、want Lax・HttpOnly・Secure", c)
	}
	return rec.Header().Get("Location"), c
}

func (e *idpEnv) binding(t *testing.T) string {
	t.Helper()
	b, err := e.st.IdPBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 紐づけた iss + sub の利用者は、IdP のログインでセッションを得られる。
func TestIdPLoginBoundUser(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	e.link(t, sess, "alice")
	if b := e.binding(t); !strings.Contains(b, `"subject":"alice"`) || !strings.Contains(b, `"issuer":"`+e.idp.issuer()+`"`) {
		t.Fatalf("紐づけ = %s", b)
	}
	if b := e.binding(t); !strings.Contains(b, `"preferred_username":"alice-name"`) || !strings.Contains(b, `"email":"alice@example.com"`) {
		t.Fatalf("紐づけに preferred_username と email が入っていない: %s", b)
	}
	if rec := e.req("GET", "/api/idp", testWebHost, "", sess); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"preferred_username":"alice-name"`) {
		t.Errorf("GET /api/idp = %d %s", rec.Code, rec.Body)
	}

	// ログインの画面は IdP のログインを出す。
	rec := e.req("GET", "/api/idp/login-option", testWebHost, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"linked":true`) || !strings.Contains(rec.Body.String(), `"issuer_host":"127.0.0.1"`) {
		t.Errorf("login-option = %d %s", rec.Code, rec.Body)
	}

	authURL, sc := e.startLogin(t)
	code, state := e.idp.authorize(t, authURL, "alice", nil)
	cb := e.callback(code, state, sc)
	if cb.Code != http.StatusFound || cb.Header().Get("Location") != "/" {
		t.Fatalf("戻り = %d %s %s", cb.Code, cb.Header().Get("Location"), e.logs)
	}
	got := sessionCookie(cb)
	if got == nil {
		t.Fatal("セッションの cookie が無い")
	}
	if got.SameSite != http.SameSiteStrictMode || !got.HttpOnly || !got.Secure || got.MaxAge != int(store.SessionTTL/time.Second) {
		t.Errorf("セッションの cookie = %+v（パスワードのログインと同じ形のはず）", got)
	}
	if rec := e.req("GET", "/api/me", testWebHost, "", got); rec.Code != 200 {
		t.Errorf("IdP のログインのセッションで /api/me = %d", rec.Code)
	}
	// client secret を設定したので、token の口には secret を付けて送る。
	e.idp.mu.Lock()
	r := e.idp.tokenRequests[len(e.idp.tokenRequests)-1]
	e.idp.mu.Unlock()
	if id, pw, ok := r.BasicAuth(); !ok || id != "rajikura" || pw != "s3cret" {
		t.Errorf("token の口の client の認証 = %q %q %v", id, pw, ok)
	}
	// 同じ state はもう使えない。
	if cb := e.callback(code, state, sc); cb.Header().Get("Location") != "/?idp_error=expired" {
		t.Errorf("2 回目の戻り = %s", cb.Header().Get("Location"))
	}
}

// 違う sub・違う iss・state の食い違い・nonce の食い違いは断られ、セッションは作らない。
func TestIdPLoginRejected(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	e.link(t, sess, "alice")

	for _, tc := range []struct {
		name  string
		sub   string
		tweak func(map[string]any)
		// badState なら、戻り先の state を cookie と違うものにする。
		badState bool
		want     string
	}{
		{name: "違う sub", sub: "mallory", want: "/?idp_error=mismatch"},
		{name: "違う iss", sub: "alice", tweak: func(c map[string]any) { c["iss"] = "https://evil.example" }, want: "/?idp_error=failed"},
		{name: "nonce の食い違い", sub: "alice", tweak: func(c map[string]any) { c["nonce"] = "other" }, want: "/?idp_error=failed"},
		{name: "state の食い違い", sub: "alice", badState: true, want: "/?idp_error=expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authURL, sc := e.startLogin(t)
			code, state := e.idp.authorize(t, authURL, tc.sub, tc.tweak)
			if tc.badState {
				state = "forged"
			}
			cb := e.callback(code, state, sc)
			if cb.Code != http.StatusFound || cb.Header().Get("Location") != tc.want {
				t.Errorf("戻り = %d %s、want %s", cb.Code, cb.Header().Get("Location"), tc.want)
			}
			if sessionCookie(cb) != nil {
				t.Error("断ったのにセッションの cookie を付けた")
			}
		})
	}
	// state の cookie が無い戻り（ほかのブラウザで始めたログインの CSRF）も断る。
	authURL, _ := e.startLogin(t)
	code, state := e.idp.authorize(t, authURL, "alice", nil)
	if cb := e.callback(code, state, nil); cb.Header().Get("Location") != "/?idp_error=expired" || sessionCookie(cb) != nil {
		t.Errorf("cookie の無い戻り = %s", cb.Header().Get("Location"))
	}
	// IdP が拒んだ戻り。
	authURL, sc := e.startLogin(t)
	_, state = e.idp.authorize(t, authURL, "alice", nil)
	r := httptest.NewRequest("GET", "/api/idp/callback?error=access_denied&state="+url.QueryEscape(state), nil)
	r.Host = testWebHost
	r.AddCookie(sc)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Header().Get("Location") != "/?idp_error=denied" {
		t.Errorf("IdP が拒んだ戻り = %s", rec.Header().Get("Location"))
	}
}

// 紐づけていなければ、IdP のログインは始めない。
func TestIdPLoginWithoutBinding(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	rec := e.req("GET", "/api/idp/login-option", testWebHost, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"linked":false`) {
		t.Errorf("設定の無い login-option = %d %s", rec.Code, rec.Body)
	}
	if rec := e.req("GET", "/api/idp/login", testWebHost, ""); rec.Header().Get("Location") != "/?idp_error=unavailable" {
		t.Errorf("設定の無いログイン = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	e.configure(t, sess, e.defaultConfig())
	if rec := e.req("GET", "/api/idp/login", testWebHost, ""); rec.Header().Get("Location") != "/?idp_error=unavailable" {
		t.Errorf("紐づけの無いログイン = %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

// ほかのホスト（IP のアドレス）で開いた画面から IdP のログインを始めたら、まず Web UI の URL へ移る。紐づけは Web UI の URL の
// ホストからだけ始める。戻り先（ほかのサイトからの top-level の GET）は、Host とオリジンの検査（protect.go）に断られない。
func TestIdPLoginHosts(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	rec := e.req("POST", "/api/idp/link", "192.168.1.10:8080", `{"password":"secret"}`, sess)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), testWebURL) {
		t.Errorf("ほかのホストからの紐づけ = %d %s、want 409", rec.Code, rec.Body)
	}
	e.link(t, sess, "alice")
	rec = e.req("GET", "/api/idp/login", "192.168.1.10:8080", "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != testWebURL+"/api/idp/login" {
		t.Errorf("ほかのホストからのログイン = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if cookieNamed(rec, idpStateCookie) != nil {
		t.Error("ほかのホストで state の cookie を付けた")
	}
	authURL, sc := e.startLogin(t)
	code, state := e.idp.authorize(t, authURL, "alice", nil)
	// e.callback は Sec-Fetch-Site: cross-site を付けて送る。
	if cb := e.callback(code, state, sc); cb.Code != http.StatusFound || cb.Header().Get("Location") != "/" {
		t.Errorf("ほかのサイトからの戻り = %d %s", cb.Code, cb.Body)
	}
}

// 紐づけを始める口は今のパスワードを求める。IdP の設定の口と紐づけの口は、API トークンでは通らない。
func TestIdPEndpointsNeedSessionAndPassword(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	for _, pw := range []string{`{"password":"wrong"}`, `{}`} {
		if rec := e.req("POST", "/api/idp/link", testWebHost, pw, sess); rec.Code != http.StatusForbidden {
			t.Errorf("パスワード %s の紐づけ = %d、want 403", pw, rec.Code)
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/idp", ""},
		{"PUT", "/api/idp", e.defaultConfig()},
		{"POST", "/api/idp/link", `{"password":"secret"}`},
		{"DELETE", "/api/idp/link", ""},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Host = testWebHost
		r.Header.Set("Authorization", "Bearer "+e.token)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("API トークンで %s %s = %d、want 403", c.method, c.path, rec.Code)
		}
		// セッションの cookie が付いていても、API トークンを付けた要求は通さない。
		r = httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Host = testWebHost
		r.Header.Set("Authorization", "Bearer "+e.token)
		r.AddCookie(sess)
		rec = httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("API トークンと cookie で %s %s = %d、want 403", c.method, c.path, rec.Code)
		}
		if rec := e.req(c.method, c.path, testWebHost, c.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("ログインなしの %s %s = %d、want 401", c.method, c.path, rec.Code)
		}
	}
	// セッションでは読める。client secret の値は返さない。
	r := httptest.NewRequest("GET", "/api/idp", nil)
	r.Host = testWebHost
	r.AddCookie(sess)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	var got idpResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Issuer != e.idp.issuer() || got.WebURL != testWebURL ||
		got.RedirectURL != testWebURL+"/api/idp/callback" || !got.ClientSecretSet || !got.WebHostAllowed || strings.Contains(rec.Body.String(), "s3cret") {
		t.Errorf("GET /api/idp = %s, %v", rec.Body, err)
	}
}

// issuer か client id を変えると紐づけが消える。Web UI の URL・client secret だけを変えても消えない。
func TestIdPSettingsChangeClearsBinding(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	e.link(t, sess, "alice")

	e.configure(t, sess, `{"issuer":"`+e.idp.issuer()+`","client_id":"rajikura","web_url":"https://rajikura.test:8443","client_secret":"other"}`)
	if e.binding(t) == "" {
		t.Error("Web UI の URL と client secret を変えたら紐づけが消えた")
	}
	rec := e.req("GET", "/api/idp", testWebHost, "", sess)
	if !strings.Contains(rec.Body.String(), `"web_host":"rajikura.test:8443"`) || !strings.Contains(rec.Body.String(), `"web_host_allowed":true`) {
		t.Errorf("GET /api/idp = %s", rec.Body)
	}
	e.configure(t, sess, e.defaultConfig())

	e.configure(t, sess, `{"issuer":"`+e.idp.issuer()+`","client_id":"other","web_url":"`+testWebURL+`"}`)
	if b := e.binding(t); b != "" {
		t.Errorf("client id を変えたのに紐づけ = %s", b)
	}
	e.configure(t, sess, e.defaultConfig())
	e.link(t, sess, "alice")
	e.configure(t, sess, `{"issuer":"https://other.example","client_id":"rajikura","web_url":"`+testWebURL+`"}`)
	if b := e.binding(t); b != "" {
		t.Errorf("issuer を変えたのに紐づけ = %s", b)
	}

	// 外す口はパスワードを求めない。
	e.configure(t, sess, e.defaultConfig())
	e.link(t, sess, "alice")
	if rec := e.req("DELETE", "/api/idp/link", testWebHost, "", sess); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"binding":null`) {
		t.Errorf("DELETE /api/idp/link = %d %s", rec.Code, rec.Body)
	}
	if b := e.binding(t); b != "" {
		t.Errorf("外したのに紐づけ = %s", b)
	}

	// 値の誤りは 400 で、何も変えない。
	for _, body := range []string{
		`{"issuer":"ftp://x","client_id":"a","web_url":"https://a"}`,
		`{"issuer":"https://x","client_id":"","web_url":"https://a"}`,
		`{"issuer":"https://x","client_id":"a","web_url":"https://a/sub"}`,
		`{"issuer":"https://x","client_id":"a"}`,
		`{"issuer":"https://x","client_id":"a","web_url":"https://a","extra":1}`,
	} {
		if rec := e.req("PUT", "/api/idp", testWebHost, body, sess); rec.Code != 400 {
			t.Errorf("PUT %s = %d、want 400", body, rec.Code)
		}
	}
	// 全部空なら IdP の設定を消す。
	e.configure(t, sess, `{"issuer":"","client_id":"","web_url":"","client_secret":""}`)
	rec = e.req("GET", "/api/idp", testWebHost, "", sess)
	if !strings.Contains(rec.Body.String(), `"issuer":""`) || !strings.Contains(rec.Body.String(), `"client_secret_set":false`) {
		t.Errorf("消した後の GET /api/idp = %s", rec.Body)
	}
}

// client secret を省くと、secret を送らない public client（PKCE だけ）としてログインする。
func TestIdPPublicClient(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, `{"issuer":"`+e.idp.issuer()+`","client_id":"rajikura","web_url":"`+testWebURL+`"}`)
	e.link(t, sess, "alice")
	e.idp.mu.Lock()
	r, form := e.idp.tokenRequests[len(e.idp.tokenRequests)-1], e.idp.tokenForms[len(e.idp.tokenForms)-1]
	e.idp.mu.Unlock()
	if _, _, ok := r.BasicAuth(); ok || form.Get("client_secret") != "" || form.Get("client_id") != "rajikura" || form.Get("code_verifier") == "" {
		t.Errorf("public client の token の要求 = basic %v、form %v", ok, form)
	}
}

// IdP のログインの失敗は、パスワードのログインの失敗の数に入らない。
func TestIdPFailuresDoNotCountTowardLoginLimit(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	e.link(t, sess, "alice")
	for range LoginMaxFailures + 2 {
		authURL, sc := e.startLogin(t)
		code, state := e.idp.authorize(t, authURL, "mallory", nil)
		if cb := e.callback(code, state, sc); cb.Header().Get("Location") != "/?idp_error=mismatch" {
			t.Fatalf("違う sub の戻り = %s", cb.Header().Get("Location"))
		}
		if cb := e.callback("x", "forged", sc); cb.Header().Get("Location") != "/?idp_error=expired" {
			t.Fatalf("食い違う state の戻り = %s", cb.Header().Get("Location"))
		}
	}
	// 数に入っていれば 5 回で止まり、正しいパスワードも 429 になる。
	for i := range LoginMaxFailures - 1 {
		if rec := e.req("POST", "/api/login", testWebHost, `{"password":"wrong"}`); rec.Code != 401 {
			t.Fatalf("%d 回目の間違い = %d、want 401", i+1, rec.Code)
		}
	}
	if rec := e.req("POST", "/api/login", testWebHost, `{"password":"secret"}`); rec.Code != 200 {
		t.Errorf("IdP のログインの失敗の後のパスワードのログイン = %d、want 200", rec.Code)
	}
}

// ログインの途中の状態は 10 分で消え、数の上限を超えたら古いものから捨てる。
func TestIdPPendingExpiry(t *testing.T) {
	f := &idpFlows{pending: map[string]idpPending{}}
	now := time.Now()
	add := func(s string) { f.add(s, idpPending{expires: now.Add(idpPendingTTL)}, now) }
	add("a")
	if _, ok := f.take("a", now.Add(idpPendingTTL)); ok {
		t.Error("10 分を過ぎた state を受け付けた")
	}
	add("b")
	if _, ok := f.take("b", now.Add(idpPendingTTL-time.Second)); !ok {
		t.Error("10 分の内側の state を受け付けない")
	}
	if _, ok := f.take("b", now); ok {
		t.Error("同じ state を 2 回受け付けた")
	}
	for i := range idpMaxPending + 1 {
		add(string(rune('A' + i)))
	}
	if len(f.pending) != idpMaxPending {
		t.Errorf("途中の状態の数 = %d、want %d", len(f.pending), idpMaxPending)
	}
	if _, ok := f.pending["A"]; ok {
		t.Error("いちばん古い途中の状態が残った")
	}
}

// 載せた戻り先のルートは、IdP に登録する戻り先の URL（config.IdPCallbackPath）と同じ。
func TestIdPCallbackPath(t *testing.T) {
	if config.IdPCallbackPath != "/api/idp/callback" {
		t.Errorf("config.IdPCallbackPath = %q、registerIdP のルートと揃える", config.IdPCallbackPath)
	}
}

// preferred_username の欄が無い前の紐づけの JSON は、そのまま読め、空として扱う。
func TestIdPBindingWithoutPreferredUsername(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	old := `{"issuer":"` + e.idp.issuer() + `","subject":"alice","email":"alice@example.com"}`
	if err := e.st.SetIdPBinding(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	rec := e.req("GET", "/api/idp", testWebHost, "", sess)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"preferred_username":""`) || !strings.Contains(rec.Body.String(), `"subject":"alice"`) {
		t.Fatalf("GET /api/idp = %d %s", rec.Code, rec.Body)
	}
	// 前の紐づけでも、iss + sub で通る。
	authURL, sc := e.startLogin(t)
	code, state := e.idp.authorize(t, authURL, "alice", nil)
	if cb := e.callback(code, state, sc); cb.Code != http.StatusFound || cb.Header().Get("Location") != "/" {
		t.Fatalf("戻り = %d %s", cb.Code, cb.Header().Get("Location"))
	}
}

// ID トークンに preferred_username が無ければ、空で保存する。
func TestIdPLinkWithoutPreferredUsernameClaim(t *testing.T) {
	e := newIdPEnv(t)
	sess := e.login(t)
	e.configure(t, sess, e.defaultConfig())
	e.linkTweak(t, sess, "alice", func(c map[string]any) { delete(c, "preferred_username") })
	if b := e.binding(t); !strings.Contains(b, `"preferred_username":""`) {
		t.Fatalf("紐づけ = %s", b)
	}
}
