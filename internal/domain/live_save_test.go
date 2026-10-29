package domain

import (
	"testing"
	"time"
)

// liveJob は、タイムフリーの対象外の放送回 1 つを持つ（ライブで録る）ジョブ。
func liveJob(state State, end time.Time) Job {
	a := Airing{StationID: "TBS", Start: end.Add(-time.Hour), End: end}
	a.TimeFree.InNG = 2
	return Job{State: state, Airings: []Airing{a}, Deadline: end.Add(LiveDeadlineAfter)}
}

// 保存のやり直しの間隔は前の試行の開始から 15 分 → 1 時間 → 3 時間 → 以降 6 時間ごとで、打ち切りより後になるなら打ち切りの
// 時刻に置き、打ち切りの時刻以降に失敗したら次は無い。録音期限と試行回数の上限は見ない。
func TestLiveSaveRetryAt(t *testing.T) {
	end := jst(2026, 10, 7, 13, 0)
	j := liveJob(StateRunning, end)
	cutoff := j.LiveSaveCutoff()
	if !cutoff.Equal(end.Add(24 * time.Hour)) {
		t.Fatalf("LiveSaveCutoff = %v", cutoff)
	}
	start := end.Add(-time.Hour - time.Minute)
	tests := []struct {
		name      string
		attempts  int
		lastStart time.Time
		now       time.Time
		want      time.Time
		ok        bool
	}{
		{"1 回目の後は 15 分", 1, start, end, start.Add(15 * time.Minute), true},
		{"2 回目の後は 1 時間", 2, end, end, end.Add(time.Hour), true},
		{"3 回目の後は 3 時間", 3, end, end, end.Add(3 * time.Hour), true},
		{"4 回目の後は 6 時間", 4, end, end, end.Add(6 * time.Hour), true},
		{"20 回目の後も 6 時間（試行回数の上限は見ない）", 20, end, end, end.Add(6 * time.Hour), true},
		{"打ち切りより後なら打ち切りの時刻", 4, cutoff.Add(-time.Hour), cutoff.Add(-time.Hour), cutoff, true},
		{"打ち切りの時刻に失敗したら次は無い", 5, cutoff, cutoff, time.Time{}, false},
		{"打ち切りより後に失敗したら次は無い", 1, cutoff, cutoff.Add(time.Minute), time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := LiveSaveRetryAt(tt.attempts, tt.lastStart, cutoff, tt.now)
			if ok != tt.ok || !got.Equal(tt.want) {
				t.Errorf("LiveSaveRetryAt = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// 録れた分を残す期限は、ライブで録るジョブの最後の試行が保存か検証の段階で失敗したときだけ、その終わりの 30 日後。
// 手動再試行は録音期限の内側か、retrying・expired でその期限の前なら受け付ける。
func TestLiveKeepUntilAndManualRetryInTime(t *testing.T) {
	end := jst(2026, 10, 7, 13, 0)
	ended := end.Add(2 * time.Hour)
	at := func(stage Stage) *Attempt { return &Attempt{EndedAt: ended, FailedStage: stage} }
	keep := ended.Add(30 * 24 * time.Hour)
	timefree := Job{State: StateExpired, Airings: []Airing{{StationID: "TBS", Start: end.Add(-time.Hour), End: end}}, Deadline: end.Add(time.Hour)}
	tests := []struct {
		name     string
		job      Job
		last     *Attempt
		keep     time.Time
		now      time.Time
		retrying bool
	}{
		{"保存の失敗・30 日以内", liveJob(StateExpired, end), at(StageSave), keep, keep.Add(-time.Second), true},
		{"検証の失敗・retrying", liveJob(StateRetrying, end), at(StageVerify), keep, keep.Add(-time.Second), true},
		{"保存の失敗・30 日ちょうど", liveJob(StateExpired, end), at(StageSave), keep, keep, false},
		{"入手の失敗", liveJob(StateExpired, end), at(StageFetch), time.Time{}, ended, false},
		{"成功した試行", liveJob(StateExpired, end), at(""), time.Time{}, ended, false},
		{"試行が無い", liveJob(StateExpired, end), nil, time.Time{}, ended, false},
		{"done（状態が違う）", liveJob(StateDone, end), at(StageSave), keep, ended, false},
		{"タイムフリーから録るジョブ", timefree, at(StageSave), time.Time{}, ended, false},
		{"録音期限の内側なら試行を問わない", liveJob(StateExpired, end), nil, time.Time{}, end, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LiveKeepUntil(tt.job, tt.last); !got.Equal(tt.keep) {
				t.Errorf("LiveKeepUntil = %v, want %v", got, tt.keep)
			}
			if got := ManualRetryInTime(tt.job, tt.last, tt.now); got != tt.retrying {
				t.Errorf("ManualRetryInTime = %v, want %v", got, tt.retrying)
			}
		})
	}
}
