package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
)

// 人が録る・録らないを決めるジョブの操作と、録らない予定の確定（ADR 0019）。

// 版 17 までの DB の skipped のジョブは、版 18 で確定した録らなかった（settled）になる。ほかのジョブは印なし・確定なしで、
// 放送回（job_airings）と試行（attempts）は残る。
func TestMigrateJobByPerson(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v17.db")
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
		if m.version > 17 {
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
			VALUES (1, 'ふらっと', '2026-10-07', 0, 'skipped', '2026-10-07T01:40:00.000Z', 0, '2026-10-14T20:00:00.000Z', ''),
			       (2, 'ふらっと', '2026-10-08', 0, 'done', '2026-10-08T01:40:00.000Z', 1, '2026-10-15T20:00:00.000Z', 'x.m4a'),
			       (3, 'ふらっと', '2026-10-09', 0, 'waiting', '2026-10-09T01:40:00.000Z', 0, '2026-10-16T20:00:00.000Z', '')`,
		`INSERT INTO job_airings (job_id, position, airing_key, station_id, start_at, end_at, title)
			VALUES (1, 0, 'TBS@2026-10-06T23:30:00Z', 'TBS', '2026-10-06T23:30:00.000Z', '2026-10-07T00:30:00.000Z', '特番'),
			       (2, 0, 'TBS@2026-10-07T23:30:00Z', 'TBS', '2026-10-07T23:30:00.000Z', '2026-10-08T00:30:00.000Z', 'ふらっと (1)')`,
		`INSERT INTO attempts (job_id, started_at, ended_at) VALUES (2, '2026-10-08T01:40:00.000Z', '2026-10-08T01:41:00.000Z')`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 18 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	if j, err := s.GetJob(ctx, 1); err != nil || j.State != domain.StateSkipped || !j.Settled || j.ByPerson ||
		len(j.Airings) != 1 || j.Airings[0].Title != "特番" {
		t.Errorf("skipped のジョブ = %+v, %v", j, err)
	}
	if j, err := s.GetJob(ctx, 2); err != nil || j.State != domain.StateDone || j.Settled || j.ByPerson || len(j.Airings) != 1 {
		t.Errorf("done のジョブ = %+v, %v", j, err)
	}
	if as, err := s.Attempts(ctx, 2); err != nil || len(as) != 1 {
		t.Errorf("attempts = %+v, %v", as, err)
	}
	if j, err := s.GetJob(ctx, 3); err != nil || j.State != domain.StateWaiting || j.Settled || j.ByPerson {
		t.Errorf("waiting のジョブ = %+v, %v", j, err)
	}
}

// insertSkipJob は、最初の試行の時刻が 10-05 10:05 のジョブを作る。
func insertSkipJob(t *testing.T, s *Store, j domain.Job) int64 {
	t.Helper()
	j.ShowName, j.Day, j.Airings = "x", day, []domain.Airing{flat1}
	j.NextAttemptAt, j.Deadline = jst(2026, 10, 5, 10, 5), jst(2026, 10, 13, 5, 0)
	id, err := s.InsertJob(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSkipByPerson(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	before, after := jst(2026, 10, 5, 6, 0), jst(2026, 10, 5, 12, 0)

	// 放送の前なら録らない予定（確定の前）、最初の試行の時刻を過ぎていれば録らなかった（確定）。どちらも人が決めた印が付く。
	id := insertSkipJob(t, s, domain.Job{State: domain.StateWaiting})
	if err := s.SkipByPerson(ctx, id, before); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateSkipped || !j.ByPerson || j.Settled {
		t.Errorf("放送の前: %+v", j)
	}
	id = insertSkipJob(t, s, domain.Job{State: domain.StateWaiting})
	if err := s.SkipByPerson(ctx, id, after); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateSkipped || !j.ByPerson || !j.Settled {
		t.Errorf("放送の後: %+v", j)
	}

	// 試行のある waiting と、waiting でないジョブは断る。
	for _, j := range []domain.Job{
		{State: domain.StateWaiting, Attempts: 1},
		{State: domain.StateRunning}, {State: domain.StateRetrying}, {State: domain.StateDone},
		{State: domain.StateExpired}, {State: domain.StateSkipped},
	} {
		id := insertSkipJob(t, s, j)
		if err := s.SkipByPerson(ctx, id, before); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("%s（試行 %d 回）: %v", j.State, j.Attempts, err)
		}
		if got, _ := s.GetJob(ctx, id); got.State != j.State || got.ByPerson {
			t.Errorf("断った後: %+v", got)
		}
	}
	if err := s.SkipByPerson(ctx, 9999, before); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}
}

func TestRecordByPerson(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 6, 0)

	// 確定の前（録らない予定）は waiting に戻し、人が決めた印を付ける。次の試行の時刻はそのまま。
	id := insertSkipJob(t, s, domain.Job{State: domain.StateSkipped})
	if err := s.RecordByPerson(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateWaiting || !j.ByPerson || j.Settled ||
		!j.NextAttemptAt.Equal(jst(2026, 10, 5, 10, 5)) {
		t.Errorf("確定の前: %+v", j)
	}
	// 確定した後（録らなかった）は手動再試行と同じく retrying にする。
	id = insertSkipJob(t, s, domain.Job{State: domain.StateSkipped, Settled: true})
	later := jst(2026, 10, 6, 12, 0)
	if err := s.RecordByPerson(ctx, id, later); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateRetrying || !j.NextAttemptAt.Equal(later) {
		t.Errorf("確定の後: %+v", j)
	}
	// 確定の後で録音期限を過ぎていれば断る。
	id = insertSkipJob(t, s, domain.Job{State: domain.StateSkipped, Settled: true})
	if err := s.RecordByPerson(ctx, id, jst(2026, 10, 13, 5, 0)); !errors.Is(err, ErrPastDeadline) {
		t.Errorf("録音期限を過ぎた: %v", err)
	}
	// skipped でないジョブは断る。
	for _, st := range []domain.State{domain.StateWaiting, domain.StateRunning, domain.StateRetrying, domain.StateDone, domain.StateExpired} {
		id := insertSkipJob(t, s, domain.Job{State: st})
		if err := s.RecordByPerson(ctx, id, now); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("%s: %v", st, err)
		}
	}
	if err := s.RecordByPerson(ctx, 9999, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}
}

// 手動再試行は、確定の前の skipped（録らない予定）を断る（放送の前に録りにいかないように）。
func TestManualRetryRefusesUnsettledSkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id := insertSkipJob(t, s, domain.Job{State: domain.StateSkipped})
	if err := s.ManualRetry(ctx, id, jst(2026, 10, 5, 6, 0)); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("録らない予定の再試行: %v", err)
	}
}

// 展開の差分は、確定の前の skipped の状態も変え、確定した skipped は変えない。
func TestExpandDayUnsettledSkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	unsettled := insertSkipJob(t, s, domain.Job{State: domain.StateSkipped})
	settled := insertSkipJob(t, s, domain.Job{State: domain.StateSkipped, Settled: true})
	_, _, err := s.ExpandDay(ctx, day, func(jobs []domain.Job) domain.Plan {
		var p domain.Plan
		for _, j := range jobs {
			j.State = domain.StateWaiting
			p.Update = append(p.Update, j)
		}
		return p
	})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, unsettled); j.State != domain.StateWaiting {
		t.Errorf("確定の前: %+v", j)
	}
	if j, _ := s.GetJob(ctx, settled); j.State != domain.StateSkipped {
		t.Errorf("確定の後: %+v", j)
	}
	// 消すのも、確定の前のものだけ。
	_, _, err = s.ExpandDay(ctx, day, func(jobs []domain.Job) domain.Plan { return domain.Plan{Delete: jobs} })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJob(ctx, unsettled); !errors.Is(err, ErrNotFound) {
		t.Errorf("確定の前（waiting に戻したもの）が消えない: %v", err)
	}
	if _, err := s.GetJob(ctx, settled); err != nil {
		t.Errorf("確定したものが消えた: %v", err)
	}
}

func TestSettleSkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id := insertSkipJob(t, s, domain.Job{State: domain.StateSkipped})
	insertSkipJob(t, s, domain.Job{State: domain.StateSkipped, Settled: true})
	insertSkipJob(t, s, domain.Job{State: domain.StateWaiting})
	if js, err := s.DueUnsettledSkipped(ctx, jst(2026, 10, 5, 10, 4)); err != nil || len(js) != 0 {
		t.Errorf("最初の試行の時刻の前 = %+v, %v", js, err)
	}
	js, err := s.DueUnsettledSkipped(ctx, jst(2026, 10, 5, 10, 5))
	if err != nil || len(js) != 1 || js[0].ID != id || len(js[0].Airings) != 1 {
		t.Fatalf("最初の試行の時刻 = %+v, %v", js, err)
	}
	if ok, err := s.SettleSkipped(ctx, id); err != nil || !ok {
		t.Fatalf("SettleSkipped = %v, %v", ok, err)
	}
	if j, _ := s.GetJob(ctx, id); !j.Settled {
		t.Errorf("確定していない: %+v", j)
	}
	if ok, err := s.SettleSkipped(ctx, id); err != nil || ok {
		t.Errorf("2 度目 = %v, %v", ok, err)
	}
}
