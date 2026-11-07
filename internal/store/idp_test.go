package store

import (
	"bytes"
	"context"
	"testing"
)

const testIdPSecret = "idp.client_secret"

func TestIdPSettingsAndBinding(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if body, sealed, err := s.IdPSettings(ctx, testIdPSecret); err != nil || body != "" || sealed != nil {
		t.Fatalf("初期 = %q, %v, %v", body, sealed, err)
	}
	if b, err := s.IdPBinding(ctx); err != nil || b != "" {
		t.Fatalf("初期の紐づけ = %q, %v", b, err)
	}
	if err := s.SaveIdPSettings(ctx, IdPSave{Body: `{"issuer":"a"}`, SecretName: testIdPSecret, SetSecret: true, Sealed: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdPBinding(ctx, `{"sub":"1"}`); err != nil {
		t.Fatal(err)
	}
	// client secret を変えない保存は、client secret を残す。紐づけを消さない保存は、紐づけを残す。
	if err := s.SaveIdPSettings(ctx, IdPSave{Body: `{"issuer":"b"}`, SecretName: testIdPSecret}); err != nil {
		t.Fatal(err)
	}
	body, sealed, err := s.IdPSettings(ctx, testIdPSecret)
	if err != nil || body != `{"issuer":"b"}` || !bytes.Equal(sealed, []byte("x")) {
		t.Fatalf("保存の後 = %q, %q, %v", body, sealed, err)
	}
	if b, err := s.IdPBinding(ctx); err != nil || b != `{"sub":"1"}` {
		t.Fatalf("紐づけ = %q, %v（残るはず）", b, err)
	}
	// 紐づけを消す保存と、client secret を消す保存。
	if err := s.SaveIdPSettings(ctx, IdPSave{Body: `{"issuer":"c"}`, SecretName: testIdPSecret, SetSecret: true, ClearBinding: true}); err != nil {
		t.Fatal(err)
	}
	if b, err := s.IdPBinding(ctx); err != nil || b != "" {
		t.Errorf("紐づけ = %q, %v（消えるはず）", b, err)
	}
	if _, sealed, err := s.IdPSettings(ctx, testIdPSecret); err != nil || sealed != nil {
		t.Errorf("client secret = %q, %v（消えるはず）", sealed, err)
	}
	if err := s.SetIdPBinding(ctx, `{"sub":"2"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIdPBinding(ctx); err != nil {
		t.Fatal(err)
	}
	if b, err := s.IdPBinding(ctx); err != nil || b != "" {
		t.Errorf("外した後の紐づけ = %q, %v", b, err)
	}
	// 空の本文は設定を消す。
	if err := s.SaveIdPSettings(ctx, IdPSave{SecretName: testIdPSecret}); err != nil {
		t.Fatal(err)
	}
	if body, _, err := s.IdPSettings(ctx, testIdPSecret); err != nil || body != "" {
		t.Errorf("消した後の設定 = %q, %v", body, err)
	}
}

// ResetPassword は紐づけを消し、IdP の設定と client secret は残す（ADR 0041）。
func TestResetPasswordClearsIdPBindingKeepsSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.SetPasswordHash(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIdPSettings(ctx, IdPSave{Body: `{"issuer":"a"}`, SecretName: testIdPSecret, SetSecret: true, Sealed: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdPBinding(ctx, `{"sub":"1"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx); err != nil {
		t.Fatal(err)
	}
	if b, err := s.IdPBinding(ctx); err != nil || b != "" {
		t.Errorf("紐づけ = %q, %v（消えるはず）", b, err)
	}
	body, sealed, err := s.IdPSettings(ctx, testIdPSecret)
	if err != nil || body != `{"issuer":"a"}` || !bytes.Equal(sealed, []byte("x")) {
		t.Errorf("IdP の設定 = %q, %q, %v（残るはず）", body, sealed, err)
	}
}
