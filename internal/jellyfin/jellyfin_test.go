package jellyfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fake struct {
	mu        sync.Mutex
	calls     []string
	keys      []string
	refreshSt int
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	f := &fake{refreshSt: http.StatusNoContent}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.keys = append(f.keys, r.Header.Get("X-Emby-Token"))
		switch {
		case r.Method == "GET" && r.URL.Path == "/Library/VirtualFolders":
			w.Write([]byte(`[{"Name":"音楽","ItemId":"aaa"},{"Name":"ラジオ","ItemId":"bbb"}]`))
		case r.Method == "POST" && r.URL.Path == "/Items/bbb/Refresh":
			w.WriteHeader(f.refreshSt)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestRescanLooksUpIDByName(t *testing.T) {
	f, srv := newFake(t)
	c := &Client{URL: srv.URL + "/", APIKey: "k", Library: "ラジオ"}
	if err := c.Rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.calls, ",")
	if got != "GET /Library/VirtualFolders,POST /Items/bbb/Refresh" {
		t.Errorf("calls = %s", got)
	}
	for _, k := range f.keys {
		if k != "k" {
			t.Errorf("token = %q", k)
		}
	}
}

func TestRescanLibraryNotFound(t *testing.T) {
	f, srv := newFake(t)
	c := &Client{URL: srv.URL, APIKey: "k", Library: "無い"}
	err := c.Rescan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "見つからない") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("Refresh を叩いた: %v", f.calls)
	}
}

func TestRescanErrorsDoNotLeakKey(t *testing.T) {
	f, srv := newFake(t)
	f.refreshSt = http.StatusInternalServerError
	c := &Client{URL: srv.URL, APIKey: "SECRET-KEY", Library: "ラジオ"}
	err := c.Rescan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "SECRET-KEY") {
		t.Errorf("エラーに API キーが出ている: %v", err)
	}
	bad := &Client{URL: "http://127.0.0.1:1", APIKey: "SECRET-KEY", Library: "ラジオ"}
	if err := bad.Rescan(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET-KEY") {
		t.Errorf("err = %v", err)
	}
}
