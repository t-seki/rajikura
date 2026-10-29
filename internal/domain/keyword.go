package domain

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// キーワード（用語集。ADR 0014）。番組でも予約でもない、番組の外の決まり。番組表を探して、当たった
// まだ始まっていない放送回を、番組名から決まる番組の 1 回の予約として足す（無ければ番組を作る）。
// 足す先の番組と、まとめて足す分割番組のパート（自動の組）は、Web UI の番組表の「この回を録音」の既定（web/src/guide/reserve.ts の
// defaultShowName・defaultDest・autoGroup）と同じ決め方にする。

// DefaultKeywordMaxPending は、キーワードごとの、足した放送回のうちまだ録音を終えていない回の数の上限の既定
// （設定の keyword_max_pending）。
const DefaultKeywordMaxPending = 10

// Keyword はキーワード。語（Words）で見分ける（KeywordKey が同じキーワードは 1 つだけ）。
type Keyword struct {
	// ID は保存先が振る。作った順に増え、使い回さない。キーワードどうしが同じ放送回に当たったときは、ID の小さい方が足す。
	ID int64
	// Words は空白（全角も）で区切った語。どの語も、番組名・出演者・説明のどれかに含まれれば当たる（KeywordMatcher）。
	Words string
	// Exclude は除外する語（空白で区切る）。どれか 1 つでも含まれれば外す。空なら外さない。
	Exclude string
	// Stations は当てる局の ID。空ならエリアの全局。
	Stations []string
	// Disabled は無効なキーワードか（人が止めている）。無効なキーワードは放送回に当てない。
	// 零値（有効）が既定になるように、有効ではなく無効を持つ（Show.Disabled と同じ）。
	Disabled bool
}

// KeywordKey は、語 words を見分けるための形。語を NormalizeTitle で正規化し、重ねを除いて並べ替え、空白 1 つでつなぐ
// （語の並びと全角・半角・大文字・小文字の違いは、同じキーワードとみなす。語は AND なので、並びを変えても当たる放送回は同じ）。
// 正規化して空になる語は捨てる。語が 1 つも無ければ空。
func KeywordKey(words string) string {
	ws := splitNormalized(words)
	slices.Sort(ws)
	return strings.Join(slices.Compact(ws), " ")
}

// Key は KeywordKey(k.Words)。
func (k Keyword) Key() string { return KeywordKey(k.Words) }

// Validate はキーワードの値が正しいか確かめる。語が正規化して空ならエラー。局の ID が空のものもエラー。
func (k Keyword) Validate() error {
	if k.Key() == "" {
		return errors.New("キーワードの語が空")
	}
	for _, st := range k.Stations {
		if strings.TrimSpace(st) == "" {
			return errors.New("キーワードの局が空")
		}
	}
	return nil
}

// Matcher はキーワードの語と除外する語の照合（局の絞り込みは InStations）。
func (k Keyword) Matcher() KeywordMatcher { return NewKeywordMatcher(k.Words, k.Exclude) }

// InStations は、局 station がキーワードの局の絞り込みの内側か（Stations が空なら、どの局も内側）。
func (k Keyword) InStations(station string) bool {
	return len(k.Stations) == 0 || slices.Contains(k.Stations, station)
}

// DefaultShowName は、番組表の番組名 title から決まる番組の名前（番組表の「この回を録音」の既定と同じ）。
// 末尾の印（TrimTrailingMark）を落とし、NFKC をかけて前後の空白を落とす。
func DefaultShowName(title string) string {
	return strings.TrimFunc(norm.NFKC.String(TrimTrailingMark(title)), unicode.IsSpace)
}

// showNameKey は、番組の名前を比べる形（NFKC と前後の空白）。番組の名前は正規化せずに保存するので、揃えて比べる。
func showNameKey(name string) string {
	return strings.TrimFunc(norm.NFKC.String(name), unicode.IsSpace)
}

// FindShow は、名前が name の番組を shows から探し、添字を返す。完全に同じ名前を先に選び、無ければ
// NFKC と前後の空白を揃えて比べる（番組表の「この回を録音」の既定の足す先の番組と同じ）。無ければ -1。
func FindShow(shows []Show, name string) int {
	if i := slices.IndexFunc(shows, func(s Show) bool { return s.Name == name }); i >= 0 {
		return i
	}
	key := showNameKey(name)
	return slices.IndexFunc(shows, func(s Show) bool { return showNameKey(s.Name) == key })
}

// HasTrailingMark は、番組名 title の末尾に印（TrimTrailingMark が落とす形。`(N)`、`(N時台)`、` Part2`、` ①` など。全角でもよい）があるか。
func HasTrailingMark(title string) bool {
	return rawTrailingMarkRe.MatchString(strings.TrimFunc(title, unicode.IsSpace))
}

// partKey は、分割番組のパートを見分ける番組名の形（末尾の印を落として NormalizeTitle をかけたもの）。
func partKey(title string) string { return NormalizeTitle(TrimTrailingMark(title)) }

// KeywordParts は、キーワードが放送回 a に当たったときに、まとめて足す放送回（a の自動の組。用語集）を開始の順に返す（a を含む）。
// 同じ局・同じ放送日で、番組名の末尾の印を落とすと番組名が a と同じになる放送回（dayAirings から選ぶ。比べるのは partKey。
// タイムフリーの対象外の放送回も含む。ライブで録る）を集め、その中に末尾に印のある放送回が 1 つでもあれば、集めたものを組にする（a に印が無くてもよい。
// `番組` と `番組 Part2` の、Part1 にあたる放送回に印が無い形）。1 つも無ければ a だけ（1 日に何度もある同じ番組名の放送回や、
// 同じ日の再放送を巻き込まないため）。番組表の「この回を録音」の初期の選択（web/src/guide/reserve.ts の autoGroup）と同じ規則。
// a のほかのパートのうち、録音期限（AiringDeadline。会員種別 m）を now で過ぎたタイムフリーの対象外の放送回（放送の終了の
// 5 分後を過ぎた。ライブで録れない）は除く（1 回の予約の API が録音期限を過ぎた放送回として断るため）。
func KeywordParts(a Airing, dayAirings []Airing, now time.Time, m Membership) []Airing {
	key := partKey(a.Title)
	if key == "" {
		return []Airing{a}
	}
	var parts []Airing
	marked := HasTrailingMark(a.Title)
	seen := map[string]bool{}
	for _, b := range dayAirings {
		if b.StationID != a.StationID || b.Day() != a.Day() || partKey(b.Title) != key || seen[b.Key()] {
			continue
		}
		if b.Key() != a.Key() && b.TimeFreeExcluded() && !now.Before(AiringDeadline(b, m)) {
			continue
		}
		seen[b.Key()] = true
		marked = marked || HasTrailingMark(b.Title)
		parts = append(parts, b)
	}
	if !marked {
		return []Airing{a}
	}
	if !seen[a.Key()] {
		parts = append(parts, a)
	}
	sortAirings(parts)
	return parts
}

// 1 回の予約を足せない理由（POST /api/shows/{name}/onces の 409 の reason と同じ値）。
const (
	// RefuseOtherShow は、ほかの番組がその放送回をもう当てている。
	RefuseOtherShow = "other_show"
	// RefuseAlready は、足す先の番組がその放送回をもう当てている。
	RefuseAlready = "already"
)

// JobsByAiring はジョブ jobs を、放送回の Key → その放送回を持つジョブにする。同じ放送回を持つジョブが複数あれば、
// 録音済みを先、次に録音予定（waiting・running・retrying）、録音できなかったと録らない予定・録らなかった（expired・skipped）の順に選ぶ。
func JobsByAiring(jobs []Job) map[string]Job {
	priority := func(s State) int {
		switch s {
		case StateDone:
			return 3
		case StateWaiting, StateRunning, StateRetrying:
			return 2
		default:
			return 1
		}
	}
	out := map[string]Job{}
	for _, j := range jobs {
		for _, a := range j.Airings {
			k := a.Key()
			if old, ok := out[k]; !ok || priority(j.State) > priority(old.State) {
				out[k] = j
			}
		}
	}
	return out
}

// CheckOnce は、放送回 a を番組 self の 1 回の予約として足せるかを決める。足せれば reason は空。足せなければ
// reason（RefuseOtherShow・RefuseAlready）と、当てている番組の名前を返す。
// self が nil なら、まだ無い番組（これから作る）に足すものとして決める（RefuseAlready にはならない）。
// jobs は JobsByAiring の形。now は今の時刻、m は会員種別（self の当たり方 Show.Hit に使う）。
//
//   - 放送回にジョブがあれば、そのジョブの番組で決める（self なら RefuseAlready、ほかの番組なら RefuseOtherShow）
//   - ジョブが無ければ、ほかの有効な番組の毎週・1 回の予約に当たる（Show.Matches）とき RefuseOtherShow
//     （今日より前の放送日でも。足すと展開で矛盾になるため）。self は、1 回の予約の Key が一致するか、self の
//     予約で録る（Show.Hit が HitRecord）とき RefuseAlready。今日より前の放送日で、開始日を持たない
//     毎週の予約だけに当たる放送回と、録らない予定・録らなかったにする放送回（HitSkip。どの放送日でも）は、
//     1 回の予約で足せる
//
// タイムフリーの対象外の放送回（Airing.TimeFreeExcluded）も足せる（ライブで録る）。録音期限を過ぎたかは見ない（1 回の予約の API が見る。キーワードはまだ始まっていない放送回にだけ当たる）。
// 無効な番組にも足せる（当たらないだけ）。無効なほかの番組の予約は「当てている」とみなさない。
func CheckOnce(a Airing, self *Show, shows []Show, jobs map[string]Job, now time.Time, m Membership) (reason, showName string) {
	selfName := ""
	if self != nil {
		selfName = self.Name
	}
	if j, ok := jobs[a.Key()]; ok {
		if self != nil && j.ShowName == selfName {
			return RefuseAlready, selfName
		}
		return RefuseOtherShow, j.ShowName
	}
	for _, s := range shows {
		if (self == nil || s.Name != selfName) && s.Matches(a) {
			return RefuseOtherShow, s.Name
		}
	}
	if self == nil {
		return "", ""
	}
	if slices.ContainsFunc(self.Onces, func(o Once) bool { return o.Key() == a.Key() }) || self.Hit(a, now, m) == HitRecord {
		return RefuseAlready, selfName
	}
	return "", ""
}

// KeywordHit は、キーワードが当たった放送回。
type KeywordHit struct {
	Airing Airing
	// StationName は放送回の局の名前（通知と記録に使う）。
	StationName string
	// DayAirings は、その放送回と同じ局・同じ放送日の番組表の放送回（分割番組のパートを決める。KeywordParts）。
	DayAirings []Airing
}

// KeywordInput は PlanKeywords に渡すもの。
type KeywordInput struct {
	// Keywords は当てる（有効な）キーワード。ID の順に当てる。
	Keywords []Keyword
	// Hits はキーワードの ID → 当たった放送回（開始の順。まだ始まっていない放送回だけ）。
	Hits map[int64][]KeywordHit
	// Shows は今の番組の全部（無効な番組を含む）。
	Shows []Show
	// Jobs は JobsByAiring の形のジョブ。
	Jobs map[string]Job
	// Recorded は、どれかのキーワードが足した放送回の記録にある放送回の Key。記録にある放送回には、もう足さない
	// （人が消した・別の番組へ移した回を足し直さない。ほかのキーワードが足した回も、自分の記録にも上限の数にも入れない）。
	Recorded map[string]bool
	// Pending はキーワードの ID → 足した放送回のうち、まだ録音を終えていない回の数。
	Pending map[int64]int
	// LimitNotified はキーワードの ID → 上限で足すのを止めたことを、もう通知したか。
	LimitNotified map[int64]bool
	// MaxPending は上限（設定の keyword_max_pending）。1 以上。
	MaxPending int
	// Now は今の時刻、Membership は会員種別（CheckOnce に渡す）。
	Now        time.Time
	Membership Membership
}

// KeywordAddition は、キーワードが足す 1 回の予約のまとまり（当たった放送回 1 つと、そのパート）。
type KeywordAddition struct {
	Keyword Keyword
	Hit     KeywordHit
	// ShowName は足す先の番組の名前。
	ShowName string
	// Created なら、足す先の番組を新しく作る（Show がその番組）。
	Created bool
	Show    Show
	// Airings は足す放送回（パート。開始の順）。それぞれを 1 回の予約にする。
	Airings []Airing
	// Separate は、足す先の番組が分割番組でない（SplitNone）まま、足し終えた時点で、足した放送回の放送日に 2 つ以上の
	// 放送回を当てる（1 回で 2 つ以上まとめて足したときと、別々に当たって同じ番組に足したとき）ので、パートごとに別の
	// ファイルになるか（ADR 0003）。通知の本文にその旨を添えるために持つ。既存の番組の設定は変えない。
	Separate bool
}

// KeywordPlan は PlanKeywords の結果。
type KeywordPlan struct {
	Additions []KeywordAddition
	// Limited は、上限で足すのを止めたことを、今度通知するキーワード（まだ通知していなかったもの）。
	Limited []Keyword
	// LimitNotified は、上限の通知の印を変えるキーワードの ID → 新しい値（立てる・下ろす）。
	LimitNotified map[int64]bool
}

// PlanKeywords は、キーワードの当たった放送回から、足す 1 回の予約と作る番組を決める。キーワードは ID の順、
// 当たった放送回は開始の順に見る。次のどれかに当たれば、足さずに飛ばす（通知もしない）。
//
//   - 記録にある放送回（どれかのキーワードが足した）
//   - 足す先の番組が無効な番組
//   - 足す放送回（パート）のどれかが、CheckOnce で足せない（ほかの番組が当てている・足す先の番組がもう当てている）。
//     パートはまとめて足すか、1 つも足さないか。タイムフリーの対象外の放送回は飛ばさずに足す（ライブで録る）
//
// 足す先の番組の名前は、番組名から DefaultShowName で決め、FindShow で探す。無ければその名前で番組を作る
// （有効、check_title はオン、毎週の予約は無し）。作った番組の split は、この回の処理で足し終えた時点で、同じ放送日に
// 2 つ以上の放送回を当てるなら分割番組でつなぐ（SplitJoin）、それ以外は分割番組でない（SplitNone）（作った番組に、
// 同じ回の中で後から足すことがあるので、終わりに決める）。既存の番組の split は変えない（KeywordAddition.Separate で知らせる）。
// 上限: キーワードの Pending が MaxPending 以上なら、それ以上は足さない（足すときは、パートの数だけ Pending を増やす。
// パートの途中では切らない）。上限で止めた回があり、まだ通知していなければ Limited に入れて印を立てる。止めた回が無く、
// Pending が上限を下回っていれば印を下ろす（上限を下回ってから、また達したら、もう一度通知する）。
func PlanKeywords(in KeywordInput) KeywordPlan {
	plan := KeywordPlan{LimitNotified: map[int64]bool{}}
	shows := slices.Clone(in.Shows)
	for i := range shows {
		shows[i].Onces = slices.Clone(shows[i].Onces)
	}
	recorded := map[string]bool{}
	for k, v := range in.Recorded {
		recorded[k] = v
	}
	// created は、この回の処理で新しく作った番組の添字。targets は plan.Additions ごとの、足す先の番組の添字
	created := map[int]bool{}
	var targets []int
	keywords := slices.Clone(in.Keywords)
	slices.SortFunc(keywords, func(x, y Keyword) int { return int(x.ID - y.ID) })
	for _, k := range keywords {
		if k.Disabled {
			continue
		}
		pending := in.Pending[k.ID]
		limited := false
		for _, hit := range in.Hits[k.ID] {
			if recorded[hit.Airing.Key()] {
				continue
			}
			name := DefaultShowName(hit.Airing.Title)
			if name == "" {
				continue
			}
			idx := FindShow(shows, name)
			var self *Show
			if idx >= 0 {
				self = &shows[idx]
				if self.Disabled {
					continue
				}
				name = self.Name
			}
			parts := KeywordParts(hit.Airing, hit.DayAirings, in.Now, in.Membership)
			refused := false
			for _, p := range parts {
				if recorded[p.Key()] {
					refused = true
					break
				}
				if reason, _ := CheckOnce(p, self, shows, in.Jobs, in.Now, in.Membership); reason != "" {
					refused = true
					break
				}
			}
			if refused {
				continue
			}
			if pending >= in.MaxPending {
				limited = true
				continue
			}
			add := KeywordAddition{Keyword: k, Hit: hit, ShowName: name, Airings: parts}
			if idx < 0 {
				// split は、この回の処理で足し終えてから決める（下の decideSplit）
				r := NewShow(name)
				shows = append(shows, r)
				idx = len(shows) - 1
				add.Created, add.Show = true, r
				created[idx] = true
			}
			for _, p := range parts {
				shows[idx].Onces = append(shows[idx].Onces, Once{StationID: p.StationID, Start: p.Start, KeywordID: k.ID})
				recorded[p.Key()] = true
			}
			pending += len(parts)
			plan.Additions = append(plan.Additions, add)
			targets = append(targets, idx)
		}
		switch {
		case limited && !in.LimitNotified[k.ID]:
			plan.Limited = append(plan.Limited, k)
			plan.LimitNotified[k.ID] = true
		case !limited && pending < in.MaxPending && in.LimitNotified[k.ID]:
			plan.LimitNotified[k.ID] = false
		}
	}
	// 新しく作った番組は、足し終えた時点で同じ放送日に 2 つ以上の放送回を当てるなら、分割番組でつなぐ（SplitJoin）番組にする
	// （別々に当たった印の無いパートも、1 つのファイルにつなぐ）。既存の番組の設定は変えない。
	for idx := range created {
		if maxOncesPerDay(shows[idx]) >= 2 {
			shows[idx].Split = SplitJoin
		}
	}
	for i := range plan.Additions {
		add, idx := &plan.Additions[i], targets[i]
		if add.Created {
			add.Show.Split = shows[idx].Split
		}
		// 分割番組でない番組が、足し終えた時点で、足した放送回の放送日に 2 つ以上の放送回を当てるなら、パートごとに別の
		// ファイルになる（ADR 0003）
		add.Separate = shows[idx].Split == SplitNone && countOnDay(shows[idx], add.Hit.DayAirings, add.Airings[0].Day()) >= 2
	}
	return plan
}

// countOnDay は、番組 r が放送日 day に当てる放送回の数。番組表の放送回 dayAirings のうち r の予約に当たるもの
// （Show.Matches）と、r の 1 回の予約のうち放送日が day のもの（番組表に無い局のものも）を、重ねずに数える。
func countOnDay(r Show, dayAirings []Airing, day BroadcastDay) int {
	seen := map[string]bool{}
	for _, a := range dayAirings {
		if a.Day() == day && r.Matches(a) {
			seen[a.Key()] = true
		}
	}
	for _, o := range r.Onces {
		if BroadcastDayOf(o.Start) == day {
			seen[o.Key()] = true
		}
	}
	return len(seen)
}

// maxOncesPerDay は、番組 r の 1 回の予約を放送日ごとに数えた数の最大。
func maxOncesPerDay(r Show) int {
	n := 0
	for _, o := range r.Onces {
		n = max(n, countOnDay(r, nil, BroadcastDayOf(o.Start)))
	}
	return n
}
