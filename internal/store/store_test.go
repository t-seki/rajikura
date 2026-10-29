package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

func jst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, domain.JST)
}

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rajikura.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenPragmasAndMigrations(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)

	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	var timeout int
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
		t.Errorf("busy_timeout = %d, %v", timeout, err)
	}
	var fk int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v", fk, err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != ms[len(ms)-1].version {
		t.Errorf("SchemaVersion = %d, %v", v, err)
	}

	// 開き直しても当て直さない。
	if err := s.PutShow(ctx, domain.NewShow("x")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var n int
	if err := s2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(ms) {
		t.Errorf("schema_migrations = %d, %v", n, err)
	}
	if _, err := s2.GetShow(ctx, "x"); err != nil {
		t.Errorf("開き直したらデータが残っている: %v", err)
	}
}

func TestShows(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	ann := domain.NewShow("ANN",
		domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 1, Minute: 0}),
		domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 22, Minute: 0}),
	)
	flat := domain.Show{Name: "ふらっと", Split: domain.SplitSeparate, Weekly: []domain.Weekly{domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30})}}
	if err := s.PutShows(ctx, []domain.Show{ann, flat}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetShow(ctx, "ANN")
	if err != nil {
		t.Fatal(err)
	}
	if got.Split != domain.SplitNone || !got.CheckTitle || len(got.Weekly) != 2 {
		t.Fatalf("got %+v", got)
	}
	if r := got.Weekly[0]; r.StationID != "LFR" || r.Weekday != time.Monday || r.Start != (domain.Clock{Hour: 1}) {
		t.Errorf("weekly[0] = %+v", r)
	}
	if r := got.Weekly[1]; r.StationID != "TBS" || r.Weekday != time.Friday || r.Start != (domain.Clock{Hour: 22}) {
		t.Errorf("weekly[1] = %+v", r)
	}

	// 番組名をキーに丸ごと置き換える。
	ann2 := domain.Show{Name: "ANN", Split: domain.SplitJoin, CheckTitle: false, Weekly: []domain.Weekly{domain.NewWeekly("QRR", time.Sunday, domain.Clock{Hour: 3})}}
	if err := s.PutShow(ctx, ann2); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetShow(ctx, "ANN")
	if got.Split != domain.SplitJoin || got.CheckTitle || len(got.Weekly) != 1 || got.Weekly[0].StationID != "QRR" || got.Weekly[0].Weekday != time.Sunday {
		t.Errorf("置き換えた後: %+v", got)
	}

	all, err := s.ListShows(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "ANN" || all[1].Name != "ふらっと" || all[1].Split != domain.SplitSeparate {
		t.Errorf("ListShows = %+v, %v", all, err)
	}

	if err := s.DeleteShow(ctx, "ANN"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetShow(ctx, "ANN"); !errors.Is(err, ErrNotFound) {
		t.Errorf("消した後: %v", err)
	}
	if err := s.DeleteShow(ctx, "ANN"); !errors.Is(err, ErrNotFound) {
		t.Errorf("無い番組を消す: %v", err)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM weekly`).Scan(&n)
	if n != 1 {
		t.Errorf("毎週の予約が番組ごと消えていない: %d", n)
	}
	if err := s.PutShow(ctx, domain.Show{}); err == nil {
		t.Error("番組名が空ならエラー")
	}
}

var (
	day   = domain.NewBroadcastDay(2026, 10, 5)
	flat1 = domain.Airing{ProgramID: "p1", StationID: "LFR", Start: jst(2026, 10, 5, 8, 30), End: jst(2026, 10, 5, 10, 0),
		Title: "ふらっと (1)", Performer: "出演者", Description: "説明", ImageURL: "https://example.com/a.png",
		TimeFree: domain.TimeFreeFlags{OutNG: 2}} // ts_in_ng が 0 でないとライブで録るジョブになる（live_test.go）
	news  = domain.Airing{ProgramID: "p2", StationID: "LFR", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 10, 10), Title: "ニュース"}
	flat2 = domain.Airing{StationID: "LFR", Start: jst(2026, 10, 5, 10, 10), End: jst(2026, 10, 5, 11, 0), Title: "ふらっと (2)"}
	shows = []domain.Show{{Name: "ふらっと", Split: domain.SplitJoin, CheckTitle: true, Weekly: []domain.Weekly{
		domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}),
		domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 10, Minute: 10})}}}
)

func expand(t *testing.T, s *Store, airings ...domain.Airing) domain.Plan {
	t.Helper()
	jobs, err := s.JobsOnDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Expand(domain.ExpandInput{
		Shows: shows, StationID: "LFR", Day: day, Airings: airings, Jobs: jobs,
		Membership: domain.MembershipFree, TFMaxDelay: time.Minute,
	})
}

// applyPlanTest は、テストで差分を作ってから当てるために applyPlan をトランザクションで包む。
func (s *Store) applyPlanTest(ctx context.Context, plan domain.Plan) ([]int64, error) {
	var ids []int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		ids, err = applyPlan(ctx, tx, plan)
		return err
	})
	return ids, err
}

func expandFunc(airings ...domain.Airing) func([]domain.Job) domain.Plan {
	return func(jobs []domain.Job) domain.Plan {
		return domain.Expand(domain.ExpandInput{
			Shows: shows, StationID: "LFR", Day: day, Airings: airings, Jobs: jobs,
			Membership: domain.MembershipFree, TFMaxDelay: time.Minute,
		})
	}
}

func TestExpandDay(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	plan, ids, err := s.ExpandDay(ctx, day, expandFunc(flat1, news, flat2))
	if err != nil || len(plan.Create) != 1 || len(ids) != 1 {
		t.Fatalf("ExpandDay = %+v, %v, %v", plan, ids, err)
	}
	// 2 回目は既存のジョブを読んで展開するので、差分は無い。
	plan, ids, err = s.ExpandDay(ctx, day, expandFunc(flat1, news, flat2))
	if err != nil || !plan.Empty() || len(ids) != 0 {
		t.Errorf("2 回目: %+v, %v, %v", plan, ids, err)
	}
	// 休止回を外す。
	plan, _, err = s.ExpandDay(ctx, day, expandFunc(flat1))
	if err != nil || len(plan.Update) != 1 {
		t.Fatalf("休止回: %+v, %v", plan, err)
	}
	jobs, _ := s.ListJobs(ctx)
	if len(jobs) != 1 || len(jobs[0].Airings) != 1 {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestExpandDayConcurrent(t *testing.T) {
	// 2 つの展開が同時に同じ放送日を展開しても、ジョブは 1 つだけ（読み出しから適用までが 1 つのトランザクション）。
	// 同時に書く場面を作るために、別々に開いた接続から展開する。
	ctx := context.Background()
	s, path := openTemp(t)
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		st := s
		if i%2 == 1 {
			st = s2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 展開に時間がかかっても（読み出しと適用の間が空いても）重複しないこと。
			slow := func(jobs []domain.Job) domain.Plan {
				time.Sleep(20 * time.Millisecond)
				return expandFunc(flat1, news, flat2)(jobs)
			}
			if _, _, err := st.ExpandDay(ctx, day, slow); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Errorf("jobs = %d, %v", len(jobs), err)
	}
}

func TestApplyPlan(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	plan := expand(t, s, flat1, news, flat2)
	ids, err := s.applyPlanTest(ctx, plan)
	if err != nil || len(ids) != 1 {
		t.Fatalf("applyPlan = %v, %v", ids, err)
	}
	j, err := s.GetJob(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	want := plan.Create[0]
	want.ID = ids[0]
	assertJob(t, j, want)
	if j.Airings[0].Start.Location() != domain.JST {
		t.Errorf("読み出した時刻は日本時間: %v", j.Airings[0].Start.Location())
	}

	// 同じ番組表で展開し直しても差分は無い（保存と読み出しで中身が変わらない）。
	if p := expand(t, s, flat1, news, flat2); !p.Empty() {
		t.Errorf("差分が出た: %+v", p)
	}

	// 休止回: flat2 が消えたら外す。
	plan = expand(t, s, flat1, news)
	if len(plan.Update) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := s.applyPlanTest(ctx, plan); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJob(ctx, ids[0])
	if len(j.Airings) != 1 || j.Airings[0].Title != "ふらっと (1)" {
		t.Errorf("休止回を外した後: %+v", j.Airings)
	}

	// 放送回が 0 になったら消す。
	plan = expand(t, s, news)
	if len(plan.Delete) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := s.applyPlanTest(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJob(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("消えていない: %v", err)
	}
}

func TestApplyPlanSkipsNonWaiting(t *testing.T) {
	// 展開の後に実行が始まったジョブは、差分を当てても触らない。
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, err := s.applyPlanTest(ctx, expand(t, s, flat1, flat2))
	if err != nil {
		t.Fatal(err)
	}
	update := expand(t, s, flat1)
	del := expand(t, s)
	if _, ok, err := s.ClaimDue(ctx, jst(2026, 10, 6, 0, 0)); !ok || err != nil {
		t.Fatalf("ClaimDue = %v, %v", ok, err)
	}
	if _, err := s.applyPlanTest(ctx, update); err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyPlanTest(ctx, del); err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, ids[0])
	if err != nil || j.State != domain.StateRunning || len(j.Airings) != 2 {
		t.Errorf("job = %+v, %v", j, err)
	}
}

func TestApplyPlanAtomic(t *testing.T) {
	// 途中で失敗したら、何も当てない。
	ctx := context.Background()
	s, _ := openTemp(t)
	plan := expand(t, s, flat1)
	bad := plan.Create[0]
	bad.State = "broken" // CHECK 制約で失敗する
	plan.Create = append(plan.Create, bad)
	if _, err := s.applyPlanTest(ctx, plan); err == nil {
		t.Fatal("失敗するはず")
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Errorf("巻き戻っていない: %d, %v", len(jobs), err)
	}
}

func TestApplyPlanDoesNotReuseJobID(t *testing.T) {
	// 展開がいちばん大きい id のジョブを消して同じトランザクションで作り直しても、消えたジョブの id を使い回さない。
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, err := s.applyPlanTest(ctx, expand(t, s, flat1, flat2))
	if err != nil || len(ids) != 1 {
		t.Fatalf("applyPlan = %v, %v", ids, err)
	}
	old, err := s.GetJob(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	plan := expand(t, s, flat1, flat2)
	if !plan.Empty() {
		t.Fatalf("plan = %+v", plan)
	}
	recreated := old
	recreated.ID = 0
	plan.Delete = []domain.Job{old}
	plan.Create = []domain.Job{recreated}
	newIDs, err := s.applyPlanTest(ctx, plan)
	if err != nil || len(newIDs) != 1 {
		t.Fatalf("applyPlan = %v, %v", newIDs, err)
	}
	if newIDs[0] == old.ID {
		t.Errorf("作り直したジョブの id = %d（消えたジョブと同じ）", newIDs[0])
	}
	if _, err := s.GetJob(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("消えたジョブ: %v", err)
	}
	if j, err := s.GetJob(ctx, newIDs[0]); err != nil || len(j.Airings) != 2 {
		t.Errorf("作り直したジョブ = %+v, %v", j, err)
	}
}

func assertJob(t *testing.T, got, want domain.Job) {
	t.Helper()
	if got.ID != want.ID || got.ShowName != want.ShowName || got.Day != want.Day || got.Part != want.Part ||
		got.State != want.State || got.Attempts != want.Attempts || got.OutputPath != want.OutputPath ||
		!got.NextAttemptAt.Equal(want.NextAttemptAt) || !got.Deadline.Equal(want.Deadline) {
		t.Errorf("job = %+v\nwant %+v", got, want)
	}
	if len(got.Airings) != len(want.Airings) {
		t.Fatalf("airings = %d, want %d", len(got.Airings), len(want.Airings))
	}
	for i := range got.Airings {
		g, w := got.Airings[i], want.Airings[i]
		if g.ProgramID != w.ProgramID || g.StationID != w.StationID || !g.Start.Equal(w.Start) || !g.End.Equal(w.End) ||
			g.Title != w.Title || g.Performer != w.Performer || g.Description != w.Description ||
			g.ImageURL != w.ImageURL || g.TimeFree != w.TimeFree {
			t.Errorf("airing[%d] = %+v\nwant %+v", i, g, w)
		}
	}
}

func TestClaimDue(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, err := s.applyPlanTest(ctx, expand(t, s, flat1, flat2))
	if err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob(ctx, ids[0])

	// 次の試行の時刻の前は取れない。
	if _, ok, err := s.ClaimDue(ctx, j.NextAttemptAt.Add(-time.Second)); ok || err != nil {
		t.Fatalf("早すぎる: %v, %v", ok, err)
	}
	got, ok, err := s.ClaimDue(ctx, j.NextAttemptAt)
	if !ok || err != nil {
		t.Fatalf("ClaimDue = %v, %v", ok, err)
	}
	if got.ID != j.ID || got.State != domain.StateRunning || got.Attempts != 1 || len(got.Airings) != 2 {
		t.Errorf("got %+v", got)
	}
	// running のものはもう取れない。
	if _, ok, _ := s.ClaimDue(ctx, j.NextAttemptAt.Add(time.Hour)); ok {
		t.Error("running を二重に取った")
	}
}

func TestClaimDueConcurrent(t *testing.T) {
	// 別々に開いた接続から同時に取っても、1 件は 1 回しか取れない（ClaimDue の取り出しは原子的）。
	ctx := context.Background()
	s, path := openTemp(t)
	const n = 20
	for i := range n {
		a := flat1
		a.ProgramID = ""
		a.Start = a.Start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, Part: i + 1, Airings: []domain.Airing{a},
			NextAttemptAt: jst(2026, 10, 5, 12, 0), Deadline: jst(2026, 10, 13, 5, 0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	var mu sync.Mutex
	claimed := map[int64]int{}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for w := range 8 {
		st := s
		if w%2 == 1 {
			st = s2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, ok, err := st.ClaimDue(ctx, jst(2026, 10, 5, 13, 0))
				if err != nil {
					errs <- err
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				claimed[j.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(claimed) != n {
		t.Errorf("取れたのは %d 件, want %d", len(claimed), n)
	}
	for id, c := range claimed {
		if c != 1 {
			t.Errorf("ジョブ %d を %d 回取った", id, c)
		}
	}
}

func TestTransitions(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, err := s.applyPlanTest(ctx, expand(t, s, flat1, flat2))
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]
	now := jst(2026, 10, 5, 12, 0)

	// waiting から done へは行けない。
	if err := s.Complete(ctx, id, "x.m4a", nil, domain.ChapterStatus{}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("waiting → done は許されない: %v", err)
	}
	if _, ok, _ := s.ClaimDue(ctx, now); !ok {
		t.Fatal("取れない")
	}
	next := now.Add(15 * time.Minute)
	if err := s.Retry(ctx, id, next); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob(ctx, id)
	if j.State != domain.StateRetrying || !j.NextAttemptAt.Equal(next) {
		t.Errorf("Retry の後: %+v", j)
	}
	if _, ok, _ := s.ClaimDue(ctx, next); !ok {
		t.Fatal("retrying を取れない")
	}
	if err := s.Expire(ctx, id); err != nil {
		t.Fatal(err)
	}
	// 手動の再試行で、試行回数を数え直す。
	if err := s.ManualRetry(ctx, id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJob(ctx, id)
	if j.State != domain.StateRetrying || j.Attempts != 0 || !j.NextAttemptAt.Equal(now.Add(time.Hour)) {
		t.Errorf("ManualRetry の後: %+v", j)
	}
	if _, ok, _ := s.ClaimDue(ctx, now.Add(time.Hour)); !ok {
		t.Fatal("取れない")
	}
	// 一部だけ取れた: 取れた分を保存して done、欠落を記録する。
	if err := s.Complete(ctx, id, "/lib/ふらっと.m4a", []domain.Airing{flat2}, domain.ChapterStatus{}); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJob(ctx, id)
	if j.State != domain.StateDone || j.OutputPath != "/lib/ふらっと.m4a" || len(j.Missing) != 1 || j.Missing[0].Title != "ふらっと (2)" {
		t.Errorf("Complete の後: %+v", j)
	}
}

func TestManualRetry(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, err := s.applyPlanTest(ctx, expand(t, s, flat1, flat2))
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]
	now := jst(2026, 10, 5, 12, 0)

	// waiting は拒む。
	if err := s.ManualRetry(ctx, id, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("waiting: %v", err)
	}
	// running も拒む（running → retrying は遷移表にあるが、人は動かせない）。
	if _, ok, _ := s.ClaimDue(ctx, now); !ok {
		t.Fatal("取れない")
	}
	if err := s.ManualRetry(ctx, id, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("running: %v", err)
	}
	// retrying: 状態は retrying のまま、次の試行が今になり、試行回数が 0 に戻る。試行の履歴は残る。
	if _, err := s.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: now, EndedAt: now, Error: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Retry(ctx, id, now.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.ManualRetry(ctx, id, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob(ctx, id)
	if j.State != domain.StateRetrying || j.Attempts != 0 || !j.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Errorf("retrying の後: %+v", j)
	}
	if as, _ := s.Attempts(ctx, id); len(as) != 1 {
		t.Errorf("試行の履歴 = %d 件, want 1", len(as))
	}
	// expired → retrying。
	if _, ok, _ := s.ClaimDue(ctx, now.Add(time.Minute)); !ok {
		t.Fatal("取れない")
	}
	if err := s.Expire(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.ManualRetry(ctx, id, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJob(ctx, id)
	if j.State != domain.StateRetrying || j.Attempts != 0 || !j.NextAttemptAt.Equal(now.Add(2*time.Minute)) {
		t.Errorf("expired の後: %+v", j)
	}
	// 録音期限を過ぎたジョブは、retrying でも expired でも拒む。
	if err := s.ManualRetry(ctx, id, j.Deadline); !errors.Is(err, ErrPastDeadline) {
		t.Errorf("retrying・期限切れ: %v", err)
	}
	if _, ok, _ := s.ClaimDue(ctx, now.Add(3*time.Minute)); !ok {
		t.Fatal("取れない")
	}
	if err := s.Expire(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.ManualRetry(ctx, id, j.Deadline.Add(time.Second)); !errors.Is(err, ErrPastDeadline) {
		t.Errorf("expired・期限切れ: %v", err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateExpired {
		t.Errorf("拒んだ後の状態 = %s", j.State)
	}
	// done は拒む。
	if err := s.ManualRetry(ctx, id, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ClaimDue(ctx, now.Add(4*time.Minute)); !ok {
		t.Fatal("取れない")
	}
	if err := s.Complete(ctx, id, "/lib/x.m4a", nil, domain.ChapterStatus{}); err != nil {
		t.Fatal(err)
	}
	if err := s.ManualRetry(ctx, id, now.Add(5*time.Minute)); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("done: %v", err)
	}
	// 無いジョブ。
	if err := s.ManualRetry(ctx, 999, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}
}

// 録らなかった（skipped）のジョブは、手動再試行で retrying になる。録音期限を過ぎていれば拒み、skipped のまま残す。
func TestManualRetrySkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 6, 12, 0)
	deadline := jst(2026, 10, 13, 5, 0)
	insert := func() int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, State: domain.StateSkipped, Settled: true, Airings: []domain.Airing{flat1},
			NextAttemptAt: jst(2026, 10, 5, 10, 5), Deadline: deadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := insert()
	if j, err := s.GetJob(ctx, id); err != nil || j.State != domain.StateSkipped {
		t.Fatalf("読んだ skipped のジョブ = %+v, %v", j, err)
	}
	// 録りにいかない（ClaimDue は取らない）。
	if _, ok, _ := s.ClaimDue(ctx, now); ok {
		t.Fatal("skipped のジョブを取った")
	}
	if err := s.ManualRetry(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateRetrying || j.Attempts != 0 || !j.NextAttemptAt.Equal(now) {
		t.Errorf("手動再試行の後: %+v", j)
	}
	// 録音期限を過ぎていれば拒む。
	late := insert()
	if err := s.ManualRetry(ctx, late, deadline); !errors.Is(err, ErrPastDeadline) {
		t.Errorf("録音期限を過ぎた: %v", err)
	}
	if j, _ := s.GetJob(ctx, late); j.State != domain.StateSkipped {
		t.Errorf("拒んだ後の状態 = %s", j.State)
	}
}

func TestRecoverAndList(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	var ids []int64
	for i := range 3 {
		a := flat1
		a.Start = a.Start.Add(time.Duration(i) * time.Hour)
		id, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, Part: i + 1, Airings: []domain.Airing{a},
			NextAttemptAt: jst(2026, 10, 5, 12, i), Deadline: jst(2026, 10, 13, 5, 0),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 3 件目は now の時点ではまだ due でない。
	now := jst(2026, 10, 5, 12, 1)
	s.ClaimDue(ctx, now)
	s.ClaimDue(ctx, now)

	running, err := s.ListJobs(ctx, domain.StateRunning)
	if err != nil || len(running) != 2 {
		t.Fatalf("running = %d, %v", len(running), err)
	}
	n, err := s.Recover(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	retrying, _ := s.ListJobs(ctx, domain.StateRetrying)
	waiting, _ := s.ListJobs(ctx, domain.StateWaiting)
	both, _ := s.ListJobs(ctx, domain.StateRetrying, domain.StateWaiting)
	all, _ := s.ListJobs(ctx)
	if len(retrying) != 2 || len(waiting) != 1 || len(both) != 3 || len(all) != 3 {
		t.Errorf("retrying %d, waiting %d, both %d, all %d", len(retrying), len(waiting), len(both), len(all))
	}
	if retrying[0].Attempts != 1 {
		t.Errorf("回復しても試行回数は残る: %d", retrying[0].Attempts)
	}
	// 回復したジョブはすぐ取れる。取れるのは回復した 2 件だけ。
	for _, want := range ids[:2] {
		j, ok, err := s.ClaimDue(ctx, now)
		if !ok || err != nil || j.ID != want {
			t.Errorf("ClaimDue = %d, %v, %v; want %d", j.ID, ok, err, want)
		}
	}
	if j, ok, _ := s.ClaimDue(ctx, now); ok {
		t.Errorf("due でないジョブ %d を取った", j.ID)
	}
}

func TestAttempts(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, _ := s.applyPlanTest(ctx, expand(t, s, flat1))
	a := domain.Attempt{
		JobID: ids[0], StartedAt: jst(2026, 10, 5, 12, 0), EndedAt: jst(2026, 10, 5, 12, 1),
		FailedStage: domain.StageVerify, Error: "長さが合わない",
		ExtinfTotal: 5400*time.Second + 500*time.Millisecond, ProbeDuration: 5380 * time.Second,
	}
	// 開始時刻の新しい試行を先に記録し、古い試行を後から記録しても、開始時刻の順に並ぶ。
	b := domain.Attempt{JobID: ids[0], StartedAt: jst(2026, 10, 5, 12, 15), EndedAt: jst(2026, 10, 5, 12, 16)}
	bid, err := s.RecordAttempt(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.RecordAttempt(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Attempts(ctx, ids[0])
	if err != nil || len(got) != 2 {
		t.Fatalf("Attempts = %+v, %v", got, err)
	}
	if got[1].ID != bid {
		t.Errorf("並び = %d, %d; want %d, %d", got[0].ID, got[1].ID, id, bid)
	}
	g := got[0]
	if g.ID != id || !g.StartedAt.Equal(a.StartedAt) || !g.EndedAt.Equal(a.EndedAt) || g.FailedStage != a.FailedStage ||
		g.Error != a.Error || g.ExtinfTotal != a.ExtinfTotal || g.ProbeDuration != a.ProbeDuration {
		t.Errorf("attempt = %+v\nwant %+v", g, a)
	}
	if got[1].FailedStage != "" {
		t.Errorf("成功した試行の段階は空: %q", got[1].FailedStage)
	}
	if _, err := s.RecordAttempt(ctx, domain.Attempt{JobID: 999, StartedAt: time.Now()}); err == nil {
		t.Error("無いジョブの試行は記録できない（外部キー）")
	}
}

func TestLatestAttemptsFromDay(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	ids, _ := s.applyPlanTest(ctx, expand(t, s, flat1))
	if got, err := s.LatestAttemptsFromDay(ctx, domain.NewBroadcastDay(2000, 1, 1)); err != nil || len(got) != 0 {
		t.Fatalf("試行が無いとき = %+v, %v", got, err)
	}
	// 開始時刻の新しい試行を先に記録しても、直近は開始時刻の最も新しいもの。
	newer := domain.Attempt{JobID: ids[0], StartedAt: jst(2026, 10, 5, 12, 15), EndedAt: jst(2026, 10, 5, 12, 16), FailedStage: domain.StageSave, Error: "保存できない"}
	older := domain.Attempt{JobID: ids[0], StartedAt: jst(2026, 10, 5, 12, 0), EndedAt: jst(2026, 10, 5, 12, 1)}
	nid, _ := s.RecordAttempt(ctx, newer)
	if _, err := s.RecordAttempt(ctx, older); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestAttemptsFromDay(ctx, domain.NewBroadcastDay(2000, 1, 1))
	if err != nil || len(got) != 1 {
		t.Fatalf("LatestAttemptsFromDay = %+v, %v", got, err)
	}
	if g := got[ids[0]]; g.ID != nid || g.FailedStage != domain.StageSave || g.Error != "保存できない" || !g.StartedAt.Equal(newer.StartedAt) {
		t.Errorf("直近 = %+v", g)
	}
	all, _ := s.Attempts(ctx, ids[0])
	if got[ids[0]].ID != all[len(all)-1].ID {
		t.Errorf("Attempts の最後と違う")
	}
}

func TestTimeFormat(t *testing.T) {
	// UTC の RFC 3339 で、文字列の順と時刻の順が合う。
	a := formatTime(jst(2026, 10, 5, 9, 0))
	b := formatTime(jst(2026, 10, 5, 9, 0).Add(1500 * time.Millisecond))
	if a != "2026-10-05T00:00:00.000Z" || b != "2026-10-05T00:00:01.500Z" || !(a < b) {
		t.Errorf("a = %s, b = %s", a, b)
	}
	got, err := parseTime(b)
	if err != nil || !got.Equal(jst(2026, 10, 5, 9, 0).Add(1500*time.Millisecond)) || got.Location() != domain.JST {
		t.Errorf("parseTime = %v, %v", got, err)
	}
}

func TestSameProgramID(t *testing.T) {
	// 分割番組の放送回が同じ番組表の ID でも、別の放送回として保存し、欠落の印も別に付ける。
	ctx := context.Background()
	s, _ := openTemp(t)
	a1, a2 := flat1, flat2
	a1.ProgramID, a2.ProgramID = "same", "same"
	plan, ids, err := s.ExpandDay(ctx, day, expandFunc(a1, news, a2))
	if err != nil || len(ids) != 1 || len(plan.Create[0].Airings) != 2 {
		t.Fatalf("ExpandDay = %+v, %v, %v", plan, ids, err)
	}
	if _, ok, _ := s.ClaimDue(ctx, jst(2026, 10, 6, 0, 0)); !ok {
		t.Fatal("取れない")
	}
	if err := s.Complete(ctx, ids[0], "x.m4a", []domain.Airing{a2}, domain.ChapterStatus{}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob(ctx, ids[0])
	if len(j.Airings) != 2 || len(j.Missing) != 1 || j.Missing[0].Title != "ふらっと (2)" {
		t.Errorf("job = %+v", j)
	}
}

func TestCompleteResetsMissing(t *testing.T) {
	// Complete を欠落なし（nil）で呼ぶと、前に付いていた欠落の印が消える。
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.InsertJob(ctx, domain.Job{
		ShowName: "x", Day: day, Airings: []domain.Airing{flat1, flat2}, Missing: []domain.Airing{flat2},
		NextAttemptAt: jst(2026, 10, 5, 12, 0), Deadline: jst(2026, 10, 13, 5, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); len(j.Missing) != 1 {
		t.Fatalf("前提: 欠落が 1 つ: %+v", j.Missing)
	}
	if _, ok, _ := s.ClaimDue(ctx, jst(2026, 10, 5, 12, 0)); !ok {
		t.Fatal("取れない")
	}
	if err := s.Complete(ctx, id, "x.m4a", nil, domain.ChapterStatus{}); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); len(j.Missing) != 0 {
		t.Errorf("欠落の印が残っている: %+v", j.Missing)
	}
}

func TestPutShowValidates(t *testing.T) {
	// 範囲外の値は書き込まない（読み出しで ListShows 全体が読めなくならないように）。
	ctx := context.Background()
	s, _ := openTemp(t)
	bad := []domain.Show{
		domain.NewShow("a", domain.NewWeekly("LFR", time.Weekday(9), domain.Clock{Hour: 1})),
		domain.NewShow("b", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 25})),
		domain.NewShow("c", domain.NewWeekly("", time.Monday, domain.Clock{Hour: 1})),
	}
	for _, show := range bad {
		if err := s.PutShow(ctx, show); err == nil {
			t.Errorf("PutShow(%s) は失敗するはず", show.Name)
		}
	}
	if err := s.PutShows(ctx, []domain.Show{domain.NewShow("ok"), bad[0]}); err == nil {
		t.Error("PutShows は 1 つでも不正なら失敗するはず")
	}
	all, err := s.ListShows(ctx)
	if err != nil || len(all) != 0 {
		t.Errorf("ListShows = %+v, %v", all, err)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	// DB がバイナリの知らない新しい版まで当たっていたら、開かずにエラーにする。
	ctx := context.Background()
	s, path := openTemp(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (9999, '')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s2, err := Open(ctx, path); err == nil {
		s2.Close()
		t.Error("新しい版の DB を開けてしまった")
	}
}

// 版 1 までの DB を開くと、既存の番組の split が既定（false）、check_title が既定（true）で足される。
func TestMigrateShowSplit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		string(v1),
		`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-10-01T00:00:00.000Z')`,
		`INSERT INTO shows (name, join_airings) VALUES ('ふらっと', 0)`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 2 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	got, err := s.GetShow(ctx, "ふらっと")
	if err != nil {
		t.Fatal(err)
	}
	if got.Split != domain.SplitNone || !got.CheckTitle {
		t.Errorf("got %+v, want Split=SplitNone CheckTitle=true", got)
	}
}

// 版 7 までの DB（番組名ルールの行を含む）を開くと、番組名ルールの行が消え、毎週の予約の行は残る。
// 版 8 で rules 表（版 9 で weekly 表に改名）から kind・title・weekdays の列が消える。
func TestMigrateDropTitleRule(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7.db")
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
		if m.version > 7 {
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
		`INSERT INTO shows (id, name) VALUES (1, 'ANN'), (2, 'ふらっと')`,
		`INSERT INTO rules (show_id, position, kind, station_id, weekday, start_hm, title, weekdays) VALUES
			(1, 0, 'time', 'LFR', 1, '01:00', '', ''),
			(1, 1, 'title', 'LFR', 0, '', 'オールナイト', '1,5'),
			(1, 2, 'time', 'TBS', 5, '23:30', '', ''),
			(2, 0, 'title', 'LFR', 0, '', 'ふらっと', '')`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 8 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	ann, err := s.GetShow(ctx, "ANN")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Weekly{
		domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 1}),
		domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 23, Minute: 30}),
	}
	if !slices.Equal(ann.Weekly, want) {
		t.Errorf("ANN の毎週の予約 = %+v, want %+v", ann.Weekly, want)
	}
	// 番組名ルールしか持たなかった番組は、毎週の予約の無い番組として残る。
	flat, err := s.GetShow(ctx, "ふらっと")
	if err != nil || len(flat.Weekly) != 0 {
		t.Errorf("ふらっと = %+v, %v", flat, err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('weekly') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	if want := []string{"id", "show_id", "position", "station_id", "weekday", "start_hm", "from_day", "until_day"}; !slices.Equal(cols, want) {
		t.Errorf("weekly の列 = %v, want %v", cols, want)
	}
	// 番組を消すと、作り直した weekly の行も一緒に消える（外部キーが残っている）。
	if err := s.DeleteShow(ctx, "ANN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM weekly`).Scan(&n); err != nil || n != 0 {
		t.Errorf("番組を消した後の毎週の予約 = %d, %v", n, err)
	}
}

func TestListJobsFromDayRange(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	today := domain.NewBroadcastDay(2026, 10, 12)
	from := today.AddDays(-120)
	mk := func(day domain.BroadcastDay, st domain.State) int64 {
		id, err := s.InsertJob(ctx, domain.Job{ShowName: "番組", Day: day, State: st, NextAttemptAt: jst(2026, 10, 12, 12, 0), Deadline: jst(2026, 10, 13, 12, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: jst(2026, 10, 12, 12, 0)}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	future := mk(today.AddDays(3), domain.StateWaiting)
	edge := mk(from, domain.StateDone)
	old := mk(from.AddDays(-1), domain.StateDone)

	jobs, err := s.ListJobsFromDay(ctx, from)
	if err != nil || len(jobs) != 2 || jobs[0].ID != edge || jobs[1].ID != future {
		t.Fatalf("ListJobsFromDay = %+v, %v; want [%d %d]（120 日前は読み、121 日前は読まない）", jobs, err, edge, future)
	}
	if jobs, err := s.ListJobsFromDay(ctx, from, domain.StateWaiting); err != nil || len(jobs) != 1 || jobs[0].ID != future {
		t.Errorf("状態で絞る = %+v, %v", jobs, err)
	}
	latest, err := s.LatestAttemptsFromDay(ctx, from)
	if err != nil || len(latest) != 2 || latest[old].ID != 0 || latest[edge].ID == 0 || latest[future].ID == 0 {
		t.Errorf("LatestAttemptsFromDay = %+v, %v", latest, err)
	}
	if all, _ := s.ListJobs(ctx); len(all) != 3 {
		t.Errorf("ListJobs は全件 = %d", len(all))
	}
}
