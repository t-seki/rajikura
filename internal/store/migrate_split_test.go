package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 版 19 までの DB の番組は、版 20 で分割番組の設定（ADR 0003）に移る。split がオンなら join_airings に従って
// つなぐ・つながないに、オフなら join_airings によらず分割番組でないにする。毎週・1 回の予約（番組の子の行）は残る。
func TestMigrateSplitMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v19.db")
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
		if m.version > 19 {
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-10-01T00:00:00.000Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO reservations (id, name, join_airings, split, check_title, enabled) VALUES
			(1, 'つなぐ', 1, 1, 1, 1),
			(2, 'つながない', 0, 1, 1, 1),
			(3, '分割番組でない', 1, 0, 1, 1),
			(4, '分割番組でない・連結しない', 0, 0, 0, 0)`,
		`INSERT INTO weekly (reservation_id, position, station_id, weekday, start_hm, from_day) VALUES
			(1, 0, 'TBS', 1, '08:30', NULL), (1, 1, 'TBS', 1, '10:00', NULL),
			(2, 0, 'TBS', 2, '08:30', NULL),
			(3, 0, 'LFR', 3, '01:00', '2026-10-01'),
			(4, 0, 'QRR', 4, '22:00', NULL)`,
		`INSERT INTO onces (id, reservation_id, station_id, start_at) VALUES
			(1, 1, 'TBS', '2026-10-20T23:30:00.000Z'),
			(2, 3, 'LFR', '2026-10-21T16:00:00.000Z'),
			(3, 4, 'QRR', '2026-10-22T13:00:00.000Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v < 20 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	for _, tt := range []struct {
		name   string
		split  domain.SplitMode
		weekly int
		onces  int
	}{
		{"つなぐ", domain.SplitJoin, 2, 1},
		{"つながない", domain.SplitSeparate, 1, 0},
		{"分割番組でない", domain.SplitNone, 1, 1},
		{"分割番組でない・連結しない", domain.SplitNone, 1, 1},
	} {
		r, err := s.GetShow(ctx, tt.name)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if r.Split != tt.split || len(r.Weekly) != tt.weekly || len(r.Onces) != tt.onces {
			t.Errorf("%s = %+v, want split=%v weekly=%d onces=%d", tt.name, r, tt.split, tt.weekly, tt.onces)
		}
	}
	if r, err := s.GetShow(ctx, "分割番組でない"); err != nil || r.Weekly[0].From.String() != "2026-10-01" ||
		!r.Onces[0].Start.Equal(time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("子の行の中身 = %+v, %v", r, err)
	}
	if r, err := s.GetShow(ctx, "分割番組でない・連結しない"); err != nil || r.CheckTitle || !r.Disabled {
		t.Errorf("ほかの列 = %+v, %v", r, err)
	}
	var join int
	if err := s.db.QueryRowContext(ctx, `SELECT join_airings FROM shows WHERE name = '分割番組でない'`).Scan(&join); err != nil || join != 0 {
		t.Errorf("分割番組でない番組の join_airings = %d, %v", join, err)
	}
}
