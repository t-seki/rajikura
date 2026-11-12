package server

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// チャプターの入れ直し（ADR 0038）。ジョブ 1 件の指定（POST /api/jobs/{id}/chapters）は、タイムフリーから録った done のジョブを
// チャプターの値と録音期限によらず受け付け、頼みを DB に書いて録音のループを起こし、要約を返す（状態・試行回数は変えない）。
// done でない・ライブ録音は文言を分けて 409、無いジョブは 404 で、どれも起こさない。要約の chaptersRedoable は受け付けるジョブだけ
// true、chaptersRedoRequested は頼んでまだ書き込んでいない間だけ true。まとめて（POST /api/jobs/chapters）は、チャプターの値が
// 値なしか入らなかったのジョブにだけ頼みを書き、受け付けた数を返す。
func TestRedoChapters(t *testing.T) {
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
	rec := &countingRecorder{}
	h := Handler(Health{LastPoll: time.Now}, &UI{
		Sessions: st, Jobs: st, Passwords: passwords(string(hash)), Recorder: rec,
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return now },
	})
	ck := login(t, h)
	ctx := context.Background()
	path := func(id int64) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/chapters" }
	detail := func(id int64) jobDetail {
		t.Helper()
		var d jobDetail
		decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &d)
		return d
	}
	// 録音期限を過ぎた放送回（録り直しは断るが、チャプターの入れ直しは受け付ける）。
	old := domain.Airing{StationID: "TBS", Start: now.Add(-40 * 24 * time.Hour), End: now.Add(-40*24*time.Hour + time.Hour)}
	live := old
	live.TimeFree.InNG = 2
	add := func(name string, state domain.State, ch domain.Chapters, a domain.Airing) int64 {
		return addJobOf(t, st, domain.Job{ShowName: name, Day: mustDay(t, "2026-09-02"), State: state, Airings: []domain.Airing{a},
			Attempts: 2, NextAttemptAt: now.Add(-3 * time.Hour), Deadline: now.Add(-time.Hour), OutputPath: "/lib/" + name + ".m4a", Chapters: ch})
	}

	added := add("added", domain.StateDone, domain.ChaptersAdded, old)
	if d := detail(added); !d.ChaptersRedoable || d.ChaptersRedoRequested || d.Rerecordable {
		t.Errorf("done の詳細: chaptersRedoable=%v chaptersRedoRequested=%v rerecordable=%v", d.ChaptersRedoable, d.ChaptersRedoRequested, d.Rerecordable)
	}
	r := do(h, "POST", path(added), "", ck)
	var sum jobSummary
	decode(t, r.Body.Bytes(), &sum)
	if r.Code != 200 || sum.State != "done" || sum.Attempts != 2 || !sum.ChaptersRedoable || !sum.ChaptersRedoRequested || sum.Chapters != "added" {
		t.Errorf("チャプターの入れ直し = %d %s", r.Code, r.Body)
	}
	if rec.n.Load() != 1 {
		t.Errorf("起こした数 = %d, want 1", rec.n.Load())
	}
	if j, err := st.GetJob(ctx, added); err != nil || !j.ChapterRedoAt.Equal(now) {
		t.Errorf("頼みの時刻 = %+v, %v", j, err)
	}

	refusals := []struct {
		name string
		id   int64
		msg  string
	}{
		{"expired", add("expired", domain.StateExpired, domain.ChaptersNone, old), "この状態ではチャプターを入れ直せない（入れ直せるのは録音済みのジョブだけ）"},
		{"ライブ録音", add("live", domain.StateDone, domain.ChaptersNone, live), "ライブ録音のジョブにはチャプターを入れ直せない"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if d := detail(tt.id); d.ChaptersRedoable {
				t.Errorf("chaptersRedoable = true")
			}
			r := do(h, "POST", path(tt.id), "", ck)
			var body struct{ Error string }
			decode(t, r.Body.Bytes(), &body)
			if r.Code != 409 || body.Error != tt.msg {
				t.Errorf("チャプターの入れ直し = %d %q, want 409 %q", r.Code, body.Error, tt.msg)
			}
			if j, err := st.GetJob(ctx, tt.id); err != nil || !j.ChapterRedoAt.IsZero() {
				t.Errorf("拒んだ後: %+v, %v", j, err)
			}
		})
	}
	if r := do(h, "POST", path(9999), "", ck); r.Code != 404 {
		t.Errorf("無いジョブのチャプターの入れ直し = %d", r.Code)
	}
	if r := do(h, "POST", path(added), ""); r.Code != 401 {
		t.Errorf("ログインしない入れ直し = %d, want 401", r.Code)
	}
	if rec.n.Load() != 1 {
		t.Errorf("拒んだのに起こした: 起こした数 = %d", rec.n.Load())
	}

	// まとめて: 値なし・入らなかったの 2 件だけ（入った・待っている・ライブ録音・done でないは飛ばす）。
	add("none", domain.StateDone, domain.ChaptersNone, old)
	add("not_added", domain.StateDone, domain.ChaptersNotAdded, old)
	add("waiting", domain.StateDone, domain.ChaptersWaiting, old)
	var all struct {
		Accepted *int `json:"accepted"`
	}
	r = do(h, "POST", "/api/jobs/chapters", "", ck)
	decode(t, r.Body.Bytes(), &all)
	if r.Code != 200 || all.Accepted == nil || *all.Accepted != 2 {
		t.Fatalf("まとめての入れ直し = %d %s", r.Code, r.Body)
	}
	if rec.n.Load() != 2 {
		t.Errorf("起こした数 = %d, want 2", rec.n.Load())
	}
	if r := do(h, "POST", "/api/jobs/chapters", ""); r.Code != 401 {
		t.Errorf("ログインしないまとめての入れ直し = %d, want 401", r.Code)
	}
}
