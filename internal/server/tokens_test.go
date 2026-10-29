package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/store"
)

func newTokenUI(t *testing.T, password string) http.Handler {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash := ""
	if password != "" {
		b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		hash = string(b)
	}
	ui := &UI{
		Sessions: st, Tokens: st, Jobs: st, Passwords: passwords(hash),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) },
	}
	return Handler(Health{LastPoll: time.Now}, ui)
}

func doBearer(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	// httptest の既定の Host（example.com）は Host の検査で断られるので、ブラウザで localhost を開いた形にする。
	req.Host = "localhost:8080"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPITokenFlow(t *testing.T) {
	h := newTokenUI(t, "secret")
	cookie := login(t, h)

	if rec := do(h, "POST", "/api/tokens", `{"name":"  "}`, cookie); rec.Code != 400 {
		t.Errorf("名前が空 = %d、want 400", rec.Code)
	}
	rec := do(h, "POST", "/api/tokens", `{"name":"手元の CLI"}`, cookie)
	if rec.Code != 201 {
		t.Fatalf("発行 = %d %s", rec.Code, rec.Body)
	}
	var created struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Token == "" || created.Name != "手元の CLI" {
		t.Fatalf("発行の応答 = %s, %v", rec.Body, err)
	}

	// 一覧にトークンそのものは出ない。使う前は lastUsedAt が null。
	rec = do(h, "GET", "/api/tokens", "", cookie)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), created.Token) || !strings.Contains(rec.Body.String(), `"lastUsedAt":null`) {
		t.Fatalf("一覧 = %d %s", rec.Code, rec.Body)
	}

	// Bearer で /api/ を叩ける。
	if rec := doBearer(h, "GET", "/api/jobs", "", created.Token); rec.Code != 200 {
		t.Errorf("Bearer で /api/jobs = %d %s", rec.Code, rec.Body)
	}
	if rec := doBearer(h, "GET", "/api/me", "", created.Token); rec.Code != 200 {
		t.Errorf("Bearer で /api/me = %d", rec.Code)
	}
	// 使ったので lastUsedAt が入る。トークンでも一覧は叩ける。
	rec = doBearer(h, "GET", "/api/tokens", "", created.Token)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lastUsedAt":"2026-10-13T21:00:00+09:00"`) {
		t.Errorf("使った後の一覧 = %d %s", rec.Code, rec.Body)
	}
	// トークンでの logout も壊れない。
	if rec := doBearer(h, "POST", "/api/logout", "", created.Token); rec.Code != 200 {
		t.Errorf("Bearer で logout = %d", rec.Code)
	}

	// 知らないトークン、Bearer の中身が空は 401。
	for _, tok := range []string{"rjk_unknown", " "} {
		if rec := doBearer(h, "GET", "/api/jobs", "", tok); rec.Code != 401 {
			t.Errorf("トークン %q = %d、want 401", tok, rec.Code)
		}
	}
	// 失効: 消すと 401、2 回目は 404。
	if rec := do(h, "DELETE", "/api/tokens/"+itoa(created.ID), "", cookie); rec.Code != 204 {
		t.Fatalf("失効 = %d", rec.Code)
	}
	if rec := doBearer(h, "GET", "/api/jobs", "", created.Token); rec.Code != 401 {
		t.Errorf("失効したトークン = %d、want 401", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/tokens/"+itoa(created.ID), "", cookie); rec.Code != 404 {
		t.Errorf("2 回目の失効 = %d、want 404", rec.Code)
	}
	// cookie も無く Bearer も無ければ 401。
	if rec := do(h, "GET", "/api/tokens", ""); rec.Code != 401 {
		t.Errorf("認証なし = %d、want 401", rec.Code)
	}
}

func TestAPITokenNeedsPassword(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, secret, err := st.CreateAPIToken(context.Background(), "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Health{LastPoll: time.Now}, &UI{Sessions: st, Passwords: st, Tokens: st, Jobs: st, Files: fstest.MapFS{"index.html": {Data: []byte("x")}}})
	if rec := doBearer(h, "GET", "/api/jobs", "", secret); rec.Code != 503 {
		t.Errorf("パスワード未設定でトークン = %d、want 503", rec.Code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
