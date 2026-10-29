package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 文化放送（QRR）の放送日 day の 9:00 の放送回と、それに当たる毎週の予約の番組。展開の依頼のテストで足す。
var (
	qrrMorning = prog{id: "Q1", start: jst(2026, 10, 7, 9, 0), end: jst(2026, 10, 7, 9, 1), title: "おはよう寺ちゃん"}
	qrrShow    = domain.NewShow("おはよう寺ちゃん", domain.NewWeekly("QRR", time.Wednesday, domain.Clock{Hour: 9}))
)

// hasJob は、番組 name のジョブがあるか。
func (e *env) hasJob(name string) bool {
	e.t.Helper()
	for _, j := range e.jobs() {
		if j.ShowName == name {
			return true
		}
	}
	return false
}

// expandPending は積まれている展開の依頼を展開し終えるまで待つ。
func (e *env) expandPending() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	if err := e.app.ExpandPending(ctx); err != nil {
		e.t.Fatalf("展開の依頼が展開されない: %v", err)
	}
}

// 録音中でも、展開役は番組の変更の依頼を展開する（録音の終わりを待たない）。
func TestExpandWhileRecording(t *testing.T) {
	e := newFlowEnv(t)
	e.fake.setPrograms("QRR", day, qrrMorning)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	recording := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			once.Do(func() { close(recording) })
			<-release
		}
	})
	runDone := make(chan error, 1)
	go func() {
		_, err := e.app.RunDue(e.ctx)
		runDone <- err
	}()
	select {
	case <-recording:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("録音が始まらない")
	}

	e.putShows(qrrShow)
	e.app.RequestExpand(qrrShow)
	e.expandPending()
	ok := e.hasJob(qrrShow.Name)
	select {
	case <-runDone:
		t.Error("録音が終わってから展開した")
	default:
	}
	close(release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("録音中に展開されない: %+v", e.jobs())
	}
}

// 試し直すまでの間は 1 分 → 5 分 → 15 分 → 30 分、以降 30 分ごと。
func TestRetryDelay(t *testing.T) {
	a := &App{}
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for retries, w := range want {
		if got := a.retryDelay(retries); got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", retries, got, w)
		}
	}
}

// 展開できなかった局・放送日は、ExpandRetryDelays の最初の間の後に試し直す。
func TestExpandRetriesFailedRequest(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	const interval = 300 * time.Millisecond
	e.app.ExpandRetryDelays = []time.Duration{interval}
	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	e.putShows(qrrShow)
	e.app.RequestExpand(qrrShow)
	e.expandPending()
	failedAt := time.Now()
	if e.hasJob(qrrShow.Name) {
		t.Fatal("番組表を取れないのにジョブができた")
	}

	// 番組表が取れるようになっても、試し直すまではジョブができない。
	e.fake.set(func(f *fakeRadiko) { f.guideFail = false })
	e.fake.setPrograms("QRR", day, qrrMorning)
	deadline := time.Now().Add(10 * time.Second)
	for !e.hasJob(qrrShow.Name) {
		if time.Now().After(deadline) {
			t.Fatal("失敗した依頼を試し直さない")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if elapsed := time.Since(failedAt); elapsed < interval-50*time.Millisecond {
		t.Errorf("試し直しが早すぎる: %v（want %v 以上）", elapsed, interval)
	}
}

// stationGuide は、Airings が呼ばれた時刻を、局ごとと「局/放送日」ごとに記録する。
type stationGuide struct {
	Guide
	mu    sync.Mutex
	calls map[string][]time.Time
}

func newStationGuide(g Guide) *stationGuide {
	return &stationGuide{Guide: g, calls: map[string][]time.Time{}}
}

func (g *stationGuide) Airings(ctx context.Context, stationID string, day domain.BroadcastDay) ([]domain.Airing, error) {
	g.mu.Lock()
	now := time.Now()
	g.calls[stationID] = append(g.calls[stationID], now)
	g.calls[stationID+"/"+day.String()] = append(g.calls[stationID+"/"+day.String()], now)
	g.mu.Unlock()
	return g.Guide.Airings(ctx, stationID, day)
}

// count は、key（局か「局/放送日」）の Airings が呼ばれた数。
func (g *stationGuide) count(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls[key])
}

// times は、key の Airings が呼ばれた時刻。
func (g *stationGuide) times(key string) []time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls[key])
}

// waitCount は、key の Airings が n 回呼ばれるまで待つ。
func (g *stationGuide) waitCount(t *testing.T, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for g.count(key) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s の番組表を %d 回取らない（%d 回）", key, n, g.count(key))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 試し直すのは展開できなかった局・放送日だけで、展開できた放送日は取り直さない。
func TestExpandRetriesOnlyFailedDays(t *testing.T) {
	// TBS は放送日 day（今日）の番組表だけがあり、7 日先までのほかの放送日は 404。
	e := newFlowEnv(t)
	g := newStationGuide(e.app.Guide)
	e.app.Guide = g
	e.app.ExpandRetryDelays = []time.Duration{20 * time.Millisecond}
	e.app.RequestExpand(flatShow)
	e.expandPending()
	next := "TBS/" + day.AddDays(1).String()
	g.waitCount(t, next, 3) // 最初の展開と、2 回の試し直し
	if n := g.count("TBS/" + day.String()); n != 1 {
		t.Errorf("展開できた放送日 %s の番組表を %d 回取った（want 1）", day, n)
	}
}

// 試し直すまでの間は、試し直すたびに延びる。Warn には試し直しの回数と次の時刻が出る。
func TestExpandRetryBacksOff(t *testing.T) {
	e := newFlowEnv(t)
	var mu sync.Mutex
	var logs strings.Builder
	e.app.Log = slog.New(slog.NewTextHandler(lockedBuilder{&mu, &logs}, nil))
	g := newStationGuide(e.app.Guide)
	e.app.Guide = g
	e.app.ExpandRetryDelays = []time.Duration{50 * time.Millisecond, 400 * time.Millisecond}
	e.app.RequestExpand(flatShow)
	e.expandPending()
	key := "TBS/" + day.AddDays(1).String()
	g.waitCount(t, key, 3)
	ts := g.times(key)
	first, second := ts[1].Sub(ts[0]), ts[2].Sub(ts[1])
	if first < 40*time.Millisecond || second < 350*time.Millisecond || second <= first {
		t.Errorf("試し直しの間 = %v, %v（want 50ms 以上、400ms 以上で延びる）", first, second)
	}
	mu.Lock()
	out := logs.String()
	mu.Unlock()
	// 3 回目の取得が始まった時点で、最初の失敗（retries=0）と 1 回目の試し直し（retries=1）の Warn は出ている
	// （2 回目の試し直しの Warn は、3 回目の取得が終わってから出るので見ない）。
	for _, want := range []string{"retries=0", "retries=1", "next="} {
		if !strings.Contains(out, want) {
			t.Errorf("ログに %s が無い:\n%s", want, out)
		}
	}
}

// lockedBuilder は、mu で守った strings.Builder に書く。
type lockedBuilder struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedBuilder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// retryKeys は、試し直しを待っている局・放送日（局/放送日）を、展開役の中で読んで返す。
func (e *env) retryKeys() []string {
	e.t.Helper()
	var keys []string
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	if err := e.app.onExpander(ctx, func(context.Context) error {
		for _, r := range e.app.expandRetries {
			keys = append(keys, targetKey(r.station, r.day))
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	slices.Sort(keys)
	return keys
}

// 番組表の同期が終わったら、それより前に積まれた依頼で展開できなかった局・放送日のうち、同期で展開できたものと
// 同期の対象でなかったものの試し直しを捨てる。同期でも展開できなかったものは試し直しを続ける。
func TestSyncDropsFailedRequests(t *testing.T) {
	tbs := func(i int) string { return targetKey("TBS", day.AddDays(i)) }
	for _, tt := range []struct {
		name string
		// guideFail は、同期の間だけ番組表を 500 で返すか（同期が丸ごと失敗する）。
		guideFail bool
		// want は同期の後に試し直しを待っている局・放送日。
		want []string
	}{
		// 同期は TBS の今日と翌日を展開でき、2 日先から 7 日先は展開できない（一部が失敗した同期）。
		{"一部が失敗した同期", false, []string{tbs(2), tbs(3), tbs(4), tbs(5), tbs(6), tbs(7)}},
		// どの局・放送日も展開できない同期では、同期の対象（TBS）の試し直しは全部続ける。
		{"丸ごと失敗した同期", true, []string{tbs(1), tbs(2), tbs(3), tbs(4), tbs(5), tbs(6), tbs(7)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// 同期の対象はふらっと・すっぴん（TBS）。TBS は今日（day）の番組表だけがある。依頼の番組の 1 つ（QRR）は
			// 保存先に無い（消した番組と同じ）ので、同期の対象でない。QRR の番組表は無い（404）。
			e := newFlowEnv(t)
			e.app.ExpandRetryDelays = []time.Duration{time.Hour} // 同期の前に試し直さない
			e.app.RequestExpand(flatShow, qrrShow)
			e.expandPending()
			var before []string
			for i := 1; i <= syncDaysAhead; i++ {
				before = append(before, tbs(i))
			}
			for i := 0; i <= syncDaysAhead; i++ {
				before = append(before, targetKey("QRR", day.AddDays(i)))
			}
			slices.Sort(before)
			if got := e.retryKeys(); !slices.Equal(got, before) {
				t.Fatalf("依頼の後の試し直し = %v, want %v", got, before)
			}

			// 翌日の番組表が出た後に同期する。
			e.fake.setPrograms("TBS", day.AddDays(1), prog{id: "X1", start: jst(2026, 10, 8, 11, 0), end: jst(2026, 10, 8, 11, 1), title: "翌日の番組"})
			if tt.guideFail {
				e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
			}
			if err := e.app.Startup(e.ctx); err != nil {
				t.Fatal(err)
			}
			if got := e.retryKeys(); !slices.Equal(got, tt.want) {
				t.Errorf("同期の後の試し直し = %v, want %v", got, tt.want)
			}
		})
	}
}

// 同期の後も試し直しを続ける局・放送日は、実際に試し直される。同期で捨てた局・放送日は試し直されない。
func TestRetryAfterPartialSync(t *testing.T) {
	e := newFlowEnv(t)
	g := newStationGuide(e.app.Guide)
	e.app.Guide = g
	// 同期の前には試し直さず、同期の後は短い間で試し直す。
	e.app.ExpandRetryDelays = []time.Duration{time.Second, 100 * time.Millisecond}
	e.app.RequestExpand(flatShow, qrrShow)
	e.expandPending()
	e.fake.setPrograms("TBS", day.AddDays(1), prog{id: "X1", start: jst(2026, 10, 8, 11, 0), end: jst(2026, 10, 8, 11, 1), title: "翌日の番組"})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	// 同期の後の数を控え、試し直しの間を何回か待つ。
	failed, synced, dropped := "TBS/"+day.AddDays(2).String(), "TBS/"+day.AddDays(1).String(), "QRR/"+day.String()
	n := g.count(failed)
	g.waitCount(t, failed, n+2)
	if c := g.count(synced); c != 2 { // 依頼と同期の 1 回ずつ
		t.Errorf("同期で展開できた %s の番組表を %d 回取った（want 2）", synced, c)
	}
	if c := g.count(dropped); c != 1 { // 依頼の 1 回だけ
		t.Errorf("同期の対象でない %s の番組表を %d 回取った（want 1）", dropped, c)
	}
}

// countingGuide は、Airings が同時に何本走ったかの最大を数える。
type countingGuide struct {
	Guide
	inFlight atomic.Int32
	max      atomic.Int32
	calls    atomic.Int32
}

func (g *countingGuide) Airings(ctx context.Context, stationID string, day domain.BroadcastDay) ([]domain.Airing, error) {
	n := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	g.calls.Add(1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			break
		}
	}
	// 重なりを見つけやすくするため、少し待つ。
	time.Sleep(2 * time.Millisecond)
	return g.Guide.Airings(ctx, stationID, day)
}

// 展開（番組表を取って展開する処理）は展開役 1 本だけで走る。番組表の同期・展開の依頼・最初の試行の前の取り直しが
// 重なっても、番組表を取る処理が同時に 2 本走らない。
func TestExpandRunsOneAtATime(t *testing.T) {
	e := newFlowEnv(t)
	e.fake.setPrograms("QRR", day, qrrMorning)
	e.stopExpander()
	g := &countingGuide{Guide: e.app.Guide}
	e.app.Guide = g
	e.app.PollInterval = 5 * time.Millisecond
	e.app.SyncInterval = 20 * time.Millisecond
	e.app.ExpandRetryDelays = []time.Duration{10 * time.Millisecond}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- e.app.Serve(ctx) }()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				e.app.RequestExpand(qrrShow, flatShow)
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m := g.max.Load(); m != 1 {
		t.Errorf("番組表を取る処理が同時に %d 本走った", m)
	}
	if g.calls.Load() == 0 {
		t.Error("番組表を一度も取らなかった")
	}
}

// 展開役は 1 本だけ。動いているところに Serve・RunExpander を呼ぶとエラーになる。
func TestExpanderRunsOnlyOnce(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	// newEnv の展開役が動き出すまで待つ。
	e.expandPending()
	if err := e.app.Serve(e.ctx); !errors.Is(err, errExpanderRunning) {
		t.Errorf("Serve = %v, want errExpanderRunning", err)
	}
	if err := e.app.RunExpander(e.ctx); !errors.Is(err, errExpanderRunning) {
		t.Errorf("RunExpander = %v, want errExpanderRunning", err)
	}
}
