package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/server"
)

// failingAreaGuide は、自分のエリアの番組表をいつも取れない AreaGuide。
type failingAreaGuide struct{}

func (failingAreaGuide) AreaAirings(context.Context, domain.BroadcastDay) ([]app.AreaStation, error) {
	return nil, errors.New("落ちた")
}

func (failingAreaGuide) AreaAiringsOf(context.Context, string, domain.BroadcastDay) ([]app.AreaStation, error) {
	return nil, errors.New("落ちた")
}

func (failingAreaGuide) Areas(context.Context) (app.AreaInfo, error) {
	return app.AreaInfo{}, errors.New("落ちた")
}

// rerecord: エリアフリー・ダブルプランで自分のエリアの局一覧を取れずに拒まれたときは、「録音期限を過ぎている」ではなく、その理由を添える。
func TestRerecordAreaUnknown(t *testing.T) {
	api := newTestAPIWith(t, func(ui *server.UI) {
		ui.AreaGuide = failingAreaGuide{}
		ui.Membership = domain.MembershipDouble
	})
	now := time.Now()
	a := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)}
	id, err := api.Store.InsertJob(context.Background(), domain.Job{ShowName: "x", Day: domain.BroadcastDayOf(now), State: domain.StateDone,
		Airings: []domain.Airing{a}, Attempts: 2, NextAttemptAt: now.Add(-time.Hour), Deadline: now.Add(24 * time.Hour), OutputPath: "/library/x.m4a"})
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := api.run(t, "rerecord", strconv.FormatInt(id, 10))
	want := fmt.Sprintf("ジョブ %d: 拒んだ（自分のエリアの局一覧を取れないので、いまは録り直せない。後でやり直す）", id)
	if code != 1 || !strings.Contains(out, want) {
		t.Errorf("rerecord: %d %q %q, want %q", code, out, stderr, want)
	}
}

// rerecord: done のタイムフリーのジョブは retrying・試行回数 0 になる。done でない・ライブ録音・録音期限を過ぎた・無い ID は
// わけを添えて拒むが、ほかの ID は続け、1 件でも拒んだら終了コード 1。ID が無い・整数でないのは使い方の誤りで 2（ADR 0025）。
func TestRerecord(t *testing.T) {
	api := newTestAPI(t)
	ctx := context.Background()
	now := time.Now()
	add := func(name string, state domain.State, inNG int, deadline time.Time) string {
		t.Helper()
		a := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Title: name}
		if deadline.Before(now) {
			// 録音期限は放送回と会員種別（非会員）から決まるので、7 日を過ぎた放送回にする。
			a.Start, a.End = now.Add(-9*24*time.Hour), now.Add(-9*24*time.Hour+time.Hour)
		}
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
