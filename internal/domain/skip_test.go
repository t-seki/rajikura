package domain

import (
	"slices"
	"testing"
	"time"
)

// 今日から先の放送日の、番組名による録らない予定と、人が決めたジョブの展開（ADR 0019）。

// afterFirstAttempt は day の「ふらっと」の最初の試行の時刻（11:04:30）を過ぎた、同じ放送日の時刻。
var afterFirstAttempt = jst(2026, 10, 5, 12, 0)

func expandAt(now time.Time, shows []Show, jobs []Job, airings []Airing) Plan {
	return Expand(ExpandInput{
		Shows: shows, StationID: "LFR", Day: day, Airings: airings, Jobs: jobs,
		Membership: MembershipFree, TFMaxDelay: 60 * time.Second, Now: now,
	})
}

// retitled は放送回 a の番組名を title にしたもの。
func retitled(a Airing, title string) Airing {
	a.Title = title
	return a
}

func skippedJob(id int64, show string, part int, as ...Airing) Job {
	j := waitingJob(id, show, part, as...)
	j.State = StateSkipped
	return j
}

func byPerson(j Job) Job {
	j.ByPerson = true
	return j
}

// 番組名が食い違えば録らない予定になり、直れば waiting に戻る（連結する番組）。
func TestExpandSkipBeforeAiringJoin(t *testing.T) {
	other := joinedShow("別の番組", flatWeekly()...)
	plan := expandAt(beforeDay, []Show{other}, nil, schedule)
	if len(plan.Create) != 1 || plan.Create[0].State != StateSkipped || plan.Create[0].Settled ||
		!slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Fatalf("全部食い違う: plan = %+v", plan)
	}
	skipped := plan.Create[0]
	skipped.ID = 1
	if p := expandAt(beforeDay, []Show{other}, []Job{skipped}, schedule); !p.Empty() {
		t.Errorf("繰り返し: plan = %+v", p)
	}
	// 番組名が直れば waiting に戻す。
	fixed := []Airing{retitled(flat1, "別の番組 (1)"), news, retitled(flat2, "別の番組 (2)"), ann}
	p := expandAt(beforeDay, []Show{other}, []Job{skipped}, fixed)
	if len(p.Update) != 1 || p.Update[0].State != StateWaiting || p.Update[0].ID != 1 ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"別の番組 (1)", "別の番組 (2)"}) {
		t.Fatalf("直った: plan = %+v", p)
	}
	// また食い違えば skipped に戻す。
	waiting := p.Update[0]
	if p := expandAt(beforeDay, []Show{other}, []Job{waiting}, schedule); len(p.Update) != 1 ||
		p.Update[0].State != StateSkipped || p.Update[0].Settled {
		t.Errorf("また食い違う: plan = %+v", p)
	}

	// 一部だけ食い違えば、その放送回を waiting のジョブから外し、直れば戻す。
	flat := joinedShow("ふらっと", flatWeekly()...)
	job := waitingJob(1, "ふらっと", 0, flat1, flat2)
	special := []Airing{flat1, news, retitled(flat2, "特番"), ann}
	p = expandAt(beforeDay, []Show{flat}, []Job{job}, special)
	if len(p.Update) != 1 || p.Update[0].State != StateWaiting || !slices.Equal(titles(p.Update[0].Airings), []string{"ふらっと (1)"}) {
		t.Fatalf("一部食い違う: plan = %+v", p)
	}
	if p := expandAt(beforeDay, []Show{flat}, []Job{p.Update[0]}, schedule); len(p.Update) != 1 ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Errorf("一部直った: plan = %+v", p)
	}
}

// 連結する番組のジョブがほかの局の放送回だけを持ち、展開している局にその番組の当たる放送回が無ければ、ジョブに触らない
// （ほかの局の録らない予定を waiting に戻さない）。この局で当たる放送回があれば、今までどおり足す。
func TestExpandJoinLeavesOtherStationJob(t *testing.T) {
	other := joinedShow("別の番組", flatWeekly()...)
	tbs := retitled(flat1, "特番")
	tbs.StationID = "TBS"
	skipped := skippedJob(1, "別の番組", 0, tbs)
	// LFR の番組表に、番組の当たる放送回が無い。
	if p := expandAt(beforeDay, []Show{other}, []Job{skipped}, []Airing{news, ann}); !p.Empty() {
		t.Errorf("ほかの局のジョブを触った: plan = %+v", p)
	}
	// 最初の試行の時刻を過ぎても同じ（確定もさせない）。
	if p := expandAt(afterFirstAttempt, []Show{other}, []Job{skipped}, []Airing{news, ann}); !p.Empty() {
		t.Errorf("最初の試行の時刻の後: plan = %+v", p)
	}
	// LFR で当たる放送回の番組名が全部食い違っても触らない（waiting に戻さず、LFR の放送回も足さない）。
	if p := expandAt(beforeDay, []Show{other}, []Job{skipped}, schedule); !p.Empty() {
		t.Errorf("番組名が全部食い違う: plan = %+v", p)
	}
	if p := expandAt(afterFirstAttempt, []Show{other}, []Job{skipped}, schedule); !p.Empty() {
		t.Errorf("番組名が全部食い違う（最初の試行の時刻の後）: plan = %+v", p)
	}
	// 録音予定のジョブなら今までどおり（食い違う放送回は足さず、waiting のまま）。
	waiting := waitingJob(1, "別の番組", 0, tbs)
	if p := expandAt(beforeDay, []Show{other}, []Job{waiting}, schedule); !p.Empty() {
		t.Errorf("録音予定のジョブ: plan = %+v", p)
	}
	// LFR に番組名の合う放送回が 1 つでもあれば、今までどおりジョブに足して waiting にする。
	partly := []Airing{retitled(flat1, "別の番組 (1)"), news, flat2, ann}
	if p := expandAt(beforeDay, []Show{other}, []Job{skipped}, partly); len(p.Update) != 1 || p.Update[0].State != StateWaiting ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"別の番組 (1)", "特番"}) {
		t.Errorf("一部合う: plan = %+v", p)
	}
	// この局（LFR）の放送回を持つ録らない予定は、LFR の展開で食い違う放送回と合う放送回が混ざれば、今までどおり
	// 合う放送回で waiting に戻す（持つ放送回と合う放送回の数が同じでも）。
	own := skippedJob(2, "別の番組", 0, retitled(flat1, "特番"))
	mixed := []Airing{retitled(flat1, "特番"), news, retitled(flat2, "別の番組 (2)"), ann}
	if p := expandAt(beforeDay, []Show{other}, []Job{own}, mixed); len(p.Update) != 1 || p.Update[0].ID != 2 ||
		p.Update[0].State != StateWaiting || !slices.Equal(titles(p.Update[0].Airings), []string{"別の番組 (2)"}) {
		t.Errorf("この局の放送回を持つ録らない予定: plan = %+v", p)
	}
	// LFR で当たる放送回があれば、今までどおりジョブに足す。
	fixed := []Airing{retitled(flat1, "別の番組 (1)"), news, retitled(flat2, "別の番組 (2)"), ann}
	p := expandAt(beforeDay, []Show{other}, []Job{skipped}, fixed)
	if len(p.Update) != 1 || p.Update[0].ID != 1 || p.Update[0].State != StateWaiting ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"別の番組 (1)", "特番", "別の番組 (2)"}) { // 特番は (1) と同じ開始時刻で、局の順

		t.Errorf("当たる放送回がある: plan = %+v", p)
	}
}

// 連結しない番組は、放送回ごとのジョブが waiting と skipped を行き来する。
func TestExpandSkipBeforeAiringNoJoin(t *testing.T) {
	flat := joinedShow("ふらっと", flatWeekly()...)
	flat.Split = SplitSeparate
	special := []Airing{flat1, news, retitled(flat2, "特番"), ann}
	plan := expandAt(beforeDay, []Show{flat}, nil, special)
	if len(plan.Create) != 2 || !slices.Equal(states(plan.Create), []State{StateWaiting, StateSkipped}) ||
		plan.Create[1].Part != 2 || plan.Create[1].Settled {
		t.Fatalf("作る: plan = %+v", plan)
	}
	jobs := []Job{waitingJob(1, "ふらっと", 1, flat1), waitingJob(2, "ふらっと", 2, flat2)}
	p := expandAt(beforeDay, []Show{flat}, jobs, special)
	if len(p.Update) != 1 || p.Update[0].ID != 2 || p.Update[0].State != StateSkipped || p.Update[0].Part != 2 {
		t.Fatalf("食い違う: plan = %+v", p)
	}
	jobs[1] = p.Update[0]
	if p := expandAt(beforeDay, []Show{flat}, jobs, schedule); len(p.Update) != 1 || p.Update[0].ID != 2 ||
		p.Update[0].State != StateWaiting {
		t.Errorf("直った: plan = %+v", p)
	}
}

// check_title が false の番組と、1 回の予約の放送回は、番組名を確かめずに録る。
func TestExpandSkipBeforeAiringNotChecked(t *testing.T) {
	other := joinedShow("別の番組", flatWeekly()...)
	other.CheckTitle = false
	if p := expandAt(beforeDay, []Show{other}, nil, schedule); len(p.Create) != 1 || p.Create[0].State != StateWaiting ||
		len(p.Create[0].Airings) != 2 {
		t.Errorf("check_title なし: plan = %+v", p)
	}
	once := joinedShow("別の番組")
	once.Onces = []Once{onceOf(flat1)}
	if p := expandAt(beforeDay, []Show{once}, nil, schedule); len(p.Create) != 1 || p.Create[0].State != StateWaiting {
		t.Errorf("1 回の予約: plan = %+v", p)
	}
}

// 最初の試行の時刻を過ぎた展開で確定する。
func TestExpandSkipSettles(t *testing.T) {
	other := joinedShow("別の番組", flatWeekly()...)
	// 録らない予定のジョブが、そのまま確定する。
	p := expandAt(afterFirstAttempt, []Show{other}, []Job{skippedJob(1, "別の番組", 0, flat1, flat2)}, schedule)
	if len(p.Update) != 1 || p.Update[0].State != StateSkipped || !p.Update[0].Settled {
		t.Fatalf("録らない予定のまま: plan = %+v", p)
	}
	// 確定した後は触らない（番組名が直っても waiting に戻さない）。
	settled := p.Update[0]
	fixed := []Airing{retitled(flat1, "別の番組 (1)"), news, retitled(flat2, "別の番組 (2)"), ann}
	if p := expandAt(afterFirstAttempt, []Show{other}, []Job{settled}, fixed); !p.Empty() {
		t.Errorf("確定した後: plan = %+v", p)
	}
	// 録音予定のジョブが、最初の試行の時刻を過ぎた展開で食い違えば、確定した録らなかったにする。
	p = expandAt(afterFirstAttempt, []Show{other}, []Job{waitingJob(1, "別の番組", 0, flat1, flat2)}, schedule)
	if len(p.Update) != 1 || p.Update[0].State != StateSkipped || !p.Update[0].Settled {
		t.Errorf("録音予定から: plan = %+v", p)
	}
	// 確定の前の録らない予定は、最初の試行の時刻を過ぎた展開で番組名が直っていれば waiting に戻る。
	if p := expandAt(afterFirstAttempt, []Show{other}, []Job{skippedJob(1, "別の番組", 0, flat1, flat2)}, fixed); len(p.Update) != 1 ||
		p.Update[0].State != StateWaiting || p.Update[0].Settled {
		t.Errorf("直っていた: plan = %+v", p)
	}
}

// 人が決めたジョブは番組名で状態を変えない。休止・番組の無効化・メタデータの更新は効く。
func TestExpandByPerson(t *testing.T) {
	other := joinedShow("別の番組", flatWeekly()...)
	flat := joinedShow("ふらっと", flatWeekly()...)

	// 人が録ると決めたジョブ（waiting）は、番組名が食い違っても waiting のまま、当たる放送回を全部持つ。
	recorded := byPerson(waitingJob(1, "別の番組", 0, flat1))
	p := expandAt(beforeDay, []Show{other}, []Job{recorded}, schedule)
	if len(p.Update) != 1 || p.Update[0].State != StateWaiting || !p.Update[0].ByPerson ||
		!slices.Equal(titles(p.Update[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Fatalf("録ると決めた: plan = %+v", p)
	}
	if p := expandAt(beforeDay, []Show{other}, []Job{p.Update[0]}, schedule); !p.Empty() {
		t.Errorf("録ると決めた・繰り返し: plan = %+v", p)
	}
	// 人が録らないと決めたジョブ（skipped）は、番組名が合っていても skipped のまま。
	skipped := byPerson(skippedJob(1, "ふらっと", 0, flat1, flat2))
	if p := expandAt(beforeDay, []Show{flat}, []Job{skipped}, schedule); !p.Empty() {
		t.Errorf("録らないと決めた: plan = %+v", p)
	}
	// 連結しない番組でも同じ。
	noJoin := other
	noJoin.Split = SplitSeparate
	jobs := []Job{byPerson(waitingJob(1, "別の番組", 1, flat1)), byPerson(waitingJob(2, "別の番組", 2, flat2))}
	if p := expandAt(beforeDay, []Show{noJoin}, jobs, schedule); !p.Empty() {
		t.Errorf("録ると決めた（連結しない）: plan = %+v", p)
	}

	// 休止回は外し、放送回が 0 になれば消す。
	withoutFlat2 := []Airing{flat1, news, ann}
	if p := expandAt(beforeDay, []Show{flat}, []Job{skipped}, withoutFlat2); len(p.Update) != 1 ||
		p.Update[0].State != StateSkipped || !slices.Equal(titles(p.Update[0].Airings), []string{"ふらっと (1)"}) {
		t.Errorf("休止: plan = %+v", p)
	}
	if p := expandAt(beforeDay, []Show{flat}, []Job{skipped}, []Airing{news, ann}); len(p.Delete) != 1 || p.Delete[0].ID != 1 {
		t.Errorf("全部休止: plan = %+v", p)
	}
	// 無効にした番組・消した番組のジョブは消す。
	disabled := flat
	disabled.Disabled = true
	if p := expandAt(beforeDay, []Show{disabled}, []Job{skipped}, schedule); len(p.Delete) != 1 {
		t.Errorf("無効にした: plan = %+v", p)
	}
	if p := expandAt(beforeDay, nil, []Job{recorded}, schedule); len(p.Delete) != 1 {
		t.Errorf("消した: plan = %+v", p)
	}
	// 終了時刻が延びれば写す。
	longer := flat2
	longer.End = flat2.End.Add(30 * time.Minute)
	p = expandAt(beforeDay, []Show{flat}, []Job{skipped}, []Airing{flat1, news, longer, ann})
	if len(p.Update) != 1 || !p.Update[0].Airings[1].End.Equal(longer.End) || p.Update[0].State != StateSkipped {
		t.Errorf("終了時刻: plan = %+v", p)
	}
	// 録らない予定のジョブ（rajikura が決めたもの）にも、休止は効く。
	if p := expandAt(beforeDay, []Show{other}, []Job{skippedJob(1, "別の番組", 0, flat1, flat2)}, []Airing{news, ann}); len(p.Delete) != 1 {
		t.Errorf("録らない予定の休止: plan = %+v", p)
	}
	// 人が録らないと決めたジョブも、最初の試行の時刻を過ぎた展開で確定する。
	if p := expandAt(afterFirstAttempt, []Show{flat}, []Job{skipped}, schedule); len(p.Update) != 1 ||
		!p.Update[0].Settled || !p.Update[0].ByPerson {
		t.Errorf("人が決めたものの確定: plan = %+v", p)
	}
}
