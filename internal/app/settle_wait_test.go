package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// 最初の試行の時刻に番組表を取り直せなかった録らない予定の扱い（ADR 0019 の「番組表を取り直せなかったときの確定」）。

// failingGuide は、fail にある局の番組表を取れなくする（ほかの局は Guide のまま）。
type failingGuide struct {
	Guide
	mu   sync.Mutex
	fail map[string]bool
}

func (g *failingGuide) Airings(ctx context.Context, stationID string, day domain.BroadcastDay) ([]domain.Airing, error) {
	g.mu.Lock()
	fail := g.fail[stationID]
	g.mu.Unlock()
	if fail {
		return nil, errors.New("取れない")
	}
	return g.Guide.Airings(ctx, stationID, day)
}

func (g *failingGuide) set(station string, fail bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fail[station] = fail
}

func (e *env) useFailingGuide() *failingGuide {
	g := &failingGuide{Guide: e.app.Guide, fail: map[string]bool{}}
	e.app.Guide = g
	return g
}

// waitJob は、番組 name のジョブが ok を満たすまで待つ（展開役の試し直しを待つ）。
func (e *env) waitJob(name string, ok func(domain.Job) bool) domain.Job {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j := e.job(name)
		if ok(j) {
			return j
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("ジョブが変わらない: %+v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// skipPlannedThenGuideFails は、すっぴんしゃんの特番を録らない予定にしてから番組表を取れなくし、最初の試行の時刻を過ぎて
// RunDue を呼ぶ。試し直しの間は delay。
func skipPlannedThenGuideFails(t *testing.T, delay time.Duration) (*env, *failingGuide) {
	t.Helper()
	e := newEnv(t, beforeBroadcast)
	e.app.ExpandRetryDelays = []time.Duration{delay}
	g := e.useFailingGuide()
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.putShows(suppinShow)
	e.sync()
	g.set("TBS", true)
	e.clock.Set(afterBroadcast)
	if e.runDue() {
		t.Error("録らない予定のジョブを実行した")
	}
	return e, g
}

// 取り直せなければ確定させずに録らない予定のまま残し、局・放送日を試し直しに積む。次の周でも確定させない。
// 試し直しで番組名が直っていれば録音予定に戻して録る。
func TestSkipPlannedWaitsWhenGuideFailsThenUnskips(t *testing.T) {
	e, g := skipPlannedThenGuideFails(t, 100*time.Millisecond)
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || j.Settled {
		t.Fatalf("取り直せないのに確定した: %+v", j)
	}
	if got, want := e.retryKeys(), []string{targetKey("TBS", day)}; !slices.Equal(got, want) {
		t.Errorf("試し直し = %v, want %v", got, want)
	}
	// 次の周（取り直しの印がある）でも確定させない。
	e.clock.Set(afterBroadcast.Add(time.Minute))
	if e.runDue() {
		t.Error("録らない予定のジョブを実行した")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || j.Settled {
		t.Fatalf("次の周で確定した: %+v", j)
	}

	// 番組表が取れるようになり、番組名が直っていれば、試し直しの展開が録音予定に戻す。
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	g.set("TBS", false)
	e.waitJob(suppinShow.Name, func(j domain.Job) bool { return j.State == domain.StateWaiting })
	if !e.runDue() {
		t.Fatal("録音予定に戻したジョブを実行しない")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Errorf("job = %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned, notify.KindUnskipped}) {
		t.Errorf("通知 = %v", got)
	}
}

// 試し直しで番組表が取れても番組名が食い違ったままなら、展開が確定させる（通知しない）。
func TestSkipPlannedWaitsWhenGuideFailsThenSettlesOnRetry(t *testing.T) {
	e, g := skipPlannedThenGuideFails(t, 100*time.Millisecond)
	if j := e.job(suppinShow.Name); j.Settled {
		t.Fatalf("取り直せないのに確定した: %+v", j)
	}
	g.set("TBS", false)
	e.waitJob(suppinShow.Name, func(j domain.Job) bool { return j.Settled })
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped {
		t.Errorf("job = %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("通知 = %v", got)
	}
	if e.fake.playlistCount(special.start) != 0 {
		t.Error("録らなかったの放送回を入手した")
	}
}

// 番組表の同期で取れても、同じく展開が確定させる。
func TestSkipPlannedWaitsWhenGuideFailsThenSettlesOnSync(t *testing.T) {
	e, g := skipPlannedThenGuideFails(t, time.Hour) // 同期の前に試し直さない
	g.set("TBS", false)
	e.sync()
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("同期で確定しない: %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("通知 = %v", got)
	}
}

// 取れないまま録音期限を過ぎたら、取り直さずに確定させる（通知しない）。
func TestSkipPlannedSettlesAtDeadlineWhenGuideFails(t *testing.T) {
	e, _ := skipPlannedThenGuideFails(t, time.Hour)
	j := e.job(suppinShow.Name)
	if j.Settled {
		t.Fatalf("取り直せないのに確定した: %+v", j)
	}
	// 録音期限の直前ではまだ待つ。
	e.clock.Set(j.Deadline.Add(-time.Minute))
	e.runDue()
	if j := e.job(suppinShow.Name); j.Settled {
		t.Fatalf("録音期限の前に確定した: %+v", j)
	}
	e.clock.Set(j.Deadline)
	if e.runDue() {
		t.Error("録らない予定のジョブを実行した")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("録音期限で確定しない: %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("通知 = %v", got)
	}
}

// serve を止めている間に録音期限を過ぎた録らない予定も、取り直さずに確定させる。
func TestSkipPlannedSettlesWithoutRefreshAfterDeadline(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.putShows(suppinShow)
	e.sync()
	j := e.job(suppinShow.Name)
	g := newStationGuide(e.app.Guide)
	e.app.Guide = g
	e.clock.Set(j.Deadline.Add(time.Hour))
	e.runDue()
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("確定しない: %+v", j)
	}
	if n := g.count("TBS/" + day.String()); n != 0 {
		t.Errorf("録音期限を過ぎたのに番組表を %d 回取り直した", n)
	}
}

// ほかの局の展開は、録らない予定のジョブを録音予定に戻さない（同期のたびに通知が往復しない）。
func TestOtherStationExpandLeavesSkipPlanned(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.fake.setPrograms("QRR", day, qrrMorning)
	e.putShows(suppinShow, qrrShow)
	e.sync()
	e.sync()
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || j.Settled {
		t.Errorf("job = %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("通知 = %v", got)
	}
}

// 局 A（TBS）の取り直しに失敗して確定を待つ間に、局 B（QRR）の展開が走っても、録音予定に戻さず録らない（レビューで
// 見つかった場面）。
func TestOtherStationExpandWhileSettleWaits(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.app.ExpandRetryDelays = []time.Duration{time.Hour}
	g := e.useFailingGuide()
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.fake.setPrograms("QRR", day, qrrMorning)
	e.putShows(suppinShow, qrrShow)
	e.sync()
	g.set("TBS", true)
	e.clock.Set(afterBroadcast)
	// QRR のジョブ（おはよう寺ちゃん）を録り終えるまで回す。すっぴんしゃんは確定を待つ。
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || j.Settled {
		t.Fatalf("取り直せないのに確定した: %+v", j)
	}
	// QRR の展開（番組を変えた直後の展開と同期）が走る。
	e.app.RequestExpand(qrrShow)
	e.expandPending()
	e.sync()
	e.clock.Set(afterBroadcast.Add(time.Minute))
	if e.runDue() {
		t.Error("録らない予定のジョブを実行した")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || j.Settled {
		t.Errorf("job = %+v", j)
	}
	if e.fake.playlistCount(special.start) != 0 {
		t.Error("特番を入手した")
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("通知 = %v", got)
	}
}

// 人が録らないと決めたジョブは、番組表を取れなくても今までどおり確定させる。
func TestByPersonSettlesWhenGuideFails(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	g := e.useFailingGuide()
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(suppinShow)
	e.sync()
	if err := e.store.SkipByPerson(e.ctx, e.job(suppinShow.Name).ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	g.set("TBS", true)
	e.clock.Set(afterBroadcast)
	if e.runDue() {
		t.Error("録らないと決めたジョブを実行した")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("job = %+v", j)
	}
	if keys := e.retryKeys(); len(keys) != 0 {
		t.Errorf("試し直しに積んだ: %v", keys)
	}
}
