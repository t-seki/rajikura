package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// failingAreaGuide は、fail の間は自分のエリアの局一覧を取れない ownAreaGuide。
type failingAreaGuide struct {
	ownAreaGuide
	failMu sync.Mutex
	fail   bool
}

func (g *failingAreaGuide) setFail(fail bool) {
	g.failMu.Lock()
	defer g.failMu.Unlock()
	g.fail = fail
}

func (g *failingAreaGuide) AreaAiringsOf(ctx context.Context, areaID string, day domain.BroadcastDay) ([]AreaStation, error) {
	g.failMu.Lock()
	fail := g.fail
	g.failMu.Unlock()
	if fail {
		return nil, errors.New("boom")
	}
	return g.ownAreaGuide.AreaAiringsOf(ctx, areaID, day)
}

// oldAiring は展開が触らないジョブの放送回（TBS。エリアの外からはタイムフリーで聴けない（ts_out_ng））。
var oldAiring = domain.Airing{StationID: "TBS", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 11, 0), Title: "前の回",
	TimeFree: domain.TimeFreeFlags{OutNG: 1}}

// newOwnAreaEnv は、ダブルプランで、自分のエリアの局一覧が TBS・QRR の環境に、展開が触らない retrying のジョブ（タイムフリーから
// 録る。次の試行は先）を 1 つ足して、起動の処理（番組表の同期）まで済ませる。
func newOwnAreaEnv(t *testing.T) (*env, *failingAreaGuide, int64) {
	t.Helper()
	e := newFlowEnv(t)
	if err := e.store.DeleteShow(e.ctx, suppinShow.Name); err != nil {
		t.Fatal(err)
	}
	g := &failingAreaGuide{}
	g.set("TBS", "QRR")
	e.app.AreaGuide = g
	e.app.Membership = domain.MembershipDouble
	old := domain.Job{ShowName: flatShow.Name, Day: oldAiring.Day(), State: domain.StateRetrying,
		NextAttemptAt: jst(2026, 10, 20, 12, 0), Airings: []domain.Airing{oldAiring}}
	old.Deadline = domain.JobDeadline(old.Airings, domain.MembershipDouble)
	id, err := e.store.InsertJob(e.ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	return e, g, id
}

func (e *env) getJob(id int64) domain.Job {
	e.t.Helper()
	j, err := e.store.GetJob(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *env) ownArea() string {
	e.t.Helper()
	v, err := e.store.OwnArea(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// areaChangedNotifications は、自分のエリアが変わったの通知を読む。
func (e *env) areaChangedNotifications() []store.Notification {
	e.t.Helper()
	ns, err := e.store.Notifications(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.Notification
	for _, n := range ns {
		if n.Kind == notify.KindAreaChanged {
			out = append(out, n)
		}
	}
	return out
}

// 何も覚えていなければ、自分のエリアを覚えるだけで、付け直さず、通知もしない。
func TestOwnAreaRememberedFirst(t *testing.T) {
	e, g, old := newOwnAreaEnv(t)
	before := e.getJob(old)
	g.set("QRR")
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	e.expandPending()
	if got := e.ownArea(); got != "JP13" {
		t.Errorf("own area = %q, want JP13", got)
	}
	if j := e.getJob(old); !j.Deadline.Equal(before.Deadline) || !j.NextAttemptAt.Equal(before.NextAttemptAt) {
		t.Errorf("付け直した: %+v", j)
	}
	if ns := e.areaChangedNotifications(); len(ns) != 0 {
		t.Errorf("通知した: %+v", ns)
	}
}

// 再起動の前と後で自分のエリアが違えば、起動の後の最初の認証で付け直す。展開が触らないタイムフリーから録るジョブで、今の自分のエリアから
// タイムフリーで聴けない放送回を含むものは、録音期限が過ぎた値（放送の終了の 5 分後）になり、録音の方式は変わらず、期限間近が出ない。
// 自分のエリアが変わったことを 1 通通知し（notify.success = false でも送る）、新しいエリアを覚える。同じエリアのままなら付け直さない。
func TestOwnAreaChangedRevisesDeadlines(t *testing.T) {
	e, g, old := newOwnAreaEnv(t)
	// 再起動の前は JP27 にいた。
	if err := e.store.SetOwnArea(e.ctx, "JP27"); err != nil {
		t.Fatal(err)
	}
	e.app.Notifier = &notify.Discord{WebhookURL: "http://127.0.0.1:1/never"}
	e.app.MuteSuccess = true
	g.set("QRR") // 新しいエリア（JP13）の局一覧に TBS は無い
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	e.expandPending()

	passed := oldAiring.End.Add(domain.LiveDeadlineAfter)
	j := e.getJob(old)
	if !j.Deadline.Equal(passed) || !j.NextAttemptAt.Equal(passed) {
		t.Errorf("deadline = %v, next = %v; want どちらも %v", j.Deadline, j.NextAttemptAt, passed)
	}
	if j.Live() || j.Airings[0].OutOfArea {
		t.Errorf("録音の方式が変わった: %+v", j.Airings)
	}
	if got := e.ownArea(); got != "JP13" {
		t.Errorf("own area = %q, want JP13", got)
	}
	ns := e.areaChangedNotifications()
	if len(ns) != 1 {
		t.Fatalf("自分のエリアが変わったの通知 = %+v, want 1 通", ns)
	}
	if n := ns[0]; !strings.HasPrefix(n.Message, "【自分のエリアが変わった】") || !strings.Contains(n.Message, "JP27 から JP13") ||
		!strings.Contains(n.Message, "1 件。うち録音期限が過ぎた値になったジョブ 1 件") || !n.Deliver || n.JobID != 0 {
		t.Errorf("通知 = %+v", n)
	}
	near, err := e.store.DeadlineNearJobs(e.ctx, e.clock.Now(), deadlineNearWithin)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range near {
		if n.ID == old {
			t.Errorf("録音期限が過ぎた値になったジョブが期限間近の対象: %+v", n)
		}
	}

	// 同じエリアのままの認証では、付け直さず、通知もしない。
	e.app.seeOwnArea(e.ctx, e.app.log(), "JP13")
	e.expandPending()
	if ns := e.areaChangedNotifications(); len(ns) != 1 {
		t.Errorf("同じエリアで通知した: %d 通", len(ns))
	}

	// 次の試行で、取れた分が無いので録音できなかったにする。
	if !e.runDue() {
		t.Fatal("録音期限に寄せた次の試行が実行されない")
	}
	if j := e.getJob(old); j.State != domain.StateExpired {
		t.Errorf("state = %s, want expired", j.State)
	}
}

// radiko がエリア外と判定したときは、自分のエリアが変わったとは見なさない。
func TestOwnAreaOutOfAreaDoesNotRevise(t *testing.T) {
	e, g, old := newOwnAreaEnv(t)
	if err := e.store.SetOwnArea(e.ctx, "JP27"); err != nil {
		t.Fatal(err)
	}
	before := e.getJob(old)
	g.set("QRR")
	e.fake.set(func(f *fakeRadiko) { f.outOfArea = true })
	e.runDue()
	e.expandPending()
	if got := e.ownArea(); got != "JP27" {
		t.Errorf("own area = %q, want JP27 のまま", got)
	}
	if j := e.getJob(old); !j.Deadline.Equal(before.Deadline) {
		t.Errorf("付け直した: %+v", j)
	}
	if ns := e.areaChangedNotifications(); len(ns) != 0 {
		t.Errorf("通知した: %+v", ns)
	}
}

// 新しいエリアの局一覧を取れなければ、覚えている自分のエリアを変えずに、次の認証でやり直す。
func TestOwnAreaRetriesWhenStationsUnavailable(t *testing.T) {
	e, g, old := newOwnAreaEnv(t)
	if err := e.store.SetOwnArea(e.ctx, "JP27"); err != nil {
		t.Fatal(err)
	}
	before := e.getJob(old)
	g.set("QRR")
	g.setFail(true)
	e.app.seeOwnArea(e.ctx, e.app.log(), "JP13")
	e.expandPending()
	if got := e.ownArea(); got != "JP27" {
		t.Errorf("own area = %q, want JP27 のまま", got)
	}
	if j := e.getJob(old); !j.Deadline.Equal(before.Deadline) {
		t.Errorf("付け直した: %+v", j)
	}

	g.setFail(false)
	e.app.seeOwnArea(e.ctx, e.app.log(), "JP13")
	e.expandPending()
	if got := e.ownArea(); got != "JP13" {
		t.Errorf("own area = %q, want JP13", got)
	}
	if j := e.getJob(old); !j.Deadline.Equal(oldAiring.End.Add(domain.LiveDeadlineAfter)) {
		t.Errorf("deadline = %v, want 放送の終了の 5 分後", j.Deadline)
	}
	if ns := e.areaChangedNotifications(); len(ns) != 1 {
		t.Errorf("通知 = %d 通, want 1", len(ns))
	}
}

// 番組表の画面の認証（RadikoAreaGuide）で自分のエリアが分かったときも比べる（OnArea）。差し替えた自分のエリアは TTL を待たずに効く。
func TestRadikoAreaGuideReportsOwnArea(t *testing.T) {
	f := newAreaServer(t)
	g, _ := f.guide()
	var got []string
	g.OnArea = func(area string) { got = append(got, area) }
	if _, err := g.Areas(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "JP13" {
		t.Fatalf("OnArea = %v, want [JP13]", got)
	}
	g.setOwnArea("JP27")
	info, err := g.Areas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Own != "JP27" || len(got) != 1 {
		t.Errorf("own = %s, OnArea = %v; want JP27、認証し直さない", info.Own, got)
	}
}

// 会員種別の保存も、自分のエリアの変化と同じ付け直しを通る（今の自分のエリアの局一覧で、エリアの外から録るかを決め直す）。
func TestSettingsAppliedRevisesWithOwnArea(t *testing.T) {
	e, g, old := newOwnAreaEnv(t)
	g.set("QRR")
	e.app.Membership = domain.MembershipAreaFree
	m := string(domain.MembershipAreaFree)
	e.app.SettingsApplied(e.ctx, config.Patch{Membership: &m},
		config.Config{Membership: domain.MembershipDouble}, config.Config{Membership: domain.MembershipAreaFree})
	j := e.getJob(old)
	if want := oldAiring.End.Add(domain.LiveDeadlineAfter); !j.Deadline.Equal(want) || !j.NextAttemptAt.Equal(want) {
		t.Errorf("deadline = %v, next = %v; want どちらも %v（放送の終了の 5 分後）", j.Deadline, j.NextAttemptAt, want)
	}
	// 会員種別の保存では、自分のエリアの変化を通知しない。
	if ns := e.areaChangedNotifications(); len(ns) != 0 {
		t.Errorf("通知した: %+v", ns)
	}
}
