package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 版 8 までの DB（番組・毎週の予約・1 回の予約・ジョブ・試行の行を含む）を開くと、版 9 で表と列が
// reservations・weekly・onces・reservation_id・reservation_name に変わり、版 23（0023_rename_shows.sql）で
// shows・show_id・show_name に戻る。行はそのまま読める。
// 外部キー（番組を消すと毎週と 1 回の予約も消える）と索引も新しい名前の表を指す。
func TestMigrateRenameReservations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8.db")
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
		if m.version > 8 {
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
		`INSERT INTO shows (id, name, join_airings, split, check_title, enabled) VALUES (1, 'ふらっと', 1, 1, 0, 0), (2, 'ANN', 0, 0, 1, 1)`,
		`INSERT INTO rules (show_id, position, station_id, weekday, start_hm) VALUES (1, 0, 'TBS', 1, '08:30'), (2, 0, 'LFR', 1, '01:00')`,
		`INSERT INTO oneshots (show_id, station_id, start_at) VALUES (1, 'TBS', '2026-10-07T01:10:00.000Z')`,
		`INSERT INTO jobs (id, show_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path)
			VALUES (1, 'ふらっと', '2026-10-07', 0, 'done', '2026-10-07T01:40:00.000Z', 1, '2026-10-14T20:00:00.000Z', 'x.m4a')`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 9 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}

	flat, err := s.GetShow(ctx, "ふらっと")
	if err != nil {
		t.Fatal(err)
	}
	if flat.Split != domain.SplitJoin || flat.CheckTitle || !flat.Disabled {
		t.Errorf("ふらっと = %+v, want Split=SplitJoin CheckTitle=false Disabled=true", flat)
	}
	if want := []domain.Weekly{domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: 8, Minute: 30})}; !slices.Equal(flat.Weekly, want) {
		t.Errorf("ふらっと の毎週の予約 = %+v, want %+v", flat.Weekly, want)
	}
	if len(flat.Onces) != 1 || flat.Onces[0].StationID != "TBS" || !flat.Onces[0].Start.Equal(jst(2026, 10, 7, 10, 10)) {
		t.Errorf("ふらっと の 1 回の予約 = %+v", flat.Onces)
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ShowName != "ふらっと" || jobs[0].State != domain.StateDone ||
		len(jobs[0].Airings) != 1 || jobs[0].Airings[0].Title != "ふらっと (1)" || jobs[0].OutputPath != "x.m4a" {
		t.Errorf("jobs = %+v", jobs)
	}
	if as, err := s.Attempts(ctx, 1); err != nil || len(as) != 1 {
		t.Errorf("attempts = %+v, %v", as, err)
	}

	// 古い名前の表と索引は残らず、新しい名前の索引がある。
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	for _, old := range []string{"rules", "oneshots", "oneshots_start", "reservations", "jobs_reservation_day"} {
		if slices.Contains(names, old) {
			t.Errorf("古い名前 %s が残っている: %v", old, names)
		}
	}
	for _, want := range []string{"shows", "weekly", "onces", "onces_start", "jobs_show_day"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s が無い: %v", want, names)
		}
	}
	var bad int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil || bad != 0 {
		t.Errorf("foreign_key_check = %d, %v", bad, err)
	}

	// 番組を消すと、毎週と 1 回の予約の行も消える（外部キーが新しい名前の表を指している）。ジョブは残る。
	if err := s.DeleteShow(ctx, "ふらっと"); err != nil {
		t.Fatal(err)
	}
	var weekly, onces int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM weekly WHERE show_id = 1`).Scan(&weekly); err != nil || weekly != 0 {
		t.Errorf("番組を消した後の毎週の予約 = %d, %v", weekly, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM onces`).Scan(&onces); err != nil || onces != 0 {
		t.Errorf("番組を消した後の 1 回の予約 = %d, %v", onces, err)
	}
	if ann, err := s.GetShow(ctx, "ANN"); err != nil || len(ann.Weekly) != 1 || ann.Split != domain.SplitNone {
		t.Errorf("ANN = %+v, %v", ann, err)
	}
	if jobs, err := s.ListJobs(ctx); err != nil || len(jobs) != 1 {
		t.Errorf("番組を消した後の jobs = %+v, %v", jobs, err)
	}
}
