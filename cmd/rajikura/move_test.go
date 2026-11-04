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

// move: ID を渡すと、保存先のあるジョブの保存先の移しを頼む（ADR 0039）。-show を渡すと、ジョブをその番組に付け替えてから頼む。保存先の
// 無いジョブ・録音中のジョブ・無い ID・無い番組はわけを添えて拒むが、ほかの ID は続け、1 件でも拒んだら終了コード 1。ID が無い・整数で
// ないのは使い方の誤りで 2。
func TestMove(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	if err := api.Store.PutShow(ctx, domain.NewShow("今の番組", domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: 8}))); err != nil {
		t.Fatal(err)
	}
	add := func(name string, state domain.State, output string) string {
		t.Helper()
		a := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Title: name}
		id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: name, Day: domain.BroadcastDayOf(a.Start), State: state,
			Airings: []domain.Airing{a}, NextAttemptAt: now, Deadline: now.Add(24 * time.Hour), OutputPath: output})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(id, 10)
	}
	get := func(id string) domain.Job {
		t.Helper()
		n, _ := strconv.ParseInt(id, 10, 64)
		j, err := api.Store.GetJob(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	done := add("古い名前", domain.StateDone, "/library/TBS/古い名前/古い名前 2026-10-05.m4a")
	waiting := add("まだ", domain.StateWaiting, "")
	running := add("録音中", domain.StateRunning, "/library/TBS/録音中/録音中 2026-10-05.m4a")

	code, out, stderr := api.run(t, "move", "-show", "今の番組", waiting, running, "99999", done)
	if code != 1 {
		t.Fatalf("move: %d %q %q", code, out, stderr)
	}
	for _, want := range []string{
		fmt.Sprintf("ジョブ %s: 拒んだ（まだ保存していないジョブで、保存先を移せるのは保存先のあるジョブだけ）", waiting),
		fmt.Sprintf("ジョブ %s: 拒んだ（状態が running で、録音中のジョブは保存先を移せない（録音が終わってから頼む））", running),
		"ジョブ 99999: 拒んだ（ID が無い）",
		fmt.Sprintf("ジョブ %s: 保存先の移しを受け付けた", done),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い: %q", want, out)
		}
	}
	if !strings.Contains(stderr, "3 件のジョブを保存先の移しにできなかった") {
		t.Errorf("stderr = %q", stderr)
	}
	if j := get(done); j.ShowName != "今の番組" || j.MoveRequestedAt.IsZero() || j.OutputPath != "/library/TBS/古い名前/古い名前 2026-10-05.m4a" {
		t.Errorf("受け付けたジョブ = %+v（今の番組に付け替わり、頼みの印が付き、保存先はまだ変わらない）", j)
	}
	if j := get(running); j.ShowName != "録音中" || !j.MoveRequestedAt.IsZero() {
		t.Errorf("拒んだジョブを変えた: %+v", j)
	}

	// 無い番組は断り、ジョブを変えない。-show を付けなければジョブの今の番組のまま頼む。
	other := add("別の名前", domain.StateExpired, "/library/TBS/別の名前/別の名前 2026-10-05.m4a")
	code, out, _ = api.run(t, "move", "-show", "無い番組", other)
	if code != 1 || !strings.Contains(out, fmt.Sprintf("ジョブ %s: 拒んだ（番組「無い番組」が無い）", other)) {
		t.Errorf("無い番組: %d %q", code, out)
	}
	if j := get(other); j.ShowName != "別の名前" || !j.MoveRequestedAt.IsZero() {
		t.Errorf("無い番組で変えた: %+v", j)
	}
	if code, out, stderr := api.run(t, "move", other); code != 0 || !strings.Contains(out, "保存先の移しを受け付けた") {
		t.Fatalf("move（-show なし）: %d %q %q", code, out, stderr)
	}
	if j := get(other); j.ShowName != "別の名前" || j.MoveRequestedAt.IsZero() {
		t.Errorf("-show なしで受け付けたジョブ = %+v", j)
	}

	for _, args := range [][]string{{"move"}, {"move", "abc"}, {"move", "-show"}} {
		if code, _, _ := api.run(t, args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
}
