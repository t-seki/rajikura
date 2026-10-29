package server

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 録り直し（POST /api/jobs/{id}/rerecord）は、done のタイムフリーから録るジョブで録音期限の内側のものを retrying・試行回数 0・
// 次の試行を今にし、保存先は変えない。done でない・ライブ録音・録音期限を過ぎた、は文言を分けて 409 にし、状態を変えない。
// 無いジョブは 404。要約の rerecordable は、受け付けるジョブだけ true（ADR 0025）。
func TestRerecord(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	ctx := context.Background()
	path := func(id int64) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/rerecord" }
	detail := func(id int64) jobDetail {
		t.Helper()
		var d jobDetail
		decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &d)
		return d
	}
	timefree := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)}
	live := timefree
	live.TimeFree.InNG = 2
	add := func(name string, state domain.State, deadline time.Time, a domain.Airing) int64 {
		return addJobOf(t, st, domain.Job{ShowName: name, Day: mustDay(t, "2026-10-12"), State: state, Airings: []domain.Airing{a},
			Attempts: 2, NextAttemptAt: now.Add(-3 * time.Hour), Deadline: deadline, OutputPath: "/lib/" + name + ".m4a"})
	}

	ok := add("ok", domain.StateDone, now.Add(time.Hour), timefree)
	if d := detail(ok); !d.Rerecordable || d.Retryable {
		t.Errorf("done の詳細: rerecordable=%v retryable=%v", d.Rerecordable, d.Retryable)
	}
	rec := do(h, "POST", path(ok), "", ck)
	var sum jobSummary
	decode(t, rec.Body.Bytes(), &sum)
	if rec.Code != 200 || sum.State != "retrying" || sum.Attempts != 0 || !sum.NextAttemptAt.Equal(now) || sum.OutputPath != "/lib/ok.m4a" || sum.Rerecordable {
		t.Errorf("録り直し = %d %s", rec.Code, rec.Body)
	}

	refusals := []struct {
		name  string
		id    int64
		state domain.State
		msg   string
	}{
		{"expired", add("expired", domain.StateExpired, now.Add(time.Hour), timefree), domain.StateExpired, "この状態では録り直せない（録り直せるのは録音済みのジョブだけ）"},
		{"ライブ録音", add("live", domain.StateDone, now.Add(time.Hour), live), domain.StateDone, "ライブ録音は録り直せない"},
		{"録音期限を過ぎた", add("late", domain.StateDone, now, timefree), domain.StateDone, "録音期限を過ぎているので録り直せない"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if d := detail(tt.id); d.Rerecordable {
				t.Errorf("rerecordable = true")
			}
			rec := do(h, "POST", path(tt.id), "", ck)
			var body struct{ Error string }
			decode(t, rec.Body.Bytes(), &body)
			if rec.Code != 409 || body.Error != tt.msg {
				t.Errorf("録り直し = %d %q, want 409 %q", rec.Code, body.Error, tt.msg)
			}
			if j, err := st.GetJob(ctx, tt.id); err != nil || j.State != tt.state || j.Attempts != 2 {
				t.Errorf("拒んだ後: %+v, %v", j, err)
			}
		})
	}
	if rec := do(h, "POST", path(9999), "", ck); rec.Code != 404 {
		t.Errorf("無いジョブの録り直し = %d", rec.Code)
	}
	if rec := do(h, "POST", path(ok), ""); rec.Code != 401 {
		t.Errorf("ログインしない録り直し = %d, want 401", rec.Code)
	}
}
