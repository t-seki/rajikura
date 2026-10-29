package domain

import (
	"slices"
	"testing"
	"time"
)

// 2026-10-05（月曜）の LFR の番組表。
var (
	day      = NewBroadcastDay(2026, 10, 5)
	flat1    = Airing{ProgramID: "p1", StationID: "LFR", Start: jst(2026, 10, 5, 8, 30), End: jst(2026, 10, 5, 10, 0), Title: "ふらっと (1)"}
	news     = Airing{ProgramID: "p2", StationID: "LFR", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 10, 10), Title: "ニュース"}
	flat2    = Airing{ProgramID: "p3", StationID: "LFR", Start: jst(2026, 10, 5, 10, 10), End: jst(2026, 10, 5, 11, 0), Title: "ふらっと (2)"}
	ann      = Airing{ProgramID: "p4", StationID: "LFR", Start: jst(2026, 10, 6, 1, 0), End: jst(2026, 10, 6, 3, 0), Title: "オールナイトニッポン"}
	schedule = []Airing{ann, flat2, news, flat1} // 順不同で渡しても並べる
)

// flatWeekly は「ふらっと」の放送回（flat1・flat2）に当たる毎週の予約に、extra の開始時刻（月曜の LFR）の
// 毎週の予約を足したもの。
func flatWeekly(extra ...Clock) []Weekly {
	weekly := []Weekly{NewWeekly("LFR", time.Monday, Clock{8, 30}), NewWeekly("LFR", time.Monday, Clock{10, 10})}
	for _, c := range extra {
		weekly = append(weekly, NewWeekly("LFR", time.Monday, c))
	}
	return weekly
}

// joinedShow は、分割番組でつなぐ（SplitJoin）番組を作る（NewShow の既定は分割番組でない）。
func joinedShow(name string, weekly ...Weekly) Show {
	r := NewShow(name, weekly...)
	r.Split = SplitJoin
	return r
}

func expand(shows []Show, jobs []Job, airings []Airing) Plan {
	return Expand(ExpandInput{
		Shows: shows, StationID: "LFR", Day: day, Airings: airings, Jobs: jobs,
		Membership: MembershipFree, TFMaxDelay: 60 * time.Second,
	})
}

func titles(as []Airing) []string {
	var ts []string
	for _, a := range as {
		ts = append(ts, a.Title)
	}
	return ts
}

func TestExpandJoin(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 1 || len(plan.Update)+len(plan.Delete)+len(plan.Conflicts) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	j := plan.Create[0]
	if got := titles(j.Airings); !slices.Equal(got, []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Errorf("放送回 = %v（間のニュースは含めない）", got)
	}
	if j.Part != 0 || j.State != StateWaiting || j.ShowName != "ふらっと" || j.Day != day {
		t.Errorf("job = %+v", j)
	}
	if want := jst(2026, 10, 5, 11, 4).Add(30 * time.Second); !j.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v（最後の放送回の終了 + 150 秒 + 2 分）", j.NextAttemptAt, want)
	}
	if want := jst(2026, 10, 13, 5, 0); !j.Deadline.Equal(want) {
		t.Errorf("Deadline = %v, want %v", j.Deadline, want)
	}
}

// 連結しない番組（分割番組でつながない番組と、分割番組でない番組。ADR 0003）は、同じ放送日の放送回ごとにジョブを作り、
// パート番号で区別する。分割番組でない番組に同じ放送日の放送回が 2 つ当たっても連結しない。
func TestExpandNoJoinParts(t *testing.T) {
	for _, split := range []SplitMode{SplitSeparate, SplitNone} {
		show := Show{Name: "ふらっと", Split: split, Weekly: flatWeekly()}
		plan := expand([]Show{show}, nil, schedule)
		if len(plan.Create) != 2 {
			t.Fatalf("split=%v plan = %+v", split, plan)
		}
		for i, want := range []string{"ふらっと (1)", "ふらっと (2)"} {
			j := plan.Create[i]
			if j.Part != i+1 || len(j.Airings) != 1 || j.Airings[0].Title != want {
				t.Errorf("split=%v Create[%d] = part %d %v", split, i, j.Part, titles(j.Airings))
			}
		}

		// 放送回が 1 つだけならパート番号は付けない。
		plan = expand([]Show{show}, nil, []Airing{flat1, news})
		if len(plan.Create) != 1 || plan.Create[0].Part != 0 {
			t.Errorf("split=%v 1 つだけのとき: %+v", split, plan.Create)
		}
	}
}

func TestExpandMultipleWeekly(t *testing.T) {
	// 同じ番組の複数の毎週の予約が当たっても、ジョブは 1 つ。
	show := joinedShow("ANN",
		NewWeekly("LFR", time.Monday, Clock{1, 0}),
		NewWeekly("LFR", time.Monday, Clock{1, 0}),
	)
	plan := expand([]Show{show}, nil, schedule)
	if len(plan.Create) != 1 || len(plan.Conflicts) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := titles(plan.Create[0].Airings); !slices.Equal(got, []string{"オールナイトニッポン"}) {
		t.Errorf("放送回 = %v", got)
	}
	if plan.Create[0].Day != day {
		t.Errorf("深夜番組の放送日 = %v", plan.Create[0].Day)
	}
}

func TestExpandConflict(t *testing.T) {
	// 同じ放送回に、複数の番組の毎週の予約が当たったら矛盾。
	a := joinedShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}))
	b := joinedShow("ナイト", NewWeekly("LFR", time.Monday, Clock{1, 0}))
	flat := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{a, b, flat}, nil, schedule)
	if len(plan.Conflicts) != 1 {
		t.Fatalf("Conflicts = %+v", plan.Conflicts)
	}
	c := plan.Conflicts[0]
	if c.Airing.Title != "オールナイトニッポン" || !slices.Equal(c.Shows, []string{"ANN", "ナイト"}) {
		t.Errorf("conflict = %+v", c)
	}
	// 矛盾した放送回のジョブは作らない。ほかの番組は作る。
	if len(plan.Create) != 1 || plan.Create[0].ShowName != "ふらっと" {
		t.Errorf("Create = %+v", plan.Create)
	}
}

func TestExpandIgnoresOtherStationsAndDays(t *testing.T) {
	show := joinedShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}))
	other := ann
	other.StationID = "TBS"
	other.ProgramID = "x"
	nextDay := ann
	nextDay.ProgramID = "y"
	nextDay.Start = nextDay.Start.Add(24 * time.Hour)
	nextDay.End = nextDay.End.Add(24 * time.Hour)
	plan := expand([]Show{show}, nil, []Airing{other, nextDay})
	if !plan.Empty() {
		t.Errorf("plan = %+v", plan)
	}
}

func TestExpandIdempotent(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{show}, nil, schedule)
	j := plan.Create[0]
	j.ID = 1
	if p := expand([]Show{show}, []Job{j}, schedule); !p.Empty() {
		t.Errorf("同じ番組表で展開し直したら差分は無い: %+v", p)
	}
}

func waitingJob(id int64, show string, part int, as ...Airing) Job {
	j := Job{ID: id, ShowName: show, Day: day, Part: part, State: StateWaiting, Airings: as}
	j.Deadline = JobDeadline(as, MembershipFree)
	j.NextAttemptAt = FirstAttemptAt(lastEnd(as), 60*time.Second)
	return j
}

func TestExpandCancelled(t *testing.T) {
	join := joinedShow("ふらっと", flatWeekly()...)
	noJoin := Show{Name: "ふらっと", Weekly: join.Weekly}
	withoutFlat2 := []Airing{flat1, news, ann}
	tests := []struct {
		name       string
		shows      []Show
		jobs       []Job
		airings    []Airing
		wantUpdate [][]string
		wantDelete []int64
	}{
		{
			name:       "連結するジョブから休止回を外す",
			shows:      []Show{join},
			jobs:       []Job{waitingJob(1, "ふらっと", 0, flat1, flat2)},
			airings:    withoutFlat2,
			wantUpdate: [][]string{{"ふらっと (1)"}},
		},
		{
			name:       "放送回が 0 になったジョブは消す",
			shows:      []Show{join},
			jobs:       []Job{waitingJob(1, "ふらっと", 0, flat1, flat2)},
			airings:    []Airing{news, ann},
			wantDelete: []int64{1},
		},
		{
			name:  "連結しない番組: 休止した回のジョブを消し、残りのパート番号を外す",
			shows: []Show{noJoin},
			jobs: []Job{
				waitingJob(1, "ふらっと", 1, flat1),
				waitingJob(2, "ふらっと", 2, flat2),
			},
			airings:    withoutFlat2,
			wantUpdate: [][]string{{"ふらっと (1)"}},
			wantDelete: []int64{2},
		},
		{
			name:       "番組が消えたら waiting のジョブを消す",
			shows:      nil,
			jobs:       []Job{waitingJob(1, "ふらっと", 0, flat1, flat2)},
			airings:    schedule,
			wantDelete: []int64{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := expand(tt.shows, tt.jobs, tt.airings)
			if len(plan.Create) != 0 {
				t.Errorf("Create = %+v", plan.Create)
			}
			var gotUpdate [][]string
			for _, j := range plan.Update {
				gotUpdate = append(gotUpdate, titles(j.Airings))
			}
			if !slices.EqualFunc(gotUpdate, tt.wantUpdate, slices.Equal) {
				t.Errorf("Update = %v, want %v", gotUpdate, tt.wantUpdate)
			}
			var gotDelete []int64
			for _, j := range plan.Delete {
				gotDelete = append(gotDelete, j.ID)
			}
			if !slices.Equal(gotDelete, tt.wantDelete) {
				t.Errorf("Delete = %v, want %v", gotDelete, tt.wantDelete)
			}
		})
	}
}

func TestExpandUpdatePartNumber(t *testing.T) {
	// 連結しない番組で、残った 1 つのジョブのパート番号を外す。
	noJoin := Show{Name: "ふらっと", Weekly: flatWeekly()}
	plan := expand([]Show{noJoin}, []Job{
		waitingJob(1, "ふらっと", 1, flat1),
		waitingJob(2, "ふらっと", 2, flat2),
	}, []Airing{flat1})
	if len(plan.Update) != 1 || plan.Update[0].ID != 1 || plan.Update[0].Part != 0 {
		t.Errorf("Update = %+v", plan.Update)
	}
}

func TestExpandTimeChange(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly(Clock{10, 20})...)
	noJoin := Show{Name: "ふらっと", Weekly: show.Weekly}
	extended := flat2 // 終了時刻だけが変わった
	extended.End = jst(2026, 10, 5, 11, 30)
	moved := flat2 // 開始時刻が変わった
	moved.Start = jst(2026, 10, 5, 10, 20)
	moved.End = jst(2026, 10, 5, 11, 30)

	// 終了時刻が変わったら、waiting のジョブの放送回を新しい値に更新する。
	plan := expand([]Show{show}, []Job{waitingJob(1, "ふらっと", 0, flat1, flat2)}, []Airing{flat1, news, extended})
	if len(plan.Update) != 1 || len(plan.Create)+len(plan.Delete) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	j := plan.Update[0]
	if !j.Airings[1].End.Equal(extended.End) {
		t.Errorf("終了時刻が更新されていない: %v", j.Airings[1].End)
	}
	if want := FirstAttemptAt(extended.End, 60*time.Second); !j.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v", j.NextAttemptAt, want)
	}

	// 開始時刻が変わったら、連結するジョブの放送回を入れ替える。
	plan = expand([]Show{show}, []Job{waitingJob(1, "ふらっと", 0, flat1, flat2)}, []Airing{flat1, news, moved})
	if len(plan.Update) != 1 || len(plan.Create)+len(plan.Delete) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := plan.Update[0].Airings; len(got) != 2 || !got[1].Start.Equal(moved.Start) {
		t.Errorf("放送回 = %+v", got)
	}

	// 連結しない番組: 終了時刻だけなら同じジョブを更新する。
	jobs := []Job{waitingJob(1, "ふらっと", 1, flat1), waitingJob(2, "ふらっと", 2, flat2)}
	plan = expand([]Show{noJoin}, jobs, []Airing{flat1, news, extended})
	if len(plan.Update) != 1 || plan.Update[0].ID != 2 || len(plan.Create)+len(plan.Delete) != 0 {
		t.Fatalf("plan = %+v", plan)
	}

	// 連結しない番組: 開始時刻が変わったら、休止 + 新しい放送回として作り直す。
	plan = expand([]Show{noJoin}, jobs, []Airing{flat1, news, moved})
	if len(plan.Update) != 0 || len(plan.Delete) != 1 || plan.Delete[0].ID != 2 ||
		len(plan.Create) != 1 || plan.Create[0].Part != 2 || !plan.Create[0].Airings[0].Start.Equal(moved.Start) {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestExpandSameProgramID(t *testing.T) {
	// radiko の番組表では、分割番組の放送回が同じ ID で返る。どちらも捨てない。
	a1, a2 := flat1, flat2
	a1.ProgramID, a2.ProgramID = "same", "same"
	airings := []Airing{a1, news, a2}

	show := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{show}, nil, airings)
	if len(plan.Create) != 1 || !slices.Equal(titles(plan.Create[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Fatalf("連結: plan = %+v", plan)
	}
	j := plan.Create[0]
	j.ID = 1
	if p := expand([]Show{show}, []Job{j}, airings); !p.Empty() {
		t.Errorf("連結: 展開し直したら差分は無い: %+v", p)
	}

	noJoin := Show{Name: "ふらっと", Weekly: show.Weekly}
	plan = expand([]Show{noJoin}, nil, airings)
	if len(plan.Create) != 2 || plan.Create[0].Part != 1 || plan.Create[1].Part != 2 {
		t.Fatalf("連結しない: plan = %+v", plan)
	}
	jobs := []Job{plan.Create[0], plan.Create[1]}
	jobs[0].ID, jobs[1].ID = 1, 2
	if p := expand([]Show{noJoin}, jobs, airings); !p.Empty() {
		t.Errorf("連結しない: 展開し直したら差分は無い: %+v", p)
	}
}

func TestExpandFiltersJobsByDay(t *testing.T) {
	// ほかの放送日のジョブは、当たる放送回が無くても触らない。
	show := joinedShow("ふらっと", flatWeekly()...)
	other := waitingJob(1, "ふらっと", 0, flat1)
	other.Day = day.AddDays(-1)
	plan := expand([]Show{show}, []Job{other}, []Airing{news})
	if !plan.Empty() {
		t.Errorf("plan = %+v", plan)
	}
	// 当たる放送回があれば、ほかの放送日のジョブとは別に作る。
	plan = expand([]Show{show}, []Job{other}, schedule)
	if len(plan.Create) != 1 || len(plan.Update)+len(plan.Delete) != 0 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestExpandLeavesNonWaitingJobs(t *testing.T) {
	join := joinedShow("ふらっと", flatWeekly(Clock{11, 0})...)
	noJoin := Show{Name: "ふらっと", Weekly: join.Weekly}
	for _, st := range []State{StateRunning, StateRetrying, StateDone, StateExpired} {
		t.Run(string(st), func(t *testing.T) {
			j := waitingJob(1, "ふらっと", 0, flat1, flat2)
			j.State = st
			// 休止回があっても、新しい放送回があっても触らない。
			extra := Airing{ProgramID: "p9", StationID: "LFR", Start: jst(2026, 10, 5, 11, 0), End: jst(2026, 10, 5, 11, 30), Title: "ふらっと (3)"}
			if p := expand([]Show{join}, []Job{j}, []Airing{flat1, extra}); !p.Empty() {
				t.Errorf("連結: plan = %+v", p)
			}
			if p := expand(nil, []Job{j}, nil); !p.Empty() {
				t.Errorf("番組が消えても: plan = %+v", p)
			}

			k := waitingJob(2, "ふらっと", 1, flat1)
			k.State = st
			p := expand([]Show{noJoin}, []Job{k}, []Airing{flat1, flat2})
			// flat1 は既存のジョブのまま。flat2 だけ新しく作る。
			if len(p.Create) != 1 || p.Create[0].Part != 2 || len(p.Update)+len(p.Delete)+len(p.Conflicts) != 0 {
				t.Errorf("連結しない: plan = %+v", p)
			}
		})
	}
}

func TestExpandDisabledShow(t *testing.T) {
	for _, split := range []SplitMode{SplitJoin, SplitSeparate, SplitNone} {
		join := split == SplitJoin
		show := Show{Name: "ふらっと", Split: split, Disabled: true, Weekly: flatWeekly()}
		// 無効な番組にはジョブを作らない。
		if p := expand([]Show{show}, nil, schedule); !p.Empty() {
			t.Errorf("join=%v 作る: plan = %+v", join, p)
		}
		// waiting のジョブは、番組が Shows に無いとき（番組を消したとき）と同じく消え、
		// retrying のジョブは残る。
		waiting := waitingJob(1, "ふらっと", 0, flat1, flat2)
		retrying := waitingJob(2, "ふらっと", 0, flat1)
		retrying.State = StateRetrying
		if !join {
			waiting = waitingJob(1, "ふらっと", 2, flat2)
			retrying.Part = 1
		}
		jobs := []Job{waiting, retrying}
		got := expand([]Show{show}, jobs, schedule)
		pruned := expand(nil, jobs, schedule)
		if len(got.Delete) != 1 || got.Delete[0].ID != 1 || len(got.Create)+len(got.Update)+len(got.Conflicts) != 0 {
			t.Errorf("join=%v 無効にした後: plan = %+v", join, got)
		}
		if len(pruned.Delete) != 1 || pruned.Delete[0].ID != got.Delete[0].ID {
			t.Errorf("join=%v 番組を消したときと違う: %+v", join, pruned)
		}
	}
	// 有効に戻せばジョブを作る。
	show := joinedShow("ふらっと", flatWeekly()...)
	if p := expand([]Show{show}, nil, schedule); len(p.Create) != 1 {
		t.Errorf("有効に戻した後: plan = %+v", p)
	}
}

func TestExpandPartConflict(t *testing.T) {
	// 番組表が変わって、新しい放送回のパート番号が waiting 以外のジョブと重なるなら矛盾。
	noJoin := Show{Name: "ふらっと", Weekly: flatWeekly(Clock{9, 0})}
	done := waitingJob(1, "ふらっと", 2, flat2)
	done.State = StateDone
	other := Airing{ProgramID: "p8", StationID: "LFR", Start: jst(2026, 10, 5, 9, 0), End: jst(2026, 10, 5, 9, 30), Title: "ふらっと (0)"}
	plan := expand([]Show{noJoin}, []Job{done}, []Airing{flat1, other, flat2})
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Airing.Title != "ふらっと (0)" {
		t.Errorf("Conflicts = %+v", plan.Conflicts)
	}
}

func TestExpandKeepsOtherStations(t *testing.T) {
	// ほかの局の放送回は触らない。
	show := joinedShow("ふらっと", append(flatWeekly(), NewWeekly("TBS", time.Monday, Clock{12, 0}))...)
	tbs := Airing{ProgramID: "t1", StationID: "TBS", Start: jst(2026, 10, 5, 12, 0), End: jst(2026, 10, 5, 13, 0), Title: "ふらっと (3)"}
	plan := expand([]Show{show}, []Job{waitingJob(1, "ふらっと", 0, flat1, tbs)}, []Airing{news})
	if len(plan.Update) != 1 || !slices.Equal(titles(plan.Update[0].Airings), []string{"ふらっと (3)"}) {
		t.Errorf("plan = %+v", plan)
	}
}

func TestExpandAddsNewAiringToWaitingJob(t *testing.T) {
	show := joinedShow("ふらっと", flatWeekly()...)
	plan := expand([]Show{show}, []Job{waitingJob(1, "ふらっと", 0, flat1)}, schedule)
	if len(plan.Update) != 1 || !slices.Equal(titles(plan.Update[0].Airings), []string{"ふらっと (1)", "ふらっと (2)"}) {
		t.Errorf("plan = %+v", plan)
	}
}

func TestAiringKey(t *testing.T) {
	a := Airing{StationID: "LFR", Start: jst(2026, 10, 5, 8, 30)}
	if got := a.Key(); got != "LFR@2026-10-04T23:30:00Z" {
		t.Errorf("Key = %s", got)
	}
	a.ProgramID = "p1"
	if got := a.Key(); got != "LFR@2026-10-04T23:30:00Z" {
		t.Errorf("番組表の ID は Key に使わない: %s", got)
	}
}
