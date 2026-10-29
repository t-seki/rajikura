package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/metrics"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/store"
)

// fakeDiscord は Discord の Webhook の偽のサーバー。受けた本文を持つ。fail が true なら 500 を返す。
type fakeDiscord struct {
	mu   sync.Mutex
	got  []string
	fail bool
	// tries は受けた要求の数（失敗させたものを含む）。
	tries int
}

// useDiscord は偽の Discord を立て、App の通知先を、そこへ送る notify.Discord にする。
func (e *env) useDiscord() *fakeDiscord {
	e.t.Helper()
	d := &fakeDiscord{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.tries++
		if d.fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		d.got = append(d.got, body.Content)
		w.WriteHeader(http.StatusNoContent)
	}))
	e.t.Cleanup(srv.Close)
	e.app.Notifier = &notify.Discord{WebhookURL: srv.URL + "/api/webhooks/1/secret"}
	return d
}

func (d *fakeDiscord) setFail(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = fail
}

// takeTries は受けた要求の数を返して 0 に戻す。
func (d *fakeDiscord) takeTries() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.tries
	d.tries = 0
	return n
}

// take は受けた本文を返して空にする。
func (d *fakeDiscord) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	got := d.got
	d.got = nil
	return got
}

// expectOne は、受けた本文がちょうど 1 通で、want をすべて含むことを確かめ、その本文を返す。
func (d *fakeDiscord) expectOne(t *testing.T, want ...string) string {
	t.Helper()
	got := d.take()
	if len(got) != 1 {
		t.Fatalf("通知 = %d 通: %q", len(got), got)
	}
	for _, w := range want {
		if !strings.Contains(got[0], w) {
			t.Errorf("通知に %q が無い: %q", w, got[0])
		}
	}
	return got[0]
}

func (d *fakeDiscord) expectNone(t *testing.T) {
	t.Helper()
	if got := d.take(); len(got) != 0 {
		t.Fatalf("通知が送られた: %q", got)
	}
}

// checkNotifications は録音のループの周の通知の処理（期限間近の判定・送る・古い通知を消す）を 1 回する。
func (e *env) checkNotifications() {
	e.t.Helper()
	if err := e.app.CheckNotifications(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// notifications は DB の通知を新しい順に返す。
func (e *env) notifications() []store.Notification {
	e.t.Helper()
	ns, err := e.store.Notifications(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return ns
}

// 保存したら録音済みを DB に書く。通知先には、その場ではなく周の処理で送り、送った時刻を記録する。
func TestNotifyRecorded(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	d.expectNone(t)
	ns := e.notifications()
	// 通知はそのジョブを指す。
	if len(ns) != 1 || ns[0].Kind != notify.KindRecorded || !ns[0].Deliver || !ns[0].SentAt.IsZero() || ns[0].JobID != j.ID || ns[0].Show != "" {
		t.Fatalf("通知 = %+v", ns)
	}
	e.checkNotifications()
	msg := d.expectOne(t, "【録音済み】"+flatShow.Name+"・放送日 2026-10-07（", " 分）")
	if strings.Contains(msg, "\n") {
		t.Errorf("録音済みは 1 行: %q", msg)
	}
	if ns := e.notifications(); ns[0].SentAt.IsZero() {
		t.Errorf("送った時刻が無い: %+v", ns[0])
	}
	// 送れたものは送り直さない。
	e.checkNotifications()
	d.expectNone(t)
}

// notify.success = false（MuteSuccess）なら、録音済みは DB に書くが通知先には送らない。ほかの通知は送る。
func TestNotifyRecordedMuted(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	e.app.MuteSuccess = true
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue() // ふらっとを保存する
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue() // すっぴんしゃんは録音できない
	e.checkNotifications()
	d.expectOne(t, "【録音できなかった】", suppinShow.Name)
	ns := e.notifications()
	if len(ns) != 2 || ns[1].Kind != notify.KindRecorded || ns[1].Deliver || !ns[1].SentAt.IsZero() || !ns[1].FailedAt.IsZero() {
		t.Fatalf("通知 = %+v", ns)
	}
}

// ジョブが expired になったら、録音できなかったを通知する。
func TestNotifyExpired(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	if j := e.job(flatShow.Name); j.State != domain.StateExpired {
		t.Fatalf("job = %+v", j)
	}
	e.checkNotifications()
	d.expectOne(t, "【録音できなかった】", flatShow.Name, "2026-10-07", "直近のエラー: [fetch]")
}

// 録音期限を過ぎて何も取れていないときも通知し、その前の試行のエラーを書く。
func TestNotifyExpiredAtDeadline(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.streamFail = true })
	e.runDue()
	j := e.job(flatShow.Name)
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateExpired {
		t.Fatalf("job = %+v", j)
	}
	e.checkNotifications()
	d.expectOne(t, "【録音できなかった】", flatShow.Name, "録音期限を過ぎ", "その前の試行のエラー: [fetch]")
}

// 欠落ありで保存したら通知する。再試行待ちになっただけでは通知しない。
func TestNotifyMissing(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	e.runDue()
	e.checkNotifications()
	d.expectNone(t) // retrying になっただけでは通知しない
	if ns := e.notifications(); len(ns) != 0 {
		t.Fatalf("再試行待ちで通知を書いた: %+v", ns)
	}
	e.clock.Set(e.job(flatShow.Name).Deadline)
	for e.runDue() {
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone || len(j.Missing) != 1 {
		t.Fatalf("job = %+v", j)
	}
	e.checkNotifications()
	d.expectOne(t, "【欠落あり】", flatShow.Name, "2 件のうち 1 件", j.OutputPath, "10:00〜10:01")
	if ns := e.notifications(); len(ns) != 1 {
		t.Errorf("欠落ありのほかに録音済みも書いた: %+v", ns)
	}
}

// 深夜の放送回の時刻は、暦の時刻ではなく、放送回の放送日の時刻で書く（ADR 0012）。放送回を取れないときのエラー文（ジョブの
// 失敗の理由）と、欠落ありの通知の欠落した放送回。どちらも放送日はジョブの放送日として出ているので、時刻だけを書く。
func TestNotifyMissingLateNightInBroadcastDayTime(t *testing.T) {
	late1 := prog{id: "L1", start: jst(2026, 10, 8, 1, 0), end: jst(2026, 10, 8, 1, 1), title: "深夜の番組 (1)"}
	late2 := prog{id: "L1", start: jst(2026, 10, 8, 1, 1), end: jst(2026, 10, 8, 1, 2), title: "深夜の番組 (2)"}
	lateShow := domain.Show{Name: "深夜の番組", Split: domain.SplitJoin, CheckTitle: true, Weekly: []domain.Weekly{
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 1}),
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 1, Minute: 1}),
	}}
	e := newEnv(t, jst(2026, 10, 8, 2, 0))
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin, late1, late2)
	e.putShows(lateShow)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[late2.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(lateShow.Name)
	if j.Day != day || len(j.Airings) != 2 {
		t.Fatalf("job = %+v", j)
	}
	as := e.attempts(j.ID)
	if len(as) != 1 || !strings.Contains(as[0].Error, "25:01 の放送回を取れない") || strings.Contains(as[0].Error, "10/08") {
		t.Errorf("attempts = %+v", as)
	}
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	j = e.job(lateShow.Name)
	if j.State != domain.StateDone || len(j.Missing) != 1 {
		t.Fatalf("job = %+v", j)
	}
	e.checkNotifications()
	msg := d.expectOne(t, "【欠落あり】", "放送日 2026-10-07", "TBS 25:01〜25:02 深夜の番組 (2)")
	if strings.Contains(msg, "01:01〜01:02") {
		t.Errorf("暦の時刻で書いた: %q", msg)
	}
}

// 期限間近は 1 回だけ通知する。DB に書けたらジョブを通知済みにし、送れなくても書き直さない（送り直しは行のほう）。
func TestNotifyDeadlineNear(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.streamFail = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}

	// 録音期限の 24 時間より前は通知しない。
	e.clock.Set(j.Deadline.Add(-24*time.Hour - time.Minute))
	e.checkNotifications()
	d.expectNone(t)

	// 24 時間前を過ぎた。送れなかったが、通知は DB に 1 行だけ書く。
	e.clock.Set(j.Deadline.Add(-24 * time.Hour))
	d.setFail(true)
	e.checkNotifications()
	e.checkNotifications()
	d.setFail(false)
	d.expectNone(t)
	if ns := e.notifications(); len(ns) != 1 || ns[0].Kind != notify.KindDeadlineNear || ns[0].JobID != j.ID {
		t.Fatalf("通知 = %+v", ns)
	}

	// 次の周で送り直す。
	e.checkNotifications()
	d.expectOne(t, "【期限間近】", flatShow.Name, "retrying", "直近のエラー: [fetch]")
	e.clock.Set(j.Deadline.Add(-time.Hour))
	e.checkNotifications()
	d.expectNone(t)
}

// 送れない通知は周ごとに送り直し、作ってから 1 時間を過ぎた通知を送ろうとして送れなければ、諦めて「送れなかった」の印を付ける。
// 1 通送れなければ、その周ではそれより後の通知を送らない。
func TestNotifyGiveUpAfterAnHour(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue() // ふらっとを expired にする（10-07 12:00 に作る）
	created := e.clock.Now()

	d.setFail(true)
	e.clock.Set(created.Add(30 * time.Minute))
	e.runDue() // すっぴんしゃんも expired にする（30 分後に作る）
	e.checkNotifications()
	if n := d.takeTries(); n != 1 {
		t.Errorf("1 通目で送れないのに、続けて %d 回送ろうとした", n)
	}
	e.clock.Set(created.Add(time.Hour - time.Second))
	e.checkNotifications()
	d.takeTries()
	for _, n := range e.notifications() {
		if !n.FailedAt.IsZero() {
			t.Fatalf("1 時間より前に諦めた: %+v", n)
		}
	}

	// ふらっとの通知は作ってから 1 時間たったので諦める。すっぴんしゃんの通知はまだ送り直す。
	e.clock.Set(created.Add(time.Hour))
	e.checkNotifications()
	ns := e.notifications()
	if len(ns) != 2 || ns[1].FailedAt.IsZero() || !ns[1].SentAt.IsZero() || !ns[0].FailedAt.IsZero() {
		t.Fatalf("通知 = %+v", ns)
	}
	d.setFail(false)
	e.checkNotifications()
	d.expectOne(t, suppinShow.Name)
	e.checkNotifications()
	d.expectNone(t) // 諦めたものは送らない
}

// 止まっていた間に作ってから 1 時間を過ぎた通知も、試さずに諦めない。通知先が動いていれば送る。
func TestNotifySendsOldWhenChannelWorks(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	e.runDue()
	e.clock.Set(e.clock.Now().Add(3 * time.Hour)) // 3 時間、周が回らなかった（プロセスが止まっていた）
	e.checkNotifications()
	if got := d.take(); len(got) != 2 {
		t.Fatalf("通知 = %q", got)
	}
	for _, n := range e.notifications() {
		if n.SentAt.IsZero() || !n.FailedAt.IsZero() {
			t.Errorf("送れた扱いでない: %+v", n)
		}
	}
}

// 作ってから 1 時間を過ぎた通知は、送ろうとして送れなければ諦める。その周で後ろの 1 時間を過ぎた通知は、試さずに諦める。
func TestNotifyGiveUpOldAfterTrying(t *testing.T) {
	e := newFlowEnv(t)
	e.app.Metrics = metrics.New()
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	e.runDue()
	e.clock.Set(e.clock.Now().Add(3 * time.Hour))
	d.setFail(true)
	e.checkNotifications()
	if n := d.takeTries(); n != 1 {
		t.Errorf("送ろうとした回数 = %d、want 1（1 通目で落ちていると分かった）", n)
	}
	for _, n := range e.notifications() {
		if n.FailedAt.IsZero() || !n.SentAt.IsZero() {
			t.Errorf("諦めていない: %+v", n)
		}
	}
	if got := scrape(t, e.app.Metrics); !strings.Contains(got, `rajikura_notify_failures_total{kind="expired"} 2`+"\n") {
		t.Errorf("数えていない: %s", got)
	}
	d.setFail(false)
	e.checkNotifications()
	d.expectNone(t)
}

// 通知先なし（Notifier が nil）なら、通知は DB に書くだけで送らず、時間がたっても送れなかった扱いにしない。
func TestNotifyWithoutChannel(t *testing.T) {
	e := newFlowEnv(t)
	e.app.Notifier = nil
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	e.clock.Set(e.clock.Now().Add(2 * time.Hour))
	e.checkNotifications()
	ns := e.notifications()
	if len(ns) != 1 || ns[0].Kind != notify.KindExpired || ns[0].Deliver || !ns[0].FailedAt.IsZero() || !strings.Contains(ns[0].Message, flatShow.Name) {
		t.Fatalf("通知 = %+v", ns)
	}
}

// 番組表の同期が 2 回続けて失敗したら、異常の始まりを通知する。続いている間は書かない。同期が成功したら復旧を通知する。
func TestNotifySyncFailures(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	if err := e.app.Startup(e.ctx); err != nil { // 1 回目
		t.Fatal(err)
	}
	e.checkNotifications()
	d.expectNone(t)
	e.app.syncLogged(e.ctx) // 2 回目
	e.checkNotifications()
	d.expectOne(t, "【番組表の同期の失敗】", "2 回続けて")
	e.app.syncLogged(e.ctx) // 3 回目
	e.checkNotifications()
	d.expectNone(t)

	e.fake.set(func(f *fakeRadiko) { f.guideFail = false })
	e.app.syncLogged(e.ctx)
	e.checkNotifications()
	d.expectOne(t, "【復旧】", "番組表の同期")
	e.app.syncLogged(e.ctx) // 続けて成功しても、復旧は 1 度だけ
	e.checkNotifications()
	d.expectNone(t)

	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	e.app.syncLogged(e.ctx)
	e.checkNotifications()
	d.expectNone(t)
	e.app.syncLogged(e.ctx)
	e.checkNotifications()
	d.expectOne(t, "【番組表の同期の失敗】")
}

// 再起動しても、続いている異常の始まりを送り直さない（続いているかは DB の通知の行で分かる）。
func TestNotifySyncFailuresAfterRestart(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.app.syncLogged(e.ctx)
	e.checkNotifications()
	d.expectOne(t, "【番組表の同期の失敗】")

	// 再起動した。同じ DB を開いた新しい App で、起動時の同期からまた 2 回続けて失敗する。
	e.stopExpander()
	b := &App{Store: e.store, Guide: e.app.Guide, Fetcher: e.app.Fetcher, Media: e.app.Media, Preset: e.app.Preset,
		WorkDir: e.app.WorkDir, Library: e.app.Library, Membership: e.app.Membership, Now: e.app.Now, Log: e.app.Log,
		Notifier: e.app.Notifier}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.RunExpander(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	if err := b.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	b.syncLogged(e.ctx)
	if err := b.CheckNotifications(e.ctx); err != nil {
		t.Fatal(err)
	}
	d.expectNone(t)

	// 復旧は再起動の後でも通知する。
	e.fake.set(func(f *fakeRadiko) { f.guideFail = false })
	b.syncLogged(e.ctx)
	if err := b.CheckNotifications(e.ctx); err != nil {
		t.Fatal(err)
	}
	d.expectOne(t, "【復旧】")
}

// radiko がエリア外と判定したら通知する。続いている間は書かず、認証が通ったら復旧を通知する。試行の失敗としての扱いは今のまま。
func TestNotifyOutOfArea(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.outOfArea = true })
	e.runDue()
	e.checkNotifications()
	d.expectOne(t, "【エリア外】")
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	e.runDue() // すっぴんしゃんも同じくエリア外
	e.checkNotifications()
	d.expectNone(t)

	// 認証が通ったら復旧を通知する。
	e.fake.set(func(f *fakeRadiko) { f.outOfArea = false })
	e.clock.Set(j.NextAttemptAt)
	e.runDue()
	e.checkNotifications()
	got := d.take()
	if len(got) != 2 || !strings.Contains(got[0], "【復旧】") || !strings.Contains(got[0], "エリア外") || !strings.Contains(got[1], "【録音済み】") {
		t.Fatalf("通知 = %q", got)
	}
	e.fake.set(func(f *fakeRadiko) { f.outOfArea = true })
	e.clock.Set(e.job(suppinShow.Name).NextAttemptAt)
	e.runDue()
	e.checkNotifications()
	d.expectOne(t, "【エリア外】")
}

// ジョブの出来事の通知は、ctx が取り消されていても DB に書く（保存や Complete の直後に SIGTERM が重なったとき）。
// 送るのは、次の周（再起動の後を含む）。
func TestNotifyEventsAfterCancel(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	j := e.job(flatShow.Name)
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	log := e.app.log()

	e.app.notifyExpired(ctx, log, j, "[fetch] boom", "")
	e.app.notifyMissing(ctx, log, j, "/library/x.m4a", j.Airings[1:])
	e.app.notifyRecorded(ctx, log, j, 2*time.Minute, "")
	// サービスの異常は取り消しを切らない（取り消されたら書かない）。
	e.app.authResult(ctx, log, radiko.ErrOutOfArea)

	e.checkNotifications()
	got := d.take()
	if len(got) != 3 || !strings.Contains(got[0], "[fetch] boom") || !strings.Contains(got[1], "/library/x.m4a") || !strings.Contains(got[2], "（2 分）") {
		t.Fatalf("通知 = %q", got)
	}
}

// 送るのに失敗しても、ジョブの処理は止めない。
func TestNotifyFailureDoesNotStopJob(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	d.setFail(true)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	e.checkNotifications()
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	for _, j := range e.jobs() {
		if j.State != domain.StateExpired {
			t.Errorf("job = %+v", j)
		}
	}
}

// 通知の行は 90 日で消す。まだ復旧していないサービスの異常の始まりは消さない。
func TestNotificationRetention(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	e.app.Notifier = nil
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue() // ふらっとの録音済み
	e.app.authResult(e.ctx, e.app.log(), radiko.ErrOutOfArea)
	if ns := e.notifications(); len(ns) != 2 {
		t.Fatalf("通知 = %+v", ns)
	}
	e.clock.Set(e.clock.Now().Add(90*24*time.Hour - time.Minute))
	e.checkNotifications()
	if ns := e.notifications(); len(ns) != 2 {
		t.Fatalf("90 日より前に消した: %+v", ns)
	}
	e.clock.Set(e.clock.Now().Add(2 * time.Minute))
	e.checkNotifications()
	if ns := e.notifications(); len(ns) != 1 || ns[0].Kind != notify.KindOutOfArea {
		t.Fatalf("通知 = %+v", ns)
	}
}
