package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/store"
)

// newProtectedUI は、パスワード secret と API トークンを持つ UI を、allowed を -allowed-hosts にして立てる。
// /metrics は WithMetrics で前に置く。
func newProtectedUI(t *testing.T, allowed ...string) (h http.Handler, token string, now *time.Time, logs *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
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
	_, token, err = st.CreateAPIToken(context.Background(), "cli", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	now = &t0
	logs = &bytes.Buffer{}
	ui := &UI{
		Sessions: st, Passwords: st, Tokens: st, Jobs: st,
		Files:        fstest.MapFS{"index.html": {Data: []byte("<p>index</p>")}},
		Now:          func() time.Time { return *now },
		Log:          slog.New(slog.NewTextHandler(logs, nil)),
		AllowedHosts: allowed,
	}
	metrics := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("# metrics")) })
	return WithMetrics(Handler(Health{LastPoll: time.Now}, ui), metrics), token, now, logs
}

// send は host を Host にして要求を送る。header はヘッダの名前と値を交互に並べる。
func send(h http.Handler, method, path, host, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// IP のアドレス（v4・v6、ポートあり・なし）と localhost の Host は、設定なしで通る。
func TestHostAllowedWithoutSetting(t *testing.T) {
	h, _, _, _ := newProtectedUI(t)
	for _, host := range []string{
		"127.0.0.1", "127.0.0.1:8080", "192.168.1.10:8080", "10.42.0.15:8080",
		"[::1]", "[::1]:8080", "[fe80::1%25eth0]:8080", "::1",
		"localhost", "localhost:5173", "LOCALHOST:8080",
	} {
		if rec := send(h, "GET", "/", host, ""); rec.Code != 200 {
			t.Errorf("Host %q の GET / = %d、want 200", host, rec.Code)
		}
		if rec := send(h, "GET", "/api/me", host, ""); rec.Code != 401 {
			t.Errorf("Host %q の GET /api/me = %d、want 401（ログインしていない）", host, rec.Code)
		}
	}
}

// 許していない名前の Host は、/api/ も SPA も 421 で断り、-allowed-hosts に足すよう返して、ログに Host を出す。
// /healthz・/metrics は Host を見ない。
func TestHostRejected(t *testing.T) {
	h, _, _, logs := newProtectedUI(t)
	rec := send(h, "GET", "/api/me", "rajikura.example.com", "")
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("GET /api/me = %d、want 421", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.Contains(body["error"], "-allowed-hosts に足す") {
		t.Errorf("/api/ の 421 の本文 = %s（%v）、want JSON の error に -allowed-hosts", rec.Body, err)
	}
	rec = send(h, "GET", "/shows", "rajikura.example.com:443", "")
	if rec.Code != http.StatusMisdirectedRequest || !strings.Contains(rec.Body.String(), "-allowed-hosts に足す") ||
		strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("SPA = %d %q（%s）、want 421 の平文", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	for _, host := range []string{"evil.example", "", "127.0.0.1.evil.example", "localhost.evil.example"} {
		if rec := send(h, "POST", "/api/login", host, `{"password":"secret"}`); rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q の POST /api/login = %d、want 421", host, rec.Code)
		}
	}
	if !strings.Contains(logs.String(), "host=rajikura.example.com") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("断った Host がログに無い: %s", logs)
	}
	for _, p := range []string{"/healthz", "/metrics"} {
		if rec := send(h, "GET", p, "evil.example", ""); rec.Code != 200 {
			t.Errorf("許していない Host の GET %s = %d、want 200", p, rec.Code)
		}
	}
}

// -allowed-hosts に並べた名前は、大文字小文字とポートに依らず通る（k3s の Gateway API の経路の名前の形）。
func TestHostAllowedByName(t *testing.T) {
	h, _, _, _ := newProtectedUI(t, "rajikura.example.com", "pc.tailnet.ts.net")
	for _, host := range []string{"rajikura.example.com", "Rajikura.Example.Com:443", "pc.tailnet.ts.net:8080"} {
		if rec := send(h, "GET", "/", host, ""); rec.Code != 200 {
			t.Errorf("Host %q の GET / = %d、want 200", host, rec.Code)
		}
		if rec := send(h, "POST", "/api/login", host, `{"password":"secret"}`, "Sec-Fetch-Site", "same-origin"); rec.Code != 200 {
			t.Errorf("Host %q のログイン = %d %s、want 200", host, rec.Code, rec.Body)
		}
	}
	if rec := send(h, "GET", "/", "other.example.com", ""); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("並べていない名前 = %d、want 421", rec.Code)
	}
}

// ほかのオリジンのページからの、状態を変える要求（ログイン前の口も含む）は 403 で断る。同じオリジンのものは通る。
func TestCrossOriginRejected(t *testing.T) {
	h, token, _, _ := newProtectedUI(t, "rajikura.example.com")
	host := "rajikura.example.com"
	cases := []struct {
		name   string
		header []string
	}{
		{"Sec-Fetch-Site が cross-site", []string{"Sec-Fetch-Site", "cross-site"}},
		{"Sec-Fetch-Site が same-site", []string{"Sec-Fetch-Site", "same-site", "Origin", "https://other.example.com"}},
		{"Origin だけがほかのオリジン", []string{"Origin", "https://evil.example"}},
	}
	for _, c := range cases {
		for _, p := range []struct{ method, path, body string }{
			{"POST", "/api/login", `{"password":"secret"}`},
			{"POST", "/api/setup", `{"code":"x","password":"y"}`},
			{"PUT", "/api/password", `{"current":"secret","password":"x"}`},
			{"POST", "/api/logout", ""},
			{"DELETE", "/api/tokens/1", ""},
		} {
			rec := send(h, p.method, p.path, host, p.body, append([]string{"Authorization", "Bearer " + token}, c.header...)...)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("%s: %s %s = %d %s、want 403 の JSON", c.name, p.method, p.path, rec.Code, rec.Body)
			}
		}
	}
	// 読むだけの GET は、ほかのオリジンからでも検査しない（cookie は SameSite=Strict で付かない）。
	if rec := send(h, "GET", "/api/me", host, "", "Sec-Fetch-Site", "cross-site"); rec.Code != 401 {
		t.Errorf("ほかのオリジンの GET /api/me = %d、want 401", rec.Code)
	}
	// 同じオリジンからの要求は通る。
	if rec := send(h, "POST", "/api/login", host, `{"password":"secret"}`, "Sec-Fetch-Site", "same-origin"); rec.Code != 200 {
		t.Errorf("Sec-Fetch-Site が same-origin のログイン = %d、want 200", rec.Code)
	}
	if rec := send(h, "POST", "/api/login", host, `{"password":"secret"}`, "Origin", "https://rajikura.example.com"); rec.Code != 200 {
		t.Errorf("Origin が同じオリジンのログイン = %d、want 200", rec.Code)
	}
}

// Origin も Sec-Fetch-Site も付けない Authorization: Bearer の要求（CLI の形）は、状態を変える要求でも通る。
func TestBearerWithoutOriginPasses(t *testing.T) {
	h, token, _, _ := newProtectedUI(t)
	rec := send(h, "POST", "/api/tokens", "127.0.0.1:8080", `{"name":"other"}`, "Authorization", "Bearer "+token, "Content-Type", "application/json")
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Errorf("CLI の形の POST /api/tokens = %d %s、want 成功", rec.Code, rec.Body)
	}
	rec = send(h, "POST", "/api/logout", "127.0.0.1:8080", "", "Authorization", "Bearer "+token)
	if rec.Code != 200 {
		t.Errorf("CLI の形の POST /api/logout = %d %s、want 200", rec.Code, rec.Body)
	}
}

// retryAfter は 429 の Retry-After と、本文の retry_after（秒）を返す。
func retryAfter(t *testing.T, rec *httptest.ResponseRecorder) (string, float64) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 の本文が JSON でない: %s", rec.Body)
	}
	secs, _ := body["retry_after"].(float64)
	return rec.Header().Get("Retry-After"), secs
}

// ログインを 5 回続けて間違えたら 1 分受け付けず（正しいパスワードも 429）、明けた後も間違えるたびに倍にし（上限 15 分）、
// 成功したら 0 に戻す。
func TestLoginLimit(t *testing.T) {
	h, _, now, logs := newProtectedUI(t)
	host := "localhost:8080"
	wrong := func() int { return send(h, "POST", "/api/login", host, `{"password":"wrong"}`).Code }
	right := func() *httptest.ResponseRecorder { return send(h, "POST", "/api/login", host, `{"password":"secret"}`) }

	for i := range LoginMaxFailures {
		if got := wrong(); got != 401 {
			t.Fatalf("%d 回目の間違い = %d、want 401", i+1, got)
		}
	}
	if !strings.Contains(logs.String(), "ログインの受け付けを止める") {
		t.Errorf("止め始めたときのログが無い: %s", logs)
	}
	rec := right()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("止めている間の正しいパスワード = %d、want 429", rec.Code)
	}
	if hdr, secs := retryAfter(t, rec); hdr != "60" || secs != 60 {
		t.Errorf("Retry-After = %q、retry_after = %v、want 60", hdr, secs)
	}
	*now = now.Add(45 * time.Second)
	if hdr, secs := retryAfter(t, right()); hdr != "15" || secs != 15 {
		t.Errorf("45 秒後の Retry-After = %q、retry_after = %v、want 15", hdr, secs)
	}

	// 明けた後も、間違えるたびに倍にする: 2・4・8 分、そこから 15 分で止まる。
	*now = now.Add(15 * time.Second)
	for _, want := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		if got := wrong(); got != 401 {
			t.Fatalf("明けた後の間違い = %d、want 401", got)
		}
		rec := right()
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("明けた後に間違えた直後の正しいパスワード = %d、want 429", rec.Code)
		}
		if _, secs := retryAfter(t, rec); time.Duration(secs)*time.Second != want {
			t.Errorf("止める時間 = %v 秒、want %v", secs, want)
		}
		*now = now.Add(want)
	}

	// 成功したら 0 に戻る（また 5 回までは 401）。
	if rec := right(); rec.Code != 200 {
		t.Fatalf("明けた後の正しいパスワード = %d、want 200", rec.Code)
	}
	for i := range LoginMaxFailures - 1 {
		if got := wrong(); got != 401 {
			t.Fatalf("成功の後の %d 回目の間違い = %d、want 401", i+1, got)
		}
	}
	if rec := right(); rec.Code != 200 {
		t.Errorf("成功の後に 4 回間違えてからの正しいパスワード = %d、want 200", rec.Code)
	}
}

// PUT /api/password の今のパスワードの間違いも、ログインと同じ数に入る。止めている間は PUT /api/password も 429。
func TestChangePasswordCountsTowardLoginLimit(t *testing.T) {
	h, token, now, _ := newProtectedUI(t)
	host := "localhost:8080"
	put := func(current string) int {
		return send(h, "PUT", "/api/password", host, `{"current":"`+current+`","password":"next"}`, "Authorization", "Bearer "+token).Code
	}
	for range 3 {
		if got := put("wrong"); got != http.StatusForbidden {
			t.Fatalf("今のパスワードの間違い = %d、want 403", got)
		}
	}
	for range 2 {
		if got := send(h, "POST", "/api/login", host, `{"password":"wrong"}`).Code; got != 401 {
			t.Fatalf("ログインの間違い = %d、want 401", got)
		}
	}
	if got := send(h, "POST", "/api/login", host, `{"password":"secret"}`).Code; got != http.StatusTooManyRequests {
		t.Errorf("合わせて 5 回間違えた後のログイン = %d、want 429", got)
	}
	if got := put("secret"); got != http.StatusTooManyRequests {
		t.Errorf("止めている間の PUT /api/password = %d、want 429", got)
	}
	*now = now.Add(time.Minute)
	if got := put("secret"); got != 200 {
		t.Errorf("明けた後の PUT /api/password = %d、want 200", got)
	}
}
