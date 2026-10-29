package domain

import (
	"slices"
	"testing"
	"time"
)

// afterDay は day（2026-10-05）を今日より前にする今の時刻（翌日の放送日の 6:00）。
var afterDay = day.AddDays(1).Start().Add(time.Hour)

// fromWeekly は「ふらっと」の放送回（と extra の開始時刻）に当たる、開始日 day の毎週の予約。
func fromWeekly(extra ...Clock) []Weekly {
	var out []Weekly
	for _, r := range flatWeekly(extra...) {
		out = append(out, r.WithFrom(day))
	}
	return out
}

func TestShowHit(t *testing.T) {
	flat := joinedShow("ふらっと", fromWeekly()...)
	noFrom := joinedShow("ふらっと", flatWeekly()...)
	other := joinedShow("別の番組", fromWeekly()...)
	otherNoCheck := other
	otherNoCheck.CheckTitle = false
	otherOnce := other
	otherOnce.Onces = []Once{onceOf(flat1)}
	later := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}).WithFrom(day.AddDays(7)))
	disabled := flat
	disabled.Disabled = true
	deadline := AiringDeadline(flat1, MembershipFree)

	tests := []struct {
		name string
		r    Show
		now  time.Time
		want Hit
	}{
		{"今日より前・開始日あり", flat, afterDay, HitRecord},
		{"今日より前・開始日なし", noFrom, afterDay, HitNone},
		{"今日より前・開始日より前の放送日", later, afterDay, HitNone},
		{"今日より前・録音期限の直前", flat, deadline.Add(-time.Nanosecond), HitRecord},
		{"今日より前・録音期限を過ぎた", flat, deadline, HitNone},
		{"今日より前・番組名の食い違い", other, afterDay, HitSkip},
		{"今日より前・番組名の食い違い・check_title なし", otherNoCheck, afterDay, HitRecord},
		{"今日より前・番組名の食い違い・1 回の予約", otherOnce, afterDay, HitRecord},
		{"今日より前・無効な番組", disabled, afterDay, HitNone},
		{"今日・開始日なし", noFrom, day.Start().Add(time.Hour), HitRecord},
		{"今日・番組名の食い違い", other, day.Start().Add(time.Hour), HitSkip},
		{"今日から先・番組名の食い違い", other, day.AddDays(-1).Start(), HitSkip},
		{"今日から先・番組名の食い違い・開始日なし", joinedShow("別の番組", flatWeekly()...), day.AddDays(-1).Start(), HitSkip},
		{"今日から先・番組名の食い違い・check_title なし", otherNoCheck, day.AddDays(-1).Start(), HitRecord},
		{"今日から先・番組名の食い違い・1 回の予約", otherOnce, day.AddDays(-1).Start(), HitRecord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Hit(flat1, tt.now, MembershipFree); got != tt.want {
				t.Errorf("Hit = %v, want %v", got, tt.want)
			}
		})
	}
	// 当たらない放送回は、どの日でも当たらない。
	if got := flat.Hit(news, afterDay, MembershipFree); got != HitNone {
		t.Errorf("当たらない放送回: Hit = %v", got)
	}
}

func TestPastWeeklyDays(t *testing.T) {
	// 今日は 2026-10-15（木）。月曜の毎週の予約で、開始日は 2026-09-21（月）。
	now := jst(2026, 10, 15, 6, 0)
	from := NewBroadcastDay(2026, 9, 21)
	monday := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}).WithFrom(from))
	days := func(ds []BroadcastDay) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.String())
		}
		return out
	}
	// 非会員（7 日）では、録音期限の内側の 10-12 だけ。
	if got := days(PastWeeklyDays(now, MembershipFree, []Show{monday})); !slices.Equal(got, []string{"2026-10-12"}) {
		t.Errorf("非会員 = %v", got)
	}
	// タイムフリー30 では、開始日からの月曜が全部（古い順）。
	if got := days(PastWeeklyDays(now, MembershipTimeFree30, []Show{monday})); !slices.Equal(got,
		[]string{"2026-09-21", "2026-09-28", "2026-10-05", "2026-10-12"}) {
		t.Errorf("タイムフリー30 = %v", got)
	}
	// 開始日なし・無効な番組・開始日が今日の毎週の予約は足さない。
	noFrom := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}))
	disabled := monday
	disabled.Disabled = true
	today := joinedShow("ふらっと", NewWeekly("LFR", time.Thursday, Clock{8, 30}).WithFrom(NewBroadcastDay(2026, 10, 15)))
	if got := PastWeeklyDays(now, MembershipTimeFree30, []Show{noFrom, disabled, today}); len(got) != 0 {
		t.Errorf("足さないもの = %v", days(got))
	}
}

func pastExpand(shows []Show, jobs []Job, airings []Airing) Plan {
	return Expand(ExpandInput{
		Shows: shows, StationID: "LFR", Day: day, Airings: airings, Jobs: jobs,
		Membership: MembershipFree, TFMaxDelay: 60 * time.Second, Now: afterDay,
	})
}

func states(js []Job) []State {
	var out []State
	for _, j := range js {
		out = append(out, j.State)
	}
	return out
}

// 開始日を持つ毎週の予約は、今日より前の放送日でもジョブを作る。
func TestExpandPastDayWithFrom(t *testing.T) {
	plan := pastExpand([]Show{joinedShow("ふらっと", fromWeekly()...)}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].State != StateWaiting ||
		!slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Fatalf("plan = %+v", plan)
	}
	// 作ったジョブがあれば、同期を繰り返しても差分は無い。
	job := plan.Create[0]
	job.ID = 1
	if p := pastExpand([]Show{joinedShow("ふらっと", fromWeekly()...)}, []Job{job}, schedule); !p.Empty() {
		t.Errorf("繰り返し: plan = %+v", p)
	}
	// 録音期限を過ぎた放送回からはジョブを作らない。
	in := ExpandInput{
		Shows: []Show{joinedShow("ふらっと", fromWeekly()...)}, StationID: "LFR", Day: day, Airings: schedule,
		Membership: MembershipFree, TFMaxDelay: 60 * time.Second, Now: AiringDeadline(flat2, MembershipFree),
	}
	if p := Expand(in); !p.Empty() {
		t.Errorf("録音期限を過ぎた: plan = %+v", p)
	}
}

// 今日より前の放送日で、番組名の食い違う放送回は録らなかったのジョブになる。
func TestExpandPastDaySkipped(t *testing.T) {
	// 連結する番組で全部食い違えば、録らなかったのジョブを 1 つ作る。繰り返しても作り直さない。
	other := joinedShow("別の番組", fromWeekly()...)
	plan := pastExpand([]Show{other}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].State != StateSkipped || !plan.Create[0].Settled ||
		!slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Fatalf("全部食い違う（今日より前の放送日では作った時点で確定する）: plan = %+v", plan)
	}
	skipped := plan.Create[0]
	skipped.ID = 1
	if p := pastExpand([]Show{other}, []Job{skipped}, schedule); !p.Empty() {
		t.Errorf("繰り返し: plan = %+v", p)
	}

	// 連結する番組で一部だけ食い違えば、番組名の合う放送回だけでジョブを作る。
	partial := joinedShow("ふらっと", fromWeekly(Clock{10, 0})...)
	plan = pastExpand([]Show{partial}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].State != StateWaiting ||
		!slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Errorf("一部食い違う（連結）: plan = %+v", plan)
	}
	// 既存の waiting のジョブを更新するときも、食い違う放送回は入れない。
	if p := pastExpand([]Show{partial}, []Job{waitingJob(1, "ふらっと", 0, flat1)}, schedule); len(p.Update) != 1 ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) || len(p.Create) != 0 {
		t.Errorf("更新（連結）: plan = %+v", p)
	}
	// もう入っている食い違う放送回も外す（今日から先の放送日でも番組名を確かめるので、入っていることは無いはずだが）。
	if p := pastExpand([]Show{partial}, []Job{waitingJob(1, "ふらっと", 0, flat1, news, flat2)}, schedule); len(p.Update) != 1 ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Errorf("入っている（連結）: plan = %+v", p)
	}

	// 連結しない番組では、食い違う放送回も放送回ごとの録らなかったのジョブになる。パート番号は全部で振る。
	partial.Split = SplitSeparate
	plan = pastExpand([]Show{partial}, nil, schedule)
	if len(plan.Create) != 3 || !slices.Equal(states(plan.Create), []State{StateWaiting, StateSkipped, StateWaiting}) ||
		plan.Create[1].Part != 2 || plan.Create[1].Airings[0].Title != "ニュース" {
		t.Errorf("一部食い違う（連結しない）: plan = %+v", plan)
	}

	// check_title が false の番組は確かめず、全部録る。
	other.CheckTitle = false
	plan = pastExpand([]Show{other}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].State != StateWaiting || len(plan.Create[0].Airings) != 2 {
		t.Errorf("check_title なし: plan = %+v", plan)
	}

	// 1 回の予約の放送回は確かめずに録る（同じ番組の毎週の予約に当たっていても）。
	other.CheckTitle = true
	other.Onces = []Once{onceOf(flat2)}
	plan = pastExpand([]Show{other}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].State != StateWaiting || !slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (2)"}) {
		t.Errorf("1 回の予約（連結）: plan = %+v", plan)
	}
	other.Split = SplitSeparate
	plan = pastExpand([]Show{other}, nil, schedule)
	if len(plan.Create) != 2 || !slices.Equal(states(plan.Create), []State{StateSkipped, StateWaiting}) {
		t.Errorf("1 回の予約（連結しない）: plan = %+v", plan)
	}

	// 今日の放送日でも確かめ、最初の試行の時刻の前なら録らない予定（確定しない）にする。
	in := ExpandInput{
		Shows: []Show{joinedShow("別の番組", fromWeekly()...)}, StationID: "LFR", Day: day, Airings: schedule,
		Membership: MembershipFree, TFMaxDelay: 60 * time.Second, Now: day.Start().Add(time.Hour),
	}
	if p := Expand(in); len(p.Create) != 1 || p.Create[0].State != StateSkipped || p.Create[0].Settled {
		t.Errorf("今日: plan = %+v", p)
	}
}
