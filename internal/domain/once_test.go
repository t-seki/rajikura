package domain

import (
	"slices"
	"testing"
	"time"
)

func onceOf(a Airing) Once {
	return Once{StationID: a.StationID, Start: a.Start}
}

func TestOnceKey(t *testing.T) {
	// 1 回の予約の Key は放送回の Key と同じ形（局 + 開始時刻）。タイムゾーンが違っても同じ瞬間なら同じ。
	o := Once{StationID: "LFR", Start: flat1.Start.UTC()}
	if o.Key() != flat1.Key() {
		t.Errorf("Key = %q, want %q", o.Key(), flat1.Key())
	}
	if o.Day() != day {
		t.Errorf("Day = %v", o.Day())
	}
	// 深夜の放送回は前の放送日に属する。
	if d := onceOf(ann).Day(); d != day {
		t.Errorf("深夜の Day = %v", d)
	}
}

func TestOnceDeadline(t *testing.T) {
	// 開始時刻が属する放送日の翌日の終わりに、会員種別ごとの日数を足す（録音期限より早くならないように 1 日遅らせる）。
	want := day.AddDays(1 + 7).End()
	if got := onceOf(ann).Deadline(MembershipFree); !got.Equal(want) {
		t.Errorf("非会員 = %v, want %v", got, want)
	}
	if got := onceOf(flat1).Deadline(MembershipTimeFree30); !got.Equal(day.AddDays(1 + 30).End()) {
		t.Errorf("タイムフリー30 = %v", got)
	}
}

// 放送日の区切り（5:00）をまたぐ放送回でも、1 回の予約の期限は放送回の録音期限より早くならない。
func TestOnceDeadlineCrossesDayBoundary(t *testing.T) {
	// 10-06 04:30〜05:30。開始は放送日 10-05、終了は放送日 10-06 に属する。
	cross := Airing{StationID: "LFR", Start: jst(2026, 10, 6, 4, 30), End: jst(2026, 10, 6, 5, 30)}
	if cross.Day() != day {
		t.Fatalf("Day = %v", cross.Day())
	}
	for _, m := range []Membership{MembershipFree, MembershipTimeFree30} {
		airing := AiringDeadline(cross, m)
		if got := onceOf(cross).Deadline(m); got.Before(airing) {
			t.Errorf("%s: 1 回の予約の期限 %v が放送回の録音期限 %v より早い", m, got, airing)
		}
	}
	// 区切りをまたがない放送回でも、録音期限より早くならない（1 日遅い）。
	for _, a := range []Airing{flat1, ann} {
		if got, airing := onceOf(a).Deadline(MembershipFree), AiringDeadline(a, MembershipFree); !got.Equal(airing.AddDate(0, 0, 1)) {
			t.Errorf("%s: %v, 録音期限 %v", a.Title, got, airing)
		}
	}
}

// 今日より前の放送日の展開では、開始日を持たない毎週の予約だけに当たる放送回から新しいジョブを作らない
// （1 回の予約の放送回を含むジョブは作る）。
func TestExpandPastDayWithoutFrom(t *testing.T) {
	past := func(shows []Show, jobs []Job) Plan {
		return Expand(ExpandInput{
			Shows: shows, StationID: "LFR", Day: day, Airings: schedule, Jobs: jobs,
			Membership: MembershipFree, TFMaxDelay: 60 * time.Second, Now: day.AddDays(1).Start().Add(time.Hour),
		})
	}
	// ほかの番組の毎週の予約に当たる放送済みの回は、ジョブにならない。
	withWeekly := joinedShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}))
	one := joinedShow("ニュース")
	one.Onces = []Once{onceOf(news)}
	plan := past([]Show{withWeekly, one}, nil)
	if len(plan.Create) != 1 || plan.Create[0].ShowName != "ニュース" || len(plan.Update)+len(plan.Delete)+len(plan.Conflicts) != 0 {
		t.Errorf("ほかの番組の毎週の予約: plan = %+v", plan)
	}

	// 連結する番組なら、1 回の予約と同じ放送日の毎週の予約の放送回も同じジョブに入る。
	joined := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}))
	joined.Onces = []Once{onceOf(flat2)}
	plan = past([]Show{joined}, nil)
	if len(plan.Create) != 1 || !slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Errorf("連結する: plan = %+v", plan)
	}
	// 連結しない番組では、1 回の予約の放送回のジョブだけを作る。
	joined.Split = SplitSeparate
	plan = past([]Show{joined}, nil)
	if len(plan.Create) != 1 || !slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (2)"}) {
		t.Errorf("連結しない: plan = %+v", plan)
	}

	// 既存の waiting のジョブの更新と、当たらなくなった放送回を外すのは変わらない。
	weekly := joinedShow("ふらっと", flatWeekly()...)
	plan = past([]Show{weekly}, []Job{waitingJob(1, "ふらっと", 0, flat1)})
	if len(plan.Update) != 1 || !slices.Equal(titles(plan.Update[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) || len(plan.Create) != 0 {
		t.Errorf("更新: plan = %+v", plan)
	}
	plan = past(nil, []Job{waitingJob(1, "ふらっと", 0, flat1)})
	if len(plan.Delete) != 1 {
		t.Errorf("外す: plan = %+v", plan)
	}
}

func TestShowValidateOnces(t *testing.T) {
	// 毎週の予約の無い番組も正しい。
	if err := joinedShow("単発だけ").Validate(); err != nil {
		t.Errorf("毎週の予約の無い番組: %v", err)
	}
	show := joinedShow("単発だけ")
	show.Onces = []Once{onceOf(flat1)}
	if err := show.Validate(); err != nil {
		t.Errorf("1 回の予約だけの番組: %v", err)
	}
	for _, o := range []Once{{Start: flat1.Start}, {StationID: "LFR"}} {
		show.Onces = []Once{o}
		if err := show.Validate(); err == nil {
			t.Errorf("%+v を通した", o)
		}
	}
}

func TestShowMatchesOnce(t *testing.T) {
	show := joinedShow("単発だけ")
	show.Onces = []Once{onceOf(flat2)}
	if !show.Matches(flat2) {
		t.Error("1 回の予約の放送回に当たらない")
	}
	if show.Matches(flat1) || show.Matches(news) {
		t.Error("ほかの放送回に当たった")
	}
	// ほかの局の同じ開始時刻には当たらない。
	if show.Matches(Airing{StationID: "TBS", Start: flat2.Start, End: flat2.End}) {
		t.Error("ほかの局に当たった")
	}
	// 無効な番組の 1 回の予約は当たらない。
	show.Disabled = true
	if show.Matches(flat2) {
		t.Error("無効な番組の 1 回の予約が当たった")
	}
}

func TestExpandOnce(t *testing.T) {
	show := joinedShow("ニュース")
	show.Onces = []Once{onceOf(news)}
	plan := expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].ShowName != "ニュース" || !slices.Equal(titles(plan.Create[0].Airings), []string{"ニュース"}) ||
		len(plan.Update)+len(plan.Delete)+len(plan.Conflicts) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	// 番組表を取り直しても消えない（差分なし）。
	job := plan.Create[0]
	job.ID = 1
	if p := expand([]Show{show}, []Job{job}, schedule); !p.Empty() {
		t.Errorf("取り直し: plan = %+v", p)
	}
}

func TestExpandOnceJoinsWeekly(t *testing.T) {
	// 連結する番組で、毎週の予約の放送回（月曜 8:30）と 1 回の予約の放送回（10:10）が同じ放送日にあれば 1 つのジョブにする。
	show := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}))
	show.Onces = []Once{onceOf(flat2)}
	plan := expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 1 || !slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Fatalf("連結する: plan = %+v", plan)
	}
	// 連結しない番組ならパートに分ける。
	show.Split = SplitSeparate
	plan = expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 2 || plan.Create[0].Part != 1 || plan.Create[1].Part != 2 {
		t.Errorf("連結しない: plan = %+v", plan)
	}
	// 毎週と 1 回の予約が同じ放送回に当たっても 1 回だけ持つ。
	show.Split = SplitJoin
	show.Onces = []Once{onceOf(flat1)}
	plan = expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 1 || !slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)"}) || len(plan.Conflicts) != 0 {
		t.Errorf("重なり: plan = %+v", plan)
	}
}

func TestExpandOnceGone(t *testing.T) {
	// 番組表から 1 回の予約の放送回が消えたら、waiting のジョブから外す（放送回が 0 になれば消す）。
	show := joinedShow("ニュース")
	show.Onces = []Once{onceOf(news)}
	job := waitingJob(1, "ニュース", 0, news)
	plan := expand([]Show{show}, []Job{job}, []Airing{flat1, flat2})
	if len(plan.Delete) != 1 || plan.Delete[0].ID != 1 || len(plan.Create)+len(plan.Update) != 0 {
		t.Errorf("消えた: plan = %+v", plan)
	}
	// 開始時刻が変わったら追いかけない。
	moved := news
	moved.Start = news.Start.Add(5 * time.Minute)
	plan = expand([]Show{show}, []Job{job}, []Airing{flat1, moved, flat2})
	if len(plan.Delete) != 1 || len(plan.Create)+len(plan.Update) != 0 {
		t.Errorf("時刻が変わった: plan = %+v", plan)
	}
	// 連結する番組では、ジョブのうち 1 回の予約の放送回だけを外す。
	withWeekly := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}))
	withWeekly.Onces = []Once{onceOf(flat2)}
	plan = expand([]Show{withWeekly}, []Job{waitingJob(2, "ふらっと", 0, flat1, flat2)}, []Airing{flat1, news})
	if len(plan.Update) != 1 || !slices.Equal(titles(plan.Update[0].Airings), []string{"ふらっと (1)"}) {
		t.Errorf("連結: plan = %+v", plan)
	}
}

func TestExpandOnceDisabled(t *testing.T) {
	// 無効な番組の 1 回の予約は当たらず、waiting のジョブは消える。
	show := joinedShow("ニュース")
	show.Onces = []Once{onceOf(news)}
	show.Disabled = true
	if p := expand([]Show{show}, nil, schedule); !p.Empty() {
		t.Errorf("作る: plan = %+v", p)
	}
	plan := expand([]Show{show}, []Job{waitingJob(1, "ニュース", 0, news)}, schedule)
	if len(plan.Delete) != 1 || len(plan.Create)+len(plan.Update) != 0 {
		t.Errorf("無効にした後: plan = %+v", plan)
	}
}

func TestExpandOnceConflict(t *testing.T) {
	// ほかの番組の毎週の予約が当たっている放送回の 1 回の予約は矛盾。
	withWeekly := joinedShow("ふらっと", flatWeekly()...)
	one := joinedShow("単発")
	one.Onces = []Once{onceOf(flat1)}
	plan := expand([]Show{withWeekly, one}, nil, schedule)
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Airing.Key() != flat1.Key() ||
		!slices.Equal(plan.Conflicts[0].Shows, []string{"ふらっと", "単発"}) {
		t.Errorf("Conflicts = %+v", plan.Conflicts)
	}
}
