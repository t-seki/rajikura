package domain

import (
	"slices"
	"testing"
	"time"
)

// beforeDay は day（2026-10-05）を今日から先にする今の時刻（前の放送日の 6:00）。
var beforeDay = day.AddDays(-1).Start().Add(time.Hour)

func findUnmatched(shows []Show, airings []Airing, now time.Time) UnmatchedResult {
	return FindUnmatchedDays(UnmatchedInput{Shows: shows, StationID: "LFR", Day: day, Airings: airings, Now: now,
		Until: BroadcastDayOf(now).AddDays(7)})
}

func TestSlotStart(t *testing.T) {
	if got, want := day.SlotStart(Clock{17, 40}), jst(2026, 10, 5, 17, 40); !got.Equal(want) {
		t.Errorf("17:40 = %v, want %v", got, want)
	}
	// 0〜4 時台は暦の翌日。
	if got, want := day.SlotStart(Clock{1, 0}), jst(2026, 10, 6, 1, 0); !got.Equal(want) {
		t.Errorf("25:00 = %v, want %v", got, want)
	}
}

func TestFindUnmatchedDays(t *testing.T) {
	// 17:40 の枠に放送回が無く、その時刻は 15:30〜18:00 の「辛坊」が含む。番組名の合う放送回は 19:00〜19:30。
	shinbo := Airing{StationID: "LFR", Start: jst(2026, 10, 5, 15, 30), End: jst(2026, 10, 5, 18, 0), Title: "辛坊治郎 ズーム そこまで言うか！"}
	chari := Airing{StationID: "LFR", Start: jst(2026, 10, 5, 19, 0), End: jst(2026, 10, 5, 19, 30), Title: "チャリで30分 (2)"}
	otherStation := Airing{StationID: "TBS", Start: jst(2026, 10, 5, 17, 40), End: jst(2026, 10, 5, 18, 0), Title: "チャリで30分"}
	airings := []Airing{shinbo, chari, otherStation, flat1, news, flat2}
	weekly := NewWeekly("LFR", time.Monday, Clock{17, 40})
	r := joinedShow("チャリで30分", weekly)

	t.Run("枠に放送回が無い", func(t *testing.T) {
		res := findUnmatched([]Show{r}, airings, beforeDay)
		if len(res.Days) != 1 {
			t.Fatalf("Days = %+v", res.Days)
		}
		u := res.Days[0]
		if u.ShowName != "チャリで30分" || u.StationID != "LFR" || u.Day != day || !u.Start.Equal(jst(2026, 10, 5, 17, 40)) {
			t.Errorf("当たらない日 = %+v", u)
		}
		if u.Covering == nil || u.Covering.Title != shinbo.Title {
			t.Errorf("その時刻を含む放送回 = %+v（ほかの局の放送回は見ない）", u.Covering)
		}
		if got := titles(u.Titled); !slices.Equal(got, []string{chari.Title}) {
			t.Errorf("番組名の合う放送回 = %v", got)
		}
		if !slices.Equal(res.Targets, []string{u.Key()}) {
			t.Errorf("Targets = %v", res.Targets)
		}
	})

	t.Run("その時刻を含む放送回が無い", func(t *testing.T) {
		res := findUnmatched([]Show{r}, []Airing{flat1, news}, beforeDay)
		if len(res.Days) != 1 || res.Days[0].Covering != nil || len(res.Days[0].Titled) != 0 {
			t.Errorf("Days = %+v", res.Days)
		}
	})

	t.Run("当たる枠は当たらない日にしない", func(t *testing.T) {
		flat := joinedShow("ふらっと", flatWeekly()...)
		res := findUnmatched([]Show{flat}, airings, beforeDay)
		if len(res.Days) != 0 || len(res.Targets) != 2 {
			t.Errorf("res = %+v", res)
		}
	})

	t.Run("check_title を問わない", func(t *testing.T) {
		noCheck := r
		noCheck.CheckTitle = false
		if res := findUnmatched([]Show{noCheck}, airings, beforeDay); len(res.Days) != 1 {
			t.Errorf("Days = %+v", res.Days)
		}
	})

	t.Run("同じ枠の毎週の予約は 1 件", func(t *testing.T) {
		twice := joinedShow("チャリで30分", weekly, weekly)
		if res := findUnmatched([]Show{twice}, airings, beforeDay); len(res.Days) != 1 || len(res.Targets) != 1 {
			t.Errorf("res = %+v", res)
		}
	})

	t.Run("深夜の枠", func(t *testing.T) {
		late := joinedShow("深夜", NewWeekly("LFR", time.Monday, Clock{3, 0}))
		res := findUnmatched([]Show{late}, schedule, beforeDay)
		if len(res.Days) != 1 || !res.Days[0].Start.Equal(jst(2026, 10, 6, 3, 0)) {
			t.Fatalf("Days = %+v", res.Days)
		}
		// 3:00 は 1:00〜3:00 の放送回の終わりなので、含まない。
		if res.Days[0].Covering != nil {
			t.Errorf("Covering = %+v", res.Days[0].Covering)
		}
		at2 := joinedShow("深夜", NewWeekly("LFR", time.Monday, Clock{2, 0}))
		if res := findUnmatched([]Show{at2}, schedule, beforeDay); len(res.Days) != 1 || res.Days[0].Covering == nil ||
			res.Days[0].Covering.Title != ann.Title {
			t.Errorf("26:00 の Days = %+v", res.Days)
		}
	})

	// 対象にしない枠は、当たらない日にも Targets にも入れない。
	disabled := r
	disabled.Disabled = true
	later := joinedShow("チャリで30分", weekly.WithFrom(day.AddDays(1)))
	ended := joinedShow("チャリで30分", weekly.WithUntil(day.AddDays(-1)))
	tuesday := joinedShow("チャリで30分", NewWeekly("LFR", time.Tuesday, Clock{17, 40}))
	tbs := joinedShow("チャリで30分", NewWeekly("TBS", time.Monday, Clock{17, 30}))
	cases := []struct {
		name string
		r    Show
		now  time.Time
	}{
		{"無効な番組", disabled, beforeDay},
		{"開始日より前の放送日", later, beforeDay},
		{"終了日より後の放送日", ended, beforeDay},
		{"今日より前の放送日", r, afterDay},
		{"枠の開始時刻を過ぎた", r, jst(2026, 10, 5, 17, 40)},
		{"7 日より先の放送日", r, day.AddDays(-8).Start()},
		{"曜日が違う", tuesday, beforeDay},
		{"局が違う", tbs, beforeDay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if res := findUnmatched([]Show{c.r}, airings, c.now); len(res.Days)+len(res.Targets) != 0 {
				t.Errorf("res = %+v", res)
			}
		})
	}

	t.Run("開始日の放送日からは対象", func(t *testing.T) {
		from := joinedShow("チャリで30分", weekly.WithFrom(day))
		if res := findUnmatched([]Show{from}, airings, beforeDay); len(res.Days) != 1 {
			t.Errorf("Days = %+v", res.Days)
		}
	})

	t.Run("終了日の放送日までは対象", func(t *testing.T) {
		until := joinedShow("チャリで30分", weekly.WithUntil(day))
		if res := findUnmatched([]Show{until}, airings, beforeDay); len(res.Days) != 1 {
			t.Errorf("Days = %+v", res.Days)
		}
	})

	t.Run("今日の放送日で、枠の開始時刻の前", func(t *testing.T) {
		if res := findUnmatched([]Show{r}, airings, jst(2026, 10, 5, 17, 39)); len(res.Days) != 1 {
			t.Errorf("Days = %+v", res.Days)
		}
	})
}
