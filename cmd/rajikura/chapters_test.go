package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// chapters: ID を渡すと、タイムフリーから録った done のジョブのチャプターの入れ直しを頼む（チャプターが入ったジョブも）。done でない・
// ライブ録音・無い ID はわけを添えて拒むが、ほかの ID は続け、1 件でも拒んだら終了コード 1。-all は、チャプターの値が値なしか
// 入らなかったのジョブにだけ頼み、受け付けた数を出す。ID が無い・整数でない・-all と ID を一緒に渡すのは使い方の誤りで 2（ADR 0033）。
func TestChapters(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	add := func(name string, state domain.State, inNG int, ch domain.Chapters) string {
		t.Helper()
		// 録音期限を過ぎた古い放送回（チャプターの入れ直しは録音期限を問わない）。
		a := domain.Airing{StationID: "TBS", Start: now.Add(-40 * 24 * time.Hour), End: now.Add(-40*24*time.Hour + time.Hour), Title: name}
		a.TimeFree.InNG = inNG
		id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: name, Day: domain.BroadcastDayOf(a.Start), State: state,
			Airings: []domain.Airing{a}, Attempts: 2, NextAttemptAt: now.Add(-time.Hour), Deadline: now.Add(-time.Hour),
			OutputPath: "/library/" + name + ".m4a", Chapters: ch})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(id, 10)
	}
	redoAt := func(id string) time.Time {
		t.Helper()
		n, _ := strconv.ParseInt(id, 10, 64)
		j, err := api.Store.GetJob(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		return j.ChapterRedoAt
	}
	added := add("入った", domain.StateDone, 0, domain.ChaptersAdded)
	expired := add("期限切れ", domain.StateExpired, 0, domain.ChaptersNone)
	live := add("ライブ", domain.StateDone, 2, domain.ChaptersNone)

	code, out, stderr := api.run(t, "chapters", expired, live, "99999", added)
	if code != 1 {
		t.Fatalf("chapters: %d %q %q", code, out, stderr)
	}
	for _, want := range []string{
		fmt.Sprintf("ジョブ %s: 拒んだ（状態が expired で、チャプターを入れ直せるのは done（録音済み）だけ）", expired),
		fmt.Sprintf("ジョブ %s: 拒んだ（ライブ録音のジョブで、ライブ録音のファイルにはチャプターを入れない）", live),
		"ジョブ 99999: 拒んだ（ID が無い）",
		fmt.Sprintf("ジョブ %s: チャプターの入れ直しを受け付けた", added),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い: %q", want, out)
		}
	}
	if !strings.Contains(stderr, "3 件のジョブをチャプターの入れ直しにできなかった") {
		t.Errorf("stderr = %q", stderr)
	}
	if redoAt(added).IsZero() || !redoAt(expired).IsZero() || !redoAt(live).IsZero() {
		t.Errorf("頼みの印: added %v, expired %v, live %v", redoAt(added), redoAt(expired), redoAt(live))
	}

	// -all: 値なし・入らなかったのタイムフリーの done だけ。
	none := add("値なし", domain.StateDone, 0, domain.ChaptersNone)
	notAdded := add("入らなかった", domain.StateDone, 0, domain.ChaptersNotAdded)
	waiting := add("待っている", domain.StateDone, 0, domain.ChaptersWaiting)
	code, out, stderr = api.run(t, "chapters", "-all")
	if code != 0 || !strings.Contains(out, "チャプターの入れ直しを 2 件のジョブで受け付けた") {
		t.Fatalf("chapters -all: %d %q %q", code, out, stderr)
	}
	if redoAt(none).IsZero() || redoAt(notAdded).IsZero() || !redoAt(waiting).IsZero() || !redoAt(live).IsZero() {
		t.Errorf("-all の頼みの印: none %v, not_added %v, waiting %v, live %v", redoAt(none), redoAt(notAdded), redoAt(waiting), redoAt(live))
	}
	if code, out, _ := api.run(t, "chapters", "--all"); code != 0 || !strings.Contains(out, "2 件") {
		t.Errorf("chapters --all: %d %q", code, out)
	}

	for _, args := range [][]string{{"chapters"}, {"chapters", "-all", added}} {
		if code, _, stderr := api.run(t, args...); code != 2 || !strings.Contains(stderr, "使い方: rajikura chapters <ID>") {
			t.Errorf("%q: %d %q", args, code, stderr)
		}
	}
	if code, _, stderr := api.run(t, "chapters", "x"); code != 2 || !strings.Contains(stderr, `ID "x" は整数ではない`) {
		t.Errorf("chapters（整数でない ID）: %d %q", code, stderr)
	}
}
