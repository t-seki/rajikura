package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIPoll(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("トークンを送っていない: %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet {
			t.Errorf("GET だけを使う（既読にしない）: %s %s", r.Method, r.URL)
		}
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/notifications":
			w.Write([]byte(`{"notifications":[{"id":3,"kind":"recorded","message":"録音済み","createdAt":"2026-10-22T09:00:00+09:00","failed":false,"read":false,"link":"/jobs/12"},` +
				`{"id":2,"kind":"done","message":"録音した","createdAt":"2026-10-22T09:00:00+09:00","failed":false,"read":false}]}`))
		case "/api/notifications/status":
			w.Write([]byte(`{"unread":1,"failed":2,"anomalies":[{"kind":"sync","message":"番組表を同期できない","since":"2026-10-22T09:00:00+09:00"}],"channel":"none"}`))
		case "/api/jobs":
			w.Write([]byte(`{"jobs":[{"id":10,"state":"running"},{"id":11,"state":"running"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, err := apiClient{}.poll(context.Background(), connection{URL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	// link はあれば読み、無ければ空。
	if len(s.Notifications) != 2 || s.Notifications[0].Link != "/jobs/12" || s.Notifications[1].ID != 2 ||
		s.Notifications[1].Message != "録音した" || s.Notifications[1].Link != "" {
		t.Errorf("通知 = %+v", s.Notifications)
	}
	if s.Unread != 1 || s.Failed != 2 || len(s.Anomalies) != 1 || s.Running != 2 {
		t.Errorf("状態 = %+v", s)
	}
	want := []string{"/api/notifications", "/api/notifications/status", "/api/jobs?state=running"}
	if len(paths) != len(want) {
		t.Fatalf("読んだ口 = %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("読んだ口 = %v, want %v", paths, want)
		}
	}
}

func TestAPIPollUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"トークンが無効"}`))
	}))
	defer srv.Close()
	_, err := apiClient{}.poll(context.Background(), connection{URL: srv.URL, Token: "old"})
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("401 は errUnauthorized: %v", err)
	}
}

func TestAPIPollServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"パスワードが決まっていない","setup":true}`))
	}))
	defer srv.Close()
	_, err := apiClient{}.poll(context.Background(), connection{URL: srv.URL, Token: "t"})
	if err == nil || errors.Is(err, errUnauthorized) {
		t.Fatalf("2xx でなければ（401 のほか）誤り: %v", err)
	}
}

func TestAPIPollUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	_, err := apiClient{}.poll(context.Background(), connection{URL: u, Token: "t"})
	if err == nil || errors.Is(err, errUnauthorized) {
		t.Fatalf("つながらなければ誤り: %v", err)
	}
}

func TestAPISendTest(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
		wantIs  error
	}{
		{"送れた", 200, `{"sent":true,"notification":{"id":3}}`, "", nil},
		{"通知先に送れなかった（200）はエラーにしない", 200, `{"sent":false,"error":"x","notification":{"id":3}}`, "", nil},
		{"通知先が none", 200, `{"sent":false,"noChannel":true,"notification":{"id":3}}`, "", nil},
		{"401", 401, `{"error":"x"}`, "", errUnauthorized},
		{"200 以外", 500, `{"error":"壊れた"}`, "POST /api/notifications/test: 500 Internal Server Error（壊れた）", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/notifications/test" {
					t.Errorf("要求 = %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("トークン = %q", r.Header.Get("Authorization"))
				}
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			err := apiClient{}.sendTest(context.Background(), connection{URL: srv.URL, Token: "tok"})
			switch {
			case c.wantIs != nil:
				if !errors.Is(err, c.wantIs) {
					t.Errorf("err = %v", err)
				}
			case c.wantErr != "":
				if err == nil || err.Error() != c.wantErr {
					t.Errorf("err = %v, want %q", err, c.wantErr)
				}
			case err != nil:
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestAPISendTestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	if err := (apiClient{}).sendTest(context.Background(), connection{URL: u, Token: "tok"}); err == nil {
		t.Error("届かないのにエラーにならない")
	}
}
