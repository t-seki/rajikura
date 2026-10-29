package domain

import (
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
		{"全部取れた", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterGot}, now, ChapterRefetchNow, nil},
		{"取れたものと空", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterAbsent}, now, ChapterRefetchNow, nil},
		{"どちらも空", map[string]ChapterFetch{flat1.Key(): ChapterAbsent, flat2.Key(): ChapterAbsent}, now, ChapterGiveUp, nil},
		{"打ち切りでどちらもまだ", map[string]ChapterFetch{flat1.Key(): ChapterPending, flat2.Key(): ChapterPending}, cutoff, ChapterGiveUp, nil},
		{"打ち切りで一方だけ取れた", map[string]ChapterFetch{flat1.Key(): ChapterGot, flat2.Key(): ChapterPending}, cutoff, ChapterRefetchNow, nil},
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
	if got, _ := CheckChapters(ready, map[string]ChapterFetch{flat2.Key(): ChapterGot}, now); got != ChapterRefetchNow {
		t.Errorf("前に取れるようになった放送回: got %v", got)
	}
}
