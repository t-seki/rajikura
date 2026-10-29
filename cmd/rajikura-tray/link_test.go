package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type savedConfigs struct {
	mu  sync.Mutex
	got []trayConfig
	err error
}

func (s *savedConfigs) save(c trayConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.got = append(s.got, c)
	return nil
}

func (s *savedConfigs) list() []trayConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]trayConfig(nil), s.got...)
}

func postCallback(t *testing.T, port int, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := http.PostForm(fmt.Sprintf("http://127.0.0.1:%d/callback", port), form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestLinkSavesWhenStateMatches(t *testing.T) {
	var saved savedConfigs
	s, err := startLink("http://127.0.0.1:8080", time.Minute, saved.save, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Port < 1024 || len(s.State) != 64 {
		t.Fatalf("ポート %d、state %q", s.Port, s.State)
	}
	resp, body := postCallback(t, s.Port, url.Values{"token": {"tok"}, "state": {s.State}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "連携した。この画面は閉じてよい") {
		t.Fatalf("答え = %d %s", resp.StatusCode, body)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("連携したら nil: %v", err)
	}
	got := saved.list()
	if len(got) != 1 || got[0] != (trayConfig{URL: "http://127.0.0.1:8080", Token: "tok"}) {
		t.Fatalf("書いた値 = %+v", got)
	}
	// 連携した後は受け口を閉じる。
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := http.PostForm(fmt.Sprintf("http://127.0.0.1:%d/callback", s.Port), url.Values{"token": {"x"}, "state": {s.State}})
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("連携した後も受け口が開いている")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(saved.list()) != 1 {
		t.Fatal("閉じた後に書いた")
	}
}

func TestLinkRejectsWrongState(t *testing.T) {
	var saved savedConfigs
	s, err := startLink("http://127.0.0.1:8080", time.Minute, saved.save, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, st := range []string{"", "wrong", s.State + "x", strings.ToUpper(s.State)} {
		resp, _ := postCallback(t, s.Port, url.Values{"token": {"tok"}, "state": {st}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("state %q: %d", st, resp.StatusCode)
		}
	}
	if len(saved.list()) != 0 {
		t.Fatalf("state が合わなければ書かない: %+v", saved.list())
	}
	// 合わなくても開いたままで、合えば書ける。
	resp, _ := postCallback(t, s.Port, url.Values{"token": {"tok"}, "state": {s.State}})
	if resp.StatusCode != http.StatusOK || len(saved.list()) != 1 {
		t.Fatalf("合えば書く: %d %+v", resp.StatusCode, saved.list())
	}
}

func TestLinkRejectsEmptyTokenAndGet(t *testing.T) {
	var saved savedConfigs
	s, err := startLink("http://127.0.0.1:8080", time.Minute, saved.save, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, _ := postCallback(t, s.Port, url.Values{"state": {s.State}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("トークンが空: %d", resp.StatusCode)
	}
	g, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?token=t&state=%s", s.Port, s.State))
	if err != nil {
		t.Fatal(err)
	}
	g.Body.Close()
	if g.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET（URL に載せたトークン）は受けない: %d", g.StatusCode)
	}
	if len(saved.list()) != 0 {
		t.Fatalf("書いた: %+v", saved.list())
	}
}

func TestLinkSaveFailureKeepsOpen(t *testing.T) {
	saved := savedConfigs{err: errors.New("書けない")}
	s, err := startLink("http://127.0.0.1:8080", time.Minute, saved.save, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, _ := postCallback(t, s.Port, url.Values{"token": {"tok"}, "state": {s.State}})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("書けなければ 500: %d", resp.StatusCode)
	}
	select {
	case <-s.done:
		t.Fatal("書けなかったのに閉じた")
	default:
	}
}

func TestLinkTimesOut(t *testing.T) {
	var saved savedConfigs
	s, err := startLink("http://127.0.0.1:8080", 50*time.Millisecond, saved.save, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(); !errors.Is(err, errLinkTimeout) {
		t.Fatalf("時間が過ぎたら閉じる: %v", err)
	}
}

func TestLinkTimeoutIsFiveMinutes(t *testing.T) {
	if linkTimeout != 5*time.Minute {
		t.Fatalf("受け口は 5 分で閉じる: %v", linkTimeout)
	}
}

func TestLinkListensOnLoopbackOnly(t *testing.T) {
	s, err := startLink("http://127.0.0.1:8080", time.Minute, (&savedConfigs{}).save, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.ln.Addr().String(); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("127.0.0.1 だけで待ち受ける: %s", got)
	}
}

func TestLinkStatesDiffer(t *testing.T) {
	a, _ := newLinkState()
	b, _ := newLinkState()
	if a == b || a == "" {
		t.Fatalf("state は毎回作る: %q %q", a, b)
	}
}

func TestLinkPageURL(t *testing.T) {
	got := linkPageURL("http://127.0.0.1:8080", 50123, "abc", `PC1\taro`)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/tray-link" {
		t.Fatalf("パス = %s", u.Path)
	}
	q := u.Query()
	if q.Get("port") != "50123" || q.Get("state") != "abc" || q.Get("name") != `PC1\taro` {
		t.Fatalf("クエリ = %v", q)
	}
}
