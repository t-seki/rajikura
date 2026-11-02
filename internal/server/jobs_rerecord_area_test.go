package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// newRerecordAreaUI は、会員種別 m と、自分のエリアの番組表 area を持つ UI を作る（録り直しが今の自分のエリアの局一覧を使うかの確かめ用）。
// 今は 2026-10-12 12:00。
func newRerecordAreaUI(t *testing.T, m domain.Membership, area *fakeAreaGuide) (http.Handler, *store.Store, *http.Cookie, time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, domain.JST)
	ui := &UI{
		Sessions: st, Jobs: st, AreaGuide: area, Membership: m, Passwords: passwords(string(hash)),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	return h, st, login(t, h), now
}

// 録り直しの可否（一覧・詳細の rerecordable と、POST /api/jobs/{id}/rerecord）は、今の自分のエリアの局一覧でも決める（ADR 0036）。
// 録ったときは自分のエリアの局（印が立っていない）で、今はエリアの外の局になった放送回は、ダブルプランで tsplus_out_ng が 0 でなければ、
// 放送日から 8 日目以降は録り直せない。局一覧を取れないときも録り直せない（409。ジョブは done のまま）。
func TestRerecordUsesOwnArea(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, domain.JST)
	today := domain.BroadcastDayOf(now).String()
	stations := func(ids ...string) map[string][]app.AreaStation {
		var ss []app.AreaStation
		for _, id := range ids {
			ss = append(ss, app.AreaStation{ID: id})
		}
		return map[string][]app.AreaStation{today: ss}
	}
	// 放送日 10/1 の TBS。7 日の録音期限は 10/9 05:00（過ぎている）、30 日なら 10/31 の終わり。
	tbs := domain.Airing{StationID: "TBS", Start: time.Date(2026, 10, 1, 0, 0, 0, 0, domain.JST), End: time.Date(2026, 10, 1, 1, 0, 0, 0, domain.JST)}
	tbs.TimeFree.PlusOutNG = 1

	tests := []struct {
		name    string
		m       domain.Membership
		area    *fakeAreaGuide
		ok      bool   // 録り直せる
		msg     string // 録り直せないときの 409 の文言
		wantAsk bool   // 局一覧を取りにいくか
	}{
		{"ダブルプラン・今もエリアの中", domain.MembershipDouble, &fakeAreaGuide{stations: stations("TBS")}, true, "", true},
		{"ダブルプラン・今はエリアの外", domain.MembershipDouble, &fakeAreaGuide{stations: stations("QRR")}, false, "録音期限を過ぎているので録り直せない", true},
		{"エリアフリー・今はエリアの外", domain.MembershipAreaFree, &fakeAreaGuide{stations: stations("QRR")}, false, "録音期限を過ぎているので録り直せない", true},
		{"ダブルプラン・局一覧を取れない", domain.MembershipDouble, &fakeAreaGuide{fail: true}, false, "自分のエリアの局一覧を取れないので、いまは録り直せない（後でやり直す）", true},
		{"ダブルプラン・局一覧が空", domain.MembershipDouble, &fakeAreaGuide{stations: stations()}, false, "自分のエリアの局一覧を取れないので、いまは録り直せない（後でやり直す）", true},
		// 局一覧を使わない会員種別は、取りにいかない（取れなくても録り直せる）。
		{"タイムフリー30・局一覧を取りにいかない", domain.MembershipTimeFree30, &fakeAreaGuide{fail: true}, true, "", false},
		{"非会員・局一覧を取りにいかない", domain.MembershipFree, &fakeAreaGuide{fail: true}, false, "録音期限を過ぎているので録り直せない", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, st, ck, now := newRerecordAreaUI(t, tt.m, tt.area)
			id := addJobOf(t, st, domain.Job{ShowName: "x", Day: mustDay(t, "2026-10-01"), State: domain.StateDone, Airings: []domain.Airing{tbs},
				Attempts: 2, NextAttemptAt: now.Add(-24 * time.Hour), Deadline: time.Date(2026, 10, 9, 5, 0, 0, 0, domain.JST), OutputPath: "/lib/x.m4a"})
			idStr := strconv.FormatInt(id, 10)

			var d jobDetail
			decode(t, do(h, "GET", "/api/jobs/"+idStr, "", ck).Body.Bytes(), &d)
			var list struct{ Jobs []jobSummary }
			decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &list)
			if len(list.Jobs) != 1 || list.Jobs[0].Rerecordable != tt.ok || d.Rerecordable != tt.ok {
				t.Errorf("rerecordable: 一覧 %+v, 詳細 %v, want %v", list.Jobs, d.Rerecordable, tt.ok)
			}
			if asked := len(tt.area.asked) > 0; asked != tt.wantAsk {
				t.Errorf("局一覧を取りにいったか = %v, want %v", asked, tt.wantAsk)
			}
			if tt.wantAsk && len(tt.area.asked) != 2 {
				t.Errorf("局一覧を取りにいった回数 = %d, want 2（詳細と一覧で 1 回ずつ）", len(tt.area.asked))
			}

			rec := do(h, "POST", "/api/jobs/"+idStr+"/rerecord", "", ck)
			if tt.ok {
				var sum jobSummary
				decode(t, rec.Body.Bytes(), &sum)
				if rec.Code != 200 || sum.State != "retrying" {
					t.Fatalf("録り直し = %d %s", rec.Code, rec.Body)
				}
				return
			}
			var body struct{ Error string }
			decode(t, rec.Body.Bytes(), &body)
			if rec.Code != 409 || body.Error != tt.msg {
				t.Errorf("録り直し = %d %q, want 409 %q", rec.Code, body.Error, tt.msg)
			}
			if j, err := st.GetJob(context.Background(), id); err != nil || j.State != domain.StateDone || j.Attempts != 2 {
				t.Errorf("断った後: %+v, %v", j, err)
			}
		})
	}
}
