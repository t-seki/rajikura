package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// バックアップ（docs/adr/0017-backup-by-db-copy.md）。serve が動いたまま取る DB の写しで、ログインのセッションを
// 空にしたもの。秘密の鍵（rajikura.key）は DB の外にあるので含まない。

// BackupFileName は、t に取ったバックアップのファイルの名前（rajikura-<YYYYMMDD-HHMMSS>.db。日本時間）。
// 名前の辞書順が取った時刻の順になる。
func BackupFileName(t time.Time) string {
	return "rajikura-" + t.In(domain.JST).Format("20060102-150405") + ".db"
}

// Backup は DB のバックアップを path に作る。SQLite の VACUUM INTO で、動いている間も一貫した写しを作り、写しの中の
// sessions を空にする（消したセッションの ID がファイルの空き領域に残らないよう、secure_delete で消す）。
// 写しは WAL ではなく、1 つのファイルだけで開ける。path にファイルがあればエラーにする（VACUUM INTO が上書きしない）。
// 途中で失敗したら、作りかけの path を消す。
func (s *Store) Backup(ctx context.Context, path string) (err error) {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("バックアップの書き先 %s がもうある", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		os.Remove(path)
		return fmt.Errorf("DB を %s に写せない: %w", path, err)
	}
	defer func() {
		if err != nil {
			os.Remove(path)
			os.Remove(path + "-journal")
		}
	}()
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(DELETE)&_pragma=secure_delete(1)")
	if err != nil {
		return fmt.Errorf("写し %s を開けない: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		db.Close()
		return fmt.Errorf("写しのログインのセッションを消せない: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("写し %s を閉じられない: %w", path, err)
	}
	return nil
}
