package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// countingRecorder は WakeRecorder が呼ばれた数を数える。
type countingRecorder struct{ n atomic.Int32 }

func (c *countingRecorder) WakeRecorder() { c.n.Add(1) }

// ジョブの「再試行」「録る」「録り直す」が成功したら録音のループを起こし、拒まれたとき（409・404）と「録らない」では起こさない。
func TestJobActionsWakeRecorder(t *testing.T) {
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
	ui := &UI{
		Sessions: st, Jobs: st, Passwords: passwords(string(hash)), Recorder: rec,
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	ck := login(t, h)
	path := func(id int64, op string) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/" + op }
	post := func(id int64, op string) int { return do(h, "POST", path(id, op), "", ck).Code }

	expired := addJob(t, st, "E", "2026-10-11", 0, domain.StateExpired, now)
	if code := post(expired, "retry"); code != http.StatusOK || rec.n.Load() != 1 {
		t.Fatalf("再試行 = %d, 起こした数 = %d", code, rec.n.Load())
	}
	planned := addJobOf(t, st, domain.Job{ShowName: "P", Day: mustDay(t, "2026-10-11"), State: domain.StateSkipped,
		NextAttemptAt: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour)})
	if code := post(planned, "record"); code != http.StatusOK || rec.n.Load() != 2 {
		t.Fatalf("録る = %d, 起こした数 = %d", code, rec.n.Load())
	}
	done := addJob(t, st, "D", "2026-10-11", 0, domain.StateDone, now)
	waiting := addJob(t, st, "W", "2026-10-11", 0, domain.StateWaiting, now)
	if code := post(done, "retry"); code != http.StatusConflict {
		t.Fatalf("done の再試行 = %d", code)
	}
	if code := post(9999, "record"); code != http.StatusNotFound {
		t.Fatalf("無いジョブの録る = %d", code)
	}
	if code := post(waiting, "skip"); code != http.StatusOK {
		t.Fatalf("録らない = %d", code)
	}
	if n := rec.n.Load(); n != 2 {
		t.Errorf("拒まれた操作・録らないで起こした: 起こした数 = %d", n)
	}
	if code := post(expired, "rerecord"); code != http.StatusConflict || rec.n.Load() != 2 {
		t.Fatalf("done でないジョブの録り直し = %d, 起こした数 = %d", code, rec.n.Load())
	}
	if code := post(done, "rerecord"); code != http.StatusOK || rec.n.Load() != 3 {
		t.Fatalf("録り直し = %d, 起こした数 = %d", code, rec.n.Load())
	}
}
