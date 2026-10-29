package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestWriteLicenses(t *testing.T) {
	goList := fstest.MapFS{goLicensesFile: {Data: []byte("==> rajikura\n\nMIT\n")}}
	webList := fstest.MapFS{webLicensesFile: {Data: []byte("# Licenses\n\n## react - 19.0.0 (MIT)\n")}}
	empty := fstest.MapFS{}
	tests := []struct {
		name       string
		goFS       fstest.MapFS
		webFS      fstest.MapFS
		want       string
		wantAbsent []string
	}{
		{"Go と Web UI の両方", goList, webList, "==> rajikura\n\nMIT\n==> Web UI\n\n# Licenses\n\n## react - 19.0.0 (MIT)\n", nil},
		{"Web UI の分が無い", goList, empty, "==> rajikura\n\nMIT\n" + noWebLicenses + "\n", nil},
		// 一覧の無いビルド（手元の go build）は案内だけを出す。Web UI の分があっても出さない。
		{"Go の分が無い", empty, webList, noLicenses + "\n", []string{"react"}},
		{"Go の分が空", fstest.MapFS{goLicensesFile: {Data: nil}}, webList, noLicenses + "\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			if err := writeLicenses(&b, tt.goFS, tt.webFS); err != nil {
				t.Fatal(err)
			}
			if b.String() != tt.want {
				t.Errorf("出力 = %q, want %q", b.String(), tt.want)
			}
			for _, s := range tt.wantAbsent {
				if strings.Contains(b.String(), s) {
					t.Errorf("出力に %q がある", s)
				}
			}
		})
	}
}

// 一覧の無いビルドの案内は、Release の配布物とコンテナイメージなら一覧を出せることを伝える。
func TestNoLicensesMessage(t *testing.T) {
	for _, s := range []string{"一覧が入っていない", "Release の配布物", "コンテナイメージ"} {
		if !strings.Contains(noLicenses, s) {
			t.Errorf("noLicenses に %q が無い: %q", s, noLicenses)
		}
	}
}
