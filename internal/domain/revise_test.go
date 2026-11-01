package domain

import (
	"testing"
	"time"
)

// 録音期限の付け直しは、録音の方式を変えずに、今の会員種別と今の自分のエリアの局一覧で録音期限を求め直す。
func TestReviseJobDeadline(t *testing.T) {
	a := Airing{StationID: "TBS", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 11, 0)}
	thirty := jst(2026, 11, 5, 5, 0)
	seven := jst(2026, 10, 13, 5, 0)
	liveEnd := a.End.Add(LiveDeadlineAfter)
	with := func(f TimeFreeFlags, out bool) Airing {
		b := a
		b.TimeFree, b.OutOfArea = f, out
		return b
	}
	tests := []struct {
		name string
		a    Airing
		m    Membership
		own  map[string]bool
		want time.Time
	}{
		{"自分のエリアの局のまま", a, MembershipDouble, map[string]bool{"TBS": true}, thirty},
		{"エリアの外になり ts_out_ng でタイムフリーで聴けない", with(TimeFreeFlags{OutNG: 1}, false), MembershipDouble, map[string]bool{"QRR": true}, liveEnd},
		{"エリアの外になり tsplus_out_ng で 7 日", with(TimeFreeFlags{PlusOutNG: 1}, false), MembershipDouble, map[string]bool{"QRR": true}, seven},
		{"エリアの外から自分のエリアに入り tsplus_in_ng で 7 日", with(TimeFreeFlags{PlusInNG: 1}, true), MembershipDouble, map[string]bool{"TBS": true}, seven},
		{"局一覧が分からなければ印のまま", with(TimeFreeFlags{PlusOutNG: 1}, true), MembershipDouble, nil, seven},
		{"エリアフリーでなければ外と見なさない", with(TimeFreeFlags{PlusOutNG: 1}, true), MembershipTimeFree30, map[string]bool{"QRR": true}, thirty},
		{"ライブで録るジョブは変えない", with(TimeFreeFlags{InNG: 1}, false), MembershipDouble, map[string]bool{"QRR": true}, liveEnd},
	}
	for _, tc := range tests {
		j := Job{Airings: []Airing{tc.a}}
		live := j.Live()
		if got := ReviseJobDeadline(j, tc.m, tc.own); !got.Equal(tc.want) {
			t.Errorf("%s: ReviseJobDeadline = %v, want %v", tc.name, got, tc.want)
		}
		if j.Live() != live || !j.Airings[0].equal(tc.a) || j.Airings[0].OutOfArea != tc.a.OutOfArea {
			t.Errorf("%s: ジョブの放送回が書き換わった: %+v", tc.name, j.Airings[0])
		}
	}
	if got := ReviseJobDeadline(Job{}, MembershipFree, nil); !got.IsZero() {
		t.Errorf("放送回が無い: %v, want ゼロ値", got)
	}
}
