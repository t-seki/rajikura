package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 最後に自動のバックアップを取れた時刻は、記録するまで零値で、記録した値を返し、零値を記録すると忘れる。
func TestLastBackupAt(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if at, err := s.LastBackupAt(ctx); err != nil || !at.IsZero() {
		t.Fatalf("記録する前 = %v, %v", at, err)
	}
	want := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	if err := s.SetLastBackupAt(ctx, want); err != nil {
		t.Fatal(err)
	}
	if at, err := s.LastBackupAt(ctx); err != nil || !at.Equal(want) {
		t.Fatalf("記録した後 = %v, %v、want %v", at, err, want)
	}
	if err := s.SetLastBackupAt(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if at, err := s.LastBackupAt(ctx); err != nil || !at.IsZero() {
		t.Fatalf("忘れた後 = %v, %v", at, err)
	}
}
