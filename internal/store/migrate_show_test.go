package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// 版 22 までの DB（番組・毎週の予約・1 回の予約・ジョブ・キーワードが足した放送回・当たらない日・通知の行を含む）を開くと、
// 版 23（0023_rename_shows.sql）で表と列が shows・show_id・show_name・show に変わり、行はそのまま読める。
// 外部キー（番組を消すと毎週と 1 回の予約も消える）と索引も新しい名前の表と列を指す。
func TestMigrateRenameShows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v22.db")
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
		if m.version > 22 {
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
		`INSERT INTO reservations (id, name, join_airings, split, check_title, enabled) VALUES (1, 'ふらっと', 0, 0, 1, 1), (2, 'ANN', 0, 0, 1, 1)`,
		`INSERT INTO weekly (reservation_id, position, station_id, weekday, start_hm) VALUES (1, 0, 'TBS', 1, '08:30'), (2, 0, 'LFR', 1, '01:00')`,
		`INSERT INTO keywords (id, words, words_key) VALUES (1, 'ふらっと', 'ふらっと')`,
		`INSERT INTO onces (id, reservation_id, station_id, start_at, keyword_id) VALUES (1, 1, 'TBS', '2026-10-20T23:30:00.000Z', 1)`,
		`INSERT INTO keyword_airings (keyword_id, station_id, start_at, end_at, title, reservation_name, added_at)
			VALUES (1, 'TBS', '2026-10-20T23:30:00.000Z', '2026-10-21T00:30:00.000Z', 'ふらっと', 'ふらっと', '2026-10-19T00:00:00.000Z')`,
		`INSERT INTO unmatched_days (reservation_name, station_id, day, start_at, active) VALUES ('ふらっと', 'TBS', '2026-10-27', '2026-10-26T23:30:00.000Z', 1)`,
		`INSERT INTO jobs (id, reservation_name, broadcast_day, state, next_attempt_at, deadline)
			VALUES (1, 'ふらっと', '2026-10-07', 'done', '2026-10-07T01:40:00.000Z', '2026-10-14T20:00:00.000Z')`,
		`INSERT INTO notifications (kind, message, created_at, deliver, reservation) VALUES ('unmatched_day', '当たらない', '2026-10-07T00:00:00.000Z', 0, 'ふらっと')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v < 23 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}

	flat, err := s.GetShow(ctx, "ふらっと")
	if err != nil {
		t.Fatal(err)
	}
	if len(flat.Weekly) != 1 || len(flat.Onces) != 1 || flat.Onces[0].KeywordID != 1 {
		t.Errorf("ふらっと = %+v", flat)
	}
	if jobs, err := s.ListJobs(ctx); err != nil || len(jobs) != 1 || jobs[0].ShowName != "ふらっと" {
		t.Errorf("jobs = %+v, %v", jobs, err)
	}
	if recs, err := s.KeywordAirings(ctx, 1); err != nil || len(recs) != 1 || recs[0].ShowName != "ふらっと" {
		t.Errorf("keyword_airings = %+v, %v", recs, err)
	}
	if days, err := s.UnmatchedDays(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil || len(days["ふらっと"]) != 1 {
		t.Errorf("unmatched_days = %+v, %v", days, err)
	}
	if ns, err := s.Notifications(ctx); err != nil || len(ns) != 1 || ns[0].Show != "ふらっと" {
		t.Errorf("notifications = %+v, %v", ns, err)
	}

	var names []string
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	for _, old := range []string{"reservations", "jobs_reservation_day"} {
		if slices.Contains(names, old) {
			t.Errorf("古い名前 %s が残っている: %v", old, names)
		}
	}
	for _, want := range []string{"shows", "jobs_show_day"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s が無い: %v", want, names)
		}
	}
	var oldCols int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name LIKE 'reservation%'`).Scan(&oldCols); err != nil || oldCols != 0 {
		t.Errorf("reservation で始まる列 = %d, %v", oldCols, err)
	}
	var bad int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil || bad != 0 {
		t.Errorf("foreign_key_check = %d, %v", bad, err)
	}

	// 番組を消すと、毎週と 1 回の予約の行も消える（外部キーが新しい名前の表と列を指している）。
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
	if ann, err := s.GetShow(ctx, "ANN"); err != nil || len(ann.Weekly) != 1 {
		t.Errorf("ANN = %+v, %v", ann, err)
	}
}
