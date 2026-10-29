package artwork

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serve(t *testing.T, ctype string, body []byte, status int) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func TestFetch(t *testing.T) {
	want := []byte("\xff\xd8jpeg")
	got, err := Fetch(context.Background(), nil, serve(t, "image/jpeg", want, 0))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := Fetch(context.Background(), nil, serve(t, "image/jpeg; charset=binary", want, 0)); err != nil {
		t.Errorf("パラメータつきの Content-Type: %v", err)
	}
}

func TestFetchRejects(t *testing.T) {
	big := make([]byte, MaxBytes+1)
	cases := map[string]string{
		"未対応の形式":          serve(t, "image/webp", []byte("x"), 0),
		"Content-Type なし": serve(t, "", nil, 0),
		"大きすぎる":           serve(t, "image/jpeg", big, 0),
		"404":             serve(t, "image/jpeg", []byte("x"), 404),
		"空の本体":            serve(t, "image/jpeg", nil, 0),
		"つながらない":          "http://127.0.0.1:1/x.jpg",
	}
	for name, url := range cases {
		if _, err := Fetch(context.Background(), nil, url); err == nil {
			t.Errorf("%s: エラーにならない", name)
		}
	}
	if _, err := Fetch(context.Background(), nil, ""); !errors.Is(err, ErrNoURL) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchExactLimit(t *testing.T) {
	if _, err := Fetch(context.Background(), nil, serve(t, "image/jpeg", make([]byte, MaxBytes), 0)); err != nil {
		t.Errorf("上限ちょうど: %v", err)
	}
}

func TestFetchCanceled(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Fetch(ctx, nil, s.URL); err == nil {
		t.Error("エラーにならない")
	}
}
