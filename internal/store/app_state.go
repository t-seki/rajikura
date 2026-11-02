package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// keyLastSavedAt は、最後にジョブを保存した時刻の app_state のキー。
const keyLastSavedAt = "last_saved_at"

// LastSavedAt は、最後にジョブを保存した時刻を返す。まだ保存していなければ零値。
func (s *Store) LastSavedAt(ctx context.Context) (time.Time, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, keyLastSavedAt).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return parseTime(v)
}

// SetLastSavedAt は、最後にジョブを保存した時刻を at に記録する。
func (s *Store) SetLastSavedAt(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, keyLastSavedAt, formatTime(at))
	return err
}

// keyLastBackupAt は、最後に自動のバックアップを取れた時刻の app_state のキー。
const keyLastBackupAt = "last_backup_at"

// LastBackupAt は、最後に自動のバックアップを取れた時刻を返す。まだ取れていない（取る先を変えた後を含む）なら零値。
func (s *Store) LastBackupAt(ctx context.Context) (time.Time, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, keyLastBackupAt).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return parseTime(v)
}

// SetLastBackupAt は、最後に自動のバックアップを取れた時刻を at に記録する。at が零値なら忘れる。
func (s *Store) SetLastBackupAt(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM app_state WHERE key = ?`, keyLastBackupAt)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, keyLastBackupAt, formatTime(at))
	return err
}

// keyOwnArea は、最後に見た自分のエリア（radiko の認証の応答のエリア ID）の app_state のキー
// （docs/adr/0032-revise-deadlines-when-own-area-changes.md）。設定（人が変える値）ではないので、ここに置く。
const keyOwnArea = "own_area"

// OwnArea は、最後に見た自分のエリアのエリア ID を返す。まだ覚えていなければ空。
func (s *Store) OwnArea(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, keyOwnArea).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetOwnArea は、最後に見た自分のエリアを area に記録する。
func (s *Store) SetOwnArea(ctx context.Context, area string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, keyOwnArea, area)
	return err
}
