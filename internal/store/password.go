package store

import (
	"context"
	"database/sql"
	"errors"
)

// keyPasswordHash は、Web UI のパスワードの bcrypt の hash の app_state のキー（docs/adr/0016-first-password-by-setup-code.md）。
const keyPasswordHash = "password_hash"

// PasswordHash は、Web UI のパスワードの bcrypt の hash を返す。まだ決めていなければ空。
func (s *Store) PasswordHash(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, keyPasswordHash).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetPasswordHash は、Web UI のパスワードの bcrypt の hash を hash にする。
func (s *Store) SetPasswordHash(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, keyPasswordHash, hash)
	return err
}

// ResetPassword は、パスワード・ログインのセッション・API トークンを全部消す（serve -reset-password）。
// 消した後は、パスワードが無いので、セットアップコードからやり直す。
func (s *Store) ResetPassword(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_state WHERE key = ?`, keyPasswordHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM api_tokens`)
		return err
	})
}
