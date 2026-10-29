package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAPITokens(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

	tok, secret, err := s.CreateAPIToken(ctx, "手元の CLI", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, APITokenPrefix) || len(secret) < 40 || tok.Name != "手元の CLI" {
		t.Errorf("tok %+v, secret %q", tok, secret)
	}

	// DB にトークンそのものが残らない（hash だけ）。
	var name, hash, created, used string
	if err := s.db.QueryRowContext(ctx, `SELECT name, token_hash, created_at, IFNULL(last_used_at, '') FROM api_tokens`).
		Scan(&name, &hash, &created, &used); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{name, hash, created, used} {
		if strings.Contains(v, secret) {
			t.Errorf("DB にトークンそのものが残っている: %q", v)
		}
	}
	if hash != hashAPIToken(secret) || hash == secret {
		t.Errorf("token_hash = %q", hash)
	}

	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || !list[0].LastUsedAt.IsZero() || !list[0].CreatedAt.Equal(now) {
		t.Fatalf("list = %+v, %v", list, err)
	}

	if ok, err := s.APITokenValid(ctx, "rjk_unknown", now); err != nil || ok {
		t.Errorf("知らないトークン: %v %v", ok, err)
	}
	later := now.Add(time.Hour)
	if ok, err := s.APITokenValid(ctx, secret, later); err != nil || !ok {
		t.Fatalf("有効なはず: %v %v", ok, err)
	}
	list, _ = s.ListAPITokens(ctx)
	if !list[0].LastUsedAt.Equal(later) {
		t.Errorf("LastUsedAt = %v", list[0].LastUsedAt)
	}

	if err := s.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.APITokenValid(ctx, secret, later); ok {
		t.Error("失効したトークンが有効")
	}
	if err := s.DeleteAPIToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("2 回目の失効 = %v", err)
	}
}
