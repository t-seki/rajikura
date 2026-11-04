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

// 保存先の移し（ADR 0039。POST /api/jobs/{id}/move）。保存先のあるジョブを状態によらず受け付け、頼みを DB に書いて録音のループを起こし、
// 要約を返す（状態・保存先は変えない）。body の show で今ある番組を指定すると、ジョブをその番組に付け替える。保存先の無い・録音中は文言を
// 分けて 409、無いジョブ・無い番組は 404 で、どれも起こさず何も変えない。要約の movable は受け付けるジョブだけ true、moveRequested は
// 頼んでまだ移していない間だけ true。詳細に保存先の移しの履歴（outputMoves）が移した順に出る（無ければ空の配列）。
func TestMoveJob(t *testing.T) {
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
	if err := st.PutShow(ctx, domain.NewShow("今の番組", domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: 8}))); err != nil {
		t.Fatal(err)
	}
	path := func(id int64) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/move" }
	detail := func(id int64) jobDetail {
		t.Helper()
		var d jobDetail
		decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &d)
		return d
	}
	a := domain.Airing{StationID: "TBS", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)}
	add := func(state domain.State, output string) int64 {
		return addJobOf(t, st, domain.Job{ShowName: "古い名前", Day: mustDay(t, "2026-10-12"), State: state, Airings: []domain.Airing{a},
			Attempts: 2, NextAttemptAt: now.Add(-time.Hour), Deadline: now.Add(24 * time.Hour), OutputPath: output})
	}
	const output = "/lib/TBS/古い名前/古い名前 2026-10-12.m4a"

	done := add(domain.StateDone, output)
	if d := detail(done); !d.Movable || d.MoveRequested || d.OutputMoves == nil || len(d.OutputMoves) != 0 {
		t.Errorf("done の詳細: movable=%v moveRequested=%v outputMoves=%#v", d.Movable, d.MoveRequested, d.OutputMoves)
	}
	r := do(h, "POST", path(done), `{"show":"今の番組"}`, ck)
	var sum jobSummary
	decode(t, r.Body.Bytes(), &sum)
	if r.Code != 200 || sum.State != "done" || sum.Attempts != 2 || sum.ShowName != "今の番組" || !sum.MoveRequested ||
		sum.OutputPath != output {
		t.Errorf("保存先の移し = %d %s", r.Code, r.Body)
	}
	if rec.n.Load() != 1 {
		t.Errorf("起こした数 = %d, want 1", rec.n.Load())
	}
	// body が無ければ、ジョブの今の番組のまま頼む。
	expired := add(domain.StateExpired, output)
	if r := do(h, "POST", path(expired), "", ck); r.Code != 200 {
		t.Errorf("body の無い保存先の移し = %d %s", r.Code, r.Body)
	}
	if j, err := st.GetJob(ctx, expired); err != nil || j.ShowName != "古い名前" || !j.MoveRequestedAt.Equal(now) {
		t.Errorf("body の無い保存先の移しの後 = %+v, %v", j, err)
	}

	for _, tt := range []struct {
		name, body string
		id         int64
		code       int
		msg        string
	}{
		{"保存先が無い", "", add(domain.StateWaiting, ""), 409, "まだ保存していないジョブは、保存先を移せない"},
		{"録音中", "", add(domain.StateRunning, output), 409, "録音中のジョブは、保存先を移せない（録音が終わってから頼む）"},
		{"無い番組", `{"show":"無い番組"}`, add(domain.StateDone, output), 404, "番組が無い"},
		{"無いジョブ", "", 9999, 404, "ジョブが無い"},
		{"読めない body", `{`, add(domain.StateDone, output), 400, "body が JSON として読めない"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := do(h, "POST", path(tt.id), tt.body, ck)
			var body struct{ Error string }
			decode(t, r.Body.Bytes(), &body)
			if r.Code != tt.code || body.Error != tt.msg {
				t.Errorf("保存先の移し = %d %q, want %d %q", r.Code, body.Error, tt.code, tt.msg)
			}
			if j, err := st.GetJob(ctx, tt.id); err == nil && (!j.MoveRequestedAt.IsZero() || j.ShowName != "古い名前") {
				t.Errorf("拒んだ後: %+v", j)
			}
		})
	}
	if rec.n.Load() != 2 {
		t.Errorf("拒んだのに起こした: 起こした数 = %d", rec.n.Load())
	}
	if d := detail(add(domain.StateRunning, output)); d.Movable {
		t.Error("録音中のジョブの movable = true")
	}
	if r := do(h, "POST", path(done), ""); r.Code != 401 {
		t.Errorf("ログインしない保存先の移し = %d, want 401", r.Code)
	}

	// 履歴は移した順に出る。移し終えたら moveRequested は false。
	at := now.Add(time.Minute)
	to := "/lib/TBS/今の番組/今の番組 2026-10-12.m4a"
	if err := st.FinishOutputMove(ctx, domain.OutputMove{JobID: done, At: at, From: output, To: to, Result: domain.OutputMoveTargetExists}, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOutputMove(ctx, domain.OutputMove{JobID: done, At: at.Add(time.Minute), From: output, To: to, Result: domain.OutputMoved}, true, now); err != nil {
		t.Fatal(err)
	}
	d := detail(done)
	if d.MoveRequested || d.OutputPath != to || len(d.OutputMoves) != 2 {
		t.Fatalf("移した後の詳細 = %+v", d)
	}
	m0, m1 := d.OutputMoves[0], d.OutputMoves[1]
	if !m0.At.Equal(at) || m0.Result != "target_exists" || m0.From != output || m0.To != to || m0.Error != "" {
		t.Errorf("outputMoves[0] = %+v", m0)
	}
	if m1.Result != "moved" {
		t.Errorf("outputMoves[1] = %+v", m1)
	}
}
