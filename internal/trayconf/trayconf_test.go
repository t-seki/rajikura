package trayconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := Path(t.TempDir())
	want := Config{URL: "http://127.0.0.1:8080", Token: "tok-123"}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("読み直した値 = %+v, want %+v", got, want)
	}
	// 書き直すと置き換わり、一時のファイルは残らない。
	want.Token = "tok-456"
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(path); got != want {
		t.Fatalf("書き直した値 = %+v, want %+v", got, want)
	}
	ents, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("tray.toml のほかにファイルが残った: %v", ents)
	}
}

func TestPath(t *testing.T) {
	got := Path(filepath.Join("C:", "AppData"))
	if filepath.Base(got) != "tray.toml" || filepath.Base(filepath.Dir(got)) != "rajikura" {
		t.Fatalf("tray.toml の場所 = %s", got)
	}
}

func TestReadMissingIsEmpty(t *testing.T) {
	c, err := Read(filepath.Join(t.TempDir(), "rajikura", "tray.toml"))
	if err != nil || c != (Config{}) {
		t.Fatalf("無ければ空: %+v %v", c, err)
	}
}

func TestReadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.toml")
	if err := os.WriteFile(path, []byte("url = \"http://127.0.0.1:8080\"\ntokn = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "tokn") {
		t.Fatalf("知らないキーは誤り: %v", err)
	}
}

func TestParseURL(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8080", "https://rajikura.example.com/", "http://[::1]:8080"} {
		if _, err := ParseURL(ok); err != nil {
			t.Errorf("ParseURL(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"127.0.0.1:8080", "ftp://example.com", "http://", "", "rajikura.example.com"} {
		if _, err := ParseURL(bad); err == nil {
			t.Errorf("ParseURL(%q) が通った", bad)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080/":  "http://127.0.0.1:8080",
		"http://127.0.0.1:8080":   "http://127.0.0.1:8080",
		"https://example.com/x//": "https://example.com/x",
	} {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
