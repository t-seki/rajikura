package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/config"
)

// backup_dir と backup_keep は保存でき、取る先は作られて書けることを確かめる。最後に自動のバックアップを取れた時刻は、
// 取れていなければ null、取れていればその時刻を返し、取る先を変えると忘れる。
func TestSettingsBackup(t *testing.T) {
	h, s, cookie := newSettingsUI(t)
	var body struct {
		config.View
		LastBackupAt *time.Time `json:"last_backup_at"`
	}
	rec := do(h, "GET", "/api/settings", "", cookie)
	decode(t, rec.Body.Bytes(), &body)
	if *body.Settings.BackupDir != "" || *body.Settings.BackupKeep != config.DefaultBackupKeep || body.LastBackupAt != nil ||
		!strings.Contains(rec.Body.String(), `"last_backup_at":null`) {
		t.Fatalf("GET（何も保存していない） = %s", rec.Body)
	}

	dir := filepath.Join(t.TempDir(), "backups")
	rec = do(h, "PATCH", "/api/settings", `{"backup_dir":`+quoteJSON(dir)+`,"backup_keep":3}`, cookie)
	if rec.Code != 200 {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	decode(t, rec.Body.Bytes(), &body)
	if *body.Settings.BackupDir != dir || *body.Settings.BackupKeep != 3 || s.Current().BackupDir != dir {
		t.Fatalf("PATCH の答え = %s", rec.Body)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("取る先のフォルダを作っていない: %v", err)
	}

	at := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	if err := s.Store.SetLastBackupAt(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	decode(t, do(h, "GET", "/api/settings", "", cookie).Body.Bytes(), &body)
	if body.LastBackupAt == nil || !body.LastBackupAt.Equal(at) {
		t.Fatalf("last_backup_at = %v、want %v", body.LastBackupAt, at)
	}
	// 取る先を変えずに保存しても、忘れない。
	do(h, "PATCH", "/api/settings", `{"backup_keep":4}`, cookie)
	decode(t, do(h, "GET", "/api/settings", "", cookie).Body.Bytes(), &body)
	if body.LastBackupAt == nil {
		t.Fatal("取る先を変えていないのに last_backup_at を忘れた")
	}
	// 取る先を変えたら忘れる（新しい取る先には、まだ取れていない）。
	do(h, "PATCH", "/api/settings", `{"backup_dir":`+quoteJSON(filepath.Join(t.TempDir(), "other"))+`}`, cookie)
	decode(t, do(h, "GET", "/api/settings", "", cookie).Body.Bytes(), &body)
	if body.LastBackupAt != nil {
		t.Fatalf("取る先を変えたのに last_backup_at = %v", body.LastBackupAt)
	}
}
