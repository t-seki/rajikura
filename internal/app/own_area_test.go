package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	// onFetch があれば、局一覧を返す前に 1 度だけ呼ぶ（付け直しの途中で何かが起きるテスト用）。
	onFetch func()
	// byArea にあるエリア ID の AreaAiringsOf は、その局一覧を返す（無いエリアは ownAreaGuide の局一覧）。
	byArea map[string][]string
	// fetches は AreaAiringsOf で頼まれたエリア ID（取れなかったものを含む）。
	fetches []string
}

func (g *failingAreaGuide) fetchCount() int {
	g.failMu.Lock()
	defer g.failMu.Unlock()
	return len(g.fetches)
}

func (g *failingAreaGuide) setFail(fail bool) {
	g.failMu.Lock()
	defer g.failMu.Unlock()
	g.fail = fail
}

func (g *failingAreaGuide) AreaAiringsOf(ctx context.Context, areaID string, day domain.BroadcastDay) ([]AreaStation, error) {
	g.failMu.Lock()
	fail, onFetch := g.fail, g.onFetch
	g.onFetch = nil
	g.fetches = append(g.fetches, areaID)
	ss, ok := g.byArea[areaID]
	g.failMu.Unlock()
	if onFetch != nil {
		onFetch()
	}
	if fail {
		return nil, errors.New("boom")
	}
	if ok {
		var out []AreaStation
		for _, s := range ss {
			out = append(out, AreaStation{ID: s, Name: s})
		}
		return out, nil
	}
	return g.ownAreaGuide.AreaAiringsOf(ctx, areaID, day)
}

// oldAiring は展開が触らないジョブの放送回（TBS。エリアの外からはタイムフリーで聴けない（ts_out_ng））。
var oldAiring = domain.Airing{StationID: "TBS", Start: jst(2026, 10, 5, 10, 0), End: jst(2026, 10, 5, 11, 0), Title: "前の回",
	TimeFree: domain.TimeFreeFlags{OutNG: 1}}

// newOwnAreaEnv は、ダブルプランで、自分のエリアの局一覧が TBS・QRR の環境に、展開が触らない retrying のジョブ（タイムフリーから
// 録る。次の試行は先）を 1 つ足して、起動の処理（会員種別の付け直しと番組表の同期）まで済ませる。remembered が空でなければ、
// 起動の前に DB に覚えていた自分のエリアにする。
func newOwnAreaEnv(t *testing.T, remembered string, opts ...func(g *failingAreaGuide)) (*env, *failingAreaGuide, int64) {
	t.Helper()
	e := newFlowEnv(t)
	if err := e.store.DeleteShow(e.ctx, suppinShow.Name); err != nil {
		t.Fatal(err)
	}
	g := &failingAreaGuide{}
	g.set("TBS", "QRR")
	for _, o := range opts {
		o(g)
	}
	e.app.AreaGuide = g
	e.app.Membership = domain.MembershipDouble
	old := domain.Job{ShowName: flatShow.Name, Day: oldAiring.Day(), State: domain.StateRetrying,
		NextAttemptAt: jst(2026, 10, 20, 12, 0), Airings: []domain.Airing{oldAiring}}
	old.Deadline = domain.JobDeadline(old.Airings, domain.MembershipDouble)
	id, err := e.store.InsertJob(e.ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if remembered != "" {
		// 起動の前（止める前）に覚えていた自分のエリア。
		if err := e.store.SetOwnArea(e.ctx, remembered); err != nil {
			t.Fatal(err)
		}
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
	e, g, old := newOwnAreaEnv(t, "")
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
	e, g, old := newOwnAreaEnv(t, "JP27") // 再起動の前は JP27 にいた。
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
	e, g, old := newOwnAreaEnv(t, "JP27")
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
	e, g, old := newOwnAreaEnv(t, "JP27")
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
	e, g, old := newOwnAreaEnv(t, "")
	g.set("QRR")
	e.app.Membership = domain.MembershipAreaFree
	m := string(domain.MembershipAreaFree)
	e.app.SettingsApplied(e.ctx, config.Patch{Membership: &m},
		config.Config{Membership: domain.MembershipDouble}, config.Config{Membership: domain.MembershipAreaFree})
	// 付け直しは展開役が行う。
	e.expandPending()
	j := e.getJob(old)
	if want := oldAiring.End.Add(domain.LiveDeadlineAfter); !j.Deadline.Equal(want) || !j.NextAttemptAt.Equal(want) {
		t.Errorf("deadline = %v, next = %v; want どちらも %v（放送の終了の 5 分後）", j.Deadline, j.NextAttemptAt, want)
	}
	// 会員種別の保存では、自分のエリアの変化を通知しない。
	if ns := e.areaChangedNotifications(); len(ns) != 0 {
		t.Errorf("通知した: %+v", ns)
	}
}

// 自分のエリアの付け直しの途中（局一覧を取っている間）に会員種別を保存しても、2 つの付け直しは展開役で順に行うので並ばず、
// 後から保存した会員種別の値が残る。
func TestReviseAfterMembershipSavedDuringOwnAreaRevise(t *testing.T) {
	e, g, old := newOwnAreaEnv(t, "JP27")
	g.set("QRR")
	before := domain.JobDeadline([]domain.Airing{oldAiring}, domain.MembershipDouble)
	m := string(domain.MembershipTimeFree30)
	g.failMu.Lock()
	g.onFetch = func() {
		// 展開役が自分のエリアの付け直しで局一覧を取っている間に、会員種別をタイムフリー30 にして保存する。
		e.app.Membership = domain.MembershipTimeFree30
		e.app.SettingsApplied(e.ctx, config.Patch{Membership: &m},
			config.Config{Membership: domain.MembershipDouble}, config.Config{Membership: domain.MembershipTimeFree30})
		// 保存は待たず、保存した goroutine では付け直さない。
		if j := e.getJob(old); !j.Deadline.Equal(before) {
			t.Errorf("保存した goroutine で付け直した: deadline = %v", j.Deadline)
		}
	}
	g.failMu.Unlock()
	e.app.seeOwnArea(e.ctx, e.app.log(), "JP13")
	e.expandPending()
	// 自分のエリアの付け直しは、読んだ時点の会員種別（ダブルプラン）で行った。TBS はエリアの外で ts_out_ng なので過ぎた値になった。
	ns := e.areaChangedNotifications()
	if len(ns) != 1 || !strings.Contains(ns[0].Message, "1 件。うち録音期限が過ぎた値になったジョブ 1 件") {
		t.Fatalf("通知 = %+v", ns)
	}
	// 頼まれた会員種別の付け直しが後から走り、タイムフリー30（エリアの外とは見なさない）の 30 日になる（展開役が先に拾っていても同じ）。
	e.expandPending()
	if j, want := e.getJob(old), domain.JobDeadline([]domain.Airing{oldAiring}, domain.MembershipTimeFree30); !j.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v（後から保存した会員種別の値）", j.Deadline, want)
	}
}

// 会員種別の付け直しは、エリアフリー・ダブルプランで局一覧を取れなければ録音期限を変えず、依頼を残して、展開役が次に起きたときに
// 付け直す。取れない間、展開役は自分から起き直さない。
func TestMembershipReviseWaitsForStations(t *testing.T) {
	e, g, old := newOwnAreaEnv(t, "JP13")
	before := e.getJob(old)
	g.set("QRR") // TBS は自分のエリアの外（ts_out_ng なのでタイムフリーで聴けない）
	g.setFail(true)
	e.app.Membership = domain.MembershipAreaFree
	m := string(domain.MembershipAreaFree)
	e.app.SettingsApplied(e.ctx, config.Patch{Membership: &m},
		config.Config{Membership: domain.MembershipDouble}, config.Config{Membership: domain.MembershipAreaFree})
	e.expandPending()
	if j := e.getJob(old); !j.Deadline.Equal(before.Deadline) || !j.NextAttemptAt.Equal(before.NextAttemptAt) {
		t.Fatalf("局一覧を取れないのに付け直した: deadline = %v, next = %v", j.Deadline, j.NextAttemptAt)
	}
	// 次に起きるまで、局一覧を取りに行き直さない。
	n := g.fetchCount()
	if n == 0 {
		t.Fatal("局一覧を取りに行っていない")
	}
	time.Sleep(100 * time.Millisecond)
	if got := g.fetchCount(); got != n {
		t.Errorf("起こされないのに局一覧を取り直した: %d 回 → %d 回", n, got)
	}

	// 取れるようになって、展開役が次に起きたとき（番組を変えた直後の展開の依頼）に付け直す。
	g.setFail(false)
	e.app.RequestExpand(flatShow)
	e.expandPending()
	if j, want := e.getJob(old), oldAiring.End.Add(domain.LiveDeadlineAfter); !j.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v（放送の終了の 5 分後）", j.Deadline, want)
	}
	// 付け直した後は、依頼が残らない。
	n = g.fetchCount()
	e.app.RequestExpand(flatShow)
	e.expandPending()
	if got := g.fetchCount(); got != n {
		t.Errorf("付け直した後も局一覧を取りに行った: %d 回 → %d 回", n, got)
	}
}

// serve の起動のときに、展開役で会員種別の付け直しを 1 回行う（止める前に拾われなかった依頼を拾い直す）。自分のエリアは、DB に
// 覚えている値を使う（認証で今のエリアが分かっていても、それではない）。自分のエリアが変わったとは通知しない。
func TestStartupRevisesWithRememberedArea(t *testing.T) {
	e, g, old := newOwnAreaEnv(t, "JP27", func(g *failingAreaGuide) {
		// 覚えている JP27 の局一覧には TBS が無い。今の認証のエリア（空で頼む）の局一覧には TBS がある。
		g.byArea = map[string][]string{"JP27": {"QRR"}}
	})
	if j, want := e.getJob(old), oldAiring.End.Add(domain.LiveDeadlineAfter); !j.Deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v（JP27 の局一覧で付け直す）", j.Deadline, want)
	}
	g.failMu.Lock()
	fetches := slices.Clone(g.fetches)
	g.failMu.Unlock()
	if len(fetches) == 0 || fetches[0] != "JP27" {
		t.Errorf("局一覧を頼んだエリア = %v, want 最初が JP27", fetches)
	}
	if ns := e.areaChangedNotifications(); len(ns) != 0 {
		t.Errorf("通知した: %+v", ns)
	}

	// 覚えている値が無ければ（初めての起動）、今の認証のエリアを使う（空で頼む）。
	e2, g2, old2 := newOwnAreaEnv(t, "", func(g *failingAreaGuide) {
		g.byArea = map[string][]string{"JP27": {"QRR"}}
	})
	if j := e2.getJob(old2); !j.Deadline.Equal(domain.JobDeadline([]domain.Airing{oldAiring}, domain.MembershipDouble)) {
		t.Errorf("deadline = %v, want 30 日のまま", j.Deadline)
	}
	g2.failMu.Lock()
	defer g2.failMu.Unlock()
	if len(g2.fetches) == 0 || g2.fetches[0] != "" {
		t.Errorf("局一覧を頼んだエリア = %v, want 最初が空（今の認証のエリア）", g2.fetches)
	}
}
