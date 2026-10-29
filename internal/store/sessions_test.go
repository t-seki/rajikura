package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

	id, exp, err := s.CreateSession(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) < 32 || !exp.Equal(now.Add(30*24*time.Hour)) {
		t.Errorf("id %q, exp %v", id, exp)
	}
	if ok, err := s.SessionValid(ctx, id, now.Add(time.Hour)); err != nil || !ok {
		t.Errorf("有効なはず: %v %v", ok, err)
	}
	if ok, _ := s.SessionValid(ctx, "unknown", now); ok {
		t.Error("知らない id が有効")
	}
	if ok, _ := s.SessionValid(ctx, id, exp); ok {
		t.Error("期限ちょうどで有効")
	}
	// 期限切れを確かめたら消えるので、時刻を戻しても有効に戻らない。
	if ok, _ := s.SessionValid(ctx, id, now); ok {
		t.Error("期限切れを確かめたら消えるはず")
	}

	id2, _, _ := s.CreateSession(ctx, now)
	if err := s.DeleteSession(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, id2, now); ok {
		t.Error("消したセッションが有効")
	}
	if err := s.DeleteSession(ctx, id2); err != nil {
		t.Errorf("無いセッションを消してエラー: %v", err)
	}

	// 作るときに、期限切れのセッションを消す。
	old, _, _ := s.CreateSession(ctx, now)
	if _, _, err := s.CreateSession(ctx, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, old, now); ok {
		t.Error("期限切れが作成時に消えていない")
	}
}

// DB にはセッションの id そのものでなく SHA-256 の hash を置く。
func TestSessionIDStoredAsHash(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	id, _, err := s.CreateSession(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.db.QueryRowContext(ctx, `SELECT id_hash FROM sessions`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == id || got != hashAPIToken(id) {
		t.Errorf("id_hash = %q（id = %q）", got, id)
	}
	// hash を cookie の値として出されても通らない。
	if ok, _ := s.SessionValid(ctx, got, now); ok {
		t.Error("hash そのものでセッションが有効になった")
	}
}

// 版 18 までの DB のセッションは、版 19 で消える。
func TestMigrateSessionsHash(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v18.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 18 {
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-10-01T00:00:00.000Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions (id, created_at, expires_at) VALUES ('oldid', '2026-10-01T00:00:00.000Z', '2099-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v < 19 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions の行 = %d, %v", n, err)
	}
	if ok, _ := s.SessionValid(ctx, "oldid", time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)); ok {
		t.Error("置き換え前のセッションが有効")
	}
}
