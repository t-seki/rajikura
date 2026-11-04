package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// jobs <ID>: 概要・放送回・試行の履歴・チャプターの書き込みの履歴・保存先の移しの履歴が出る。結果 3 種の文、秒までの時刻を確かめる。
func TestJobsDetail(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	a := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Title: "番組A"}
	id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: "番組A", Day: domain.BroadcastDayOf(now), Part: 2, State: domain.StateDone,
		OutputPath: "/library/a.m4a", Airings: []domain.Airing{a}, Missing: []domain.Airing{a}, NextAttemptAt: now, Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	st := now.Add(-time.Hour).Truncate(time.Second)
	if _, err := api.Store.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: st, EndedAt: st.Add(time.Minute), FailedStage: domain.StageFetch, Error: "落ちた\n2 行目"}); err != nil {
		t.Fatal(err)
	}
	for _, w := range []domain.ChapterWrite{
		{JobID: id, At: st, By: domain.ChapterWriteByRefetch, Result: domain.ChapterWritten, Count: 12},
		{JobID: id, At: st.Add(time.Second), By: domain.ChapterWriteByRedo, Result: domain.ChapterNotFetched},
		{JobID: id, At: st.Add(2 * time.Second), By: domain.ChapterWriteByRedo, Result: domain.ChapterWriteFailed, Error: "ファイルが無い"},
	} {
		if _, err := api.Store.FinishChapterWrite(ctx, w, domain.ChaptersNotAdded); err != nil {
			t.Fatal(err)
		}
	}

	for _, m := range []domain.OutputMove{
		{JobID: id, At: st, From: "/library/局/旧/旧 2026-10-05.m4a", To: "/library/局/新/新 2026-10-05.m4a", Result: domain.OutputMoveTargetExists},
		{JobID: id, At: st.Add(time.Second), From: "/library/a.m4a", Result: domain.OutputMoveNotShowPath},
		{JobID: id, At: st.Add(2 * time.Second), From: "/x/a/a 2026-10-05.m4a", To: "/x/b/b 2026-10-05.m4a", Result: domain.OutputMoveFailed, Error: "書けない"},
		{JobID: id, At: st.Add(3 * time.Second), From: "/x/a/a 2026-10-05.m4a", To: "/x/b/b 2026-10-05.m4a", Result: domain.OutputMoveNoFile},
	} {
		// 移した（moved）でないので、保存先は変わらない。
		if err := api.Store.FinishOutputMove(ctx, m, true, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	code, out, errOut := api.run(t, "jobs", strconv.FormatInt(id, 10))
	if code != 0 {
		t.Fatalf("jobs ID: %d %q", code, errOut)
	}
	for _, want := range []string{
		"保存先の移しの履歴", "移し先に同じ名前のファイルがあるので移さなかった", "/library/局/新/新 2026-10-05.m4a",
		"保存先が番組の名前の形でないので移さなかった", "移せなかった（ファイルは前のまま）", "書けない", "保存先にファイルが無いので移さなかった",
		"番組: 番組A (2)", "状態: done", "試行回数: ", "保存先: /library/a.m4a（欠落 1）", "長さ: 記録なし", "チャプター: 入らなかった",
		"直近のエラー: [fetch] 落ちた 2 行目", "受け付ける操作: ", "放送回", "試行の履歴", "チャプターの書き込みの履歴",
		st.In(domain.JST).Format("2006-01-02 15:04:05"), "入手", "チャプターの取り直し", "12 件を書き込んだ",
		"チャプターの入れ直し", "チャプターを取れなかったので書かなかった", "書き込めなかった（ファイルは前のまま）", "ファイルが無い", "欠落",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}

// 履歴が空なら見出しの下に「なし」を出し、受け付ける操作と入れ直しの待ちが API の印のとおりに出る。
func TestJobsDetailEmptyAndOperations(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	a := domain.Airing{StationID: "TBS", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: "番組B"}
	id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: "番組B", Day: domain.BroadcastDayOf(now), State: domain.StateWaiting,
		Airings: []domain.Airing{a}, NextAttemptAt: now.Add(3 * time.Hour), Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := api.run(t, "jobs", strconv.FormatInt(id, 10))
	if code != 0 {
		t.Fatalf("jobs ID: %d %q", code, errOut)
	}
	if !strings.Contains(out, "受け付ける操作: skip\n") || !strings.Contains(out, "次の試行: ") {
		t.Errorf("waiting の概要:\n%s", out)
	}
	if !strings.Contains(out, "試行の履歴\nなし\n") || !strings.Contains(out, "チャプターの書き込みの履歴\nなし\n") ||
		!strings.HasSuffix(out, "保存先の移しの履歴\nなし\n") {
		t.Errorf("履歴が空のとき:\n%s", out)
	}
	if strings.Contains(out, "チャプター:") || strings.Contains(out, "入れ直しを待っている") {
		t.Errorf("チャプターの行が出ている:\n%s", out)
	}

	// 録音済みのタイムフリーのジョブ: chapters を受け付け、入れ直しを頼むと待ちの文が出る。
	b := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Title: "番組C"}
	done, err := api.Store.InsertJob(ctx, domain.Job{ShowName: "番組C", Day: domain.BroadcastDayOf(now), State: domain.StateDone,
		OutputPath: "/library/c.m4a", Airings: []domain.Airing{b}, NextAttemptAt: now, Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ds := strconv.FormatInt(done, 10)
	_, out, _ = api.run(t, "jobs", ds)
	if !strings.Contains(out, "chapters・move\n") || strings.Contains(out, "入れ直しを待っている") ||
		strings.Contains(out, "移しを待っている") {
		t.Errorf("done の概要:\n%s", out)
	}
	if code, _, errOut := api.run(t, "chapters", ds); code != 0 {
		t.Fatalf("chapters: %d %q", code, errOut)
	}
	_, out, _ = api.run(t, "jobs", ds)
	if !strings.Contains(out, "チャプターの入れ直しを待っている。") {
		t.Errorf("入れ直しの待ち:\n%s", out)
	}
	if code, _, errOut := api.run(t, "move", ds); code != 0 {
		t.Fatalf("move: %d %q", code, errOut)
	}
	_, out, _ = api.run(t, "jobs", ds)
	if !strings.Contains(out, "保存先の移しを待っている。") {
		t.Errorf("移しの待ち:\n%s", out)
	}
}

func TestJobsDetailUsageAndNotFound(t *testing.T) {
	api := newTestAPI(t)
	for _, args := range [][]string{{"jobs", "1", "2"}, {"jobs", "abc"}, {"jobs", "-state", "done", "1"}} {
		if code, _, _ := api.run(t, args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
	code, out, errOut := api.run(t, "jobs", "9999")
	if code != 1 || !strings.Contains(out+errOut, "ジョブ 9999: ID が無い") {
		t.Errorf("無い ID = %d %q %q", code, out, errOut)
	}
}
