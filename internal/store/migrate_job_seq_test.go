package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
)

// migrateJobSeqFrom は版 21 まで当てた DB を作り、stmts を流してから閉じる。DB のパスと、流した後の
// jobs・job_airings・attempts の定義（schemaOf）と行（rowsOf）を返す。
func migrateJobSeqFrom(t *testing.T, stmts ...string) (path, schema, rows string) {
	t.Helper()
	ctx := context.Background()
	path = filepath.Join(t.TempDir(), "v21.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 21 {
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-10-01T00:00:00.000Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path, schemaOf(t, db), rowsOf(t, db)
}

// dump は q の結果を 1 行ずつ文字列にしてつなぐ。
func dump(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rs, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(vals...))
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// schemaOf は jobs・job_airings・attempts の列・索引・外部キーを並べる（AUTOINCREMENT は PRAGMA に出ない）。
func schemaOf(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, tbl := range []string{"jobs", "job_airings", "attempts"} {
		fmt.Fprintf(&b, "== %s\n%s\n", tbl, dump(t, db, `PRAGMA table_info(`+tbl+`)`))
		fmt.Fprintf(&b, "fk:\n%s\n", dump(t, db, `PRAGMA foreign_key_list(`+tbl+`)`))
		var names []string
		rs, err := db.Query(`SELECT name FROM pragma_index_list(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var n string
			if err := rs.Scan(&n); err != nil {
				t.Fatal(err)
			}
			names = append(names, n)
		}
		rs.Close()
		slices.Sort(names)
		for _, n := range names {
			fmt.Fprintf(&b, "index %s: %s\n%s\n", n,
				dump(t, db, `SELECT "unique", origin, partial FROM pragma_index_list('`+tbl+`') WHERE name = '`+n+`'`),
				dump(t, db, `PRAGMA index_info(`+n+`)`))
		}
	}
	return b.String()
}

// rowsOf は jobs・job_airings・attempts の行を全部並べる。
func rowsOf(t *testing.T, db *sql.DB) string {
	t.Helper()
	return dump(t, db, `SELECT * FROM jobs ORDER BY id`) + "\n--\n" +
		dump(t, db, `SELECT * FROM job_airings ORDER BY job_id, position`) + "\n--\n" +
		dump(t, db, `SELECT * FROM attempts ORDER BY id`)
}

func jobSeq(t *testing.T, s *Store) (int64, bool) {
	t.Helper()
	var seq int64
	err := s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'jobs'`).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return seq, true
}

func newJob() domain.Job {
	return domain.Job{ShowName: "番組", Day: domain.NewBroadcastDay(2026, 10, 20),
		NextAttemptAt: jst(2026, 10, 20, 12, 0), Deadline: jst(2026, 10, 27, 12, 0)}
}

// jobs を作り直しても、ジョブ・放送回・試行の行と列の定義はそのまま残り、外部キーの食い違いも無い。
// 作り直した後は、いちばん大きい id のジョブを消してから作ったジョブも別の id になる。
func TestMigrateJobAutoincrement(t *testing.T) {
	ctx := context.Background()
	path, schemaBefore, rowsBefore := migrateJobSeqFrom(t,
		`INSERT INTO jobs (id, reservation_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path,
			deadline_notified_at, by_person, settled)
			VALUES (1, 'ふらっと', '2026-10-07', 0, 'done', '2026-10-07T01:40:00.000Z', 2, '2026-10-14T20:00:00.000Z', 'a.m4a',
			        '2026-10-13T20:00:00.000Z', 1, 0),
			       (3, '別の番組', '2026-10-08', 1, 'skipped', '2026-10-08T01:40:00.000Z', 0, '2026-10-15T20:00:00.000Z', '',
			        '', 1, 1)`,
		`INSERT INTO job_airings (job_id, position, airing_key, program_id, station_id, start_at, end_at, title, performer,
			description, image_url, ts_in_ng, ts_out_ng, missing)
			VALUES (1, 0, 'LFR@2026-10-06T23:30:00Z', 'p1', 'LFR', '2026-10-06T23:30:00.000Z', '2026-10-07T00:30:00.000Z',
			        'ふらっと (1)', '出演', '説明', 'http://img', 1, 0, 0),
			       (1, 1, 'LFR@2026-10-07T00:40:00Z', '', 'LFR', '2026-10-07T00:40:00.000Z', '2026-10-07T01:30:00.000Z',
			        'ふらっと (2)', '', '', '', 0, 1, 1),
			       (3, 0, 'TBS@2026-10-07T23:30:00Z', '', 'TBS', '2026-10-07T23:30:00.000Z', '2026-10-08T00:30:00.000Z',
			        '別', '', '', '', 0, 0, 0)`,
		`INSERT INTO attempts (id, job_id, started_at, ended_at, failed_stage, error, extinf_total_ms, probe_duration_ms)
			VALUES (5, 1, '2026-10-07T01:40:00.000Z', '2026-10-07T01:41:00.000Z', 'fetch', 'だめ', 0, 0),
			       (8, 1, '2026-10-07T02:40:00.000Z', '2026-10-07T02:45:00.000Z', '', '', 3600000, 3599000),
			       (9, 3, '2026-10-08T01:40:00.000Z', '', '', '', 0, 0)`,
		`INSERT INTO notifications (kind, message, created_at, deliver, job_id) VALUES ('recorded', '録れた', '2026-10-07T02:45:00.000Z', 1, 1)`,
	)

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v < 22 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	// 版 24（0024_job_chapters.sql）は列を足すだけなので、足した列を落としてから比べる。
	for _, q := range []string{
		`ALTER TABLE jobs DROP COLUMN chapters`, `ALTER TABLE jobs DROP COLUMN chapters_check_at`,
		`ALTER TABLE jobs DROP COLUMN chapters_refetch`, `ALTER TABLE job_airings DROP COLUMN chapter_wait`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got := rowsOf(t, s.db); got != rowsBefore {
		t.Errorf("行が変わった:\n%s\nwant\n%s", got, rowsBefore)
	}
	// 版 23（0023_rename_shows.sql）は列 reservation_name と索引 jobs_reservation_day の名前だけを変えるので、版 21 の定義をその名前に読み替えて比べる。
	schemaBefore = strings.NewReplacer("reservation_name", "show_name", "jobs_reservation_day", "jobs_show_day").Replace(schemaBefore)
	if got := schemaOf(t, s.db); got != schemaBefore {
		t.Errorf("定義が変わった:\n%s\nwant\n%s", got, schemaBefore)
	}
	// 落とした列を足し直す（下でジョブを作るため）。
	if b, err := migrationFS.ReadFile("migrations/0024_job_chapters.sql"); err != nil {
		t.Fatal(err)
	} else if _, err := s.db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	if got := dump(t, s.db, `PRAGMA foreign_key_check`); got != "" {
		t.Errorf("foreign_key_check = %q", got)
	}
	var def string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'jobs'`).Scan(&def); err != nil ||
		!strings.Contains(def, "AUTOINCREMENT") {
		t.Errorf("jobs の定義 = %q, %v", def, err)
	}
	for _, tbl := range []string{"jobs_new", "job_airings_new", "attempts_new"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s が残っている: %d, %v", tbl, n, err)
		}
	}
	if seq, ok := jobSeq(t, s); !ok || seq != 3 {
		t.Errorf("sqlite_sequence の jobs = %d, %v; want 3", seq, ok)
	}
	if as, err := s.Attempts(ctx, 1); err != nil || len(as) != 2 {
		t.Errorf("attempts = %+v, %v", as, err)
	}

	// 子の行は作り直した jobs を参照していて、ジョブを消すと一緒に消える。
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM job_airings WHERE job_id = 3) + (SELECT COUNT(*) FROM attempts WHERE job_id = 3)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("消したジョブの子の行 = %d, %v", n, err)
	}
	// いちばん大きい id（3）のジョブを消してから作っても、3 を使い回さない。
	id, err := s.InsertJob(ctx, newJob())
	if err != nil || id != 4 {
		t.Errorf("InsertJob = %d, %v; want 4", id, err)
	}
}

// 通知の job_id だけが覚えている、今あるジョブより大きい id（作り直す前に消えていたジョブ）は、
// 移行の後に作るジョブに振らない。
func TestMigrateJobAutoincrementNotificationJobID(t *testing.T) {
	ctx := context.Background()
	path, _, _ := migrateJobSeqFrom(t,
		`INSERT INTO jobs (id, reservation_name, broadcast_day, state, next_attempt_at, deadline)
			VALUES (1, 'a', '2026-10-07', 'done', '2026-10-07T01:40:00.000Z', '2026-10-14T20:00:00.000Z'),
			       (2, 'a', '2026-10-08', 'done', '2026-10-08T01:40:00.000Z', '2026-10-15T20:00:00.000Z')`,
		`INSERT INTO notifications (kind, message, created_at, deliver, job_id)
			VALUES ('recorded', '消えたジョブ', '2026-10-07T02:45:00.000Z', 1, 7),
			       ('recorded', 'ある', '2026-10-08T02:45:00.000Z', 1, 2),
			       ('sync', '異常', '2026-10-08T02:45:00.000Z', 1, NULL)`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if seq, ok := jobSeq(t, s); !ok || seq != 7 {
		t.Errorf("sqlite_sequence の jobs = %d, %v; want 7", seq, ok)
	}
	if id, err := s.InsertJob(ctx, newJob()); err != nil || id != 8 {
		t.Errorf("InsertJob = %d, %v; want 8", id, err)
	}
}

// ジョブが 0 件の DB にも移行が当たる。通知の job_id があればその次から振り、どちらも無ければ 1 から振る。
// 通知の job_id があるときに確かめているのは、sqlite_sequence に jobs の行（0）があって上げる道だけ（0 行の
// INSERT … SELECT でも行ができるので、行が無いときに作る道は通らない）。
func TestMigrateJobAutoincrementNoJobs(t *testing.T) {
	ctx := context.Background()
	t.Run("通知の job_id がある", func(t *testing.T) {
		path, _, _ := migrateJobSeqFrom(t,
			`INSERT INTO notifications (kind, message, created_at, deliver, job_id)
				VALUES ('recorded', '消えたジョブ', '2026-10-07T02:45:00.000Z', 1, 4)`,
		)
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if seq, ok := jobSeq(t, s); !ok || seq != 4 {
			t.Errorf("sqlite_sequence の jobs = %d, %v; want 4", seq, ok)
		}
		if id, err := s.InsertJob(ctx, newJob()); err != nil || id != 5 {
			t.Errorf("InsertJob = %d, %v; want 5", id, err)
		}
	})
	t.Run("どちらも無い", func(t *testing.T) {
		path, _, _ := migrateJobSeqFrom(t,
			`INSERT INTO notifications (kind, message, created_at, deliver) VALUES ('sync', '異常', '2026-10-07T02:45:00.000Z', 1)`,
		)
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		// 0 行の INSERT … SELECT でも、sqlite_sequence に jobs の行（0）ができる。
		if seq, ok := jobSeq(t, s); !ok || seq != 0 {
			t.Errorf("sqlite_sequence の jobs = %d, %v; want 0", seq, ok)
		}
		if id, err := s.InsertJob(ctx, newJob()); err != nil || id != 1 {
			t.Errorf("InsertJob = %d, %v; want 1", id, err)
		}
	})
}
