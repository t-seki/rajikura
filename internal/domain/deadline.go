package domain

import "time"

// ExcludedFromTimeFree30 は放送回がタイムフリー30の対象外か。
//
// 判定に使う番組表のフラグの解釈はまだ分かっていないので、仮に常に「対象外ではない」を返す。
// MVP は非会員だけなので効かない。
func ExcludedFromTimeFree30(a Airing) bool {
	return false
}

// AvailableDays は会員種別と放送回から、録音できる日数を返す。
// 非会員・エリアフリーは 7 日、タイムフリー30・ダブルプランは 30 日（対象外の放送回は 7 日）。
func AvailableDays(m Membership, a Airing) int {
	switch m {
	case MembershipTimeFree30, MembershipDouble:
		if !ExcludedFromTimeFree30(a) {
			return 30
		}
	}
	return 7
}

// AiringDeadline は放送回の録音期限。
// タイムフリーの対象外の放送回（Airing.TimeFreeExcluded。ライブで録る）は、放送の終了の LiveDeadlineAfter（5 分）後。
// それ以外は、放送回の終了時刻が属する放送日の終わり（翌 05:00）に、会員種別ごとの日数を足す。
// 終了時刻はその瞬間を含まない（05:00 ちょうどに終わる放送回は、前の放送日に属するとみなす）。
// この数え方の根拠は人の経験（2026-10-13: タイムフリーは、この時刻で聴けなくなる）。radiko の資料や番組表の値で
// 確かめたものではない。
func AiringDeadline(a Airing, m Membership) time.Time {
	if a.TimeFreeExcluded() {
		return a.End.Add(LiveDeadlineAfter)
	}
	day := BroadcastDayOf(a.End.Add(-time.Nanosecond))
	return day.AddDays(AvailableDays(m, a)).End()
}

// JobDeadline はジョブの録音期限。持っている放送回の録音期限のうち最も早いもの。
// ライブで録るジョブ（LiveAirings）は、放送回を全部ライブで録るので、最後の放送回の終了の LiveDeadlineAfter（5 分）後
// （ジョブの放送回を順に録るので、先の放送回の録音期限では切らない）。
// 放送回が無ければゼロ値を返す。
func JobDeadline(airings []Airing, m Membership) time.Time {
	if LiveAirings(airings) {
		return lastEnd(airings).Add(LiveDeadlineAfter)
	}
	var d time.Time
	for i, a := range airings {
		ad := AiringDeadline(a, m)
		if i == 0 || ad.Before(d) {
			d = ad
		}
	}
	return d
}

// Verdict は録音期限を過ぎたジョブの扱い。
type Verdict int

const (
	// VerdictExpire は 1 つも取れていないので expired にする。
	// 足したら直す: ジョブの状態の移り方
	VerdictExpire Verdict = iota
	// VerdictSavePartial は取れた分を保存して done にし、欠落を記録する。
	VerdictSavePartial
	// VerdictSaveAll はすべて取れているので保存して done にする。
	VerdictSaveAll
)

// JudgeAtDeadline は録音期限を過ぎた時点のジョブの扱いを決める（ADR 0003）。
// fetched は airings と同じ並びで、その放送回が取れているか。fetched が airings より短ければ、
// 足りない分の放送回は取れていないものとして扱う。
//
// 返す放送回は欠落（取れていない放送回）で、airings の並びのまま。
//   - 一部だけ取れていれば VerdictSavePartial と欠落した放送回
//   - 1 つも取れていなければ VerdictExpire と、すべての放送回
//   - すべて取れていれば VerdictSaveAll と nil
func JudgeAtDeadline(airings []Airing, fetched []bool) (Verdict, []Airing) {
	var missing []Airing
	got := 0
	for i, a := range airings {
		if i < len(fetched) && fetched[i] {
			got++
			continue
		}
		missing = append(missing, a)
	}
	switch {
	case got == 0:
		return VerdictExpire, missing
	case len(missing) == 0:
		return VerdictSaveAll, nil
	default:
		return VerdictSavePartial, missing
	}
}
