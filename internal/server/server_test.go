package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		last time.Time
		now  time.Time
		want int
	}{
		{"直前に回った", base, base.Add(time.Minute), 200},
		{"ちょうど 30 分", base, base.Add(30 * time.Minute), 200},
		{"30 分を過ぎた", base, base.Add(30*time.Minute + time.Second), 503},
		{"まだ記録が無い", time.Time{}, base, 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Handler(Health{LastPoll: func() time.Time { return tt.last }, Now: func() time.Time { return tt.now }}, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d（%s）", rec.Code, tt.want, rec.Body)
			}
			if !tt.last.IsZero() && !strings.Contains(rec.Body.String(), tt.last.Format(time.RFC3339)) {
				t.Errorf("本文に最後の時刻が無い: %q", rec.Body)
			}
		})
	}
}

func TestOtherPaths(t *testing.T) {
	h := Handler(Health{LastPoll: time.Now}, nil)
	for _, c := range []struct{ method, path string }{{"GET", "/"}, {"POST", "/healthz"}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code == 200 {
			t.Errorf("%s %s が 200", c.method, c.path)
		}
	}
}

// /metrics はログインなしで、metrics のハンドラの本文を返す。ほかのパスは h に渡す。nil なら足さない。
func TestWithMetrics(t *testing.T) {
	m := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("metrics")) })
	h := WithMetrics(Handler(Health{LastPoll: time.Now}, nil), m)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || rec.Body.String() != "metrics" {
		t.Fatalf("/metrics = %d %q", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Errorf("/healthz = %d", rec.Code)
	}
	plain := WithMetrics(Handler(Health{LastPoll: time.Now}, nil), nil)
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code == 200 {
		t.Errorf("metrics が nil なのに /metrics が 200")
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, Handler(Health{LastPoll: time.Now}, nil)) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve が終わらない")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Error("止めた後も接続できる")
	}
}
