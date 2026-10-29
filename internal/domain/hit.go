package domain

import (
	"slices"
	"time"
)

// Hit は、番組の予約が放送回にどう当たるか（録るか、録らない予定・録らなかったにするか、当たらないか）。
// 展開（Expand）、番組表の状態、1 回の予約を足せるかの判定（CheckOnce）が同じものを使う（ADR 0018・0019）。
type Hit int

const (
	// HitNone は当たらない（ジョブを新しく作らない）。
	HitNone Hit = iota
	// HitRecord は録る（録音待ちのジョブを作る）。
	HitRecord
	// HitSkip は録らない（ジョブを skipped で作り、録りにいかない。最初の試行の時刻の前なら録らない予定、後なら録らなかった）。
	HitSkip
)

// Hit は、番組 s の予約（毎週と 1 回）が放送回 a にどう当たるかを決める。now は今の時刻で、今日の放送日と
// 録音期限（AiringDeadline。会員種別 m で決まる）の判定に使う。
//
//   - 無効な番組は当たらない（HitNone）。
//   - 1 回の予約に当たれば、番組名を確かめずに録る（HitRecord。同じ番組の毎週の予約に当たっていても）。
//   - 今日と今日から先の放送日では、毎週の予約に当たる放送回に当たる。
//   - 今日より前の放送日では、開始日（Weekly.From）を持つ毎週の予約だけが、録音期限の内側の放送回に当たる。
//     開始日を持たない毎週の予約と、録音期限を過ぎた放送回は当たらない。
//   - 毎週の予約だけに当たったとき、番組が番組名の食い違いを調べる（CheckTitle）なら、番組表の番組名が番組の名前で
//     始まらない（TitleMatchesName でない）放送回は録らない（HitSkip）。それ以外は録る。
//
// 放送回が番組の予約に当たるか（局・曜日・開始時刻）だけを見るなら Show.Matches を使う（展開の矛盾の判定と、
// 既存のジョブに残す放送回は Matches で決める）。
func (s Show) Hit(a Airing, now time.Time, m Membership) Hit {
	if s.Disabled {
		return HitNone
	}
	key := a.Key()
	for _, o := range s.Onces {
		if o.Key() == key {
			return HitRecord
		}
	}
	past := a.Day().Before(BroadcastDayOf(now))
	matched, from := false, false
	for _, r := range s.Weekly {
		if !r.Matches(a) {
			continue
		}
		matched = true
		if !r.From.IsZero() {
			from = true
		}
	}
	if !matched || past && (!from || !now.Before(AiringDeadline(a, m))) {
		return HitNone
	}
	if s.CheckTitle && !TitleMatchesName(a.Title, s.Name) {
		return HitSkip
	}
	return HitRecord
}

// PastWeeklyDays は、番組 shows（無効な番組は除く）の開始日を持つ毎週の予約が、今日より前で当たりうる
// 放送日（開始日から、終了日を持てば終了日まで、曜日が合い、録音期限の内側にあるもの）を、重ならないように古い順で返す。番組表の同期と、番組を
// 変えた直後の展開が、今日より前の放送日の番組表を取る範囲に足す（Hit が録る・録らないにする放送回を拾うため）。
// 録音期限は放送回の終了時刻で決まるので、Once.Deadline と同じく、放送日の翌日に終わったとみなして多めに数える。
func PastWeeklyDays(now time.Time, m Membership, shows []Show) []BroadcastDay {
	today := BroadcastDayOf(now)
	var out []BroadcastDay
	for _, s := range shows {
		if s.Disabled {
			continue
		}
		for _, r := range s.Weekly {
			if r.From.IsZero() || !r.From.Before(today) {
				continue
			}
			days := AvailableDays(m, Airing{StationID: r.StationID})
			for d := today.AddDays(-1); !d.Before(r.From); d = d.AddDays(-1) {
				if !now.Before(d.AddDays(1 + days).End()) {
					break
				}
				if d.Weekday() == r.Weekday && r.InDays(d) && !slices.Contains(out, d) {
					out = append(out, d)
				}
			}
		}
	}
	slices.SortFunc(out, func(x, y BroadcastDay) int { return x.Start().Compare(y.Start()) })
	return out
}
