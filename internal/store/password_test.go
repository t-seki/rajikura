package store

import (
	"context"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if got, err := s.PasswordHash(ctx); err != nil || got != "" {
		t.Fatalf("初期 = %q, %v", got, err)
	}
	if err := s.SetPasswordHash(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasswordHash(ctx, "h2"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PasswordHash(ctx); err != nil || got != "h2" {
		t.Fatalf("got = %q, %v", got, err)
	}
}

// ResetPassword は、パスワード・セッション・API トークンを消し、ほかの app_state（最後に保存した時刻）は残す。
func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if err := s.SetPasswordHash(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastSavedAt(ctx, now); err != nil {
		t.Fatal(err)
	}
	sid, _, err := s.CreateSession(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	_, tok, err := s.CreateAPIToken(ctx, "cli", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ResetPassword(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PasswordHash(ctx); err != nil || got != "" {
		t.Errorf("パスワード = %q, %v（消えるはず）", got, err)
	}
	if ok, err := s.SessionValid(ctx, sid, now); err != nil || ok {
		t.Errorf("セッション = %v, %v（消えるはず）", ok, err)
	}
	if ok, err := s.APITokenValid(ctx, tok, now); err != nil || ok {
		t.Errorf("API トークン = %v, %v（消えるはず）", ok, err)
	}
	if got, err := s.LastSavedAt(ctx); err != nil || !got.Equal(now) {
		t.Errorf("最後に保存した時刻 = %v, %v（残るはず）", got, err)
	}
}
