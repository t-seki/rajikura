package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func getFoldersAs(t *testing.T, path string) (*httptest.ResponseRecorder, foldersResponse) {
	t.Helper()
	h, _, cookie := newSettingsUI(t)
	rec := do(h, "GET", "/api/folders?path="+url.QueryEscape(path), "", cookie)
	var resp foldersResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
	}
	return rec, resp
}

func TestFoldersRequiresLogin(t *testing.T) {
	h, _, _ := newSettingsUI(t)
	rec := do(h, "GET", "/api/folders", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestFoldersEmptyPathListsRoots(t *testing.T) {
	rec, resp := getFoldersAs(t, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if resp.Path != "" || resp.Parent != "" {
		t.Errorf("path=%q parent=%q, want both empty", resp.Path, resp.Parent)
	}
	if runtime.GOOS == "windows" {
		sys := os.Getenv("SystemDrive") + `\`
		found := false
		for _, f := range resp.Folders {
			if f.Path == sys {
				found = true
			}
		}
		if !found {
			t.Errorf("roots = %v, want to include %s", resp.Folders, sys)
		}
		return
	}
	if len(resp.Folders) != 1 || resp.Folders[0].Path != "/" {
		t.Errorf("roots = %v, want [/]", resp.Folders)
	}
}

func TestFoldersListsOnlyFoldersSorted(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"b", "a", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, resp := getFoldersAs(t, dir)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if resp.Path != filepath.Clean(dir) || resp.Parent != filepath.Dir(dir) {
		t.Errorf("path=%q parent=%q", resp.Path, resp.Parent)
	}
	want := []string{".hidden", "a", "b"}
	if len(resp.Folders) != len(want) {
		t.Fatalf("folders = %v, want %v", resp.Folders, want)
	}
	for i, n := range want {
		if resp.Folders[i].Name != n || resp.Folders[i].Path != filepath.Join(dir, n) {
			t.Errorf("folders[%d] = %v, want %s", i, resp.Folders[i], n)
		}
	}
}

func TestFoldersFollowsSymlinkToFolder(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Skipf("シンボリックリンクを作れない環境: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "f"), filepath.Join(dir, "filelink")); err != nil {
		t.Skipf("シンボリックリンクを作れない環境: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("シンボリックリンクを作れない環境: %v", err)
	}
	_, resp := getFoldersAs(t, dir)
	var names []string
	for _, f := range resp.Folders {
		names = append(names, f.Name)
	}
	if len(names) != 2 || names[0] != "link" || names[1] != "target" {
		t.Errorf("folders = %v, want [link target]", names)
	}
}

func TestFoldersBadRequests(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"相対パス":      "relative/dir",
		"無いフォルダ":    filepath.Join(dir, "missing"),
		"フォルダでないもの": file,
	} {
		rec, _ := getFoldersAs(t, p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
			continue
		}
		var e map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e["error"] == "" {
			t.Errorf("%s: body = %s, want {\"error\": ...}", name, rec.Body)
		}
	}
}

func TestFoldersParentEmptyAtRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	rec, resp := getFoldersAs(t, root)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if resp.Parent != "" {
		t.Errorf("parent = %q, want empty at %s", resp.Parent, root)
	}
}
