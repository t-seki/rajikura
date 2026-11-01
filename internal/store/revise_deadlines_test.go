package store

import (
	"context"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 会員種別を保存したら、まだ終わっていないジョブ（waiting・retrying・skipped）の録音期限を付け直す。done・expired は触らない。
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

	got, err := s.ReviseDeadlines(ctx, domain.MembershipDouble)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("revised = %d, want 2（waiting と skipped）", got)
	}
	check(waiting, thirty, first)
	check(retrying, thirty, late)
	check(skipped, thirty, first)
	check(excluded, free, first)
	check(done, free, first)
	check(expired, free, first)

	// tsplus_in_ng は DB に持つ。
	if j, err := s.GetJob(ctx, excluded); err != nil || j.Airings[0].TimeFree.PlusInNG != 1 {
		t.Fatalf("airings = %+v, %v; want tsplus_in_ng = 1", j.Airings, err)
	}

	// 非会員に戻すと 7 日になり、retrying の次の試行は録音期限に寄せる。
	if got, err := s.ReviseDeadlines(ctx, domain.MembershipFree); err != nil || got != 3 {
		t.Fatalf("revised = %d, %v; want 3", got, err)
	}
	check(waiting, free, first)
	check(retrying, free, free)
	check(skipped, free, first)
}
