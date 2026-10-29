package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// newRerecordEnv は、すっぴんしゃんのジョブを 1 つ録り終えた（done にした）環境と、そのジョブを返す。
func newRerecordEnv(t *testing.T) (*env, domain.Job) {
	t.Helper()
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, flatShow.Name)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(suppinShow.Name)
	if j.State != domain.StateDone || j.OutputPath == "" {
		t.Fatalf("job = %+v", j)
	}
	return e, j
}

// kinds は通知の種類を書いた順に並べる。
func (e *env) kinds() []string {
	e.t.Helper()
	ns, err := e.store.Notifications(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, n := range ns {
		out = append(out, n.Kind)
	}
	return out
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}

// 録り直しの試行は radiko から取り直し、保存先が前の保存先と同じなら上書きして、別名のファイルを作らない。2 回録り直しても
// 保存に失敗しない。録れたら録音済みをもう一度通知する（ADR 0025）。
func TestRerecordOverwritesPrev(t *testing.T) {
	e, j := newRerecordEnv(t)
	prev := j.OutputPath
	recorded := countKind(e.kinds(), notify.KindRecorded)
	for i := 1; i <= 2; i++ {
		now := afterBroadcast.Add(time.Duration(i) * time.Hour)
		e.clock.Set(now)
		if err := e.store.Rerecord(e.ctx, j.ID, now); err != nil {
			t.Fatal(err)
		}
		if !e.runDue() {
			t.Fatalf("%d 回目の録り直しが実行されない", i)
		}
		got := e.job(suppinShow.Name)
		if got.State != domain.StateDone || got.OutputPath != prev {
			t.Fatalf("%d 回目の録り直しの後: %+v", i, got)
		}
		if n := e.fake.playlistCount(suppin.start); n != 1+i {
			t.Errorf("%d 回目の録り直しの後の入手の回数 = %d, want %d（取り直す）", i, n, 1+i)
		}
		if n := countKind(e.kinds(), notify.KindRecorded); n != recorded+i {
			t.Errorf("%d 回目の録り直しの後の録音済みの通知 = %d, want %d", i, n, recorded+i)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(prev))
	if err != nil {
		t.Fatal(err)
	}
	var m4a []string
	for _, en := range entries {
		if filepath.Ext(en.Name()) == ".m4a" {
			m4a = append(m4a, en.Name())
		}
	}
	if len(m4a) != 1 || m4a[0] != filepath.Base(prev) {
		t.Errorf("保存先のフォルダの m4a = %v, want [%s]", m4a, filepath.Base(prev))
	}
}

// 録り直しの試行が録音期限までに一部の放送回しか取れなかったら、取れた分を保存せずに expired にし、前のファイル・保存先・
// 欠落の印を前のまま残す。通知は録音できなかっただけで、録音済み・欠落ありは書かない（ADR 0025。録り直しでない試行の部分保存は
// TestDeadlinePartial のまま done）。
func TestRerecordPartialAtDeadlineKeepsPrev(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone || j.OutputPath == "" || len(j.Missing) != 0 {
		t.Fatalf("job = %+v", j)
	}
	prev := j.OutputPath
	before, err := os.ReadFile(prev)
	if err != nil {
		t.Fatal(err)
	}
	kinds := e.kinds()
	now := afterBroadcast.Add(time.Hour)
	e.clock.Set(now)
	if err := e.store.Rerecord(e.ctx, j.ID, now); err != nil {
		t.Fatal(err)
	}
	// 2 つ目の放送回だけ取れない。
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	if !e.runDue() {
		t.Fatal("録り直しが実行されない")
	}
	if got := e.job(flatShow.Name); got.State != domain.StateRetrying {
		t.Fatalf("1 回目の試行の後: %+v", got)
	}
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateExpired || got.OutputPath != prev || len(got.Missing) != 0 {
		t.Fatalf("録音期限の後: state=%s output=%q missing=%d", got.State, got.OutputPath, len(got.Missing))
	}
	if after, err := os.ReadFile(prev); err != nil || string(after) != string(before) {
		t.Errorf("前のファイルが変わった: %v", err)
	}
	after := e.kinds()
	for _, k := range []string{notify.KindRecorded, notify.KindMissing} {
		if countKind(after, k) != countKind(kinds, k) {
			t.Errorf("%s の通知が増えた", k)
		}
	}
	if countKind(after, notify.KindExpired) != countKind(kinds, notify.KindExpired)+1 {
		t.Errorf("録音できなかったの通知が 1 つ増えていない: %v", after)
	}
}

// 録り直しの試行が録れずに expired になっても、前のファイルと保存先は残り、録音できなかったを通知する（ADR 0025）。
func TestRerecordExpiredKeepsPrev(t *testing.T) {
	e, j := newRerecordEnv(t)
	prev := j.OutputPath
	before, err := os.ReadFile(prev)
	if err != nil {
		t.Fatal(err)
	}
	expired := countKind(e.kinds(), notify.KindExpired)
	now := afterBroadcast.Add(time.Hour)
	e.clock.Set(now)
	if err := e.store.Rerecord(e.ctx, j.ID, now); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	if !e.runDue() {
		t.Fatal("録り直しが実行されない")
	}
	got := e.job(suppinShow.Name)
	if got.State != domain.StateExpired || got.OutputPath != prev {
		t.Fatalf("録れなかった録り直しの後: %+v", got)
	}
	if after, err := os.ReadFile(prev); err != nil || string(after) != string(before) {
		t.Errorf("前のファイルが変わった: %v", err)
	}
	if n := countKind(e.kinds(), notify.KindExpired); n != expired+1 {
		t.Errorf("録音できなかったの通知 = %d, want %d", n, expired+1)
	}
}
