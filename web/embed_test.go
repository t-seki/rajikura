package web

import (
	"io/fs"
	"strings"
	"testing"
)

// Node でビルドしていなくても、仮の index.html が返る。
func TestFS(t *testing.T) {
	b, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "<html") {
		t.Errorf("index.html = %q", b)
	}
}
