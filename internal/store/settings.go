package store

import (
	"context"
	"database/sql"
	"errors"
)

// Settings は DB に置いた設定（秘密を除く値の JSON）と、暗号化した秘密（名前 → 暗号文）を返す。
// まだ保存していなければ、body は空。中身は解釈しない（internal/app の Settings が読む）。
func (s *Store) Settings(ctx context.Context) (body string, secrets map[string][]byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT body FROM settings WHERE id = 1`).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		body, err = "", nil
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name, sealed FROM secrets ORDER BY name`)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	secrets = map[string][]byte{}
	for rows.Next() {
		var name string
		var sealed []byte
		if err := rows.Scan(&name, &sealed); err != nil {
			return "", nil, err
		}
		secrets[name] = sealed
	}
	return body, secrets, rows.Err()
}

// SaveSettings は設定の値 body を置き換え、秘密 secrets（名前 → 暗号文。nil の暗号文は消す）を当てる。
// 1 つのトランザクションで当てる（途中で失敗したら何も変えない）。secrets に無い秘密は今のまま残す。
func (s *Store) SaveSettings(ctx context.Context, body string, secrets map[string][]byte) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (id, body) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET body = excluded.body`, body); err != nil {
			return err
		}
		for name, sealed := range secrets {
			var err error
			if sealed == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO secrets (name, sealed) VALUES (?, ?)
					ON CONFLICT (name) DO UPDATE SET sealed = excluded.sealed`, name, sealed)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}
