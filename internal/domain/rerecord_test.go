package domain

import (
	"errors"
	"testing"
	"time"
)

// 録り直しを受け付けるのは、done のタイムフリーから録るジョブで録音期限の内側のものだけ。断る理由は、done でない・
// ライブで録るジョブ・録音期限を過ぎた、で分かれる（ADR 0025）。ライブ録音の録れた分を残している間の例外は当てない。
func TestCheckRerecord(t *testing.T) {
	end := jst(2026, 10, 7, 13, 0)
	timefree := func(state State) Job {
		return Job{State: state, Airings: []Airing{{StationID: "TBS", Start: end.Add(-time.Hour), End: end}}, Deadline: end.Add(7 * 24 * time.Hour)}
	}
	tests := []struct {
		name string
		job  Job
		now  time.Time
		want error
	}{
		{"done・録音期限の内側", timefree(StateDone), end.Add(time.Hour), nil},
		{"done・録音期限の 1 秒前", timefree(StateDone), end.Add(7*24*time.Hour - time.Second), nil},
		{"done・録音期限ちょうど", timefree(StateDone), end.Add(7 * 24 * time.Hour), ErrRerecordPastDeadline},
		{"ライブで録るジョブ", liveJob(StateDone, end), end.Add(time.Minute), ErrRerecordLive},
		{"expired", timefree(StateExpired), end.Add(time.Hour), ErrInvalidTransition},
		{"retrying", timefree(StateRetrying), end.Add(time.Hour), ErrInvalidTransition},
		{"waiting", timefree(StateWaiting), end.Add(time.Hour), ErrInvalidTransition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRerecord(tt.job, tt.now)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("CheckRerecord = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("CheckRerecord = %v, want %v", err, tt.want)
			}
		})
	}
}
