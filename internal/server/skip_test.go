package server

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
)

// 人の「録らない」「録る」（POST /api/jobs/{id}/skip・record）と、録らない予定・録らなかったの出し分け（ADR 0019）。

func TestSkipAndRecordJob(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	ctx := context.Background()
	path := func(id int64, op string) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/" + op }
	post := func(id int64, op string, want int) jobSummary {
		t.Helper()
		rec := do(h, "POST", path(id, op), "", ck)
		if rec.Code != want {
			t.Fatalf("%s %d = %d %s, want %d", op, id, rec.Code, rec.Body, want)
		}
		var s jobSummary
		if want == 200 {
			decode(t, rec.Body.Bytes(), &s)
		}
		return s
	}

	// 放送の前の録音予定を「録らない」にすると、録らない予定（確定の前）で人が決めた印が付く。
	id := addJob(t, st, "W", "2026-10-12", 0, domain.StateWaiting, now)
	var d jobDetail
	decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &d)
	if !d.Skippable || d.Recordable || d.Retryable {
		t.Errorf("録音予定の操作 = %+v", d.jobSummary)
	}
	s := post(id, "skip", 200)
	if s.State != "skipped" || s.Settled || !s.ByPerson || !s.Recordable || s.Retryable || s.Skippable {
		t.Errorf("録らないの後 = %+v", s)
	}
	// 確定の前の「録る」は waiting に戻す（人が決めた印のまま）。
	s = post(id, "record", 200)
	if s.State != "waiting" || !s.ByPerson || !s.Skippable {
		t.Errorf("録るの後 = %+v", s)
	}

	// 確定した録らなかったの「録る」は手動再試行と同じく retrying にする。
	settled := addJob(t, st, "S", "2026-10-11", 0, domain.StateSkipped, now)
	if s := post(settled, "record", 200); s.State != "retrying" || !s.NextAttemptAt.Equal(now) {
		t.Errorf("録らなかったの録る = %+v", s)
	}
	// 録音期限を過ぎた録らなかったは 409。
	late := addJobOf(t, st, domain.Job{ShowName: "L", Day: mustDay(t, "2026-10-01"), State: domain.StateSkipped, Settled: true,
		Deadline: now.Add(-time.Hour)})
	post(late, "record", 409)

	// 受け付けない状態は 409 で、状態を変えない。
	for _, c := range []struct {
		op string
		st domain.State
	}{
		{"skip", domain.StateRunning}, {"skip", domain.StateRetrying}, {"skip", domain.StateDone},
		{"skip", domain.StateExpired}, {"skip", domain.StateSkipped},
		{"record", domain.StateWaiting}, {"record", domain.StateRunning}, {"record", domain.StateDone}, {"record", domain.StateExpired},
	} {
		id := addJob(t, st, "X"+c.op+string(c.st), "2026-10-12", 0, c.st, now)
		post(id, c.op, 409)
		if j, _ := st.GetJob(ctx, id); j.State != c.st || j.ByPerson {
			t.Errorf("%s を %s で拒んだのに %+v", c.st, c.op, j)
		}
	}
	// 試行のある waiting も「録らない」にできない。
	tried := addJobOf(t, st, domain.Job{ShowName: "T", Day: mustDay(t, "2026-10-12"), State: domain.StateWaiting, Attempts: 1,
		NextAttemptAt: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour)})
	post(tried, "skip", 409)
	post(9999, "skip", 404)
	post(9999, "record", 404)
	if rec := do(h, "GET", path(id, "skip"), "", ck); rec.Code == 200 {
		t.Errorf("GET で録らないにできた")
	}
}

// guideSkipResp は番組表の API の、録らない予定・録らなかったに関わる項目。
type guideSkipResp struct {
	Stations []struct {
		Airings []struct {
			Title     string `json:"title"`
			State     string `json:"state"`
			JobID     int64  `json:"jobId"`
			SkipBy    string `json:"skipBy"`
			Skippable bool   `json:"skippable"`
		} `json:"airings"`
	} `json:"stations"`
}

// 番組表では、録らない予定と録らなかったを出し分け、誰が止めたかと「録らない」を受け付けるかを付ける。
func TestGuideSkipStates(t *testing.T) {
	e := newGuideUI(t)
	ctx := context.Background()
	now := e.ui.now() // 2026-10-07（水）の 12 時
	thu := func(h int) domain.Weekly { return domain.NewWeekly("TBS", time.Thursday, domain.Clock{Hour: h}) }
	if err := e.store.CreateShow(ctx, domain.NewShow("木曜の番組", thu(8), thu(9), thu(10), thu(11))); err != nil {
		t.Fatal(err)
	}
	air := func(d, h int, title string) domain.Airing {
		return domain.Airing{StationID: "TBS", Start: jst(2026, 10, d, h, 0), End: jst(2026, 10, d, h+1, 0), Title: title}
	}
	e.area.stations["2026-10-08"] = []app.AreaStation{{ID: "TBS", Name: "TBSラジオ", Airings: []domain.Airing{
		air(8, 8, "録らない予定のジョブ"), air(8, 9, "人が止めたジョブ"), air(8, 10, "特番"), air(8, 11, "木曜の番組"),
	}}}
	day := mustDay(t, "2026-10-08")
	planned := addJobOf(t, e.store, domain.Job{ShowName: "木曜の番組", Day: day, Part: 1, State: domain.StateSkipped,
		Airings: []domain.Airing{air(8, 8, "")}, NextAttemptAt: now.Add(24 * time.Hour), Deadline: now.Add(8 * 24 * time.Hour)})
	person := addJobOf(t, e.store, domain.Job{ShowName: "木曜の番組", Day: day, Part: 2, State: domain.StateSkipped, ByPerson: true,
		Airings: []domain.Airing{air(8, 9, "")}, NextAttemptAt: now.Add(25 * time.Hour), Deadline: now.Add(8 * 24 * time.Hour)})

	var got guideSkipResp
	e.call("GET", "/api/guide?day=2026-10-08", "", 200, &got)
	type want struct {
		state, by string
		job       int64
	}
	wants := map[string]want{
		"録らない予定のジョブ": {"skip_planned", "title", planned},
		"人が止めたジョブ":   {"skip_planned", "person", person},
		"特番":         {"skip_planned", "title", 0}, // ジョブが無くても、放送の前なら録らない予定
		"木曜の番組":      {"planned", "", 0},
	}
	for _, a := range got.Stations[0].Airings {
		if w := wants[a.Title]; a.State != w.state || a.SkipBy != w.by || a.JobID != w.job || a.Skippable {
			t.Errorf("%s = %+v, want %+v", a.Title, a, w)
		}
	}

	// 確定したジョブと、ジョブが無く放送の後で番組名の食い違う放送回は録らなかった。録音予定のジョブ（試行が無い waiting）は
	// 「録らない」を受け付ける。
	early := domain.NewShow("水曜の早朝", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 5, Minute: 30}))
	if err := e.store.CreateShow(ctx, early); err != nil {
		t.Fatal(err)
	}
	e.area.stations["2026-10-07"][0].Airings = append(e.area.stations["2026-10-07"][0].Airings, air(7, 13, "確定したジョブ"),
		domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 5, 30), End: jst(2026, 10, 7, 5, 45), Title: "早朝の特番"})
	settled := addJobOf(t, e.store, domain.Job{ShowName: "木曜の番組", Day: mustDay(t, "2026-10-07"), State: domain.StateSkipped,
		Settled: true, Airings: []domain.Airing{air(7, 13, "")}, NextAttemptAt: now, Deadline: now.Add(7 * 24 * time.Hour)})
	got = guideSkipResp{}
	e.call("GET", "/api/guide?day=2026-10-07", "", 200, &got)
	seen := map[string]bool{}
	for _, a := range got.Stations[0].Airings {
		seen[a.Title] = true
		switch a.Title {
		case "確定したジョブ":
			if a.State != "skipped" || a.SkipBy != "title" || a.JobID != settled {
				t.Errorf("確定したジョブ = %+v", a)
			}
		case "早朝の特番":
			if a.State != "skipped" || a.SkipBy != "title" || a.JobID != 0 {
				t.Errorf("早朝の特番 = %+v", a)
			}
		case "朝の番組": // ジョブ waiting
			if a.State != "planned" || !a.Skippable {
				t.Errorf("朝の番組 = %+v", a)
			}
		}
	}
	if !seen["確定したジョブ"] || !seen["早朝の特番"] {
		t.Errorf("放送回が足りない: %+v", got)
	}
}
