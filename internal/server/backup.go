package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/t-seki/rajikura/internal/store"
)

// Backups はバックアップを作るもの（store.Store が満たす。docs/adr/0017-backup-by-db-copy.md）。
type Backups interface {
	// Backup は DB のバックアップ（ログインのセッションを空にした写し）を path に作る。path にファイルがあればエラー。
	Backup(ctx context.Context, path string) error
	// LastBackupAt は、最後に自動のバックアップを取れた時刻（まだなら零値。設定の画面に出す）。
	LastBackupAt(ctx context.Context) (time.Time, error)
}

var _ Backups = (*store.Store)(nil)

// registerBackup はバックアップの API を mux に載せる。Backups が nil なら載せない。
//
//   - GET /api/backup: その場で作ったバックアップのファイルを返す（rajikura-<日時>.db の名前で保存させる）。
//     作った写しは一時のフォルダ（BackupTempDir）に置き、返したら消す
func (u *UI) registerBackup(mux *http.ServeMux) {
	if u.Backups == nil {
		return
	}
	mux.HandleFunc("GET /api/backup", u.guard(u.authed(u.getBackup)))
}

func (u *UI) getBackup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp(u.BackupTempDir, "backup-")
	if err != nil {
		u.internalError(w, "バックアップの一時のフォルダを作れない", err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "rajikura.db")
	if err := u.Backups.Backup(r.Context(), path); err != nil {
		u.internalError(w, "バックアップを作れない", err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		u.internalError(w, "バックアップを読めない", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		u.internalError(w, "バックアップを読めない", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/vnd.sqlite3")
	h.Set("Content-Disposition", `attachment; filename="`+store.BackupFileName(u.now())+`"`)
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		u.log().Warn("バックアップを送り終えられなかった", "err", err)
	}
}
