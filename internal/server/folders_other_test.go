//go:build !windows

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestFoldersUnreadableFolderIs400(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("root などで読めてしまう環境")
	}
	rec, _ := getFoldersAs(t, dir)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
