package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreFolderImage(t *testing.T) {
	root := t.TempDir()
	l := Layout{Dir: filepath.Join("局", "番組")}
	if err := StoreFolderImage(root, l, []byte("one")); err != nil {
		t.Fatal(err)
	}
	// 上書きされる。
	if err := StoreFolderImage(root, l, []byte("two")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, l.Dir)
	got, err := os.ReadFile(filepath.Join(dir, FolderImage))
	if err != nil || string(got) != "two" {
		t.Fatalf("folder.jpg = %q, %v", got, err)
	}
	// 一時ファイルは残らない。
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 {
		t.Fatalf("entries = %v, %v", ents, err)
	}
}

func TestCleanPartialsImage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "局", "番組")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, FolderImage)
	stale := filepath.Join(dir, "folder.123.partial.jpg")
	for _, f := range []string{keep, stale} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := CleanPartials(root); err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("folder.jpg removed: %v", err)
	}
}
