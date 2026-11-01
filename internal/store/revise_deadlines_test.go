package store

import (
	"context"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 会員種別を保存したら、まだ終わっていないジョブ（waiting・retrying・skipped・running）の録音期限を付け直す。done・expired は触らない。
// tsplus_in_ng が 0 でない放送回（タイムフリー30 の対象外）は、タイムフリー30・ダブルプランでも 7 日のまま。
func TestReviseDeadlines(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 11, 0), Title: "番組"}
	plus := a
	plus.TimeFree.PlusInNG = 1
	free := jst(2026, 10, 13, 5, 0)   // 放送日 10-05 から 7 日
	thirty := jst(2026, 11, 5, 5, 0)  // 放送日 10-05 から 30 日
	late := jst(2026, 10, 20, 12, 0)  // 7 日の録音期限より後の次の試行
	first := jst(2026, 10, 5, 11, 30) // 最初の試行
	now := jst(2026, 10, 6, 12, 0)
	insert := func(name string, st domain.State, next, deadline time.Time, airing domain.Airing) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{ShowName: name, Day: domain.NewBroadcastDay(2026, 10, 5), State: st,
			NextAttemptAt: next, Deadline: deadline, Airings: []domain.Airing{airing}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	waiting := insert("waiting", domain.StateWaiting, first, free, a)
	retrying := insert("retrying", domain.StateRetrying, late, thirty, a)
	skipped := insert("skipped", domain.StateSkipped, first, free, a)
	excluded := insert("excluded", domain.StateWaiting, first, free, plus)
	running := insert("running", domain.StateRunning, first, free, a)
	done := insert("done", domain.StateDone, first, free, a)
	expired := insert("expired", domain.StateExpired, first, free, a)

	check := func(id int64, wantDeadline, wantNext time.Time) {
		t.Helper()
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !j.Deadline.Equal(wantDeadline) || !j.NextAttemptAt.Equal(wantNext) {
			t.Errorf("%s: deadline = %v, next = %v; want %v, %v", j.ShowName, j.Deadline, j.NextAttemptAt, wantDeadline, wantNext)
		}
	}

	got, err := s.ReviseDeadlines(ctx, domain.MembershipDouble, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revised != 3 || got.Passed != 0 {
		t.Errorf("revised = %+v, want 3（waiting・skipped・running）、過ぎた値は 0", got)
	}
	check(waiting, thirty, first)
	check(retrying, thirty, late)
	check(skipped, thirty, first)
	check(running, thirty, first)
	check(excluded, free, first)
	check(done, free, first)
	check(expired, free, first)

	// tsplus_in_ng は DB に持つ。
	if j, err := s.GetJob(ctx, excluded); err != nil || j.Airings[0].TimeFree.PlusInNG != 1 {
		t.Fatalf("airings = %+v, %v; want tsplus_in_ng = 1", j.Airings, err)
	}

	// 非会員に戻すと 7 日になり、retrying の次の試行は録音期限に寄せる。
	if got, err := s.ReviseDeadlines(ctx, domain.MembershipFree, nil, now); err != nil || got.Revised != 4 {
		t.Fatalf("revised = %+v, %v; want 4", got, err)
	}
	check(waiting, free, first)
	check(retrying, free, free)
	check(skipped, free, first)
	check(running, free, first)

	// 期限間近を出していないジョブは、録音期限が先なら期限間近の対象のまま。
	if jobs, err := s.DeadlineNearJobs(ctx, jst(2026, 10, 12, 6, 0), 24*time.Hour); err != nil || len(jobs) != 4 {
		t.Errorf("deadline near = %d 件, %v; want 4（waiting・excluded・retrying・running）", len(jobs), err)
	}
}

// 自分のエリアが変わったら、エリアフリー・ダブルプランでは、今の自分のエリアの局一覧 own で、エリアの外から録るかを決め直して
// 録音期限を付け直す。録音の方式（放送回の印）は変えない。今の自分のエリアからタイムフリーで聴けない放送回を含むタイムフリーから
// 録るジョブは、放送の終了の 5 分後（もう過ぎた値）になり、期限間近を出さない。ライブで録るジョブは変わらない。
func TestReviseDeadlinesOwnArea(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	day := domain.NewBroadcastDay(2026, 10, 5)
	// TBS は前のエリアの局で、新しいエリアからはエリアの外。エリアの外からはタイムフリーで聴けない（ts_out_ng）。
	tbs := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 11, 0), Title: "番組",
		TimeFree: domain.TimeFreeFlags{OutNG: 1}}
	// QRR は新しいエリアにもある局。
	qrr := domain.Airing{StationID: "QRR", Start: jst(2026, 10, 5, 12, 0), End: jst(2026, 10, 5, 13, 0), Title: "番組"}
	// MBS は前のエリアではエリアの外（印が立っている。ts_in_ng でライブで録る）で、新しいエリアでは自分のエリアの局。
	mbs := domain.Airing{StationID: "MBS", Start: jst(2026, 10, 5, 14, 0), End: jst(2026, 10, 5, 15, 0), Title: "番組",
		TimeFree: domain.TimeFreeFlags{InNG: 1}}
	// LFR は、前のエリアではエリアの外（ts_out_ng でライブで録る）で、新しいエリアでも外。
	lfr := domain.Airing{StationID: "LFR", Start: jst(2026, 10, 5, 16, 0), End: jst(2026, 10, 5, 17, 0), Title: "番組",
		TimeFree: domain.TimeFreeFlags{OutNG: 1}, OutOfArea: true}
	thirty := jst(2026, 11, 5, 5, 0)
	now := jst(2026, 10, 6, 12, 0)
	insert := func(name string, st domain.State, next time.Time, airings ...domain.Airing) int64 {
		t.Helper()
		j := domain.Job{ShowName: name, Day: day, State: st, NextAttemptAt: next, Airings: airings}
		j.Deadline = domain.JobDeadline(airings, domain.MembershipDouble)
		id, err := s.InsertJob(ctx, j)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	late := jst(2026, 10, 20, 12, 0)
	retrying := insert("retrying", domain.StateRetrying, late, tbs)
	settled := insert("settled", domain.StateSkipped, jst(2026, 10, 5, 11, 30), tbs)
	stays := insert("stays", domain.StateRetrying, late, qrr)
	liveIn := insert("live-in", domain.StateRetrying, late, mbs)
	liveOut := insert("live-out", domain.StateRetrying, late, lfr)
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET settled = 1 WHERE id = ?`, settled); err != nil {
		t.Fatal(err)
	}
	get := func(id int64) domain.Job {
		t.Helper()
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	if !get(retrying).Deadline.Equal(thirty) {
		t.Fatalf("前提: deadline = %v, want %v", get(retrying).Deadline, thirty)
	}

	got, err := s.ReviseDeadlines(ctx, domain.MembershipDouble, map[string]bool{"QRR": true, "MBS": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revised != 2 || got.Passed != 2 {
		t.Errorf("revised = %+v, want 2 件（retrying・settled）、どちらも過ぎた値", got)
	}
	passed := tbs.End.Add(domain.LiveDeadlineAfter)
	for _, id := range []int64{retrying, settled} {
		j := get(id)
		if !j.Deadline.Equal(passed) {
			t.Errorf("%s: deadline = %v, want %v（放送の終了の 5 分後）", j.ShowName, j.Deadline, passed)
		}
		if j.Live() || j.Airings[0].OutOfArea {
			t.Errorf("%s: 録音の方式が変わった: live = %v, airings = %+v", j.ShowName, j.Live(), j.Airings)
		}
	}
	if j := get(retrying); !j.NextAttemptAt.Equal(passed) {
		t.Errorf("retrying: next = %v, want %v（録音期限に寄せる）", j.NextAttemptAt, passed)
	}
	if j := get(stays); !j.Deadline.Equal(thirty) {
		t.Errorf("stays: deadline = %v, want %v", j.Deadline, thirty)
	}
	for _, id := range []int64{liveIn, liveOut} {
		j := get(id)
		if !j.Live() || !j.Deadline.Equal(j.LastEnd().Add(domain.LiveDeadlineAfter)) {
			t.Errorf("%s: live = %v, deadline = %v; want ライブのまま、放送の終了の 5 分後", j.ShowName, j.Live(), j.Deadline)
		}
	}
	// 録音期限が過ぎた値になったジョブには、期限間近を出さない。
	jobs, err := s.DeadlineNearJobs(ctx, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.ID == retrying {
			t.Errorf("retrying に期限間近が出る: %+v", j)
		}
	}

	// 同じ入力で付け直しても、何も変わらない。
	if got, err := s.ReviseDeadlines(ctx, domain.MembershipDouble, map[string]bool{"QRR": true, "MBS": true}, now); err != nil || got.Revised != 0 {
		t.Errorf("revised = %+v, %v; want 0", got, err)
	}
	// エリアフリーでない会員種別では、局一覧を渡してもエリアの外から録るとは見なさない（in のフラグで決める）。
	if got, err := s.ReviseDeadlines(ctx, domain.MembershipTimeFree30, map[string]bool{"QRR": true}, now); err != nil || got.Revised != 2 {
		t.Errorf("revised = %+v, %v; want 2（retrying・settled が 30 日に戻る）", got, err)
	}
	if j := get(retrying); !j.Deadline.Equal(thirty) {
		t.Errorf("retrying: deadline = %v, want %v", j.Deadline, thirty)
	}
}
