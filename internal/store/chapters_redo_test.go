package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// チャプターの入れ直しの頼み（ADR 0038）。ジョブ 1 件の指定は、タイムフリーから録った done のジョブを、チャプターの値によらず
// 受け付けて頼みの時刻を書く（状態・試行回数・チャプターの値・待ちは変えない）。done でない・ライブ録音・無いジョブは断る。
// まとめての頼みは、そのうちチャプターの値が値なしか入らなかったのジョブにだけ書き、数を返す。頼まれたジョブは、頼んだ時刻の
// 古い順に読める。
func TestRequestChapterRedo(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 20, 12, 0)
	live := flat2
	live.TimeFree.InNG = 2
	add := func(state domain.State, ch domain.Chapters, a domain.Airing) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{ShowName: "x", Day: day, State: state, Airings: []domain.Airing{a},
			Attempts: 2, NextAttemptAt: now.Add(-time.Hour), Deadline: jst(2026, 10, 12, 5, 0), OutputPath: "/lib/x.m4a", Chapters: ch})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	get := func(id int64) domain.Job {
		t.Helper()
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	none := add(domain.StateDone, domain.ChaptersNone, flat2)
	notAdded := add(domain.StateDone, domain.ChaptersNotAdded, flat2)
	added := add(domain.StateDone, domain.ChaptersAdded, flat2)
	waiting := add(domain.StateDone, domain.ChaptersWaiting, flat2)
	liveDone := add(domain.StateDone, domain.ChaptersNone, live)
	retrying := add(domain.StateRetrying, domain.ChaptersNone, flat2)

	// ジョブ 1 件の指定: 入ったのジョブも受け付ける（録音期限を過ぎていても）。
	if err := s.RequestChapterRedo(ctx, added, now); err != nil {
		t.Fatal(err)
	}
	if j := get(added); !j.ChapterRedoAt.Equal(now) || j.State != domain.StateDone || j.Attempts != 2 || j.Chapters != domain.ChaptersAdded {
		t.Fatalf("頼んだ後: %+v", j)
	}
	for _, tt := range []struct {
		name string
		id   int64
		err  error
	}{
		{"ライブ録音", liveDone, domain.ErrChapterRedoLive},
		{"retrying", retrying, domain.ErrInvalidTransition},
		{"無い", 9999, ErrNotFound},
	} {
		if err := s.RequestChapterRedo(ctx, tt.id, now); !errors.Is(err, tt.err) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.err)
		}
	}
	if j := get(liveDone); !j.ChapterRedoAt.IsZero() {
		t.Errorf("断ったのに頼みを書いた: %+v", j)
	}

	// まとめて: 値なし・入らなかったのタイムフリーの done だけ（入った・待っている・ライブ録音・done でないは飛ばす）。
	later := now.Add(time.Minute)
	n, err := s.RequestChapterRedoAll(ctx, later)
	if err != nil || n != 2 {
		t.Fatalf("RequestChapterRedoAll = %d, %v, want 2", n, err)
	}
	for id, want := range map[int64]time.Time{none: later, notAdded: later, added: now, waiting: {}, liveDone: {}, retrying: {}} {
		if j := get(id); !j.ChapterRedoAt.Equal(want) {
			t.Errorf("ジョブ %d: ChapterRedoAt = %v, want %v", id, j.ChapterRedoAt, want)
		}
	}

	// 頼まれたジョブを、頼んだ時刻の古い順に読む。
	due, err := s.DueChapterRedos(ctx)
	if err != nil || len(due) != 3 || due[0].ID != added || due[1].ID != none || due[2].ID != notAdded {
		t.Fatalf("DueChapterRedos = %+v, %v", due, err)
	}
}

// 入れ直しの結果を書くと、履歴に 1 行足し、チャプターの値を付け直して、拾ったときの頼みの時刻のままなら頼みを消す。拾った後に
// 頼み直されていれば、頼みは残す。人の録り直しを受け付けたら、頼みも消す（ADR 0038）。
func TestFinishChapterRedo(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 12, 0)
	id, err := s.InsertJob(ctx, domain.Job{ShowName: "x", Day: day, State: domain.StateDone, Airings: []domain.Airing{flat2},
		Attempts: 1, NextAttemptAt: now, Deadline: jst(2026, 10, 12, 5, 0), OutputPath: "/lib/x.m4a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestChapterRedo(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	picked, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// 拾った後に頼み直された。
	if err := s.RequestChapterRedo(ctx, id, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	w := domain.ChapterWrite{JobID: id, At: now.Add(2 * time.Second), By: domain.ChapterWriteByRedo, Result: domain.ChapterWritten, Count: 4}
	if _, err := s.FinishChapterRedo(ctx, w, domain.ChaptersAdded, picked.ChapterRedoAt); err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, id)
	if err != nil || j.Chapters != domain.ChaptersAdded || !j.ChapterRedoAt.Equal(now.Add(time.Second)) || j.State != domain.StateDone {
		t.Fatalf("頼み直された後: %+v, %v", j, err)
	}
	// 頼み直しの分を書いたら、頼みを消す。
	if _, err := s.FinishChapterRedo(ctx, w, domain.ChaptersAdded, j.ChapterRedoAt); err != nil {
		t.Fatal(err)
	}
	if j, err := s.GetJob(ctx, id); err != nil || !j.ChapterRedoAt.IsZero() {
		t.Fatalf("書いた後: %+v, %v", j, err)
	}
	if ws, err := s.ChapterWrites(ctx, id); err != nil || len(ws) != 2 || ws[0].By != domain.ChapterWriteByRedo || ws[0].Count != 4 {
		t.Errorf("ChapterWrites = %+v, %v", ws, err)
	}
	if due, err := s.DueChapterRedos(ctx); err != nil || len(due) != 0 {
		t.Errorf("DueChapterRedos = %+v, %v", due, err)
	}

	// 録り直しを受け付けたら、頼みも消す。
	if err := s.RequestChapterRedo(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Rerecord(ctx, id, domain.MembershipFree, nil, now); err != nil {
		t.Fatal(err)
	}
	if j, err := s.GetJob(ctx, id); err != nil || !j.ChapterRedoAt.IsZero() || j.State != domain.StateRetrying {
		t.Errorf("録り直しの後: %+v, %v", j, err)
	}
}
