package domain

import (
	"testing"
	"time"
)

// タイムフリーから録るジョブの最初の試行は、最後の放送回の終了 + max(局の tf_max_delay, 150 秒) + 2 分で、チャプターを待って
// 遅らせない（ADR 0027）。ライブで録るジョブは開始の 1 分前。
func TestExpandFirstAttemptNotDelayedForChapters(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if got, want := plan.Create[0].NextAttemptAt, flat2.End.Add(150*time.Second+2*time.Minute); !got.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v", got, want)
	}
	live := expand([]Show{show}, nil, []Airing{excluded(flat1), news, flat2})
	if len(live.Create) != 1 || !live.Create[0].NextAttemptAt.Equal(flat1.Start.Add(-time.Minute)) {
		t.Errorf("ライブで録るジョブ: %+v", live)
	}
}

// 前の版が終了の 90 分後に遅らせた、試行がまだ無いタイムフリーの waiting のジョブは、展開で今までの時刻に戻る。
// 戻した後に展開し直しても差分は無い。retrying のジョブの時刻は直さない（展開が触らない）。
func TestExpandRestoresDelayedWaitingTimes(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	want := waitingJob(1, "ふらっと", 0, flat1, flat2)
	delayed := want
	delayed.NextAttemptAt = flat2.End.Add(90 * time.Minute)
	plan := expand([]Show{show}, []Job{delayed}, schedule)
	if len(plan.Update) != 1 || len(plan.Create)+len(plan.Delete) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	after := plan.Update[0]
	if !after.NextAttemptAt.Equal(want.NextAttemptAt) || after.State != StateWaiting {
		t.Errorf("戻した後: %+v, want next %v", after, want.NextAttemptAt)
	}
	if p := expand([]Show{show}, []Job{after}, schedule); !p.Empty() {
		t.Errorf("展開し直したら差分は無い: %+v", p)
	}

	retrying := delayed
	retrying.State = StateRetrying
	retrying.Attempts = 1
	if p := expand([]Show{show}, []Job{retrying}, schedule); !p.Empty() {
		t.Errorf("retrying のジョブの時刻を直した: %+v", p)
	}
}
