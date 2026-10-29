package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// SessionTTL はセッションの期限。
const SessionTTL = 30 * 24 * time.Hour

// CreateSession はランダムな id のセッションを作り、id と期限を返す。DB には id の SHA-256 の hash だけを置く（API トークンと同じ計算。cookie には元の id を渡す）。同時に、期限切れのセッションを消す。
func (s *Store) CreateSession(ctx context.Context, now time.Time) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	id := hex.EncodeToString(b)
	exp := now.Add(SessionTTL)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, formatTime(now)); err != nil {
		return "", time.Time{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id_hash, created_at, expires_at) VALUES (?, ?, ?)`,
		hashAPIToken(id), formatTime(now), formatTime(exp)); err != nil {
		return "", time.Time{}, err
	}
	return id, exp, nil
}

// SessionValid は id のセッションが now の時点で有効かを返す。期限切れなら消して false を返す。
func (s *Store) SessionValid(ctx context.Context, id string, now time.Time) (bool, error) {
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE id_hash = ?`, hashAPIToken(id)).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t, err := parseTime(exp)
	if err != nil {
		return false, err
	}
	if !t.After(now) {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, hashAPIToken(id))
		return false, err
	}
	return true, nil
}

// DeleteSession は id のセッションを消す。無くてもエラーにしない。
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, hashAPIToken(id))
	return err
}
