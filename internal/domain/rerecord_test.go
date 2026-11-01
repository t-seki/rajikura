package domain

import (
	"errors"
	"testing"
	"time"
)

// 録り直しを受け付けるのは、done のタイムフリーから録るジョブで、今の会員種別で計算した録音期限の内側のものだけ。断る理由は、done でない・
// ライブで録るジョブ・録音期限を過ぎた、で分かれる（ADR 0031）。ライブ録音の録れた分を残している間の例外は当てない。
func TestCheckRerecord(t *testing.T) {
	end := jst(2026, 10, 7, 13, 0)
	// 放送日（10/7）の終わり（翌 05:00）に 7 日を足した 10/15 05:00 が、非会員の録音期限。
	free := jst(2026, 10, 15, 5, 0)
	timefree := func(state State) Job {
		return Job{State: state, Airings: []Airing{{StationID: "TBS", Start: end.Add(-time.Hour), End: end}}, Deadline: free}
	}
	excluded := timefree(StateDone)
	excluded.Airings[0].TimeFree.PlusInNG = 1
	later := free.Add(10 * 24 * time.Hour) // 非会員の録音期限を過ぎ、30 日の内側
	tests := []struct {
		name string
		job  Job
		m    Membership
		now  time.Time
		want error
	}{
		{"done・録音期限の内側", timefree(StateDone), MembershipFree, end.Add(time.Hour), nil},
		{"done・録音期限の 1 秒前", timefree(StateDone), MembershipFree, free.Add(-time.Second), nil},
		{"done・録音期限ちょうど", timefree(StateDone), MembershipFree, free, ErrRerecordPastDeadline},
		{"非会員・8 日目以降は DB の期限が新しくても断る", timefree(StateDone), MembershipFree, later, ErrRerecordPastDeadline},
		{"タイムフリー30・7 日を過ぎても受け付ける（DB の期限は 7 日のまま）", timefree(StateDone), MembershipTimeFree30, later, nil},
		{"ダブルプラン・7 日を過ぎても受け付ける", timefree(StateDone), MembershipDouble, later, nil},
		{"タイムフリー30・30 日を過ぎたら断る", timefree(StateDone), MembershipTimeFree30, jst(2026, 11, 7, 5, 0), ErrRerecordPastDeadline},
		{"タイムフリー30 の対象外の放送回は 7 日を過ぎたら断る", excluded, MembershipTimeFree30, later, ErrRerecordPastDeadline},
		{"ライブで録るジョブ", liveJob(StateDone, end), MembershipFree, end.Add(time.Minute), ErrRerecordLive},
		{"expired", timefree(StateExpired), MembershipFree, end.Add(time.Hour), ErrInvalidTransition},
		{"retrying", timefree(StateRetrying), MembershipFree, end.Add(time.Hour), ErrInvalidTransition},
		{"waiting", timefree(StateWaiting), MembershipFree, end.Add(time.Hour), ErrInvalidTransition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRerecord(tt.job, tt.m, tt.now)
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
