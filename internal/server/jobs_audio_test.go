package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// libSettings は Current の library だけを返す Settings（ほかのメソッドは呼ばれない）。
type libSettings struct {
	Settings
	library string
}

func (s libSettings) Current() config.Config { return config.Config{Library: s.library} }

func newAudioUI(t *testing.T, library string) (http.Handler, *store.Store, *http.Cookie, time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, domain.JST)
	ui := &UI{
		Sessions: st, Jobs: st, Passwords: passwords(string(hash)),
		Settings: libSettings{library: library},
		Files:    fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:      func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	return h, st, login(t, h), now
}

func addAudioJob(t *testing.T, st *store.Store, now time.Time, outputPath string) int64 {
	t.Helper()
	return addJobOf(t, st, domain.Job{
		ShowName: "番組A", Day: mustDay(t, "2026-10-10"), State: domain.StateDone, OutputPath: outputPath,
		NextAttemptAt: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour),
	})
}

func doAudio(h http.Handler, method string, id int64, ck *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/jobs/"+strconv.FormatInt(id, 10)+"/audio", nil)
	req.Host = "localhost:8080"
	req.AddCookie(ck)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestJobAudioServesRange(t *testing.T) {
	lib := t.TempDir()
	h, st, ck, now := newAudioUI(t, lib)
	path := filepath.Join(lib, "番組A", "a.m4a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := addAudioJob(t, st, now, path)

	rec := doAudio(h, "GET", id, ck, nil)
	if rec.Code != 200 || rec.Body.String() != "0123456789" || rec.Header().Get("Content-Type") != "audio/mp4" {
		t.Errorf("全体 = %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	rec = doAudio(h, "GET", id, ck, map[string]string{"Range": "bytes=2-5"})
	if rec.Code != 206 || rec.Body.String() != "2345" || rec.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Errorf("Range = %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Range"))
	}
	rec = doAudio(h, "HEAD", id, ck, nil)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Errorf("HEAD = %d %q len=%s", rec.Code, rec.Body, rec.Header().Get("Content-Length"))
	}
}

func TestJobAudioRequiresLogin(t *testing.T) {
	h, _, _, _ := newAudioUI(t, t.TempDir())
	if rec := do(h, "GET", "/api/jobs/1/audio", ""); rec.Code != 401 {
		t.Errorf("ログインなし = %d, want 401", rec.Code)
	}
}

func TestJobAudioRefusals(t *testing.T) {
	lib := t.TempDir()
	other := t.TempDir()
	h, st, ck, now := newAudioUI(t, lib)
	write := func(p string) string {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		path string
		want int
		msg  string
	}{
		{"output_path が空", "", 404, "まだ保存していない"},
		{"library の外", write(filepath.Join(other, "a.m4a")), 403, "外にあるので出さない"},
		{"library の兄弟（前方一致だけ）", lib + "x" + string(filepath.Separator) + "a.m4a", 403, "外にあるので出さない"},
		{"拡張子が違う", write(filepath.Join(lib, "a.mp3")), 403, "外にあるので出さない"},
		{"ファイルが無い", filepath.Join(lib, "none.m4a"), 404, "ファイルが無い"},
		{"フォルダ", func() string {
			d := filepath.Join(lib, "dir.m4a")
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
			return d
		}(), 500, "ファイルを読めない: "},
	}
	for _, c := range cases {
		id := addAudioJob(t, st, now, c.path)
		rec := doAudio(h, "GET", id, ck, nil)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s = %d %s, want %d %q", c.name, rec.Code, rec.Body, c.want, c.msg)
		}
	}
	if rec := doAudio(h, "GET", 9999, ck, nil); rec.Code != 404 || !strings.Contains(rec.Body.String(), "ジョブが無い") {
		t.Errorf("ジョブが無い = %d %s", rec.Code, rec.Body)
	}
}

func TestJobAudioLibraryEmpty(t *testing.T) {
	lib := t.TempDir()
	h, st, ck, now := newAudioUI(t, "")
	p := filepath.Join(lib, "a.m4a")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := addAudioJob(t, st, now, p)
	if rec := doAudio(h, "GET", id, ck, nil); rec.Code != 403 {
		t.Errorf("library が空 = %d, want 403", rec.Code)
	}
}

func TestAudioPathAllowedExtensionCase(t *testing.T) {
	lib := t.TempDir()
	if !audioPathAllowed(lib, filepath.Join(lib, "a", "B.M4A")) {
		t.Error("大文字の拡張子を断った")
	}
	if audioPathAllowed(lib, filepath.Join(lib, "..", "a.m4a")) {
		t.Error("library の外（..）を通した")
	}
}
