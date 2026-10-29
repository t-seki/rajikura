package domain

import (
	"cmp"
	"slices"
	"time"
)

// ExpandInput はジョブへの展開の入力。ある局・ある放送日の番組表を、番組の予約（毎週と 1 回）に当てる。
type ExpandInput struct {
	Shows     []Show
	StationID string
	Day       BroadcastDay
	// Airings はその局・その放送日の放送回。ほかの局・ほかの放送日の放送回は無視する。
	Airings []Airing
	// Jobs は既存のジョブ。放送日が Day でないものは無視する。
	Jobs []Job
	// Membership は録音期限の計算に使う。
	Membership Membership
	// TFMaxDelay は局の tf_max_delay。最初の試行の時刻の計算に使う。
	TFMaxDelay time.Duration
	// Now は今の時刻。放送回への当たり方（Show.Hit。今日の放送日と録音期限）と、skipped のジョブが確定するか
	// （最初の試行の時刻を過ぎたか）の判定に使う。零値なら、どの放送日も今日から先として扱い、確定させない（テストで使う）。
	Now time.Time
}

// Conflict は展開できなかった放送回。ジョブは作らない。
type Conflict struct {
	Airing Airing
	// Shows は当たった番組の名前。
	Shows []string
	// Reason は矛盾の説明。
	Reason string
}

// Plan は展開の差分。
type Plan struct {
	// Create は新しく作るジョブ（ID は 0）。
	Create []Job
	// Update は更新する既存のジョブ（展開が触るもの。Job.Expandable）。放送回・パート番号・録音期限・
	// 次の試行の時刻・状態（waiting と skipped）・確定したか（Job.Settled）を新しい値にしたもの。
	Update []Job
	// Delete は消す既存のジョブ（展開が触るもの。Job.Expandable）。放送回が 0 になった。
	Delete []Job
	// Conflicts は矛盾。
	Conflicts []Conflict
}

// Empty は差分が無いか。
func (p Plan) Empty() bool {
	return len(p.Create) == 0 && len(p.Update) == 0 && len(p.Delete) == 0 && len(p.Conflicts) == 0
}

// Expand は番組の予約（毎週と 1 回。Show.Matches）を、ある局・ある放送日の放送回に当て、既存のジョブとの
// 差分を返す。1 回の予約の放送回は毎週の予約の放送回と同じに扱う（連結する番組なら、同じ放送日の毎週の予約の放送回と
// 1 つのジョブにまとめる）。
//
// 展開が触るのは、waiting のジョブと、確定の前の skipped（録らない予定）のジョブ（Job.Expandable）。ほかのジョブ（確定した
// skipped の録らなかったを含む）は触らず、同期を繰り返しても作り直さない。
//
// 放送回を録るか録らないかは、放送回への当たり方（Show.Hit。ADR 0018・0019）で決める。今日と今日から先の放送日では、
// 予約に当たる放送回に当たる。今日より前の放送日では、1 回の予約の放送回と、開始日を持つ毎週の予約の
// 録音期限の内側の放送回だけを新しいジョブにする（開始日を持たない毎週の予約だけに当たる放送済みの回からは、新しい
// ジョブを作らない）。どちらの放送日でも、毎週の予約だけに当たり番組名の食い違う放送回（番組が CheckTitle のとき）は
// 録らない（HitSkip）。
//
// 録らないジョブは skipped にする。最初の試行の時刻（NextAttemptAt）を過ぎているか、今日より前の放送日なら、確定した
// （Job.Settled）録らなかったにし、そうでなければ録らない予定にする（Now が零値なら、どれも録らない予定）。録らない予定のジョブは
// 展開のたびに決め直し、番組名が直れば waiting に戻す。最初の試行の時刻を過ぎた展開で確定する。
// ジョブの最初の試行の時刻と録音期限は、放送回から JobFirstAttemptAt・JobDeadline で決める（放送回の 1 つでもタイムフリーの
// 対象外なら、ライブで録るジョブの時刻。Job.Live）。ライブで録るジョブと、waiting のタイムフリーから録るジョブ（前の版が設定
// chapters で最初の試行を遅らせていた。ADR 0027）は、放送回が変わらなくても時刻が違えば直す。
// 人が録る・録らないを決めたジョブ（Job.ByPerson）は、番組名では状態を変えない（連結する番組では、当たる
// （Show.Matches）放送回を番組名によらず全部持つ）。休止回を外す、放送回が 0 になれば消す、無効にした・消した番組の
// ジョブを消す、メタデータを写す、は人が決めたジョブにも効かせる（確定した後は触らない）。
//
//   - 連結する番組（分割番組でつなぐ番組。Show.Joins）は、番組 × 放送日につきジョブを 1 つ作り、当たった放送回を開始時刻の順に持たせる。
//     間に挟まる別の放送回は含めない。録る放送回が 1 つでもあれば、録らない放送回を除いた放送回で
//     ジョブ（waiting）を作る。録る放送回が無く、録らない放送回があれば、それを持つ skipped のジョブを 1 つ作る。
//     既存のジョブ（人が決めたものを除く）も同じく、展開のたびに、録らない放送回を外し、直れば戻す。録る放送回が
//     無くなり、録らない放送回だけになれば skipped にし（放送回は録らない放送回）、録る放送回が出てくれば waiting に戻す。
//   - 連結しない番組（分割番組でつながない番組と、分割番組でない番組。ADR 0003）は、当たった放送回ごとにジョブを作り、開始時刻の順にパート番号 1, 2, … を付ける（録らない
//     放送回も、新しいジョブを作らない放送回も含めて振る）。放送回が 1 つだけならパート番号は付けない（0）。
//     録る放送回は waiting、録らない放送回は skipped のジョブになる。既存のジョブ（人が決めたものを除く）も、
//     放送回ごとに waiting と skipped を行き来する。
//   - 同じ放送回に複数の番組の予約が当たったら（Show.Matches）、ジョブは作らずに矛盾として返す。
//     同じ番組の複数の毎週の予約（と 1 回の予約）が当たるのは問題ない。
//   - 展開が触るジョブの放送回が当たらなくなっていたら（休止回）、その放送回をジョブから外す。
//     放送回が 0 になったジョブは消す。1 回の予約の放送回が番組表から消えた・開始時刻が変わったときも同じで、
//     追いかけない（1 回の予約そのものは残る）。
//   - 既存の waiting のジョブには、当たる（Show.Matches）放送回のうち録らないもの（HitSkip）を除いて残す。今日より前の
//     放送日でも、開始日を持たない毎週の予約だけに当たる放送回は外さない（すでにある waiting のジョブはそのまま録音する）。
//   - 放送回の同一性は Airing.Key（局 + 開始時刻）で判定する。番組表の ID は分割番組の
//     放送回で重なるので使わない。展開が触るジョブの放送回の終了時刻やメタデータが変わっていたら、
//     新しい値に更新する。開始時刻が変わった放送回は、元の放送回の休止と新しい放送回として扱う。
//     連結する番組では同じジョブの放送回が入れ替わり、連結しない番組ではジョブを消して、新しい放送回の当たり方で作り直す
//     （今日より前の放送日で録らない放送回なら、元のジョブが消えるだけになる）。
//   - 無効な番組（Show.Disabled）の毎週と 1 回の予約は放送回に当たらない（Show.Matches）。そのため、無効な番組の
//     ジョブ（展開が触るもの）は、Shows に無い番組（消した番組）のジョブと同じく、この局の放送回を
//     外され、放送回が 0 になれば消える。
//
// 既存のジョブの、ほかの局の放送回は触らない。この局に当たる放送回が無く、この局の放送回も持たない連結する番組のジョブは、
// 状態も変えない。ほかの局の放送回だけを持つ連結する番組の録らない予定は、この局の当たる放送回の番組名が全部食い違うときも
// 触らない（この局の放送回を足さない）。どちらも、ほかの局の放送回だけで録らない予定を waiting に戻さないため。
// この局の放送回を持つジョブは、今までどおり決め直す。
// パート番号は、この局の放送回の中で振る。
func Expand(in ExpandInput) Plan {
	var plan Plan

	// この局・この放送日の放送回を、開始時刻の順に並べる。
	var airings []Airing
	seen := map[string]bool{}
	for _, a := range in.Airings {
		if a.StationID != in.StationID || a.Day() != in.Day || seen[a.Key()] {
			continue
		}
		seen[a.Key()] = true
		airings = append(airings, a)
	}
	sortAirings(airings)

	// 放送回を番組に割り当てる。
	matched := map[string][]Airing{}
	for _, a := range airings {
		var names []string
		for _, s := range in.Shows {
			if s.Matches(a) {
				names = append(names, s.Name)
			}
		}
		switch len(names) {
		case 0:
		case 1:
			matched[names[0]] = append(matched[names[0]], a)
		default:
			plan.Conflicts = append(plan.Conflicts, Conflict{
				Airing: a, Shows: names, Reason: "複数の番組の予約が当たった",
			})
		}
	}

	var jobs []Job
	for _, j := range in.Jobs {
		if j.Day == in.Day {
			jobs = append(jobs, j)
		}
	}
	// visited は、展開で行き先が決まった既存のジョブ。
	visited := make([]bool, len(jobs))

	// settles は、次の試行の時刻が next の skipped のジョブが確定するか（最初の試行の時刻を過ぎたか、今日より前の放送日）。
	settles := func(next time.Time) bool {
		if in.Now.IsZero() {
			return false
		}
		return !in.Now.Before(next) || in.Day.Before(BroadcastDayOf(in.Now))
	}
	newJob := func(show string, part int, state State, as []Airing) Job {
		next := JobFirstAttemptAt(as, in.TFMaxDelay)
		return Job{
			ShowName:      show,
			Day:           in.Day,
			Part:          part,
			State:         state,
			Airings:       as,
			Deadline:      JobDeadline(as, in.Membership),
			NextAttemptAt: next,
			Settled:       state == StateSkipped && settles(next),
		}
	}
	// update は展開が触るジョブを、新しい放送回・パート番号・状態にする。変わらなければ何もしない。
	// 放送回が 0 になったら消す。
	update := func(j Job, part int, as []Airing, state State) {
		sortAirings(as)
		if len(as) == 0 {
			plan.Delete = append(plan.Delete, j)
			return
		}
		next := JobFirstAttemptAt(as, in.TFMaxDelay)
		deadline := JobDeadline(as, in.Membership)
		settled := state == StateSkipped && settles(next)
		// 時刻（最初の試行の時刻・録音期限）も比べ、違えば直す。比べるのは、ライブで録るジョブ（時刻が局の tf_max_delay と
		// 会員種別に依らない。ライブで録ると決める前に作ったジョブの、タイムフリーの時刻を直すため）と、waiting のタイムフリーから
		// 録るジョブ（前の版が設定 chapters で終了の 90 分後に遅らせたジョブの最初の試行の時刻を、今の時刻に戻すため。ADR 0027）。
		// 展開が触る waiting のジョブは試行がまだ無い（試行の後は retrying などになり、展開は触らない）ので、直すのは最初の試行の
		// 時刻だけ。録らない予定（skipped）のタイムフリーから録るジョブの時刻は、今までどおり比べない。
		// ほかの局の放送回も持つ連結する番組のジョブは、局ごとの tf_max_delay で時刻が変わるので、局の展開ごとに直りうる。
		compareTimes := LiveAirings(as) || state == StateWaiting && j.State == StateWaiting
		sameTimes := !compareTimes || j.NextAttemptAt.Equal(next) && j.Deadline.Equal(deadline)
		if j.Part == part && j.State == state && j.Settled == settled && airingsEqual(j.Airings, as) && sameTimes {
			return
		}
		j.Part = part
		j.Airings = as
		j.State = state
		j.Settled = settled
		j.Deadline = deadline
		j.NextAttemptAt = next
		plan.Update = append(plan.Update, j)
	}
	// others はジョブの放送回のうち、ほかの局のもの。
	others := func(j Job) []Airing {
		var as []Airing
		for _, a := range j.Airings {
			if a.StationID != in.StationID {
				as = append(as, a)
			}
		}
		return as
	}
	// partTaken は、展開が触らないジョブがそのパート番号を使っているか。
	partTaken := func(show string, part int) bool {
		return slices.ContainsFunc(jobs, func(j Job) bool {
			return j.ShowName == show && j.Part == part && !j.Expandable()
		})
	}

	for _, s := range in.Shows {
		as := matched[s.Name]
		hits := make([]Hit, len(as))
		for i, a := range as {
			hits[i] = s.Hit(a, in.Now, in.Membership)
		}
		if s.Joins() {
			target := slices.IndexFunc(jobs, func(j Job) bool { return j.ShowName == s.Name && j.Part == 0 })
			if target < 0 {
				var keep, skip []Airing
				record := false
				for i, a := range as {
					switch hits[i] {
					case HitSkip:
						skip = append(skip, a)
						continue
					case HitRecord:
						record = true
					}
					keep = append(keep, a)
				}
				switch {
				case record:
					plan.Create = append(plan.Create, newJob(s.Name, 0, StateWaiting, keep))
				case len(skip) > 0:
					plan.Create = append(plan.Create, newJob(s.Name, 0, StateSkipped, skip))
				}
				continue
			}
			visited[target] = true
			j := jobs[target]
			if !j.Expandable() {
				continue
			}
			// foreign は、ジョブがこの局の放送回を持たない（ほかの局の放送回だけを持つ）こと。
			foreign := len(others(j)) == len(j.Airings)
			if len(as) == 0 && foreign {
				// この局に当たる放送回が無く、ジョブもこの局の放送回を持たない。ほかの局のジョブなので触らない
				// （ほかの局の放送回だけで waiting に戻さない）。
				continue
			}
			if j.ByPerson {
				// 人が決めたジョブは、番組名によらず当たる放送回を全部持ち、状態を変えない。
				update(j, 0, append(others(j), as...), j.State)
				continue
			}
			keep := others(j)
			var skip []Airing
			for i, a := range as {
				if hits[i] == HitSkip {
					skip = append(skip, a)
					continue
				}
				keep = append(keep, a)
			}
			if j.State == StateSkipped && foreign && len(keep) == len(j.Airings) && len(skip) > 0 {
				// ジョブはこの局の放送回を持たない（ほかの局の放送回だけを持つ）録らない予定で、この局の当たる放送回は
				// 番組名が全部食い違う（keep はほかの局の放送回のまま）。触らない（ほかの局の放送回だけで waiting に戻さず、
				// この局の放送回も足さない）。この局の放送回を持つ録らない予定は、今までどおり決め直す。
				continue
			}
			state := StateWaiting
			if len(keep) == 0 && len(skip) > 0 {
				state, keep = StateSkipped, skip
			}
			update(j, 0, keep, state)
			continue
		}

		// 連結しない番組。放送回ごとに、その放送回を持つ既存のジョブを探す。
		targets := make([]int, len(as))
		for i, a := range as {
			targets[i] = -1
			for k, j := range jobs {
				if visited[k] || j.ShowName != s.Name {
					continue
				}
				if slices.ContainsFunc(j.Airings, func(b Airing) bool { return b.Key() == a.Key() }) {
					targets[i] = k
					visited[k] = true
					break
				}
			}
		}
		for i, a := range as {
			part := 0
			if len(as) > 1 {
				part = i + 1
			}
			if t := targets[i]; t >= 0 {
				if j := jobs[t]; j.Expandable() {
					state := j.State
					if !j.ByPerson {
						switch hits[i] {
						case HitSkip:
							state = StateSkipped
						case HitRecord:
							state = StateWaiting
						}
					}
					update(j, part, []Airing{a}, state)
				}
				continue
			}
			state := StateWaiting
			switch hits[i] {
			case HitNone:
				continue
			case HitSkip:
				state = StateSkipped
			}
			if partTaken(s.Name, part) {
				plan.Conflicts = append(plan.Conflicts, Conflict{
					Airing: a, Shows: []string{s.Name}, Reason: "パート番号が展開の触らないジョブと重なる",
				})
				continue
			}
			plan.Create = append(plan.Create, newJob(s.Name, part, state, []Airing{a}))
		}
	}

	// 当たらなくなった放送回（休止回など）を、展開が触るジョブから外す。
	for i, j := range jobs {
		if visited[i] || !j.Expandable() {
			continue
		}
		rest := others(j)
		if len(rest) == len(j.Airings) {
			continue // この局の放送回を持たない
		}
		update(j, j.Part, rest, j.State)
	}
	return plan
}

func sortAirings(as []Airing) {
	slices.SortStableFunc(as, func(a, b Airing) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return cmp.Compare(a.Key(), b.Key())
	})
}

func airingsEqual(a, b []Airing) bool {
	return slices.EqualFunc(a, b, Airing.equal)
}

func lastEnd(as []Airing) time.Time {
	return Job{Airings: as}.LastEnd()
}
