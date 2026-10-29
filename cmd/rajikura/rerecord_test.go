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

// rerecord: done のタイムフリーのジョブは retrying・試行回数 0 になる。done でない・ライブ録音・録音期限を過ぎた・無い ID は
// わけを添えて拒むが、ほかの ID は続け、1 件でも拒んだら終了コード 1。ID が無い・整数でないのは使い方の誤りで 2（ADR 0025）。
func TestRerecord(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	add := func(name string, state domain.State, inNG int, deadline time.Time) string {
		t.Helper()
		a := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Title: name}
		a.TimeFree.InNG = inNG
		id, err := api.Store.InsertJob(ctx, domain.Job{ShowName: name, Day: domain.BroadcastDayOf(now), State: state,
			Airings: []domain.Airing{a}, Attempts: 2, NextAttemptAt: now.Add(-time.Hour), Deadline: deadline, OutputPath: "/library/" + name + ".m4a"})
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(id, 10)
	}
	ok := add("録音済み", domain.StateDone, 0, now.Add(24*time.Hour))
	expired := add("期限切れ", domain.StateExpired, 0, now.Add(24*time.Hour))
	live := add("ライブ", domain.StateDone, 2, now.Add(24*time.Hour))
	late := add("録音期限切れ", domain.StateDone, 0, now.Add(-time.Minute))

	code, out, stderr := api.run(t, "rerecord", expired, live, "99999", late, ok)
	if code != 1 {
		t.Fatalf("rerecord: %d %q %q", code, out, stderr)
	}
	for _, want := range []string{
		fmt.Sprintf("ジョブ %s: 拒んだ（状態が expired で、録り直せるのは done（録音済み）だけ）", expired),
		fmt.Sprintf("ジョブ %s: 拒んだ（ライブ録音のジョブで、ライブ録音は録り直せない）", live),
		"ジョブ 99999: 拒んだ（ID が無い）",
		fmt.Sprintf("ジョブ %s: 拒んだ（録音期限を過ぎている）", late),
		fmt.Sprintf("ジョブ %s: 録り直しにした", ok),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い: %q", want, out)
		}
	}
	if !strings.Contains(stderr, "4 件のジョブを録り直しにできなかった") {
		t.Errorf("stderr = %q", stderr)
	}
	id, _ := strconv.ParseInt(ok, 10, 64)
	j, err := api.Store.GetJob(ctx, id)
	if err != nil || j.State != domain.StateRetrying || j.Attempts != 0 || time.Since(j.NextAttemptAt) > time.Minute || j.OutputPath != "/library/録音済み.m4a" {
		t.Errorf("rerecord の後: %+v, %v", j, err)
	}

	if code, _, stderr := api.run(t, "rerecord"); code != 2 || !strings.Contains(stderr, "使い方: rajikura rerecord <ID>") {
		t.Errorf("rerecord（ID なし）: %d %q", code, stderr)
	}
	if code, _, stderr := api.run(t, "rerecord", "x"); code != 2 || !strings.Contains(stderr, `ID "x" は整数ではない`) {
		t.Errorf("rerecord（整数でない ID）: %d %q", code, stderr)
	}
}
