package server

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// ジョブの詳細（GET /api/jobs/{id}）に、チャプターの書き込みの履歴（chapterWrites）が書き込んだ順に出る。履歴の無いジョブでは
// 空の配列（ADR 0033）。
func TestJobDetailChapterWrites(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	ctx := context.Background()
	airing := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Title: "番組"}
	id := addJob(t, st, "番組", "2026-10-12", 0, domain.StateDone, now, airing)
	other := addJob(t, st, "番組", "2026-10-11", 0, domain.StateDone, now, airing)
	at := now.Add(-time.Hour)
	for _, w := range []domain.ChapterWrite{
		{JobID: id, At: at, By: domain.ChapterWriteByRefetch, Result: domain.ChapterWriteFailed, Error: "保存先にファイルが無い"},
		{JobID: id, At: at.Add(30 * time.Minute), By: domain.ChapterWriteByRefetch, Result: domain.ChapterWritten, Count: 12},
	} {
		if _, err := st.FinishChapterWrite(ctx, w, domain.ChaptersAdded); err != nil {
			t.Fatal(err)
		}
	}
	var d struct {
		Chapters      string `json:"chapters"`
		ChapterWrites []struct {
			At     time.Time `json:"at"`
			By     string    `json:"by"`
			Result string    `json:"result"`
			Count  int       `json:"count"`
			Error  string    `json:"error"`
		} `json:"chapterWrites"`
	}
	rec := do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	decode(t, rec.Body.Bytes(), &d)
	if d.Chapters != "added" || len(d.ChapterWrites) != 2 {
		t.Fatalf("detail = %+v", d)
	}
	w0, w1 := d.ChapterWrites[0], d.ChapterWrites[1]
	if !w0.At.Equal(at) || w0.By != "refetch" || w0.Result != "failed" || w0.Count != 0 || w0.Error != "保存先にファイルが無い" {
		t.Errorf("chapterWrites[0] = %+v", w0)
	}
	if !w1.At.Equal(at.Add(30*time.Minute)) || w1.By != "refetch" || w1.Result != "written" || w1.Count != 12 || w1.Error != "" {
		t.Errorf("chapterWrites[1] = %+v", w1)
	}

	var raw map[string]any
	decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(other, 10), "", ck).Body.Bytes(), &raw)
	if ws, ok := raw["chapterWrites"].([]any); !ok || len(ws) != 0 {
		t.Errorf("履歴の無いジョブの chapterWrites = %#v", raw["chapterWrites"])
	}
}
