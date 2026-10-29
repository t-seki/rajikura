package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
)

// 版 10 までの DB の skipped のジョブは、版 12 で expired になる。放送回（job_airings）と試行（attempts）は残る。
func TestMigrateDropJobSkip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v10.db")
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
		if m.version > 10 {
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
		`INSERT INTO jobs (id, reservation_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path)
			VALUES (1, 'ふらっと', '2026-10-07', 0, 'skipped', '2026-10-07T01:40:00.000Z', 1, '2026-10-14T20:00:00.000Z', ''),
			       (2, 'ふらっと', '2026-10-08', 0, 'done', '2026-10-08T01:40:00.000Z', 1, '2026-10-15T20:00:00.000Z', 'x.m4a')`,
		`INSERT INTO job_airings (job_id, position, airing_key, station_id, start_at, end_at, title)
			VALUES (1, 0, 'TBS@2026-10-06T23:30:00Z', 'TBS', '2026-10-06T23:30:00.000Z', '2026-10-07T00:30:00.000Z', 'ふらっと (1)')`,
		`INSERT INTO attempts (job_id, started_at, ended_at) VALUES (1, '2026-10-07T01:40:00.000Z', '2026-10-07T01:41:00.000Z')`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 12 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	j, err := s.GetJob(ctx, 1)
	if err != nil || j.State != domain.StateExpired || len(j.Airings) != 1 || j.Airings[0].Title != "ふらっと (1)" {
		t.Errorf("skipped だったジョブ = %+v, %v", j, err)
	}
	if as, err := s.Attempts(ctx, 1); err != nil || len(as) != 1 {
		t.Errorf("attempts = %+v, %v", as, err)
	}
	if j, err := s.GetJob(ctx, 2); err != nil || j.State != domain.StateDone {
		t.Errorf("done のジョブ = %+v, %v", j, err)
	}
}
