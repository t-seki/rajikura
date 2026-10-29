package main

import (
	"testing"
)

// tray.toml の読み書きのテストは internal/trayconf にある。ここでは接続先の決め方を見る。
func TestResolveConnectionOrder(t *testing.T) {
	cases := []struct {
		name    string
		cfg     trayConfig
		flagURL string
		want    string
	}{
		{"-url が先", trayConfig{URL: "http://127.0.0.1:9000/"}, "http://127.0.0.1:18080", "http://127.0.0.1:18080"},
		{"次に tray.toml の url", trayConfig{URL: "https://rajikura.example.com/"}, "", "https://rajikura.example.com"},
		{"どちらも無ければ既定", trayConfig{}, "", defaultURL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveConnection(c.cfg, c.flagURL)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != c.want {
				t.Fatalf("URL = %s, want %s", got.URL, c.want)
			}
		})
	}
	// -url で接続先を変えても、トークンは tray.toml から。
	got, _ := resolveConnection(trayConfig{URL: "http://127.0.0.1:9000", Token: "t"}, "http://127.0.0.1:18080")
	if got.Token != "t" {
		t.Fatalf("トークンは tray.toml から: %+v", got)
	}
	if _, err := resolveConnection(trayConfig{}, "127.0.0.1:8080"); err == nil {
		t.Fatal("http:// の無い URL は誤り")
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080":        "127.0.0.1",
		"https://rajikura.example.com": "rajikura.example.com",
		"http://[::1]:8080":            "::1",
		"not a url":                    "not a url",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}
