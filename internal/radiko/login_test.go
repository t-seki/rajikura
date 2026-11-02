package radiko

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// loginEnv は radiko のログインのテストの準備。会員（ログイン情報あり）の Client と、OnLogin に渡った結果を返す。
func loginEnv(t *testing.T, areaFree bool) (*fakeRadiko, *Client, *[]LoginEvent) {
	t.Helper()
	f := newFake(t)
	c := f.client()
	c.Now = func() time.Time { return jst(2026, 10, 7, 12, 0, 0) }
	c.Account = func() (Account, bool) {
		return Account{Mail: "me@example.com", Password: "secret-pass", AreaFree: areaFree}, true
	}
	var mu sync.Mutex
	events := &[]LoginEvent{}
	c.OnLogin = func(ev LoginEvent) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, ev)
	}
	return f, c, events
}

func TestAuthenticateWithoutAccountDoesNotLogIn(t *testing.T) {
	f := newFake(t)
	c := f.client()
	tok, err := c.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.loginCount != 0 || tok.Session != "" || tok.AreaFree {
		t.Fatalf("logins = %d, token = %+v; want no radiko login", f.loginCount, tok)
	}
	if len(f.authSessions) != 1 || f.authSessions[0] != "" {
		t.Fatalf("auth2 sessions = %q, want one without radiko_session", f.authSessions)
	}
	// Account が false を返す（非会員）ときも、ログインしない。
	c.Account = func() (Account, bool) { return Account{}, false }
	if _, err := c.Authenticate(context.Background()); err != nil || f.loginCount != 0 {
		t.Fatalf("err = %v, logins = %d; want no radiko login", err, f.loginCount)
	}
}

// エリアフリーの会員: radiko のログインのセッションを auth2 に付け、エリアフリーの再生リスト（areafree=1）を type=c で使う。
func TestAreaFreeLoginUsesSessionAndAreaFreePlaylist(t *testing.T) {
	f, c, events := loginEnv(t, true)
	tok, err := c.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.gotMail != "me@example.com" || f.gotPass != "secret-pass" {
		t.Fatalf("login form = %q / %q", f.gotMail, f.gotPass)
	}
	if tok.Session != "S1" || !tok.AreaFree || tok.AreaID != "JP13" {
		t.Fatalf("token = %+v, want session S1 and areafree", tok)
	}
	if len(f.authSessions) != 1 || f.authSessions[0] != "S1" {
		t.Fatalf("auth2 sessions = %q, want [S1]", f.authSessions)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %+v, want 1", *events)
	}
	ev := (*events)[0]
	if ev.Err != nil || ev.Mismatch || !ev.AreaFree || ev.AreaFreeValue != "1" {
		t.Fatalf("event = %+v, want success without mismatch", ev)
	}
	// 応答の欄は名前の順で、radiko_session の値は伏せる。
	want := []LoginField{{"areafree", `"1"`}, {"member_ukey", `"u1"`}, {"radiko_session", `"***"`}}
	if !slices.Equal(ev.Fields, want) {
		t.Fatalf("fields = %+v, want %+v", ev.Fields, want)
	}

	start, end := jst(2026, 10, 7, 10, 0, 0), jst(2026, 10, 7, 10, 5, 0)
	f.wantFT = start.Format(timeLayout)
	f.wantType = "c"
	var out bytes.Buffer
	res, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.PlaylistCreateURL, "/areafree/playlist.m3u8") {
		t.Fatalf("candidate = %s, want the areafree playlist", res.PlaylistCreateURL)
	}
	if out.String() != expectedAudio(start, end) {
		t.Fatalf("audio = %q", out.String())
	}

	// セッションは使い回す（2 回目の認証ではログインしない）。
	tok2, err := c.Authenticate(context.Background())
	if err != nil || tok2.Session != "S1" || f.loginCount != 1 {
		t.Fatalf("second auth: token = %+v, err = %v, logins = %d; want reused session", tok2, err, f.loginCount)
	}
}

// タイムフリー30 の会員: セッションは付けるが、エリアフリーの再生リストは使わない。
func TestTimeFree30LoginUsesNormalPlaylist(t *testing.T) {
	f, c, events := loginEnv(t, false)
	f.loginAreaFree = `"0"`
	tok, err := c.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Session != "S1" || tok.AreaFree || (*events)[0].Mismatch {
		t.Fatalf("token = %+v, events = %+v", tok, *events)
	}
	start, end := jst(2026, 10, 7, 10, 0, 0), jst(2026, 10, 7, 10, 5, 0)
	f.wantFT = start.Format(timeLayout)
	f.wantType = "c"
	var out bytes.Buffer
	res, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.PlaylistCreateURL, "/tf/playlist.m3u8") {
		t.Fatalf("candidate = %s, want the non-areafree playlist", res.PlaylistCreateURL)
	}
}

// 応答の areafree が、選んだ会員種別と食い違う。
func TestLoginAreaFreeMismatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		want     bool
		value    string
		mismatch bool
	}{
		{"areafree but 0", true, `"0"`, true},
		{"areafree but missing", true, `null`, true},
		{"areafree as number", true, `1`, false},
		{"timefree30 but 1", false, `"1"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, events := loginEnv(t, tc.want)
			f.loginAreaFree = tc.value
			if _, err := c.Authenticate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(*events) != 1 || (*events)[0].Err != nil || (*events)[0].Mismatch != tc.mismatch {
				t.Fatalf("events = %+v, want mismatch = %v", *events, tc.mismatch)
			}
		})
	}
}

// radiko がログインを拒んだら、非会員として認証し、ResetLogin まではログインし直さない。
func TestLoginRejectedFallsBackToFree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"401", http.StatusUnauthorized, `{"message":"invalid"}`},
		{"empty session", 0, `{"radiko_session":"","areafree":"0"}`},
		{"not json", 0, `<html></html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, events := loginEnv(t, true)
			f.loginStatus, f.loginBody = tc.status, tc.body
			tok, err := c.Authenticate(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tok.Session != "" || tok.AreaFree || tok.AreaID != "JP13" {
				t.Fatalf("token = %+v, want a non-member token", tok)
			}
			if len(f.authSessions) != 1 || f.authSessions[0] != "" {
				t.Fatalf("auth2 sessions = %q, want one without radiko_session", f.authSessions)
			}
			if len(*events) != 1 || !errors.Is((*events)[0].Err, ErrLoginRejected) {
				t.Fatalf("events = %+v, want a rejected login", *events)
			}
			if _, err := c.Authenticate(context.Background()); err != nil || f.loginCount != 1 {
				t.Fatalf("err = %v, logins = %d; want no second radiko login", err, f.loginCount)
			}
			// 保存したら（ResetLogin）、次の認証でログインし直す。
			f.loginStatus, f.loginBody = 0, ""
			c.ResetLogin()
			tok, err = c.Authenticate(context.Background())
			if err != nil || tok.Session != "S2" || f.loginCount != 2 {
				t.Fatalf("after reset: token = %+v, err = %v, logins = %d", tok, err, f.loginCount)
			}
			if len(*events) != 2 || (*events)[1].Err != nil {
				t.Fatalf("events = %+v, want a successful second login", *events)
			}
		})
	}
}

// ネットワークの失敗・5xx は一時的な失敗。異常として渡さず、次の認証でもう一度ログインする。
func TestLoginServerErrorIsTemporary(t *testing.T) {
	f, c, events := loginEnv(t, true)
	f.loginStatus, f.loginBody = http.StatusServiceUnavailable, "busy"
	if _, err := c.Authenticate(context.Background()); !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v, want ErrTemporary", err)
	}
	if len(*events) != 0 || len(f.authSessions) != 0 {
		t.Fatalf("events = %+v, auth2 = %q; want neither", *events, f.authSessions)
	}
	f.loginStatus, f.loginBody = 0, ""
	tok, err := c.Authenticate(context.Background())
	if err != nil || tok.Session != "S2" {
		t.Fatalf("token = %+v, err = %v", tok, err)
	}
}

func TestLoginErrorDoesNotLeakPassword(t *testing.T) {
	f, c, _ := loginEnv(t, true)
	f.srv.Close() // つながらない
	_, err := c.Authenticate(context.Background())
	if !errors.Is(err, ErrTemporary) || strings.Contains(err.Error(), "secret-pass") {
		t.Fatalf("err = %v", err)
	}
}

// radiko がセッションを拒んだ（auth2 が 401）ら、捨ててログインし直す。
func TestSessionRejectedAtAuth2LogsInAgain(t *testing.T) {
	f, c, events := loginEnv(t, true)
	if _, err := c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.rejectSession = map[string]bool{"S1": true}
	tok, err := c.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Session != "S2" || f.loginCount != 2 || len(*events) != 2 {
		t.Fatalf("token = %+v, logins = %d, events = %d; want a new session", tok, f.loginCount, len(*events))
	}
	if want := []string{"S1", "S1", "S2"}; !slices.Equal(f.authSessions, want) {
		t.Fatalf("auth2 sessions = %q, want %q", f.authSessions, want)
	}
	// 新しいセッションも拒まれたら、一時的な失敗にする（ログインし直すのは 1 回まで）。
	f.rejectSession["S2"], f.rejectSession["S3"] = true, true
	if _, err := c.Authenticate(context.Background()); !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v, want ErrTemporary", err)
	}
}

// 取得の途中でセッションを付けたトークンが拒まれたら、セッションも捨ててログインし直し、続きから取る。
func TestTokenRejectedDuringFetchLogsInAgain(t *testing.T) {
	f, c, _ := loginEnv(t, true)
	tok, err := c.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start, end := jst(2026, 10, 7, 10, 0, 0), jst(2026, 10, 7, 10, 12, 30)
	f.wantFT = start.Format(timeLayout)
	f.wantType = "c"
	f.expireAtPlaylistReq = 2
	var out bytes.Buffer
	if _, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != expectedAudio(start, end) {
		t.Fatalf("audio = %q", out.String())
	}
	if f.loginCount != 2 {
		t.Fatalf("logins = %d, want 2 (logged in again after the token was rejected)", f.loginCount)
	}
}

// TryLogin は持っているセッションも拒まれた印も変えず、OnLogin も呼ばない。通った結果を AdoptLogin で持たせると、OnLogin に渡り、
// 次の認証ではログインし直さない（ADR 0034）。
func TestTryLoginAndAdopt(t *testing.T) {
	f, c, events := loginEnv(t, true)
	if _, err := c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	newAcc := Account{Mail: "new@example.com", Password: "new-pass", AreaFree: true}
	a, err := c.TryLogin(context.Background(), newAcc)
	if err != nil || a.Event.Err != nil || a.Event.Mismatch {
		t.Fatalf("attempt = %+v, %v", a.Event, err)
	}
	if f.gotMail != "new@example.com" || f.gotPass != "new-pass" {
		t.Fatalf("login form = %q / %q", f.gotMail, f.gotPass)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %+v; TryLogin must not call OnLogin", *events)
	}
	// 持たせる前は、前のセッション（S1）のまま。
	if tok, err := c.Authenticate(context.Background()); err != nil || tok.Session != "S1" {
		t.Fatalf("token = %+v, %v; want the old session before adopting", tok, err)
	}
	c.Account = func() (Account, bool) { return newAcc, true }
	c.AdoptLogin(a)
	if len(*events) != 2 || (*events)[1].Err != nil {
		t.Fatalf("events = %+v, want the adopted result", *events)
	}
	tok, err := c.Authenticate(context.Background())
	if err != nil || tok.Session != "S2" || f.loginCount != 2 {
		t.Fatalf("token = %+v, err = %v, logins = %d; want the adopted session without a new login", tok, err, f.loginCount)
	}
}

// TryLogin が拒まれても、持っているセッションと拒まれた印は変わらない。5xx は ErrTemporary。
func TestTryLoginRejectedAndTemporary(t *testing.T) {
	f, c, events := loginEnv(t, true)
	if _, err := c.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.loginStatus, f.loginBody = http.StatusUnauthorized, `{"message":"invalid"}`
	a, err := c.TryLogin(context.Background(), Account{Mail: "me@example.com", Password: "wrong"})
	if err != nil || !errors.Is(a.Event.Err, ErrLoginRejected) {
		t.Fatalf("attempt = %+v, %v; want rejected", a.Event, err)
	}
	if tok, err := c.Authenticate(context.Background()); err != nil || tok.Session != "S1" || f.loginCount != 2 || len(*events) != 1 {
		t.Fatalf("token = %+v, err = %v, logins = %d, events = %d; want the session kept", tok, err, f.loginCount, len(*events))
	}
	f.loginStatus, f.loginBody = http.StatusServiceUnavailable, "busy"
	if _, err := c.TryLogin(context.Background(), Account{Mail: "me@example.com", Password: "secret-pass"}); !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v, want ErrTemporary", err)
	}

	// 拒まれた印も残る。
	f2, c2, _ := loginEnv(t, true)
	f2.loginStatus, f2.loginBody = http.StatusUnauthorized, `{"message":"invalid"}`
	if _, err := c2.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.TryLogin(context.Background(), Account{Mail: "me@example.com", Password: "wrong"}); err != nil {
		t.Fatal(err)
	}
	f2.loginStatus, f2.loginBody = 0, ""
	if tok, err := c2.Authenticate(context.Background()); err != nil || tok.Session != "" || f2.loginCount != 2 {
		t.Fatalf("token = %+v, err = %v, logins = %d; want the rejected mark kept", tok, err, f2.loginCount)
	}
}

// TryLogin はロックを持たないので、その要求の間もほかの認証は待たされない。その認証が古いログイン情報で得たセッションは、
// AdoptLogin が新しいセッションで上書きする（ADR 0034 の決定 7）。
func TestTryLoginDoesNotBlockAuthenticate(t *testing.T) {
	f, c, events := loginEnv(t, true)
	started, release := make(chan struct{}), make(chan struct{})
	f.loginHook = func(mail string) {
		if mail == "new@example.com" {
			close(started)
			<-release
		}
	}
	newAcc := Account{Mail: "new@example.com", Password: "new-pass", AreaFree: true}
	type result struct {
		a   LoginAttempt
		err error
	}
	done := make(chan result, 1)
	go func() {
		a, err := c.TryLogin(context.Background(), newAcc)
		done <- result{a, err}
	}()
	<-started
	// 保存の途中に、古いログイン情報で認証する（待たされない）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := c.Authenticate(ctx)
	close(release)
	if err != nil || tok.Session != "S1" {
		t.Fatalf("token = %+v, %v; want a login with the old account while TryLogin waits", tok, err)
	}
	r := <-done
	if r.err != nil || r.a.Event.Err != nil {
		t.Fatalf("attempt = %+v, %v", r.a.Event, r.err)
	}
	c.Account = func() (Account, bool) { return newAcc, true }
	c.AdoptLogin(r.a)
	tok, err = c.Authenticate(context.Background())
	if err != nil || tok.Session != "S2" || f.loginCount != 2 || len(*events) != 2 {
		t.Fatalf("token = %+v, err = %v, logins = %d, events = %d; want the adopted session", tok, err, f.loginCount, len(*events))
	}
}

func TestParseLiveCandidatesAreaFree(t *testing.T) {
	data := []byte(`<urls>
<url areafree="0" timefree="0"><playlist_create_url>live</playlist_create_url></url>
<url areafree="1" timefree="0"><playlist_create_url>live-areafree</playlist_create_url></url>
<url areafree="1" timefree="1"><playlist_create_url>tf-areafree</playlist_create_url></url>
</urls>`)
	for _, tc := range []struct{ areaFree, want string }{{"0", "live"}, {"1", "live-areafree"}} {
		got, err := parseLiveCandidates(data, tc.areaFree)
		if err != nil || len(got) != 1 || got[0] != tc.want {
			t.Fatalf("areafree=%s: got %v, %v; want [%s]", tc.areaFree, got, err, tc.want)
		}
	}
}
