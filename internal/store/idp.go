package store

import (
	"context"
	"database/sql"
	"errors"
)

// IdP のログインの設定と紐づけ（docs/adr/0040-idp-login-by-generic-oidc.md）。どちらも app_state に置き、新しい表は持たない。
// 中身（JSON）は解釈しない（internal/server の idp.go が読む）。client secret は暗号化して secrets の表に置く（名前は呼び出し側が渡す）。
// パスワードを戻す（ResetPassword）と紐づけは消え、IdP の設定と client secret は残る（docs/adr/0041-first-password-by-setup-code-and-reset-clears-binding.md）。
const (
	// keyIdPSettings は、IdP の設定（issuer・client id・Web UI の URL。client secret を除く）の JSON の app_state のキー。
	keyIdPSettings = "idp_settings"
	// keyIdPBinding は、紐づけ（iss・sub・表示用の email）の JSON の app_state のキー。
	keyIdPBinding = "idp_binding"
)

func (s *Store) appState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// IdPSettings は、IdP の設定の JSON と、秘密 secretName（client secret）の暗号文を返す。無ければ空と nil。
func (s *Store) IdPSettings(ctx context.Context, secretName string) (body string, sealed []byte, err error) {
	body, err = s.appState(ctx, keyIdPSettings)
	if err != nil {
		return "", nil, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE name = ?`, secretName).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return body, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return body, sealed, nil
}

// IdPSave は SaveIdPSettings に渡す変え方。
type IdPSave struct {
	// Body は IdP の設定の JSON。空なら設定を消す。
	Body string
	// SecretName は client secret の秘密の名前。
	SecretName string
	// SetSecret が true なら client secret を Sealed にする（Sealed が nil なら消す）。false なら今のまま残す。
	SetSecret bool
	Sealed    []byte
	// ClearBinding が true なら紐づけも消す（issuer か client id を変えたとき）。
	ClearBinding bool
}

// SaveIdPSettings は IdP の設定を置き換える。1 つのトランザクションで当てる（途中で失敗したら何も変えない）。
func (s *Store) SaveIdPSettings(ctx context.Context, v IdPSave) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if v.Body == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM app_state WHERE key = ?`, keyIdPSettings)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO app_state (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, keyIdPSettings, v.Body)
		}
		if err != nil {
			return err
		}
		if v.SetSecret {
			if v.Sealed == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, v.SecretName)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO secrets (name, sealed) VALUES (?, ?)
					ON CONFLICT (name) DO UPDATE SET sealed = excluded.sealed`, v.SecretName, v.Sealed)
			}
			if err != nil {
				return err
			}
		}
		if v.ClearBinding {
			_, err = tx.ExecContext(ctx, `DELETE FROM app_state WHERE key = ?`, keyIdPBinding)
		}
		return err
	})
}

// IdPBinding は紐づけの JSON を返す。紐づけが無ければ空。
func (s *Store) IdPBinding(ctx context.Context) (string, error) {
	return s.appState(ctx, keyIdPBinding)
}

// SetIdPBinding は紐づけを body（JSON）にする（前の紐づけは置き換える。紐づけは 1 つだけ）。
func (s *Store) SetIdPBinding(ctx context.Context, body string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_state (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, keyIdPBinding, body)
	return err
}

// DeleteIdPBinding は紐づけを消す（無くてもよい）。
func (s *Store) DeleteIdPBinding(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_state WHERE key = ?`, keyIdPBinding)
	return err
}
