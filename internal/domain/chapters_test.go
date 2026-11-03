package domain

import (
	"errors"
	"maps"
	"testing"
	"time"
)

// chapterJob は flat1・flat2 を持つ done のジョブ（タイムフリー）。打ち切りは flat2 の終了の 6 時間後。
func chapterJob() Job {
	as := []Airing{flat1, flat2}
	return Job{State: StateDone, Airings: as, Deadline: JobDeadline(as, MembershipFree)}
}

// 保存のたびにチャプターの値を決める。全部取れたら入った、まだできていないかもしれない放送回があり打ち切りと録音期限の前なら
// 待っている（次に引くのは保存の 30 分後。打ち切りより後なら打ち切りの時刻）、それ以外は入らなかった。取りに行っていなければ値なし。
func TestChaptersAfterSave(t *testing.T) {
	j := chapterJob()
	as := j.Airings
	cutoff := flat2.End.Add(6 * time.Hour)
	saved := flat2.End.Add(5 * time.Minute)
	pending := map[string]AiringChapterWait{flat1.Key(): AiringChapterPending}
	tests := []struct {
		name    string
		fetched []ChapterFetch
		now     time.Time
		want    ChapterStatus
	}{
		{"取りに行っていない", nil, saved, ChapterStatus{}},
		{"全部取れた", []ChapterFetch{ChapterGot, ChapterGot}, saved, ChapterStatus{Chapters: ChaptersAdded}},
		{"404 がある", []ChapterFetch{ChapterPending, ChapterGot}, saved,
			ChapterStatus{Chapters: ChaptersWaiting, CheckAt: saved.Add(30 * time.Minute), Waits: pending}},
		{"404 と空", []ChapterFetch{ChapterPending, ChapterAbsent}, saved,
			ChapterStatus{Chapters: ChaptersWaiting, CheckAt: saved.Add(30 * time.Minute), Waits: pending}},
		{"打ち切りの 10 分前", []ChapterFetch{ChapterPending, ChapterGot}, cutoff.Add(-10 * time.Minute),
			ChapterStatus{Chapters: ChaptersWaiting, CheckAt: cutoff, Waits: pending}},
		{"打ち切りちょうど", []ChapterFetch{ChapterPending, ChapterGot}, cutoff, ChapterStatus{Chapters: ChaptersNotAdded}},
		{"空と eventId が無いだけ", []ChapterFetch{ChapterAbsent, ChapterGot}, saved, ChapterStatus{Chapters: ChaptersNotAdded}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChaptersAfterSave(j, as, tt.fetched, tt.now)
			if got.Chapters != tt.want.Chapters || !got.CheckAt.Equal(tt.want.CheckAt) || !maps.Equal(got.Waits, tt.want.Waits) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}

	past := j
	past.Deadline = saved
	if got := ChaptersAfterSave(past, as, []ChapterFetch{ChapterPending, ChapterGot}, saved); got.Chapters != ChaptersNotAdded {
		t.Errorf("録音期限を過ぎた: %+v", got)
	}
}

// 待っている放送回の API を引いた結果で、待ち続ける・取り直す・やめるを決める。
func TestCheckChapters(t *testing.T) {
	j := chapterJob()
	j.Chapters = ChaptersWaiting
	j.ChapterWaits = map[string]AiringChapterWait{flat1.Key(): AiringChapterPending, flat2.Key(): AiringChapterPending}
	cutoff := flat2.End.Add(6 * time.Hour)
	now := flat2.End.Add(time.Hour)
	tests := []struct {
		name      string
		results   map[string]ChapterFetch
		now       time.Time
		want      ChapterCheckResult
		wantWaits map[string]AiringChapterWait
	}{
		{"どちらもまだ", map[string]ChapterFetch{flat1.Key(): ChapterPending, flat2.Key(): ChapterPending}, now,
			ChapterKeepWaiting, j.ChapterWaits},
		{"一方だけ取れた", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterPending}, now,
			ChapterKeepWaiting, map[string]AiringChapterWait{flat1.Key(): AiringChapterReady, flat2.Key(): AiringChapterPending}},
		{"全部取れた", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterGot}, now, ChapterWriteNow, nil},
		{"取れたものと空", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterAbsent}, now, ChapterWriteNow, nil},
		{"どちらも空", map[string]ChapterFetch{flat1.Key(): ChapterAbsent, flat2.Key(): ChapterAbsent}, now, ChapterGiveUp, nil},
		{"打ち切りでどちらもまだ", map[string]ChapterFetch{flat1.Key(): ChapterPending, flat2.Key(): ChapterPending}, cutoff, ChapterGiveUp, nil},
		{"打ち切りで一方だけ取れた", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterPending}, cutoff, ChapterWriteNow, nil},
		{"録音期限を過ぎた", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterGot}, j.Deadline, ChapterGiveUp, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, st := CheckChapters(j, tt.results, tt.now)
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			switch got {
			case ChapterKeepWaiting:
				if st.Chapters != ChaptersWaiting || !st.CheckAt.Equal(tt.now.Add(30*time.Minute)) || !maps.Equal(st.Waits, tt.wantWaits) {
					t.Errorf("status = %+v", st)
				}
			case ChapterGiveUp:
				if st.Chapters != ChaptersNotAdded {
					t.Errorf("status = %+v", st)
				}
			}
		})
	}
	// 前の見回りで取れるようになった放送回は引き直さず、そのまま取れるようになったとして数える。
	ready := j
	ready.ChapterWaits = map[string]AiringChapterWait{flat1.Key(): AiringChapterReady, flat2.Key(): AiringChapterPending}
	if got, _ := CheckChapters(ready, map[string]ChapterFetch{flat2.Key(): ChapterGot}, now); got != ChapterWriteNow {
		t.Errorf("前に取れるようになった放送回: got %v", got)
	}
}

// 書き込みの前に取った結果は、全部取れたら入った、それ以外（一部だけ・全部取れない）は入らなかった。待ちは起こさない（ADR 0033）。
func TestChaptersAfterWrite(t *testing.T) {
	tests := []struct {
		fetched []ChapterFetch
		want    Chapters
	}{
		{[]ChapterFetch{ChapterGot, ChapterGot}, ChaptersAdded},
		{[]ChapterFetch{ChapterGot, ChapterPending}, ChaptersNotAdded},
		{[]ChapterFetch{ChapterAbsent}, ChaptersNotAdded},
		{nil, ChaptersNotAdded},
	}
	for _, tt := range tests {
		if got := ChaptersAfterWrite(tt.fetched); got != tt.want {
			t.Errorf("ChaptersAfterWrite(%v) = %q, want %q", tt.fetched, got, tt.want)
		}
	}
}

// パートの長さは、保存のときに残した長さが全部の放送回にあればそれ、1 つでも無ければ全部を番組表の放送時間で代える（ADR 0033）。
func TestChapterParts(t *testing.T) {
	as := []Airing{flat1, flat2}
	guide := []time.Duration{flat1.End.Sub(flat1.Start), flat2.End.Sub(flat2.Start)}
	stored := map[string]time.Duration{flat1.Key(): 59 * time.Minute, flat2.Key(): 50 * time.Minute}
	if got := ChapterParts(as, stored); got[0] != 59*time.Minute || got[1] != 50*time.Minute {
		t.Errorf("残した長さ: %v", got)
	}
	for name, lengths := range map[string]map[string]time.Duration{
		"残していない":  nil,
		"一部だけ残した": {flat1.Key(): 59 * time.Minute},
	} {
		if got := ChapterParts(as, lengths); got[0] != guide[0] || got[1] != guide[1] {
			t.Errorf("%s: %v, want %v", name, got, guide)
		}
	}
}

// 保存したファイルの放送回は、欠落した放送回を除いたもの（並びは Airings のまま）。
func TestSavedAirings(t *testing.T) {
	j := chapterJob()
	if got := j.SavedAirings(); len(got) != 2 {
		t.Errorf("欠落なし: %v", got)
	}
	j.Missing = []Airing{flat1}
	if got := j.SavedAirings(); len(got) != 1 || got[0].Key() != flat2.Key() {
		t.Errorf("flat1 が欠落: %v", got)
	}
}

// チャプターの入れ直しを受け付けるのは、タイムフリーから録った done のジョブ（チャプターの値と録音期限は問わない）。done でなければ
// ErrInvalidTransition、ライブで録るジョブなら ErrChapterRedoLive。まとめての入れ直しは、そのうちチャプターの値が値なしか入らなかった
// のジョブにだけ当たる（ADR 0033）。
func TestCheckChapterRedo(t *testing.T) {
	live := chapterJob()
	live.Airings = []Airing{flat1, flat2}
	live.Airings[0].TimeFree.InNG = 2
	late := chapterJob()
	late.Deadline = flat1.Start
	tests := []struct {
		name  string
		job   func() Job
		err   error
		inAll map[Chapters]bool
	}{
		{"done のタイムフリー", chapterJob, nil, map[Chapters]bool{
			ChaptersNone: true, ChaptersNotAdded: true, ChaptersAdded: false, ChaptersWaiting: false}},
		{"録音期限を過ぎた done", func() Job { return late }, nil, map[Chapters]bool{ChaptersNone: true, ChaptersAdded: false}},
		{"ライブ録音", func() Job { return live }, ErrChapterRedoLive, map[Chapters]bool{ChaptersNone: false}},
		{"retrying", func() Job { j := chapterJob(); j.State = StateRetrying; return j }, ErrInvalidTransition,
			map[Chapters]bool{ChaptersNone: false}},
		{"expired", func() Job { j := chapterJob(); j.State = StateExpired; return j }, ErrInvalidTransition,
			map[Chapters]bool{ChaptersNotAdded: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for ch, want := range tt.inAll {
				j := tt.job()
				j.Chapters = ch
				if err := CheckChapterRedo(j); (tt.err == nil) != (err == nil) || (tt.err != nil && !errors.Is(err, tt.err)) {
					t.Errorf("chapters %q: CheckChapterRedo = %v, want %v", ch, err, tt.err)
				}
				if got := ChapterRedoInAll(j); got != want {
					t.Errorf("chapters %q: ChapterRedoInAll = %v, want %v", ch, got, want)
				}
			}
		})
	}
}

// 書き込めなかった（取れずに書かなかったときも）ときのチャプターの値は、前が入ったなら入ったのまま、ほかは入らなかった。
func TestChaptersAfterFailedWrite(t *testing.T) {
	for prev, want := range map[Chapters]Chapters{
		ChaptersAdded: ChaptersAdded, ChaptersWaiting: ChaptersNotAdded, ChaptersNotAdded: ChaptersNotAdded, ChaptersNone: ChaptersNotAdded,
	} {
		if got := ChaptersAfterFailedWrite(prev); got != want {
			t.Errorf("ChaptersAfterFailedWrite(%q) = %q, want %q", prev, got, want)
		}
	}
}

// 書き込みの前にチャプターを取り直した結果から、書くか・やり直すか・書かずに終えるかを決める（ADR 0038）。取れていたはずの放送回が
// 一時的に取れなければ書かない。チャプターの取り直しは、やり直しの上限（最後の放送回の終了から 12 時間）の前はやり直し、上限以後は
// 諦める。チャプターの入れ直しは、前が入ったジョブだけ止め、やり直さない。
func TestDecideChapterWrite(t *testing.T) {
	j := chapterJob()
	limit := flat2.End.Add(12 * time.Hour)
	now := flat2.End.Add(time.Hour)
	// flat1 は待っていて、取れるようになった（Ready）。flat2 は保存のときに取れて待たなかった。
	waitsReady := map[string]AiringChapterWait{flat1.Key(): AiringChapterReady}
	// flat1 は打ち切りの時点でまだ取れていない（Pending のまま）。flat2 は待たなかった。
	waitsPending := map[string]AiringChapterWait{flat1.Key(): AiringChapterPending}
	ok, pend, abs := ChapterGot, ChapterPending, ChapterAbsent
	refetch, redo := ChapterWriteByRefetch, ChapterWriteByRedo
	tests := []struct {
		name    string
		by      ChapterWriteBy
		prev    Chapters
		waits   map[string]AiringChapterWait
		fetched []ChapterFetch
		now     time.Time
		want    ChapterWriteVerdict
	}{
		{"取り直し: 全部取れた", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{ok, ok}, now, ChapterWriteProceed},
		{"取り直し: 取れたものと空だけ", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{ok, abs}, now, ChapterWriteProceed},
		{"取り直し: 取れていたはずでない放送回だけが一時的に取れない", refetch, ChaptersWaiting, waitsPending, []ChapterFetch{pend, ok}, now, ChapterWriteProceed},
		{"取り直し: 全部空", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{abs, abs}, now, ChapterWriteProceed},
		{"取り直し: 待っていて取れるようになった放送回が一時的に取れない、上限の前", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{pend, ok}, now, ChapterWriteRetry},
		{"取り直し: 待たなかった放送回が一時的に取れない、上限の前", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{ok, pend}, now, ChapterWriteRetry},
		{"取り直し: 上限の 1 分前", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{ok, pend}, limit.Add(-time.Minute), ChapterWriteRetry},
		{"取り直し: 上限ちょうど", refetch, ChaptersWaiting, waitsReady, []ChapterFetch{ok, pend}, limit, ChapterWriteSkip},
		{"取り直し: 上限の後でも、取れていたはずでない放送回だけなら書く", refetch, ChaptersWaiting, waitsPending, []ChapterFetch{pend, ok}, limit.Add(time.Hour), ChapterWriteProceed},
		{"取り直し: waits が nil なら Job.ChapterWaits", refetch, ChaptersWaiting, nil, []ChapterFetch{pend, ok}, now, ChapterWriteProceed},
		{"入れ直し: 前が入った、全部取れた", redo, ChaptersAdded, nil, []ChapterFetch{ok, ok}, now, ChapterWriteProceed},
		{"入れ直し: 前が入った、どれかが一時的に取れない", redo, ChaptersAdded, nil, []ChapterFetch{ok, pend}, now, ChapterWriteSkip},
		{"入れ直し: 前が入った、上限の前でもやり直さない", redo, ChaptersAdded, nil, []ChapterFetch{pend, pend}, limit.Add(-time.Minute), ChapterWriteSkip},
		{"入れ直し: 前が入った、取れたものと空だけ", redo, ChaptersAdded, nil, []ChapterFetch{ok, abs}, now, ChapterWriteProceed},
		{"入れ直し: 前が値なし、一部が一時的に取れない", redo, ChaptersNone, nil, []ChapterFetch{pend, ok}, now, ChapterWriteProceed},
		{"入れ直し: 前が入らなかった、一部が一時的に取れない", redo, ChaptersNotAdded, nil, []ChapterFetch{pend, ok}, now, ChapterWriteProceed},
		{"入れ直し: 前が待っている、一部が一時的に取れない", redo, ChaptersWaiting, nil, []ChapterFetch{pend, ok}, now, ChapterWriteProceed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jj := j
			jj.Chapters = tt.prev
			jj.ChapterWaits = waitsPending
			if got := DecideChapterWrite(jj, tt.by, tt.waits, tt.fetched, tt.now); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// 書き込みのやり直しの次の時刻は、今の 30 分後。上限より後なら上限。
func TestNextChapterRetry(t *testing.T) {
	j := chapterJob()
	limit := j.ChapterRetryLimit()
	if want := flat2.End.Add(12 * time.Hour); !limit.Equal(want) {
		t.Fatalf("limit = %v, want %v", limit, want)
	}
	now := flat2.End.Add(time.Hour)
	if got := NextChapterRetry(j, now); !got.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("got %v", got)
	}
	if got := NextChapterRetry(j, limit.Add(-10*time.Minute)); !got.Equal(limit) {
		t.Errorf("got %v, want %v", got, limit)
	}
}
