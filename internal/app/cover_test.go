package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/artwork"
	"github.com/t-seki/rajikura/internal/domain"
)

// coverEnv はすっぴんしゃん 1 件だけの環境で、放送回の画像の URL を imgURL にする。
func coverEnv(t *testing.T, imgURL string) *env {
	t.Helper()
	e := newEnv(t, afterBroadcast)
	p := suppin
	p.img = imgURL
	e.fake.setPrograms("TBS", day, p)
	e.putShows(suppinShow)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) folderImage() ([]byte, error) {
	return os.ReadFile(filepath.Join(e.app.Library, "TBSラジオ", suppinShow.Name, "folder.jpg"))
}

// 画像が取れたら、m4a に埋め込み（Remux に渡す）、番組のフォルダに folder.jpg を置く。
func TestCoverSaved(t *testing.T) {
	img := []byte("\xff\xd8cover")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(img)
	}))
	defer srv.Close()
	e := coverEnv(t, srv.URL+"/a.jpg")
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if len(e.media.covers) != 1 || string(e.media.covers[0]) != string(img) {
		t.Errorf("埋め込みの画像 = %q", e.media.covers)
	}
	if got, err := e.folderImage(); err != nil || string(got) != string(img) {
		t.Errorf("folder.jpg = %q, %v", got, err)
	}
}

// 画像が取れないときは、画像なしで保存する。folder.jpg には触らない（前からあるものはそのまま）。
func TestCoverFailureSavesWithoutImage(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"読めない PNG": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("png"))
		},
		"大きすぎる": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(make([]byte, artwork.MaxBytes+1))
		},
		"遅い": func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			e := coverEnv(t, srv.URL+"/a.jpg")
			e.app.ArtworkClient = &http.Client{Timeout: 100 * time.Millisecond}
			dir := filepath.Join(e.app.Library, "TBSラジオ", suppinShow.Name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "folder.jpg"), []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			for e.runDue() {
			}
			if j := e.job(suppinShow.Name); j.State != domain.StateDone {
				t.Fatalf("job = %+v", j)
			}
			if len(e.media.covers) != 1 || e.media.covers[0] != nil {
				t.Errorf("画像が埋め込まれた: %q", e.media.covers)
			}
			if got, err := e.folderImage(); err != nil || string(got) != "old" {
				t.Errorf("folder.jpg = %q, %v", got, err)
			}
		})
	}
}

// URL が空でも、画像なしで保存する。
func TestCoverNoURL(t *testing.T) {
	e := coverEnv(t, "")
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if len(e.media.covers) != 1 || e.media.covers[0] != nil {
		t.Errorf("画像が埋め込まれた: %q", e.media.covers)
	}
	if _, err := e.folderImage(); !os.IsNotExist(err) {
		t.Errorf("folder.jpg がある: %v", err)
	}
}
