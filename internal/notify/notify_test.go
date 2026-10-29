package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fakeDiscord は Discord の Webhook の偽のサーバー。
type fakeDiscord struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []string
	status int
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	t.Helper()
	f := &fakeDiscord{status: http.StatusNoContent}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.got = append(f.got, body.Content)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestDiscordSends(t *testing.T) {
	f := newFakeDiscord(t)
	d := &Discord{WebhookURL: f.srv.URL + "/api/webhooks/1/secret"}
	if err := d.Notify(context.Background(), KindExpired, "期限切れ: 番組"); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0] != "期限切れ: 番組" {
		t.Fatalf("got = %q", f.got)
	}
}

func TestDiscordTruncates(t *testing.T) {
	f := newFakeDiscord(t)
	d := &Discord{WebhookURL: f.srv.URL}
	msg := strings.Repeat("あ", DiscordMaxLen+10)
	if err := d.Notify(context.Background(), KindExpired, msg); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 {
		t.Fatalf("got = %d 通", len(f.got))
	}
	got := f.got[0]
	if n := utf8.RuneCountInString(got); n != DiscordMaxLen {
		t.Errorf("長さ = %d 文字", n)
	}
	if !strings.HasSuffix(got, truncatedNote) || !strings.HasPrefix(got, "ああ") {
		t.Errorf("切り詰めたことが書かれていない: %q", got[len(got)-60:])
	}

	// 上限ちょうどなら切り詰めない。
	exact := strings.Repeat("い", DiscordMaxLen)
	if Truncate(exact, DiscordMaxLen) != exact {
		t.Error("上限ちょうどの本文を切り詰めた")
	}
}

// 失敗のエラーに Webhook の URL を出さない。
func TestDiscordErrorHidesURL(t *testing.T) {
	f := newFakeDiscord(t)
	f.status = http.StatusInternalServerError
	secret := "/api/webhooks/1/TOPSECRET"
	d := &Discord{WebhookURL: f.srv.URL + secret}
	err := d.Notify(context.Background(), KindExpired, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err = %v", err)
	}

	// つながらないとき（*url.Error）も URL を出さない。
	f.srv.Close()
	err = d.Notify(context.Background(), KindExpired, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err = %v", err)
	}

	err = (&Discord{WebhookURL: "http://[::1]:namedport/TOPSECRET"}).Notify(context.Background(), KindExpired, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err = %v", err)
	}
}

// newFakeSlack は Slack の Incoming Webhook の偽のサーバー。text を集める。
func newFakeSlack(t *testing.T, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	got := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		*got = append(*got, body.Text)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestSlackSendsAndEscapes(t *testing.T) {
	srv, got := newFakeSlack(t, http.StatusOK)
	s := &Slack{WebhookURL: srv.URL + "/services/T/B/secret"}
	if err := s.Notify(context.Background(), KindExpired, "期限切れ: A&B <番組> 1>0"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0] != "期限切れ: A&amp;B &lt;番組&gt; 1&gt;0" {
		t.Fatalf("got = %q", *got)
	}
}

func TestSlackTruncates(t *testing.T) {
	srv, got := newFakeSlack(t, http.StatusOK)
	s := &Slack{WebhookURL: srv.URL}
	// & はエスケープで 5 文字に伸びる。エスケープの後で上限に収める。
	if err := s.Notify(context.Background(), KindExpired, strings.Repeat("&", SlackMaxLen)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got = %d 通", len(*got))
	}
	g := (*got)[0]
	if n := utf8.RuneCountInString(g); n > SlackMaxLen {
		t.Errorf("長さ = %d 文字", n)
	}
	if !strings.HasSuffix(g, truncatedNote) || !strings.HasPrefix(g, "&amp;&amp;") {
		t.Errorf("切り詰めたことが書かれていない、または先頭が残っていない: %q", g)
	}

	// 上限ちょうどなら切り詰めない。
	exact := strings.Repeat("い", SlackMaxLen)
	if slackTruncate(exact, SlackMaxLen) != exact {
		t.Error("上限ちょうどの本文を切り詰めた")
	}
}

// 切り詰めの境界が実体参照の途中に当たったら、その & の手前まで削る。
func TestSlackTruncateKeepsEntityWhole(t *testing.T) {
	keep := SlackMaxLen - utf8.RuneCountInString(truncatedNote)
	for _, tail := range []string{"&", "<", ">"} {
		srv, got := newFakeSlack(t, http.StatusOK)
		s := &Slack{WebhookURL: srv.URL}
		// 切り詰めの位置が実体参照（5 文字か 4 文字）の 3 文字目に当たる並び。
		msg := strings.Repeat("あ", keep-2) + tail + strings.Repeat("い", 30)
		if err := s.Notify(context.Background(), KindExpired, msg); err != nil {
			t.Fatal(err)
		}
		want := strings.Repeat("あ", keep-2) + truncatedNote
		if (*got)[0] != want {
			t.Errorf("%q のとき末尾 %q", tail, (*got)[0][len((*got)[0])-30:])
		}
	}
}

// 失敗のエラーに Webhook の URL を出さない。
func TestSlackErrorHidesURL(t *testing.T) {
	srv, _ := newFakeSlack(t, http.StatusInternalServerError)
	s := &Slack{WebhookURL: srv.URL + "/services/TOPSECRET"}
	err := s.Notify(context.Background(), KindExpired, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err = %v", err)
	}
	srv.Close()
	err = s.Notify(context.Background(), KindExpired, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err = %v", err)
	}
	err = (&Slack{WebhookURL: "http://[::1]:namedport/TOPSECRET"}).Notify(context.Background(), KindExpired, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("err = %v", err)
	}
}
