package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// APITokenPrefix は API トークンの先頭に付ける文字列。
const APITokenPrefix = "rjk_"

// APIToken は発行した API トークンの記録。トークンそのものは持たない。
type APIToken struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt time.Time // 一度も使っていなければゼロ値
}

func hashAPIToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateAPIToken は API トークンを発行する。返すトークンそのものは DB に残さない（hash だけ）ので、ここで 1 回だけ得られる。
func (s *Store) CreateAPIToken(ctx context.Context, name string, now time.Time) (APIToken, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return APIToken{}, "", err
	}
	token := APITokenPrefix + hex.EncodeToString(b)
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens (name, token_hash, created_at) VALUES (?, ?, ?)`,
		name, hashAPIToken(token), formatTime(now))
	if err != nil {
		return APIToken{}, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return APIToken{}, "", err
	}
	created, err := parseTime(formatTime(now))
	if err != nil {
		return APIToken{}, "", err
	}
	return APIToken{ID: id, Name: name, CreatedAt: created}, token, nil
}

// ListAPITokens は発行した API トークンを、新しい順に返す。
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_used_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var created string
		var used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &created, &used); err != nil {
			return nil, err
		}
		if t.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if used.Valid {
			if t.LastUsedAt, err = parseTime(used.String); err != nil {
				return nil, err
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken は API トークンを失効させる。無ければ ErrNotFound。
func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// APITokenValid は token が発行済みで失効していないかを返す。有効なら最後に使った日時を now にする。
func (s *Store) APITokenValid(ctx context.Context, token string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`,
		formatTime(now), hashAPIToken(token))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
