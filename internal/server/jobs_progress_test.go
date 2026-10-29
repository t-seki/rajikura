package server

import (
	"context"
	"encoding/json"
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

// progressRecorder は WakeRecorder のほかに、決めた進捗を返す（app.App の代わり）。
type progressRecorder struct {
	progress map[int64]app.Progress
}

func (p *progressRecorder) WakeRecorder() {}

func (p *progressRecorder) JobProgress(id int64) (app.Progress, bool) {
	v, ok := p.progress[id]
	return v, ok
}

// ジョブの一覧と詳細の progress は、DB の状態が running で、メモリに進捗があるときだけ付く。入手の間は録れた秒数と合計を出し、
// 検証・保存の段階では段階だけを出す。ほかのときはキーごと出さない。
func TestJobsProgress(t *testing.T) {
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
	fetching := addJob(t, st, "F", "2026-10-11", 0, domain.StateRunning, now)
	verifying := addJob(t, st, "V", "2026-10-11", 0, domain.StateRunning, now)
	noProgress := addJob(t, st, "N", "2026-10-11", 0, domain.StateRunning, now)
	// メモリには進捗が残っているが、DB はもう running でない（試行が終わった直後のずれ）。
	settled := addJob(t, st, "R", "2026-10-11", 0, domain.StateRetrying, now)
	rec := &progressRecorder{progress: map[int64]app.Progress{
		fetching:  {Stage: domain.StageFetch, Counted: true, Fetched: 42 * time.Minute, Total: time.Hour},
		verifying: {Stage: domain.StageVerify},
		settled:   {Stage: domain.StageSave},
	}}
	ui := &UI{
		Sessions: st, Jobs: st, Passwords: passwords(string(hash)), Recorder: rec,
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	ck := login(t, h)

	want := map[int64]string{
		fetching:   `{"stage":"fetch","fetchedSeconds":2520,"totalSeconds":3600}`,
		verifying:  `{"stage":"verify"}`,
		noProgress: "",
		settled:    "",
	}
	rr := do(h, "GET", "/api/jobs", "", ck)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/jobs = %d", rr.Code)
	}
	var list struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	decode(t, rr.Body.Bytes(), &list)
	if len(list.Jobs) != len(want) {
		t.Fatalf("jobs = %d 件", len(list.Jobs))
	}
	for _, j := range list.Jobs {
		var id int64
		decode(t, j["id"], &id)
		if got := string(j["progress"]); got != want[id] {
			t.Errorf("一覧の ジョブ %d の progress = %q, want %q", id, got, want[id])
		}
	}
	for id, w := range want {
		rr := do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /api/jobs/%d = %d", id, rr.Code)
		}
		var d map[string]json.RawMessage
		decode(t, rr.Body.Bytes(), &d)
		if got := string(d["progress"]); got != w {
			t.Errorf("詳細の ジョブ %d の progress = %q, want %q", id, got, w)
		}
	}
}

// 進捗を読めない Recorder（Progresses を満たさない）なら、running のジョブにも progress を付けない。
func TestJobsProgressWithoutReader(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	id := addJob(t, st, "F", "2026-10-11", 0, domain.StateRunning, now)
	rr := do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck)
	var d map[string]json.RawMessage
	decode(t, rr.Body.Bytes(), &d)
	if _, ok := d["progress"]; ok {
		t.Errorf("progress = %s", d["progress"])
	}
}
