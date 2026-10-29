package store

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// チャプターの値と待ちは、保存（Complete）で書き、待っているジョブを引く時刻の順に読み（DueChapterChecks）、取り直しを起こすと
// 録り直しと同じく retrying・試行回数 0・次の試行を今にして取り直しの印を立てる。取り直しの印は起動時の回復で消えず、
// 取り直しが尽きたら done に戻す（保存先と欠落の印は前のまま）。人の録り直しは待ちを消し、取り直しの印を外す（ADR 0027）。
func TestChapterWaitFlow(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 11, 10)
	insert := func(t *testing.T) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, State: domain.StateRunning, Airings: []domain.Airing{news, flat2},
			Attempts: 1, NextAttemptAt: now, Deadline: jst(2026, 10, 12, 5, 0),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	get := func(t *testing.T, id int64) domain.Job {
		t.Helper()
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	id := insert(t)
	waits := map[string]domain.AiringChapterWait{news.Key(): domain.AiringChapterPending}
	check := now.Add(30 * time.Minute)
	if err := s.Complete(ctx, id, "/lib/x.m4a", []domain.Airing{flat2}, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check, Waits: waits}); err != nil {
		t.Fatal(err)
	}
	j := get(t, id)
	if j.State != domain.StateDone || j.Chapters != domain.ChaptersWaiting || !j.ChapterCheckAt.Equal(check) || !maps.Equal(j.ChapterWaits, waits) {
		t.Fatalf("保存の後: %+v", j)
	}
	if due, err := s.DueChapterChecks(ctx, check.Add(-time.Second)); err != nil || len(due) != 0 {
		t.Errorf("引く時刻の前: %+v, %v", due, err)
	}
	if due, err := s.DueChapterChecks(ctx, check); err != nil || len(due) != 1 || due[0].ID != id {
		t.Errorf("引く時刻: %+v, %v", due, err)
	}

	// 待ち続ける。
	waits2 := map[string]domain.AiringChapterWait{news.Key(): domain.AiringChapterReady}
	if ok, err := s.UpdateChapterWait(ctx, id, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check.Add(30 * time.Minute), Waits: waits2}); err != nil || !ok {
		t.Fatalf("UpdateChapterWait = %v, %v", ok, err)
	}
	if j := get(t, id); !maps.Equal(j.ChapterWaits, waits2) || !j.ChapterCheckAt.Equal(check.Add(30*time.Minute)) {
		t.Errorf("待ち続けた後: %+v", j)
	}

	// 取り直しを起こす。
	at := check.Add(30 * time.Minute)
	if ok, err := s.StartChapterRefetch(ctx, id, at); err != nil || !ok {
		t.Fatalf("StartChapterRefetch = %v, %v", ok, err)
	}
	j = get(t, id)
	if j.State != domain.StateRetrying || j.Attempts != 0 || !j.NextAttemptAt.Equal(at) || !j.ChapterRefetch ||
		j.Chapters != domain.ChaptersWaiting || !j.ChapterCheckAt.IsZero() || len(j.ChapterWaits) != 0 || j.OutputPath != "/lib/x.m4a" {
		t.Fatalf("取り直しを起こした後: %+v", j)
	}
	if ok, err := s.StartChapterRefetch(ctx, id, at); err != nil || ok {
		t.Errorf("もう待っていないのに起こした: %v, %v", ok, err)
	}
	if ok, err := s.UpdateChapterWait(ctx, id, domain.ChapterStatus{Chapters: domain.ChaptersNotAdded}); err != nil || ok {
		t.Errorf("もう待っていないのに書いた: %v, %v", ok, err)
	}

	// 起動時の回復で retrying に戻しても、取り直しの印は残る。
	if _, _, err := s.ClaimDue(ctx, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if j := get(t, id); j.State != domain.StateRetrying || !j.ChapterRefetch {
		t.Fatalf("回復の後: %+v", j)
	}

	// 取り直しが尽きたら done に戻す。
	if _, _, err := s.ClaimDue(ctx, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertChapterRefetch(ctx, id); err != nil {
		t.Fatal(err)
	}
	j = get(t, id)
	if j.State != domain.StateDone || j.ChapterRefetch || j.Chapters != domain.ChaptersNotAdded || j.OutputPath != "/lib/x.m4a" ||
		len(j.Missing) != 1 || j.Missing[0].Key() != flat2.Key() {
		t.Fatalf("戻した後: %+v", j)
	}

	// 人の録り直しは、待ちを消して入らなかったにし、取り直しの印を外す。
	id2 := insert(t)
	if err := s.Complete(ctx, id2, "/lib/y.m4a", nil, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check, Waits: waits}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET chapters_refetch = 1 WHERE id = ?`, id2); err != nil {
		t.Fatal(err)
	}
	if err := s.Rerecord(ctx, id2, now); err != nil {
		t.Fatal(err)
	}
	if j := get(t, id2); j.State != domain.StateRetrying || j.Chapters != domain.ChaptersNotAdded || !j.ChapterCheckAt.IsZero() ||
		len(j.ChapterWaits) != 0 || j.ChapterRefetch {
		t.Fatalf("人の録り直しの後: %+v", j)
	}

	// 録音期限を過ぎていれば、取り直しを起こさずに入らなかったにする。
	id3 := insert(t)
	if err := s.Complete(ctx, id3, "/lib/z.m4a", nil, domain.ChapterStatus{Chapters: domain.ChaptersWaiting, CheckAt: check, Waits: waits}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.StartChapterRefetch(ctx, id3, jst(2026, 10, 12, 5, 0)); err != nil || ok {
		t.Fatalf("録音期限の後に起こした: %v, %v", ok, err)
	}
	if j := get(t, id3); j.State != domain.StateDone || j.Chapters != domain.ChaptersNotAdded || !j.ChapterCheckAt.IsZero() || len(j.ChapterWaits) != 0 {
		t.Fatalf("録音期限の後: %+v", j)
	}
}
