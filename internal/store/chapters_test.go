package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// チャプターの値と待ちは、保存（Complete）で書き、待っているジョブを引く時刻の順に読む（DueChapterChecks）。保存したファイルに
// 書き込んだら（FinishChapterWrite）、done のまま値を付け直して待ちを消し、書き込みの履歴に 1 行足す（状態・試行回数・保存先・
// 欠落の印は変えない。ADR 0038）。人の録り直しは待ちを消す（ADR 0027）。
func TestChapterWaitFlow(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 11, 10)
	insert := func(t *testing.T) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, State: domain.StateRunning, Airings: []domain.Airing{news, flat2},
			Attempts: 1, NextAttemptAt: now, Deadline: jst(2026, 10, 12, 5, 0),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	get := func(t *testing.T, id int64) domain.Job {
		t.Helper()
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	id := insert(t)
	waits := map[string]domain.AiringChapterWait{news.Key(): domain.AiringChapterPending}
	check := now.Add(30 * time.Minute)
	if err := s.Complete(ctx, id, "/lib/x.m4a", []domain.Airing{flat2}, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check, Waits: waits}); err != nil {
		t.Fatal(err)
	}
	j := get(t, id)
	if j.State != domain.StateDone || j.Chapters != domain.ChaptersWaiting || !j.ChapterCheckAt.Equal(check) || !maps.Equal(j.ChapterWaits, waits) {
		t.Fatalf("保存の後: %+v", j)
	}
	if due, err := s.DueChapterChecks(ctx, check.Add(-time.Second)); err != nil || len(due) != 0 {
		t.Errorf("引く時刻の前: %+v, %v", due, err)
	}
	if due, err := s.DueChapterChecks(ctx, check); err != nil || len(due) != 1 || due[0].ID != id {
		t.Errorf("引く時刻: %+v, %v", due, err)
	}

	// 待ち続ける。
	waits2 := map[string]domain.AiringChapterWait{news.Key(): domain.AiringChapterReady}
	if ok, err := s.UpdateChapterWait(ctx, id, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check.Add(30 * time.Minute), Waits: waits2}); err != nil || !ok {
		t.Fatalf("UpdateChapterWait = %v, %v", ok, err)
	}
	if j := get(t, id); !maps.Equal(j.ChapterWaits, waits2) || !j.ChapterCheckAt.Equal(check.Add(30*time.Minute)) {
		t.Errorf("待ち続けた後: %+v", j)
	}

	// 保存したファイルに書き込んだ。done のまま、値を付け直して待ちを消し、履歴に 1 行足す。
	at := check.Add(30 * time.Minute)
	wid, err := s.FinishChapterWrite(ctx, domain.ChapterWrite{JobID: id, At: at, By: domain.ChapterWriteByRefetch, Result: domain.ChapterWritten, Count: 3}, domain.ChaptersAdded)
	if err != nil || wid == 0 {
		t.Fatalf("FinishChapterWrite = %d, %v", wid, err)
	}
	j = get(t, id)
	if j.State != domain.StateDone || j.Attempts != 1 || j.Chapters != domain.ChaptersAdded || !j.ChapterCheckAt.IsZero() ||
		len(j.ChapterWaits) != 0 || j.OutputPath != "/lib/x.m4a" || len(j.Missing) != 1 || j.Missing[0].Key() != flat2.Key() {
		t.Fatalf("書き込んだ後: %+v", j)
	}
	if ok, err := s.UpdateChapterWait(ctx, id, domain.ChapterStatus{Chapters: domain.ChaptersNotAdded}); err != nil || ok {
		t.Errorf("もう待っていないのに書いた: %v, %v", ok, err)
	}
	// 書けなかったときも 1 行足す。
	if _, err := s.FinishChapterWrite(ctx, domain.ChapterWrite{JobID: id, At: at.Add(time.Minute), By: domain.ChapterWriteByRedo, Result: domain.ChapterWriteFailed, Error: "保存先にファイルが無い"}, domain.ChaptersNotAdded); err != nil {
		t.Fatal(err)
	}
	ws, err := s.ChapterWrites(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ChapterWrite{
		{ID: ws[0].ID, JobID: id, At: at, By: domain.ChapterWriteByRefetch, Result: domain.ChapterWritten, Count: 3},
		{ID: ws[1].ID, JobID: id, At: at.Add(time.Minute), By: domain.ChapterWriteByRedo, Result: domain.ChapterWriteFailed, Error: "保存先にファイルが無い"},
	}
	if len(ws) != 2 || !ws[0].At.Equal(want[0].At) || !ws[1].At.Equal(want[1].At) {
		t.Fatalf("ChapterWrites = %+v", ws)
	}
	for i := range ws {
		ws[i].At, want[i].At = time.Time{}, time.Time{}
		if ws[i] != want[i] {
			t.Errorf("ChapterWrites[%d] = %+v, want %+v", i, ws[i], want[i])
		}
	}
	if got := get(t, id); got.Chapters != domain.ChaptersNotAdded {
		t.Errorf("書けなかった後の値 = %q", got.Chapters)
	}
	if _, err := s.FinishChapterWrite(ctx, domain.ChapterWrite{JobID: 9999, At: at, By: domain.ChapterWriteByRefetch, Result: domain.ChapterNotFetched}, domain.ChaptersNotAdded); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}

	// 人の録り直しは、待ちを消して入らなかったにする。
	id2 := insert(t)
	if err := s.Complete(ctx, id2, "/lib/y.m4a", nil, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check, Waits: waits}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rerecord(ctx, id2, domain.MembershipFree, nil, now); err != nil {
		t.Fatal(err)
	}
	if j := get(t, id2); j.State != domain.StateRetrying || j.Chapters != domain.ChaptersNotAdded || !j.ChapterCheckAt.IsZero() ||
		len(j.ChapterWaits) != 0 {
		t.Fatalf("人の録り直しの後: %+v", j)
	}

	// ジョブを消すと、書き込みの履歴とパートの長さも消える。
	if err := s.SetPartLengths(ctx, id, map[string]time.Duration{news.Key(): time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM chapter_writes WHERE job_id = ?) + (SELECT COUNT(*) FROM job_part_lengths WHERE job_id = ?)`, id, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("消したジョブの行 = %d, %v", n, err)
	}
}

// パートの長さは保存のたびに置き換え（ミリ秒で持つ）、空にすれば消える（ADR 0038）。
func TestPartLengths(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.InsertJob(ctx, domain.Job{ShowName: "x", Day: day, State: domain.StateDone, Airings: []domain.Airing{news, flat2},
		NextAttemptAt: jst(2026, 10, 5, 11, 10), Deadline: jst(2026, 10, 12, 5, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.PartLengths(ctx, id); err != nil || len(got) != 0 {
		t.Fatalf("残していない: %v, %v", got, err)
	}
	want := map[string]time.Duration{news.Key(): 3599123 * time.Millisecond, flat2.Key(): 1800 * time.Second}
	if err := s.SetPartLengths(ctx, id, map[string]time.Duration{news.Key(): 3599123456 * time.Microsecond, flat2.Key(): 1800 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PartLengths(ctx, id); err != nil || !maps.Equal(got, want) {
		t.Errorf("PartLengths = %v, %v; want %v", got, err, want)
	}
	want2 := map[string]time.Duration{flat2.Key(): time.Hour}
	if err := s.SetPartLengths(ctx, id, want2); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PartLengths(ctx, id); err != nil || !maps.Equal(got, want2) {
		t.Errorf("置き換えた後 = %v, %v", got, err)
	}
	if err := s.SetPartLengths(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PartLengths(ctx, id); err != nil || len(got) != 0 {
		t.Errorf("消した後 = %v, %v", got, err)
	}
}

// 前の版のチャプターの取り直しの試行の途中（chapters_refetch = 1 の retrying・running）だったジョブは、移行（0028）で
// done に戻し、保存した放送回（欠落の無いもの）を全部待つ形にして、すぐ API を引かせる。保存先・欠落の印は前のまま。
// 取り直しの途中でないジョブは変えない。
func TestMigrateChapterRefetchInFlight(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v27.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
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
		if m.version > 27 {
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
		`INSERT INTO jobs (id, show_name, broadcast_day, state, next_attempt_at, attempts, deadline, output_path, chapters, chapters_refetch)
			VALUES (1, 'a', '2026-10-07', 'retrying', '2026-10-07T03:00:00.000Z', 1, '2026-10-14T20:00:00.000Z', '/lib/a.m4a', 'waiting', 1),
			       (2, 'a', '2026-10-08', 'running', '2026-10-08T03:00:00.000Z', 1, '2026-10-15T20:00:00.000Z', '/lib/b.m4a', 'waiting', 1),
			       (3, 'a', '2026-10-09', 'retrying', '2026-10-09T03:00:00.000Z', 2, '2026-10-16T20:00:00.000Z', '', '', 0)`,
		`INSERT INTO job_airings (job_id, position, airing_key, program_id, station_id, start_at, end_at, title, performer,
			description, image_url, ts_in_ng, ts_out_ng, missing)
			VALUES (1, 0, 'LFR@2026-10-06T23:30:00Z', '', 'LFR', '2026-10-06T23:30:00.000Z', '2026-10-07T00:30:00.000Z', '', '', '', '', 0, 0, 0),
			       (1, 1, 'LFR@2026-10-07T00:40:00Z', '', 'LFR', '2026-10-07T00:40:00.000Z', '2026-10-07T01:30:00.000Z', '', '', '', '', 0, 0, 1),
			       (2, 0, 'LFR@2026-10-07T23:30:00Z', '', 'LFR', '2026-10-07T23:30:00.000Z', '2026-10-08T00:30:00.000Z', '', '', '', '', 0, 0, 0),
			       (3, 0, 'LFR@2026-10-08T23:30:00Z', '', 'LFR', '2026-10-08T23:30:00.000Z', '2026-10-09T00:30:00.000Z', '', '', '', '', 0, 0, 0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	before := time.Now().Add(-time.Minute)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []int64{1, 2} {
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State != domain.StateDone || j.Chapters != domain.ChaptersWaiting || j.ChapterCheckAt.Before(before) || j.OutputPath == "" {
			t.Errorf("ジョブ %d: %+v", id, j)
		}
		if due, err := s.DueChapterChecks(ctx, time.Now().Add(time.Minute)); err != nil || len(due) != 2 {
			t.Errorf("DueChapterChecks = %d, %v", len(due), err)
		}
		// 欠落の無い放送回だけを待つ。
		want := map[string]domain.AiringChapterWait{}
		for _, a := range j.SavedAirings() {
			want[a.Key()] = domain.AiringChapterPending
		}
		if !maps.Equal(j.ChapterWaits, want) || (id == 1 && len(j.Missing) != 1) {
			t.Errorf("ジョブ %d: ChapterWaits = %v, Missing = %v", id, j.ChapterWaits, j.Missing)
		}
	}
	if j, err := s.GetJob(ctx, 3); err != nil || j.State != domain.StateRetrying || j.Chapters != domain.ChaptersNone || j.Attempts != 2 {
		t.Errorf("取り直しの途中でないジョブが変わった: %+v, %v", j, err)
	}
}
