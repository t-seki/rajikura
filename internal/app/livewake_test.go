package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/power"
)

// fakeWaker は App.Waker の代わり。掛けた時刻と取り消した回数を覚え、Set で problems・err を返す。
type fakeWaker struct {
	mu       sync.Mutex
	sets     []time.Time
	cancels  int
	closed   int
	problems []error
	err      error
	fired    chan time.Time
}

func (f *fakeWaker) Set(at time.Time) ([]error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, at)
	return f.problems, f.err
}

func (f *fakeWaker) Cancel() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	return nil
}

func (f *fakeWaker) Fired() <-chan time.Time { return f.fired }

func (f *fakeWaker) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

func (f *fakeWaker) state() ([]time.Time, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sets), f.cancels
}

// 放送日 2026-10-07（水）の TBS の、もう 1 つのタイムフリーの対象外の放送回（english より後）。
var (
	evening     = prog{id: "E2", start: jst(2026, 10, 7, 15, 0), end: jst(2026, 10, 7, 15, 3), title: "夕方の英会話", ng: true}
	eveningShow = domain.NewShow("夕方の英会話", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 15}))
	// beforeEvening は evening の最初の試行の時刻（開始の 1 分前）。
	beforeEvening = jst(2026, 10, 7, 14, 59)
)

// newWakeEnv は、ライブで録るジョブ（english と、twoLive なら evening も）を展開し、偽のタイマーと KeepAwake を差した環境。
func newWakeEnv(t *testing.T, twoLive bool) (*env, *fakeWaker, *fakeAwake, *liveWake) {
	t.Helper()
	var e *env
	if twoLive {
		e = newLiveEnv(t, jst(2026, 10, 7, 12, 0), english, evening)
		e.putShows(eveningShow)
	} else {
		e = newLiveEnv(t, jst(2026, 10, 7, 12, 0))
	}
	fw := &fakeWaker{fired: make(chan time.Time, 1)}
	fa := &fakeAwake{}
	e.app.Waker = fw
	e.app.KeepAwake = fa.acquire
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	return e, fw, fa, e.app.newLiveWake()
}

// 起こす時刻は、ライブで録るジョブの最初の試行の時刻の 3 分前（放送の開始の 4 分前）。タイムフリーから録るジョブ（ふらっと
// など）のためには掛けない。見回りで時刻が変わらなければ掛け直さない。
func TestLiveWakeThreeMinutesBeforeFirstAttempt(t *testing.T) {
	e, fw, _, lw := newWakeEnv(t, false)
	e.app.tendWake(e.ctx, lw)
	e.app.tendWake(e.ctx, lw)
	sets, cancels := fw.state()
	if want := []time.Time{beforeEnglish.Add(-3 * time.Minute)}; !slices.EqualFunc(sets, want, time.Time.Equal) || cancels != 0 {
		t.Fatalf("掛けた時刻 = %v（取り消し %d 回）, want %v", sets, cancels, want)
	}
	if !sets[0].Equal(english.start.Add(-4 * time.Minute)) {
		t.Errorf("起こす時刻 = %v, want 放送の開始の 4 分前", sets[0])
	}
}

// タイマーが鳴ったら「スリープしない」を掛け、最初の試行の時刻の 2 分後に外す（ジョブの状態は見ない）。鳴った後は
// 次のジョブの分を掛ける。
func TestLiveWakeHoldsUntilTwoMinutesAfterFirstAttempt(t *testing.T) {
	e, fw, fa, lw := newWakeEnv(t, true)
	e.app.tendWake(e.ctx, lw)
	wake := beforeEnglish.Add(-3 * time.Minute)

	e.clock.Set(wake)
	e.app.onWakeFired(lw, wake)
	if a, r := fa.counts(); a != 1 || r != 0 {
		t.Fatalf("鳴った後: acquired=%d released=%d, want 1 と 0", a, r)
	}
	e.app.tendWake(e.ctx, lw)
	sets, _ := fw.state()
	if want := []time.Time{wake, beforeEvening.Add(-3 * time.Minute)}; !slices.EqualFunc(sets, want, time.Time.Equal) {
		t.Fatalf("掛けた時刻 = %v, want %v（鳴った後に次の分）", sets, want)
	}

	e.clock.Set(beforeEnglish.Add(2*time.Minute - time.Second))
	e.app.tendWake(e.ctx, lw)
	if a, r := fa.counts(); a != 1 || r != 0 {
		t.Fatalf("2 分後の前に外した: acquired=%d released=%d", a, r)
	}
	e.clock.Set(beforeEnglish.Add(2 * time.Minute))
	e.app.tendWake(e.ctx, lw)
	if a, r := fa.counts(); a != 1 || r != 1 {
		t.Fatalf("2 分後に外していない: acquired=%d released=%d", a, r)
	}
	if j := e.job(englishShow.Name); j.State != domain.StateWaiting {
		t.Fatalf("job = %+v（ジョブの状態を見ずに外すことを確かめるため、waiting のまま）", j)
	}
}

// 鳴った知らせより先に見回りが来ても、起こす時刻を過ぎていれば鳴ったものとして「スリープしない」を掛ける。後から届いた
// 古い知らせは捨てる（2 回掛けない）。
func TestLiveWakeHoldsWhenRoundComesBeforeFired(t *testing.T) {
	e, _, fa, lw := newWakeEnv(t, true)
	e.app.tendWake(e.ctx, lw)
	wake := beforeEnglish.Add(-3 * time.Minute)
	e.clock.Set(wake.Add(5 * time.Second))
	e.app.tendWake(e.ctx, lw)
	e.app.onWakeFired(lw, wake)
	if a, r := fa.counts(); a != 1 || r != 0 {
		t.Fatalf("acquired=%d released=%d, want 1 と 0", a, r)
	}
	if !lw.at.Equal(beforeEvening.Add(-3 * time.Minute)) {
		t.Errorf("次に掛けた時刻 = %v, want evening の分", lw.at)
	}
}

// 一番早いジョブの分を掛け、一番早いものが変われば掛け直す。対象のジョブが無くなったら取り消す。
func TestLiveWakeRearmsToEarliestAndCancels(t *testing.T) {
	e, fw, _, lw := newWakeEnv(t, true)
	en, ev := e.job(englishShow.Name), e.job(eveningShow.Name)
	wakeEn, wakeEv := beforeEnglish.Add(-3*time.Minute), beforeEvening.Add(-3*time.Minute)

	e.app.tendWake(e.ctx, lw)
	if err := e.store.SkipByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw)
	if err := e.store.RecordByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw)
	sets, cancels := fw.state()
	if want := []time.Time{wakeEn, wakeEv, wakeEn}; !slices.EqualFunc(sets, want, time.Time.Equal) || cancels != 0 {
		t.Fatalf("掛けた時刻 = %v（取り消し %d 回）, want %v", sets, cancels, want)
	}

	for _, j := range []domain.Job{en, ev} {
		if err := e.store.SkipByPerson(e.ctx, j.ID, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	e.app.tendWake(e.ctx, lw)
	e.app.tendWake(e.ctx, lw)
	if sets, cancels := fw.state(); len(sets) != 3 || cancels != 1 {
		t.Fatalf("対象が無くなった後: 掛けた時刻 = %v、取り消し %d 回, want 3 件と 1 回", sets, cancels)
	}
	if !lw.at.IsZero() {
		t.Errorf("掛けている時刻が残っている: %v", lw.at)
	}
}

// ライブの録音役が戻るときは、掛けたタイマーを取り消し、起こした後の「スリープしない」を外す。
func TestLiveWakeClose(t *testing.T) {
	e, fw, fa, lw := newWakeEnv(t, true)
	e.app.tendWake(e.ctx, lw)
	wake := beforeEnglish.Add(-3 * time.Minute)
	e.clock.Set(wake)
	e.app.onWakeFired(lw, wake)
	e.app.tendWake(e.ctx, lw)
	e.app.closeWake(lw)
	if _, cancels := fw.state(); cancels != 1 {
		t.Errorf("取り消し %d 回, want 1", cancels)
	}
	if a, r := fa.counts(); a != 1 || r != 1 {
		t.Errorf("acquired=%d released=%d, want 1 と 1", a, r)
	}
	if fw.closed != 0 {
		t.Errorf("差し替えた Waker を Close した（%d 回）", fw.closed)
	}
}

// 起こせないと分かったとき（ERROR_NOT_SUPPORTED、スリープ解除タイマーの許可が 1 でない）は警告を出す。掛け直すたびに
// 同じ理由が返っても、理由ごとに 1 回だけ出す（添えた値が違っても同じ理由）。
func TestLiveWakeWarnsOncePerReason(t *testing.T) {
	e, fw, _, lw := newWakeEnv(t, true)
	buf := e.useLogBuffer()
	en := e.job(englishShow.Name)
	fw.problems = []error{power.ErrWakeNotSupported, fmt.Errorf("%w（電源に接続: 0、バッテリー: 1。1 が有効）", power.ErrWakeTimersDisallowed)}
	e.app.tendWake(e.ctx, lw)
	fw.mu.Lock()
	fw.problems = []error{power.ErrWakeNotSupported, fmt.Errorf("%w（電源に接続: 0、バッテリー: 0。1 が有効）", power.ErrWakeTimersDisallowed)}
	fw.mu.Unlock()
	if err := e.store.SkipByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw) // evening の分に掛け直す
	if err := e.store.RecordByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw) // english の分に掛け直す
	if sets, _ := fw.state(); len(sets) != 3 {
		t.Fatalf("掛け直していない: %v", sets)
	}
	// 数えるのは Warn の行だけ（異常の通知を書いたことの Info の行も、本文に同じ文を含む）。
	var warns []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "level=WARN") {
			warns = append(warns, l)
		}
	}
	log := strings.Join(warns, "\n")
	const msg = "ライブ録音の前にスリープ中の PC を起こせない見込み"
	if n := strings.Count(log, msg); n != 2 {
		t.Errorf("警告 %d 回, want 2（理由ごとに 1 回）:\n%s", n, buf.String())
	}
	for _, reason := range []string{"ERROR_NOT_SUPPORTED", "スリープ解除タイマーの許可"} {
		if n := strings.Count(log, reason); n != 1 {
			t.Errorf("%q の警告 %d 回, want 1:\n%s", reason, n, buf.String())
		}
	}
}

// タイマーを掛けられないときも警告を出し、同じ誤りでは 1 回だけ出す。掛けられなかったら掛けていないものとして扱い、
// 起こす時刻が同じままでも次の見回りで掛け直す。起こす時刻を過ぎても、鳴ったものとして「スリープしない」を掛けない。
func TestLiveWakeSetFailureWarnsOnceAndRetries(t *testing.T) {
	e, fw, fa, lw := newWakeEnv(t, true)
	buf := e.useLogBuffer()
	en := e.job(englishShow.Name)
	wakeEn, wakeEv := beforeEnglish.Add(-3*time.Minute), beforeEvening.Add(-3*time.Minute)
	fw.err = errors.New("boom")
	e.app.tendWake(e.ctx, lw)
	e.app.tendWake(e.ctx, lw)
	if sets, _ := fw.state(); !slices.EqualFunc(sets, []time.Time{wakeEn, wakeEn}, time.Time.Equal) {
		t.Fatalf("掛けた時刻 = %v, want english の分を 2 回（次の見回りで掛け直す）", sets)
	}
	if !lw.at.IsZero() {
		t.Fatalf("掛けられなかったのに、掛けた扱いが残っている: %v", lw.at)
	}

	e.clock.Set(wakeEn.Add(5 * time.Second))
	e.app.tendWake(e.ctx, lw)
	if a, _ := fa.counts(); a != 0 {
		t.Fatalf("タイマーが無いのに、鳴ったものとしてスリープを止めた（%d 回）", a)
	}

	if err := e.store.SkipByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	fw.mu.Lock()
	fw.err = nil
	fw.mu.Unlock()
	e.app.tendWake(e.ctx, lw)
	if !lw.at.Equal(wakeEv) {
		t.Errorf("掛けられるようになった後の時刻 = %v, want %v", lw.at, wakeEv)
	}
	if n := strings.Count(buf.String(), "スリープから起こすタイマーを掛けられない: boom"); n != 1 {
		t.Errorf("警告 %d 回, want 1:\n%s", n, buf.String())
	}
}

// 起こせないと分かったら、スリープから起こせない見込みのサービスの異常の始まりを通知し（対処の docs の節を添える）、
// 続いている間は掛け直しても書かない。次に起こせる見込みで掛けられたら復旧を通知し、帯（続いている異常）から消える。
func TestLiveWakeAnomalyAndRecovery(t *testing.T) {
	e, fw, _, lw := newWakeEnv(t, true)
	d := e.useDiscord()
	en := e.job(englishShow.Name)
	fw.problems = []error{fmt.Errorf("%w（電源に接続: 0、バッテリー: 1。1 が有効）", power.ErrWakeTimersDisallowed)}
	e.app.tendWake(e.ctx, lw)
	e.checkNotifications()
	d.expectOne(t, "【スリープから起こせない】", "docs/windows.md の「スリープ」", "理由: 電源プランの「スリープ解除タイマーの許可」が有効でない")
	if as := ongoing(t, e); len(as) != 1 || as[0].Anomaly != notify.KindWake || !strings.Contains(as[0].Message, "docs/windows.md") {
		t.Fatalf("続いている異常 = %+v", as)
	}

	// 続いている間は、別の理由で掛け直しても始まりを書かない。
	fw.mu.Lock()
	fw.problems = []error{power.ErrWakeNotSupported}
	fw.mu.Unlock()
	if err := e.store.SkipByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw) // evening の分に掛け直す
	if ns := e.notificationsOf(notify.KindWake); len(ns) != 1 {
		t.Fatalf("続いている間に始まりを書いた: %q", ns)
	}

	// 起こせる見込みで掛けられたら復旧を書く。
	fw.mu.Lock()
	fw.problems = nil
	fw.mu.Unlock()
	if err := e.store.RecordByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw) // english の分に掛け直す
	e.checkNotifications()
	d.expectOne(t, "【復旧】", "スリープ中の PC を起こす")
	if as := ongoing(t, e); len(as) != 0 {
		t.Fatalf("復旧の後も続いている: %+v", as)
	}
	if sets, _ := fw.state(); len(sets) != 3 {
		t.Fatalf("掛け直していない: %v", sets)
	}
}

// タイマーを掛けられない、「スリープ解除タイマーの許可」を読めない（Windows の API の失敗）は、ログのままで、
// 異常の始まりも復旧も書かない。
func TestLiveWakeAPIFailureIsNotAnomaly(t *testing.T) {
	e, fw, _, lw := newWakeEnv(t, true)
	en := e.job(englishShow.Name)
	fw.err = errors.New("boom")
	e.app.tendWake(e.ctx, lw)
	if ns := e.notifications(); len(ns) != 0 {
		t.Fatalf("掛けられないのに通知を書いた: %+v", ns)
	}

	// 起こせないと分かって異常が続いている間に、読めない（API の失敗）だけが返っても、復旧を書かない。
	fw.mu.Lock()
	fw.err = nil
	fw.problems = []error{power.ErrWakeNotSupported}
	fw.mu.Unlock()
	e.app.tendWake(e.ctx, lw)
	fw.mu.Lock()
	fw.problems = []error{errors.New("有効な電源プランを読めないので、「スリープ解除タイマーの許可」を確かめられない")}
	fw.mu.Unlock()
	if err := e.store.SkipByPerson(e.ctx, en.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.app.tendWake(e.ctx, lw) // evening の分に掛け直す
	ns := e.notifications()
	if len(ns) != 1 || ns[0].Kind != notify.KindWake {
		t.Fatalf("通知 = %+v, want 起こせない見込みの始まり 1 通だけ（掛けられない・読めないは異常にしない）", ns)
	}
	if as := ongoing(t, e); len(as) != 1 {
		t.Fatalf("続いている異常 = %+v", as)
	}
	if sets, _ := fw.state(); len(sets) != 3 {
		t.Fatalf("掛け直していない: %v", sets)
	}
}
