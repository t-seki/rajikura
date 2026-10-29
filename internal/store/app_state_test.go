package store

import (
	"context"
	"testing"
	"time"
)

func TestLastSavedAt(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if got, err := s.LastSavedAt(ctx); err != nil || !got.IsZero() {
		t.Fatalf("初期 = %v, %v", got, err)
	}
	at := jst(2026, 10, 15, 12, 0)
	if err := s.SetLastSavedAt(ctx, at); err != nil {
		t.Fatal(err)
	}
	at2 := at.Add(time.Hour)
	if err := s.SetLastSavedAt(ctx, at2); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LastSavedAt(ctx); err != nil || !got.Equal(at2) {
		t.Fatalf("got = %v, %v, want %v", got, err, at2)
	}
}
