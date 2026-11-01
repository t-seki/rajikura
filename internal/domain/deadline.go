package domain

import "time"

// ExcludedFromTimeFree30 は放送回がタイムフリー30 の対象外か（Airing.OutOfArea が立っていれば番組表の tsplus_out_ng、
// 立っていなければ tsplus_in_ng が 0 でない。TimeFreeFlags.PlusOutNG・PlusInNG）。
// 対象外の放送回は、タイムフリー30・ダブルプランでも録音期限が 7 日になる（7 日まではタイムフリーで録り、8 日目からは録らない）。
//
// tsplus_in_ng をこの意味に読むのは、2026-11-01 に実機（タイムフリー30 の会員）で確かめた。番組表で tsplus_in_ng が 2 だった
// 放送回（LFR の 2026-10-03T16:00:00Z）は、放送日から 8 日目以降は radiko の公式でも聴けなかった
// （「この番組はタイムフリー30に対応していないため、ラジコプレミアムにご登録いただいている方も含め、放送日から8日目以降は
// 聴くことができません。」）。tsplus が 1 の放送回（FM-FUJI 10/31 13:00。tsplus_in_ng・tsplus_out_ng が 1）は、7 日以内なら
// 最後まで聴けた（2026-11-01、#27）。値は 1 でも 2 でも対象外。値ごとの違いは見分けない。
// tsplus_out_ng をエリアの外から聴くときのフラグと読むのも、同じ日に確かめた（TimeFreeFlags）。
func ExcludedFromTimeFree30(a Airing) bool {
	if a.OutOfArea {
		return a.TimeFree.PlusOutNG != 0
	}
	return a.TimeFree.PlusInNG != 0
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

// ReviseJobDeadline は、録音期限の付け直し（docs/adr/0032-revise-deadlines-when-own-area-changes.md）で、まだ終わっていない
// ジョブ j に書き直す録音期限。会員種別か自分のエリアが変わったときに、今の会員種別 m と、今の自分のエリアの局一覧 own
// （局 ID の集合）で計算し直す。録り直しを受け付けるときの録音期限（RerecordDeadline）も、これで求める。
//
// 録音の方式は変えない（ジョブの放送回が持つ印 Airing.OutOfArea のまま、Job.Live で決まる）。ライブで録るジョブは、録音期限が
// 会員種別にも自分のエリアにも依らないので、JobDeadline のまま。タイムフリーから録るジョブは、放送回ごとに、今の自分のエリアから
// エリアの外から録るかを決め直してから AiringDeadline で求める。そのため、今の自分のエリアからタイムフリーで聴けない放送回
// （TimeFreeExcluded）は、放送の終了の LiveDeadlineAfter（5 分）後になる。エリアの外から録るかは、会員種別がエリアフリー・
// ダブルプランなら局が own に無いかで決め、それ以外の会員種別ではいつも立てない（Airing.OutOfArea と同じ決まり）。
// own が nil なら局一覧が分からないので、エリアフリー・ダブルプランでは放送回が持つ印をそのまま使う。
// 放送回が無ければゼロ値を返す。
func ReviseJobDeadline(j Job, m Membership, own map[string]bool) time.Time {
	if j.Live() {
		return JobDeadline(j.Airings, m)
	}
	var d time.Time
	for i, a := range j.Airings {
		switch {
		case !m.AreaFree():
			a.OutOfArea = false
		case own != nil:
			a.OutOfArea = !own[a.StationID]
		}
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
