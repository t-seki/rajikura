package domain

import (
	"slices"
	"time"
)

// UnmatchedDay は当たらない日（用語集）の 1 件。毎週の予約の曜日に当たる放送日なのに、その局にその開始時刻で始まる
// 放送回が番組表に無い枠と、番組表から読める事実（その時刻を含む放送回、番組名の合う放送回）を持つ。理由（休止・特番・
// 延長・時刻の変更）は推測で分けない。
type UnmatchedDay struct {
	ShowName  string
	StationID string
	Day       BroadcastDay
	// Start は枠の開始時刻（放送日 Day の、毎週の予約の開始時刻。0〜4 時台は暦の翌日）。
	Start time.Time
	// Covering はその時刻を含む放送回（開始 <= Start < 終了）。無ければ nil（番組表に放送回が無い）。
	Covering *Airing
	// Titled は同じ局・同じ放送日で、番組名が番組の名前で始まる（TitleMatchesName）放送回。開始時刻の順。
	// 番組の CheckTitle は問わない。
	Titled []Airing
}

// Key は当たらない日の枠を識別するキー（番組の名前・局・枠の開始時刻）。
func (u UnmatchedDay) Key() string {
	return UnmatchedKey(u.ShowName, u.StationID, u.Start)
}

// UnmatchedKey は、番組 name の局 station の、開始時刻 start の枠のキー（UnmatchedDay.Key と同じ形）。
func UnmatchedKey(name, station string, start time.Time) string {
	return name + "/" + Airing{StationID: station, Start: start}.Key()
}

// SlotStart は、放送日 d の時刻 c（毎週の予約の開始時刻）の時刻を返す。放送日は 05:00 に始まるので、
// 0〜4 時台は暦の翌日の時刻になる。
func (d BroadcastDay) SlotStart(c Clock) time.Time {
	day := d.Day
	if c.Hour < 5 {
		day++
	}
	return time.Date(d.Year, d.Month, day, c.Hour, c.Minute, 0, 0, JST)
}

// UnmatchedInput は当たらない日を求める入力。ある局・ある放送日の番組表を、番組の毎週の予約に当てる（Expand と同じ番組表）。
type UnmatchedInput struct {
	Shows     []Show
	StationID string
	Day       BroadcastDay
	// Airings はその局・その放送日の番組表。ほかの局の放送回は無視する。
	Airings []Airing
	// Now は今の時刻。今日より前の放送日と、枠の開始時刻が今より後でない枠は対象にしない。
	Now time.Time
	// Until は対象にする最後の放送日（含む）。これより後の放送日は対象にしない。零値なら制限なし。
	Until BroadcastDay
}

// UnmatchedResult は当たらない日を求めた結果。
type UnmatchedResult struct {
	// Days は当たらない日。番組の名前、枠の開始時刻の順。
	Days []UnmatchedDay
	// Targets は、当たらない日かを調べた枠（当たった枠も含む）のキー（UnmatchedKey）。ここに無い枠の記録は、番組を消した・
	// 無効にした・毎週の予約を変えた（または開始時刻を過ぎた・放送日が対象の外になった）ので、当たらない日ではなくなった。
	Targets []string
}

// FindUnmatchedDays は、ある局・ある放送日の番組表から、番組の毎週の予約の当たらない日を求める。
//
//   - 対象: 有効な番組の、局が StationID で、曜日が Day の曜日の毎週の予約。開始日を持つなら、開始日から先の放送日だけ。終了日を持つなら、終了日までの放送日だけ。
//     Day が今日より前か Until より後なら、どの枠も対象にしない。枠の開始時刻が Now より後でなければ対象にしない。
//     番組の CheckTitle は問わない。
//   - 当たらない日: 対象の枠のうち、毎週の予約に当たる（Weekly.Matches）放送回が番組表に無いもの。
//     同じ番組の複数の毎週の予約が同じ枠を指すときは 1 件にする。
//
// 番組表を取れなかった・空だったときは呼ばない（展開と同じ。呼び出し側がその局・放送日の記録に触らない）。
func FindUnmatchedDays(in UnmatchedInput) UnmatchedResult {
	var res UnmatchedResult
	today := BroadcastDayOf(in.Now)
	if in.Day.Before(today) || (!in.Until.IsZero() && in.Until.Before(in.Day)) {
		return res
	}
	var airings []Airing
	for _, a := range in.Airings {
		if a.StationID == in.StationID {
			airings = append(airings, a)
		}
	}
	sortAirings(airings)
	for _, s := range in.Shows {
		if s.Disabled {
			continue
		}
		for _, r := range s.Weekly {
			if r.StationID != in.StationID || r.Weekday != in.Day.Weekday() || !r.InDays(in.Day) {
				continue
			}
			start := in.Day.SlotStart(r.Start)
			if !start.After(in.Now) {
				continue
			}
			key := UnmatchedKey(s.Name, in.StationID, start)
			if slices.Contains(res.Targets, key) {
				continue
			}
			res.Targets = append(res.Targets, key)
			if slices.ContainsFunc(airings, func(a Airing) bool { return a.Day() == in.Day && r.Matches(a) }) {
				continue
			}
			u := UnmatchedDay{ShowName: s.Name, StationID: in.StationID, Day: in.Day, Start: start}
			for _, a := range airings {
				if u.Covering == nil && !a.Start.After(start) && a.End.After(start) {
					c := a
					u.Covering = &c
				}
				if a.Day() == in.Day && TitleMatchesName(a.Title, s.Name) {
					u.Titled = append(u.Titled, a)
				}
			}
			res.Days = append(res.Days, u)
		}
	}
	slices.SortStableFunc(res.Days, func(x, y UnmatchedDay) int {
		if x.ShowName != y.ShowName {
			if x.ShowName < y.ShowName {
				return -1
			}
			return 1
		}
		return x.Start.Compare(y.Start)
	})
	return res
}
