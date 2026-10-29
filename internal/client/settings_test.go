package client

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeSettings は一時フォルダの rajikura/client.toml に body を権限 perm で書き、そのフォルダを返す。
func writeSettings(t *testing.T, body string, perm fs.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rajikura"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rajikura", "client.toml")
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	return dir
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// 接続先とトークンは、値ごとに、フラグ → 環境変数 → 設定ファイルの順に、先にあるものを使う。
func TestResolveOrder(t *testing.T) {
	dir := writeSettings(t, "url = \"https://file.example\"\ntoken = \"file-token\"\n", 0o600)
	cfgDir := func() (string, error) { return dir, nil }
	tests := []struct {
		name             string
		src              Sources
		wantURL, wantTok string
	}{
		{"設定ファイルだけ", Sources{}, "https://file.example", "file-token"},
		{"環境変数が設定ファイルより先", Sources{Getenv: env(map[string]string{EnvURL: "https://env.example", EnvToken: "env-token"})},
			"https://env.example", "env-token"},
		{"フラグが環境変数より先", Sources{FlagURL: "https://flag.example", FlagToken: "flag-token",
			Getenv: env(map[string]string{EnvURL: "https://env.example", EnvToken: "env-token"})}, "https://flag.example", "flag-token"},
		{"値ごとに選ぶ", Sources{FlagToken: "flag-token", Getenv: env(map[string]string{EnvURL: "https://env.example"})},
			"https://env.example", "flag-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src
			src.ConfigDir = cfgDir
			if src.Getenv == nil {
				src.Getenv = env(nil)
			}
			s, warnings, err := Resolve(src)
			if err != nil {
				t.Fatal(err)
			}
			if s.URL != tt.wantURL || s.Token != tt.wantTok {
				t.Errorf("Resolve = %+v, want %s %s", s, tt.wantURL, tt.wantTok)
			}
			if len(warnings) != 0 {
				t.Errorf("警告 = %v", warnings)
			}
		})
	}
}

// 接続先かトークンがどこにも無ければエラー。設定ファイルが無いのはエラーにしない。知らないキーはエラー。
func TestResolveErrors(t *testing.T) {
	empty := func() (string, error) { return t.TempDir(), nil }
	if _, _, err := Resolve(Sources{Getenv: env(nil), ConfigDir: empty}); err == nil || !strings.Contains(err.Error(), "接続先が無い") {
		t.Errorf("接続先が無い: %v", err)
	}
	if _, _, err := Resolve(Sources{FlagURL: "https://x.example", Getenv: env(nil), ConfigDir: empty}); err == nil || !strings.Contains(err.Error(), "API トークンが無い") {
		t.Errorf("トークンが無い: %v", err)
	}
	if _, _, err := Resolve(Sources{FlagURL: "x.example", FlagToken: "t", Getenv: env(nil), ConfigDir: empty}); err == nil || !strings.Contains(err.Error(), "URL として読めない") {
		t.Errorf("scheme の無い URL: %v", err)
	}
	dir := writeSettings(t, "url = \"https://x.example\"\ntokn = \"t\"\n", 0o600)
	if _, _, err := Resolve(Sources{Getenv: env(nil), ConfigDir: func() (string, error) { return dir, nil }}); err == nil || !strings.Contains(err.Error(), "知らないキー tokn") {
		t.Errorf("知らないキー: %v", err)
	}
}

// 設定ファイルの権限が 0600 より広ければ、Windows のほかでは警告を出して続ける。Windows では確かめない。
func TestResolvePermissionWarning(t *testing.T) {
	if !PermissionTooOpen(0o644) || !PermissionTooOpen(0o640) || PermissionTooOpen(0o600) || PermissionTooOpen(0o400) {
		t.Error("PermissionTooOpen の判定が違う")
	}
	if runtime.GOOS == "windows" {
		// Windows の os.Chmod は読み取り専用の印しか変えられず、権限のビットで広さを作れない。
		t.Skip("権限のビットで広い設定ファイルを作れない（Windows）")
	}
	body := "url = \"https://x.example\"\ntoken = \"t\"\n"
	wide := writeSettings(t, body, 0o644)
	_, warnings, err := Resolve(Sources{Getenv: env(nil), ConfigDir: func() (string, error) { return wide, nil }})
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "0600 より広い") {
		t.Errorf("0644 = %v, %v", warnings, err)
	}
	// Windows として読めば確かめない。
	_, warnings, err = Resolve(Sources{Getenv: env(nil), ConfigDir: func() (string, error) { return wide, nil }, GOOS: "windows"})
	if err != nil || len(warnings) != 0 {
		t.Errorf("Windows = %v, %v", warnings, err)
	}
	narrow := writeSettings(t, body, 0o600)
	_, warnings, err = Resolve(Sources{Getenv: env(nil), ConfigDir: func() (string, error) { return narrow, nil }})
	if err != nil || len(warnings) != 0 {
		t.Errorf("0600 = %v, %v", warnings, err)
	}
}

// 接続先が http:// で localhost 以外なら、トークンが平文で流れうると警告を出して続ける。
func TestResolvePlainHTTPWarning(t *testing.T) {
	empty := func() (string, error) { return t.TempDir(), nil }
	tests := []struct {
		url  string
		warn bool
	}{
		{"http://rajikura.lan:8080", true},
		{"http://100.64.0.1:8080", true},
		{"https://rajikura.lan", false},
		{"http://localhost:8080", false},
		{"http://127.0.0.1:8080", false},
		{"http://[::1]:8080", false},
	}
	for _, tt := range tests {
		_, warnings, err := Resolve(Sources{FlagURL: tt.url, FlagToken: "t", Getenv: env(nil), ConfigDir: empty})
		if err != nil {
			t.Fatalf("%s: %v", tt.url, err)
		}
		got := len(warnings) == 1 && strings.Contains(warnings[0], "平文")
		if got != tt.warn || (!tt.warn && len(warnings) != 0) {
			t.Errorf("%s の警告 = %v, want 警告あり %v", tt.url, warnings, tt.warn)
		}
	}
}
