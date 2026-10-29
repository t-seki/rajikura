package domain

import (
	"testing"
	"time"
)

// excluded は放送回をタイムフリーの対象外（ts_in_ng が 0 でない）にしたもの。
func excluded(a Airing) Airing {
	a.TimeFree.InNG = 2
	return a
}

func TestLiveAirings(t *testing.T) {
	if LiveAirings([]Airing{flat1, flat2}) || (Job{Airings: []Airing{flat1}}).Live() {
		t.Error("対象外の放送回が無いのにライブで録る")
	}
	if !LiveAirings([]Airing{flat1, excluded(flat2)}) || !(Job{Airings: []Airing{excluded(flat1)}}).Live() {
		t.Error("対象外の放送回が 1 つでもあれば、ジョブをライブで録る")
	}
	if (Airing{TimeFree: TimeFreeFlags{OutNG: 1}}).TimeFreeExcluded() {
		t.Error("ts_out_ng だけでは対象外にしない")
	}
}

// タイムフリーの対象外の放送回の録音期限は、放送の終了の 5 分後（会員種別に依らない）。
func TestAiringDeadlineExcluded(t *testing.T) {
	for _, m := range []Membership{MembershipFree, MembershipTimeFree30} {
		if got, want := AiringDeadline(excluded(flat1), m), flat1.End.Add(5*time.Minute); !got.Equal(want) {
			t.Errorf("%s: got %v, want %v", m, got, want)
		}
	}
}

// ライブで録るジョブの録音期限は、最後の放送回の終了の 5 分後。対象外でない放送回が混ざっていても同じ。
func TestJobDeadlineLive(t *testing.T) {
	got := JobDeadline([]Airing{excluded(flat1), flat2}, MembershipFree)
	if want := flat2.End.Add(5 * time.Minute); !got.Equal(want) {
		t.Errorf("JobDeadline = %v, want %v", got, want)
	}
}

// ライブで録るジョブの最初の試行の時刻は、最初の放送回の開始の 1 分前。タイムフリーのジョブは FirstAttemptAt（最後の放送回の
// 終了から数える）。
func TestJobFirstAttemptAt(t *testing.T) {
	if got, want := JobFirstAttemptAt([]Airing{flat2, excluded(flat1)}, time.Minute), flat1.Start.Add(-time.Minute); !got.Equal(want) {
		t.Errorf("ライブ: got %v, want %v", got, want)
	}
	if got, want := JobFirstAttemptAt([]Airing{flat1, flat2}, time.Minute), FirstAttemptAt(flat2.End, time.Minute); !got.Equal(want) {
		t.Errorf("タイムフリー: got %v, want %v", got, want)
	}
}

// 展開は、対象外の放送回を持つジョブをライブで録る時刻（開始の 1 分前・終了の 5 分後）で作る。
func TestExpandLiveJobTimes(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{show}, nil, []Airing{excluded(flat1), news, flat2})
	if len(plan.Create) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	j := plan.Create[0]
	if !j.Live() || !j.NextAttemptAt.Equal(flat1.Start.Add(-time.Minute)) || !j.Deadline.Equal(flat2.End.Add(5*time.Minute)) {
		t.Errorf("job = %+v", j)
	}
	// 同じ番組表で展開し直しても差分は無い。
	j.ID = 1
	if p := expand([]Show{show}, []Job{j}, []Airing{excluded(flat1), news, flat2}); !p.Empty() {
		t.Errorf("差分がある: %+v", p)
	}
}

// 番組表で放送回が対象外になったら、タイムフリーの時刻で作ったジョブの時刻をライブで録る時刻に直し、対象外でなくなったら戻す。
func TestExpandSwitchesLiveTimes(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	tf := waitingJob(1, "ふらっと", 0, flat1, flat2)
	plan := expand([]Show{show}, []Job{tf}, []Airing{excluded(flat1), news, flat2})
	if len(plan.Update) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	lv := plan.Update[0]
	if !lv.NextAttemptAt.Equal(flat1.Start.Add(-time.Minute)) || !lv.Deadline.Equal(flat2.End.Add(5*time.Minute)) {
		t.Fatalf("ライブにした job = %+v", lv)
	}
	// 放送回は同じでも、時刻がライブのものでなければ直す（ライブで録ると決める前に作ったジョブ）。
	stale := lv
	stale.NextAttemptAt, stale.Deadline = tf.NextAttemptAt, tf.Deadline
	if p := expand([]Show{show}, []Job{stale}, []Airing{excluded(flat1), news, flat2}); len(p.Update) != 1 ||
		!p.Update[0].NextAttemptAt.Equal(lv.NextAttemptAt) {
		t.Errorf("時刻を直していない: %+v", p)
	}
	back := expand([]Show{show}, []Job{lv}, schedule)
	if len(back.Update) != 1 || !back.Update[0].NextAttemptAt.Equal(tf.NextAttemptAt) || !back.Update[0].Deadline.Equal(tf.Deadline) {
		t.Errorf("タイムフリーに戻した plan = %+v", back)
	}
}

// 今日より前の放送日では、開始日を持つ毎週の予約でも、録音期限（終了の 5 分後）を過ぎた対象外の放送回には当たらない。
func TestHitExcludedPastDeadline(t *testing.T) {
	s := joinedShow("ふらっと", NewWeekly("LFR", time.Monday, Clock{8, 30}).WithFrom(day))
	a := excluded(flat1)
	if got := s.Hit(a, jst(2026, 10, 6, 10, 0), MembershipFree); got != HitNone {
		t.Errorf("Hit = %v, want HitNone", got)
	}
	if got := s.Hit(flat1, jst(2026, 10, 6, 10, 0), MembershipFree); got != HitRecord {
		t.Errorf("対象外でなければ録音期限の内側: Hit = %v, want HitRecord", got)
	}
}
