package domain

import (
	"slices"
	"testing"
	"time"
)

// 番組の切り替え（ADR 0029）の放送回。同じ枠（LFR の木曜 25:00）で、放送日 2026-10-08 が古い番組の最終回、
// 2026-10-15 が新しい番組の初回。
var (
	handoverLast  = NewBroadcastDay(2026, 10, 8)
	handoverFirst = NewBroadcastDay(2026, 10, 15)
	oldAiring     = Airing{StationID: "LFR", Start: jst(2026, 10, 9, 1, 0), End: jst(2026, 10, 9, 3, 0), Title: "ナインティナインのオールナイトニッポン"}
	newAiring     = Airing{StationID: "LFR", Start: jst(2026, 10, 16, 1, 0), End: jst(2026, 10, 16, 3, 0), Title: "ナインティナイン岡村隆史のオールナイトニッポン"}
	// handoverNow は、どちらの放送回よりも前の今の時刻。
	handoverNow = jst(2026, 10, 1, 12, 0)
)

func handoverWeekly() Weekly { return NewWeekly("LFR", time.Thursday, Clock{1, 0}) }

func handoverExpand(shows []Show, jobs []Job, d BroadcastDay) Plan {
	return Expand(ExpandInput{
		Shows: shows, StationID: "LFR", Day: d, Airings: []Airing{oldAiring, newAiring}, Jobs: jobs,
		Membership: MembershipFree, TFMaxDelay: 60 * time.Second, Now: handoverNow,
	})
}

// 古い番組の毎週の予約に終了日、新しい番組の同じ枠の毎週の予約に開始日を付けると、矛盾にならずに、終了日までの放送日は
// 古い番組、開始日からの放送日は新しい番組のジョブになる。
func TestExpandHandoverWithUntil(t *testing.T) {
	oldShow := NewShow("ナインティナインのオールナイトニッポン", handoverWeekly().WithUntil(handoverLast))
	newShow := NewShow("ナインティナイン岡村隆史のオールナイトニッポン", handoverWeekly().WithFrom(handoverFirst))
	shows := []Show{oldShow, newShow}

	for _, tt := range []struct {
		day  BroadcastDay
		show string
		a    Airing
	}{
		{handoverLast, oldShow.Name, oldAiring},
		{handoverFirst, newShow.Name, newAiring},
	} {
		plan := handoverExpand(shows, nil, tt.day)
		if len(plan.Conflicts) != 0 {
			t.Errorf("%s: 矛盾 = %+v", tt.day, plan.Conflicts)
		}
		if len(plan.Create) != 1 || plan.Create[0].ShowName != tt.show || plan.Create[0].State != StateWaiting ||
			!slices.Equal(titles(plan.Create[0].Airings), []string{tt.a.Title}) {
			t.Errorf("%s: plan = %+v", tt.day, plan)
		}
	}

	// 終了日が無ければ、新しい番組の初回は両方の番組の予約に当たり、矛盾になる（終了日が無かったときの困りごと）。
	withoutUntil := []Show{NewShow(oldShow.Name, handoverWeekly()), newShow}
	if plan := handoverExpand(withoutUntil, nil, handoverFirst); len(plan.Conflicts) != 1 || len(plan.Create) != 0 {
		t.Errorf("終了日なし: plan = %+v", plan)
	}
}

// 終了日を付けると、終了日より後の放送日の既存の録音予定のジョブは、放送回が当たらなくなって消える。
// 終了日の放送日のジョブはそのまま残る。
func TestExpandUntilDeletesLaterJobs(t *testing.T) {
	show := NewShow("ナインティナインのオールナイトニッポン", handoverWeekly())
	jobs := map[BroadcastDay]Job{}
	for i, d := range []BroadcastDay{handoverLast, handoverFirst} {
		plan := handoverExpand([]Show{show}, nil, d)
		if len(plan.Create) != 1 {
			t.Fatalf("%s: 終了日を付ける前: plan = %+v", d, plan)
		}
		j := plan.Create[0]
		j.ID = int64(i + 1)
		jobs[d] = j
	}

	show.Weekly[0] = show.Weekly[0].WithUntil(handoverLast)
	if plan := handoverExpand([]Show{show}, []Job{jobs[handoverLast]}, handoverLast); !plan.Empty() {
		t.Errorf("終了日の放送日: plan = %+v", plan)
	}
	plan := handoverExpand([]Show{show}, []Job{jobs[handoverFirst]}, handoverFirst)
	if len(plan.Delete) != 1 || plan.Delete[0].ID != jobs[handoverFirst].ID || len(plan.Create)+len(plan.Update) != 0 {
		t.Errorf("終了日より後の放送日: plan = %+v", plan)
	}
}

// 終了日より後の放送日は、PastWeeklyDays に入らない（その放送日の番組表を取りにいかない）。
func TestPastWeeklyDaysUntil(t *testing.T) {
	// 今日は 2026-10-15（木）。月曜の毎週の予約で、開始日は 2026-09-21（月）。
	now := jst(2026, 10, 15, 6, 0)
	from := NewBroadcastDay(2026, 9, 21)
	days := func(until BroadcastDay) []string {
		monday := NewShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}).WithFrom(from).WithUntil(until))
		var out []string
		for _, d := range PastWeeklyDays(now, MembershipTimeFree30, []Show{monday}) {
			out = append(out, d.String())
		}
		return out
	}
	if got := days(NewBroadcastDay(2026, 10, 5)); !slices.Equal(got, []string{"2026-09-21", "2026-09-28", "2026-10-05"}) {
		t.Errorf("終了日が月曜（含む） = %v", got)
	}
	if got := days(NewBroadcastDay(2026, 10, 4)); !slices.Equal(got, []string{"2026-09-21", "2026-09-28"}) {
		t.Errorf("終了日が日曜 = %v", got)
	}
	if got := days(NewBroadcastDay(2026, 9, 21)); !slices.Equal(got, []string{"2026-09-21"}) {
		t.Errorf("終了日が開始日と同じ = %v", got)
	}
}
