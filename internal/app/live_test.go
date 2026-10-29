package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
)

// 放送日 2026-10-07（水）の TBS の、タイムフリーの対象外の放送回（ライブで録る）。
var (
	english     = prog{id: "E1", start: jst(2026, 10, 7, 13, 0), end: jst(2026, 10, 7, 13, 3), title: "ラジオ英会話", ng: true}
	englishShow = domain.NewShow("ラジオ英会話", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 13}))
	// beforeEnglish は english の最初の試行の時刻（開始の 1 分前）。
	beforeEnglish = jst(2026, 10, 7, 12, 59)
)

// newLiveEnv は、ふらっと（タイムフリー）とラジオ英会話（ライブ）の番組表と、ラジオ英会話の番組を持つ環境。
func newLiveEnv(t *testing.T, now time.Time, ps ...prog) *env {
	t.Helper()
	e := newEnv(t, now)
	if len(ps) == 0 {
		ps = []prog{english}
	}
	e.fake.setPrograms("TBS", day, append([]prog{flat1, news, flat2, suppin}, ps...)...)
	e.putShows(englishShow)
	e.app.LiveRetryInterval = time.Millisecond
	return e
}

// runLive はライブの録音役の 1 周を回し、始めたライブ録音が終わるまで待つ。
func (e *env) runLive() {
	e.t.Helper()
	if err := e.app.startDueLive(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	e.app.liveWG.Wait()
}

// segNames は保存したファイルの中身から、セグメントの名前（radiko の時刻）を順に取り出す。
func segNames(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`\[(\d{14})\.aac\]`).FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	if rest := regexp.MustCompile(`\[\d{14}\.aac\]`).ReplaceAllString(string(b), ""); rest != "" {
		t.Errorf("セグメントでないものが入っている: %q", rest)
	}
	return out
}

// segRange は from から to の前まで、5 秒刻みのセグメントの名前。
func segRange(from, to time.Time) []string {
	var out []string
	for s := from; s.Before(to); s = s.Add(5 * time.Second) {
		out = append(out, s.Format(radikoTime))
	}
	return out
}

func (e *env) notificationsOf(kind string) []string {
	e.t.Helper()
	var out []string
	for _, n := range e.notifications() {
		if n.Kind == kind {
			out = append(out, n.Message)
		}
	}
	return out
}

// タイムフリーの対象外の放送回のジョブは、ライブの録音役が開始の 1 分前に拾って繋ぎ始め、PDT で開始から終了までを保存する。
// タイムフリーの録音のループは拾わない。
func TestLiveRecordsFromOneMinuteBefore(t *testing.T) {
	e := newLiveEnv(t, jst(2026, 10, 7, 12, 0))
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	j := e.job(englishShow.Name)
	if !j.Live() || j.State != domain.StateWaiting || !j.NextAttemptAt.Equal(beforeEnglish) ||
		!j.Deadline.Equal(english.end.Add(5*time.Minute)) {
		t.Fatalf("job = %+v", j)
	}

	e.clock.Set(beforeEnglish.Add(-time.Second))
	e.runLive()
	if j := e.job(englishShow.Name); j.State != domain.StateWaiting || len(e.fake.liveMasters) != 0 {
		t.Fatalf("開始の 1 分前より前に繋いだ: %+v, %v", j, e.fake.liveMasters)
	}

	e.clock.Set(beforeEnglish)
	if e.runDue() {
		t.Fatal("タイムフリーの録音のループがライブで録るジョブを拾った")
	}
	e.runLive()
	j = e.job(englishShow.Name)
	if j.State != domain.StateDone || len(j.Missing) != 0 {
		t.Fatalf("job = %+v", j)
	}
	if got := e.fake.liveMasters[0]; !got.Equal(beforeEnglish) {
		t.Errorf("繋ぎ始めた時刻 = %v, want %v（開始の 1 分前）", got, beforeEnglish)
	}
	if got, want := segNames(t, j.OutputPath), segRange(english.start, english.end); !slices.Equal(got, want) {
		t.Errorf("セグメント = %v\nwant %v", got, want)
	}
	as := e.attempts(j.ID)
	if len(as) != 1 || as[0].FailedStage != "" || as[0].ExtinfTotal != 3*time.Minute || as[0].ProbeDuration != 3*time.Minute {
		t.Errorf("attempts = %+v", as)
	}
	if got := e.notificationsOf(notify.KindRecorded); len(got) != 1 || !strings.Contains(got[0], "ラジオ英会話") {
		t.Errorf("録音済みの通知 = %q", got)
	}
	if _, err := os.Stat(e.app.jobDir(j.ID)); !os.IsNotExist(err) {
		t.Errorf("作業フォルダが残っている: %v", err)
	}
	if e.runDue() {
		t.Fatal("タイムフリーの録音のループが何かを拾った")
	}
}

// 繋ぎ始める直前に番組表を取り直し、開始と終了が変わっていれば新しい値で録る。
func TestLiveRefreshesGuideBeforeStart(t *testing.T) {
	e := newLiveEnv(t, jst(2026, 10, 7, 12, 0))
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	longer := english
	longer.end = english.end.Add(time.Minute)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin, longer)
	e.clock.Set(beforeEnglish)
	e.runLive()
	j := e.job(englishShow.Name)
	if j.State != domain.StateDone || !j.Airings[0].End.Equal(longer.end) {
		t.Fatalf("job = %+v", j)
	}
	if got, want := segNames(t, j.OutputPath), segRange(longer.start, longer.end); !slices.Equal(got, want) {
		t.Errorf("セグメント = %v\nwant %v", got, want)
	}
}

// ライブを録っている間も、タイムフリーの録音のループ（ジョブの実行）と通知の判定（通知先へ送る）は止まらない。
func TestLiveDoesNotBlockTimefreeLoop(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	e.putShows(englishShow, flatShow)
	d := e.useDiscord()
	release := make(chan struct{})
	var once sync.Once
	blocked := make(chan struct{})
	e.fake.set(func(f *fakeRadiko) {
		f.onLiveMedia = func() {
			once.Do(func() { close(blocked) })
			<-release
		}
	})
	defer once.Do(func() {})
	e.app.PollInterval = 10 * time.Millisecond
	e.app.LiveCheckInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	e.stopExpander()
	go func() { done <- e.app.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("ライブ録音が始まらない")
	}
	deadline := time.Now().Add(10 * time.Second)
	var got []string
	for !slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, flatShow.Name) }) {
		if time.Now().After(deadline) {
			t.Fatalf("ライブを録っている間に、ふらっとの録音済みが送られない: %q, jobs %+v", got, e.jobs())
		}
		time.Sleep(10 * time.Millisecond)
		got = append(got, d.take()...)
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Errorf("ふらっと = %+v", j)
	}
	if j := e.job(englishShow.Name); j.State != domain.StateRunning {
		t.Errorf("ラジオ英会話 = %+v（まだ録っている途中のはず）", j)
	}
	close(release)
	for e.job(englishShow.Name).State != domain.StateDone {
		if time.Now().After(deadline.Add(10 * time.Second)) {
			t.Fatalf("ライブ録音が終わらない: %+v", e.job(englishShow.Name))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// scriptedLive は FetchLive だけを差し替えた Fetcher。FetchLive は gate を通れるまで待ち、セグメントを 1 つ書いて放送回の終わりまで
// 見終えたことにする。同時に走った数の最大を数える。
type scriptedLive struct {
	*radiko.Client
	gate chan struct{}
	// gaps は、FetchLive が結果に載せる欠けた区間。
	gaps []radiko.LiveGap

	mu        sync.Mutex
	active    int
	maxActive int
}

func (s *scriptedLive) FetchLive(ctx context.Context, token radiko.Token, req radiko.LiveRequest, w io.Writer) (radiko.LiveResult, error) {
	s.mu.Lock()
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	select {
	case <-s.gate:
	case <-ctx.Done():
		return radiko.LiveResult{}, ctx.Err()
	}
	if _, err := io.WriteString(w, "["+req.Start.In(domain.JST).Format(radikoTime)+".aac]"); err != nil {
		return radiko.LiveResult{}, err
	}
	// 放送回の終わりまで見終えたことにする（Next を放送回の終わりの先にする）。
	return radiko.LiveResult{DurationSeconds: 5, Segments: 1, Next: req.Start.Add(24 * time.Hour), Gaps: s.gaps, Complete: true}, nil
}

func (s *scriptedLive) activeNow() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// 同時に録るライブは maxLiveJobs（8 本）まで。9 本目は、どれかが終わってから始まる。
func TestLiveAtMostEight(t *testing.T) {
	const n = maxLiveJobs + 1
	names := make([]string, n)
	progs := make([]prog, n)
	rs := make([]domain.Show, n)
	for i := range n {
		names[i] = string(rune('A' + i))
		progs[i] = prog{id: names[i], start: jst(2026, 10, 7, 13, i), end: jst(2026, 10, 7, 14, 0), title: names[i], ng: true}
		rs[i] = domain.NewShow(names[i], domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 13, Minute: i}))
	}
	e := newLiveEnv(t, jst(2026, 10, 7, 13, 15), progs...)
	e.store.DeleteShow(e.ctx, englishShow.Name)
	e.putShows(rs...)
	s := &scriptedLive{Client: e.app.Fetcher.(*radiko.Client), gate: make(chan struct{})}
	e.app.Fetcher = s
	var closeGate sync.Once
	t.Cleanup(func() {
		closeGate.Do(func() { close(s.gate) })
		e.app.liveWG.Wait()
	})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	running := func() []string {
		var out []string
		for _, j := range e.jobs() {
			if j.State == domain.StateRunning {
				out = append(out, j.ShowName)
			}
		}
		slices.Sort(out)
		return out
	}

	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.waitFor("8 本が入手中", func() bool { return s.activeNow() == maxLiveJobs })
	if got := running(); !slices.Equal(got, names[:maxLiveJobs]) {
		t.Fatalf("running = %v, want %v", got, names[:maxLiveJobs])
	}
	// 8 本録っている間は、9 本目を始めない。
	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	if j := e.job(names[n-1]); j.State != domain.StateWaiting {
		t.Fatalf("9 本目 = %+v", j)
	}
	s.gate <- struct{}{} // 1 本を終わらせる
	e.waitFor("1 本が終わる", func() bool { return e.app.liveFree() == 1 })
	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	if j := e.job(names[n-1]); j.State != domain.StateRunning {
		t.Fatalf("1 本が終わった後の 9 本目 = %+v", j)
	}
	closeGate.Do(func() { close(s.gate) })
	e.app.liveWG.Wait()
	for _, name := range names {
		if j := e.job(name); j.State != domain.StateDone {
			t.Errorf("%s = %+v", name, j)
		}
	}
	if s.maxActive != maxLiveJobs {
		t.Errorf("同時に走った数の最大 = %d, want %d", s.maxActive, maxLiveJobs)
	}
	// 9 本目は枠待ちだったが欠けずに済んだので、録音済みの通知には枠待ちの行が付かない。
	for _, m := range e.notificationsOf(notify.KindRecorded) {
		if strings.Contains(m, "同時に録る本数の上限") {
			t.Errorf("録音済みの通知に枠待ちの行が付いた: %q", m)
		}
	}
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("%s にならない（jobs %+v）", what, e.jobs())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// liveWaitEnv は、同時に録る本数の上限を 1 本にして、A（13:00〜）が録っている間に B（13:01〜）が枠待ちになる環境を作り、
// A が FetchLive の gate で止まっている状態で返す。gaps は FetchLive が結果に載せる欠けた区間。
func liveWaitEnv(t *testing.T, gaps []radiko.LiveGap) (*env, *scriptedLive) {
	t.Helper()
	a := prog{id: "A", start: jst(2026, 10, 7, 13, 0), end: jst(2026, 10, 7, 14, 0), title: "A", ng: true}
	b := prog{id: "B", start: jst(2026, 10, 7, 13, 1), end: jst(2026, 10, 7, 14, 0), title: "B", ng: true}
	e := newLiveEnv(t, jst(2026, 10, 7, 13, 5), a, b)
	e.store.DeleteShow(e.ctx, englishShow.Name)
	e.putShows(
		domain.NewShow("A", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 13})),
		domain.NewShow("B", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 13, Minute: 1})),
	)
	e.app.liveMax = 1
	s := &scriptedLive{Client: e.app.Fetcher.(*radiko.Client), gate: make(chan struct{}), gaps: gaps}
	e.app.Fetcher = s
	var closeGate sync.Once
	t.Cleanup(func() {
		closeGate.Do(func() { close(s.gate) })
		e.app.liveWG.Wait()
	})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.waitFor("A が入手中", func() bool { return s.activeNow() == 1 })
	if j := e.job("B"); j.State != domain.StateWaiting {
		t.Fatalf("B = %+v", j)
	}
	return e, s
}

// 枠待ちのジョブは、見つけたときに覚え（見回りを重ねても待ち始めた時刻を上書きしない）、拾って繋ぎ始めたら繋ぎ始めた時刻を
// 覚える。枠待ちのまま欠落ありで保存すると、通知の本文の末尾に枠待ちの行が付く。枠待ちでなかった A には付かない。
func TestLiveWaitNotesMissing(t *testing.T) {
	gap := []radiko.LiveGap{{From: jst(2026, 10, 7, 13, 10), To: jst(2026, 10, 7, 13, 20)}}
	e, s := liveWaitEnv(t, gap)
	bID := e.job("B").ID
	planned := e.job("B").NextAttemptAt
	e.app.liveMu.Lock()
	w, ok := e.app.liveWaits[bID]
	_, aWaits := e.app.liveWaits[e.job("A").ID]
	e.app.liveMu.Unlock()
	if !ok || !w.since.Equal(jst(2026, 10, 7, 13, 5)) || !w.planned.Equal(planned) || !w.connected.IsZero() {
		t.Fatalf("liveWaits[B] = %+v, %v", w, ok)
	}
	if aWaits {
		t.Errorf("枠待ちでない A を覚えた")
	}
	e.clock.Set(jst(2026, 10, 7, 13, 6))
	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.app.liveMu.Lock()
	since := e.app.liveWaits[bID].since
	e.app.liveMu.Unlock()
	if !since.Equal(jst(2026, 10, 7, 13, 5)) {
		t.Errorf("見回りを重ねて待ち始めた時刻が変わった: %v", since)
	}
	e.clock.Set(jst(2026, 10, 7, 13, 7))
	s.gate <- struct{}{} // A が終わる
	e.waitFor("A が終わる", func() bool { return e.app.liveFree() == 1 })
	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	s.gate <- struct{}{} // B が終わる
	e.app.liveWG.Wait()

	want := fmt.Sprintf("\n同時に録る本数の上限（%d 本）で枠が空くのを待ったので、繋ぎ始めが %s になった（予定は %s）。",
		1, fmtTime(jst(2026, 10, 7, 13, 7)), fmtTime(planned))
	var withLine, without int
	for _, m := range e.notificationsOf(notify.KindMissing) {
		if strings.HasSuffix(m, want) {
			withLine++
		} else if !strings.Contains(m, "同時に録る本数の上限") {
			without++
		}
	}
	// A も B も同じ gaps で欠落ありになる。B だけが枠待ちの行を持つ。
	if withLine != 1 || without != 1 {
		t.Errorf("欠落ありの通知 %q: 枠待ちの行あり %d 件、なし %d 件", e.notificationsOf(notify.KindMissing), withLine, without)
	}
	e.app.liveMu.Lock()
	left := len(e.app.liveWaits)
	e.app.liveMu.Unlock()
	if left != 0 {
		t.Errorf("試行が終わった後も覚えている: %+v", e.app.liveWaits)
	}
}

// 枠待ちのまま録音期限を過ぎ、1 つも録れないまま録音できなかったになると、通知の本文の末尾に繋げなかった旨の行が付く。
func TestLiveWaitNotesExpiredWithoutConnecting(t *testing.T) {
	e, _ := liveWaitEnv(t, nil)
	planned := e.job("B").NextAttemptAt
	e.clock.Set(e.job("B").Deadline.Add(time.Second))
	if err := e.app.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.waitFor("B が録音できなかったになる", func() bool { return e.job("B").State == domain.StateExpired })
	// 状態を expired にしてから通知を書くので、状態を見ただけでは通知がまだ無いことがある。通知が書かれるのも待つ。
	e.waitFor("録音できなかったの通知が出る", func() bool { return len(e.notificationsOf(notify.KindExpired)) > 0 })
	want := fmt.Sprintf("\n同時に録る本数の上限（%d 本）で枠が空くのを待ち、録音期限までに繋げなかった（予定は %s）。", 1, fmtTime(planned))
	ms := e.notificationsOf(notify.KindExpired)
	if len(ms) != 1 || !strings.HasSuffix(ms[0], want) {
		t.Errorf("録音できなかったの通知 = %q, want 末尾 %q", ms, want)
	}
}

// 枠待ちのまま録音期限を過ぎても、セグメントがある（前のプロセスが取った分が残っている）ときは、繋げなかった旨の行を付けない。
// 欠落ありにも、録れた分を残す録音できなかったにも付けない。
func TestLiveWaitNoNotConnectedLineWithSegments(t *testing.T) {
	e, _ := liveWaitEnv(t, nil)
	b := e.job("B")
	if got := e.app.liveWaitLine(b.ID, true); !strings.Contains(got, "繋げなかった") {
		t.Fatalf("セグメントが無いときの行 = %q", got)
	}
	if got := e.app.liveWaitLine(b.ID, false); got != "" {
		t.Errorf("セグメントがあるときの行 = %q, want 空", got)
	}
	e.app.notifyLiveMissing(e.ctx, e.app.log(), b, "/x.aac", nil, nil, "")
	e.app.notifyExpired(e.ctx, e.app.log(), b, "", keptLines(b, "/work", b.Deadline))
	for _, k := range []string{notify.KindMissing, notify.KindExpired} {
		for _, m := range e.notificationsOf(k) {
			if strings.Contains(m, "同時に録る本数の上限") {
				t.Errorf("%s の通知に行が付いた: %q", k, m)
			}
		}
	}
}

// 繋ぎ直しても埋められなかった区間があっても、録れた分を保存し、欠落ありの通知に欠けた長さを書く。
func TestLiveGapSavesAndNotifiesMissing(t *testing.T) {
	long := prog{id: "E1", start: jst(2026, 10, 7, 13, 0), end: jst(2026, 10, 7, 13, 10), title: "ラジオ英会話", ng: true}
	e := newLiveEnv(t, beforeEnglish, long)
	// 13:03〜13:09 は切れている（遡れる 270 秒より長い）。
	e.fake.set(func(f *fakeRadiko) {
		f.liveOutageFrom, f.liveOutageTo = jst(2026, 10, 7, 13, 3), jst(2026, 10, 7, 13, 9)
	})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runLive()
	j := e.job(englishShow.Name)
	if j.State != domain.StateDone || len(j.Missing) != 1 {
		t.Fatalf("job = %+v", j)
	}
	// 13:03 に切れたときに持っていたのは 13:01:55 まで（遅れ 60 秒）。13:09:00 に繋ぎ直した FetchLive は、その呼び出しの範囲
	// （1 分ずつ）の終わりより後の 13:04:30 のセグメントを見て戻り、次の範囲の FetchLive が 13:09:05 に繋ぎ直して、270 秒遡った
	// 13:04:35 から取る（範囲の区切りで繋ぎ直すので、遡れる範囲が 5 秒ずれる）。
	holeFrom, holeTo := time.Date(2026, 10, 7, 13, 1, 55, 0, domain.JST), time.Date(2026, 10, 7, 13, 4, 35, 0, domain.JST)
	want := append(segRange(long.start, holeFrom), segRange(holeTo, long.end)...)
	if got := segNames(t, j.OutputPath); !slices.Equal(got, want) {
		t.Errorf("セグメント = %v\nwant %v", got, want)
	}
	got := e.notificationsOf(notify.KindMissing)
	if len(got) != 1 || !strings.Contains(got[0], "欠けた長さ 合計 2 分 40 秒") || !strings.Contains(got[0], "13:01:55〜13:04:35") ||
		!strings.Contains(got[0], j.OutputPath) {
		t.Errorf("欠落ありの通知 = %q", got)
	}
	if n := e.notificationsOf(notify.KindRecorded); len(n) != 0 {
		t.Errorf("録音済みも通知した: %q", n)
	}
	as := e.attempts(j.ID)
	if len(as) != 1 || as[0].FailedStage != "" || as[0].ExtinfTotal != time.Duration(len(want))*5*time.Second {
		t.Errorf("attempts = %+v", as)
	}
}

// 欠けた区間は放送の中の時刻なので、その放送回の開始の放送日の時刻で秒まで書く（ADR 0012）。05:00 を越える区間も、開始と同じ
// 放送日で数えて 29:00 より後にする。
func TestLiveGapLinesInBroadcastDayTime(t *testing.T) {
	at := func(d, h, mi, s int) time.Time { return time.Date(2026, 10, d, h, mi, s, 0, domain.JST) }
	late := domain.Airing{StationID: "TBS", Start: at(21, 3, 40, 0), End: at(21, 4, 0, 0), Title: "深夜の番組"}
	dawn := domain.Airing{StationID: "LFR", Start: at(21, 4, 30, 0), End: at(21, 5, 30, 0), Title: "明け方の番組"}
	gaps := []airingGap{
		{airing: late, liveGap: liveGap{From: at(21, 3, 41, 10), To: at(21, 3, 42, 0)}},
		{airing: dawn, liveGap: liveGap{From: at(21, 5, 10, 5), To: at(21, 5, 20, 0)}},
	}
	want := "\n- TBS 27:41:10〜27:42:00（50 秒）深夜の番組\n- LFR 29:10:05〜29:20:00（9 分 55 秒）明け方の番組"
	if got := liveGapLines(gaps); got != want {
		t.Errorf("liveGapLines = %q\nwant %q", got, want)
	}
}

// 何も録れなければ、録音期限（放送の終了の 5 分後）まで繋ぎ直してから、録音できなかった（expired）にする。retrying にはしない。
func TestLiveNothingExpiresAtDeadline(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	e.fake.set(func(f *fakeRadiko) {
		f.liveOutageFrom, f.liveOutageTo = jst(2026, 10, 7, 12, 0), jst(2026, 10, 7, 14, 0)
	})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runLive()
	j := e.job(englishShow.Name)
	if j.State != domain.StateExpired {
		t.Fatalf("job = %+v", j)
	}
	if now, deadline := e.clock.Now(), english.end.Add(5*time.Minute); now.Before(deadline) {
		t.Errorf("録音期限 %v より前（%v）に諦めた", deadline, now)
	}
	as := e.attempts(j.ID)
	if len(as) != 1 || as[0].FailedStage != domain.StageFetch || !strings.Contains(as[0].Error, "1 つも取れなかった") {
		t.Errorf("attempts = %+v", as)
	}
	if got := e.notificationsOf(notify.KindExpired); len(got) != 1 {
		t.Errorf("録音できなかったの通知 = %q", got)
	}
	if _, err := os.Stat(e.app.jobDir(j.ID)); !os.IsNotExist(err) {
		t.Errorf("作業フォルダが残っている: %v", err)
	}
}

// 局にライブのストリームが無い（恒久的に取れない）なら、待たずに録音できなかったにする。retrying にはしない。
func TestLiveNotAvailableExpiresWithoutRetrying(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	e.fake.set(func(f *fakeRadiko) { f.noLive = true })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runLive()
	j := e.job(englishShow.Name)
	if j.State != domain.StateExpired {
		t.Fatalf("job = %+v（retrying にしない）", j)
	}
	if got := e.notificationsOf(notify.KindExpired); len(got) != 1 || !strings.Contains(got[0], "not available") {
		t.Errorf("録音できなかったの通知 = %q", got)
	}
	if !e.clock.Now().Before(english.start) {
		t.Errorf("録音期限まで待った: %v", e.clock.Now())
	}
}

// 放送中に止まった（running のまま残った）ライブ録音は、起動し直したら retrying に戻さず、取ったセグメントを残して繋ぎ直す。
// 記録（.live）より後ろに書きかけたもの（落ちたとき）は捨てる。遡れる範囲より前に途切れた分は欠落になる。
func TestLiveResumeAfterRestart(t *testing.T) {
	long := prog{id: "E1", start: jst(2026, 10, 7, 13, 0), end: jst(2026, 10, 7, 13, 10), title: "ラジオ英会話", ng: true}
	e := newLiveEnv(t, beforeEnglish, long)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			if strings.Contains(r.URL.Path, "20261007130300") {
				cancel() // 13:03:00 のセグメントを取ろうとしたところで止める
			}
		}
	})
	if err := e.app.startDueLive(ctx); err != nil {
		t.Fatal(err)
	}
	e.app.liveWG.Wait()
	j := e.job(englishShow.Name)
	if j.State != domain.StateRunning {
		t.Fatalf("止めた後の job = %+v（running のまま残す）", j)
	}
	aac := airingBase(e.app.jobDir(j.ID), j.Airings[0]) + ".aac"
	first := segNames(t, aac)
	if len(first) == 0 || first[0] != "20261007130000" {
		t.Fatalf("止める前に取ったセグメント = %v", first)
	}
	// 落ちたときのように、記録より後ろに書きかけを足す。
	f, err := os.OpenFile(aac, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("[書きかけ]")
	f.Close()

	// 6 分止まっていた（遡れる 270 秒より長い）。同じ DB と作業ディレクトリで起動し直す。
	e.stopExpander()
	e.clock.Set(jst(2026, 10, 7, 13, 10))
	b := &App{Store: e.store, Guide: e.app.Guide, Fetcher: e.app.Fetcher, Media: e.app.Media, Preset: e.app.Preset,
		WorkDir: e.app.WorkDir, Library: e.app.Library, Membership: e.app.Membership, Now: e.app.Now, Log: e.app.Log,
		LiveRetryInterval: time.Millisecond}
	bctx, bcancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.RunExpander(bctx)
	}()
	defer func() {
		bcancel()
		<-done
	}()
	if err := b.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if j := e.job(englishShow.Name); j.State != domain.StateRunning {
		t.Fatalf("起動し直した後の job = %+v（retrying に戻さない）", j)
	}
	var mu sync.Mutex
	var refetched []string
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			refetched = append(refetched, strings.TrimSuffix(filepath.Base(r.URL.Path), ".aac"))
		}
	})
	if err := b.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	b.liveWG.Wait()
	j = e.job(englishShow.Name)
	if j.State != domain.StateDone || len(j.Missing) != 1 {
		t.Fatalf("job = %+v", j)
	}
	// 止まる前のセグメントはそのまま残り（取り直さない）、13:10 に繋ぎ直して 270 秒遡った 13:05:30 から続く。
	resumeFrom := time.Date(2026, 10, 7, 13, 5, 30, 0, domain.JST)
	want := append(slices.Clone(first), segRange(resumeFrom, long.end)...)
	if got := segNames(t, j.OutputPath); !slices.Equal(got, want) {
		t.Errorf("セグメント = %v\nwant %v", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range refetched {
		if name < resumeFrom.Format(radikoTime) {
			t.Errorf("止まる前のセグメント %s を取り直した", name)
		}
	}
	if got := e.notificationsOf(notify.KindMissing); len(got) != 1 {
		t.Errorf("欠落ありの通知 = %q", got)
	}
}

// running のジョブの一覧を読んだ後に、このプロセスの録音の goroutine が録り終えて（done にして）liveRunning から抜けたジョブは、
// 古い一覧のまま拾い直さない（作業フォルダの消えた状態で頭から録り直さない）。
func TestLiveStaleRunningListDoesNotRestartDoneJob(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	// 録り始めた直後（running）の一覧を読んでおく。
	job, ok, err := e.store.ClaimDueLive(e.ctx, e.clock.Now())
	if err != nil || !ok {
		t.Fatalf("ClaimDueLive = %v, %v", ok, err)
	}
	stale, err := e.store.RunningLive(e.ctx)
	if err != nil || len(stale) != 1 || stale[0].ID != job.ID {
		t.Fatalf("RunningLive = %+v, %v", stale, err)
	}
	// その間に録音の goroutine が録り終え、liveRunning から抜けた。
	e.app.startLive(e.ctx, e.app.current(), job, true)
	e.app.liveWG.Wait()
	done := e.job(englishShow.Name)
	if done.State != domain.StateDone {
		t.Fatalf("job = %+v", done)
	}
	masters, attempts := len(e.fake.liveMasters), len(e.attempts(job.ID))

	if err := e.app.resumeRunningLive(e.ctx, e.app.current(), stale); err != nil {
		t.Fatal(err)
	}
	e.app.liveWG.Wait()
	if got := e.job(englishShow.Name); got.State != domain.StateDone || got.OutputPath != done.OutputPath {
		t.Errorf("job = %+v", got)
	}
	if n := len(e.fake.liveMasters); n != masters {
		t.Errorf("録り終えたジョブをまた繋いだ（マスタの要求 %d → %d）", masters, n)
	}
	if n := len(e.attempts(job.ID)); n != attempts {
		t.Errorf("試行が増えた: %d → %d", attempts, n)
	}
	if _, err := os.Stat(e.app.jobDir(job.ID)); !os.IsNotExist(err) {
		t.Errorf("作業フォルダを作り直した: %v", err)
	}
}

// ライブの試行の間も、スリープしないよう伝える仕組み（attemptAwake）が掛かり、終われば外れる。
func TestLiveKeepAwake(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	fa := &fakeAwake{}
	e.app.KeepAwake = fa.acquire
	var mu sync.Mutex
	maxHeld := 0
	e.fake.set(func(f *fakeRadiko) {
		f.onLiveMedia = func() {
			mu.Lock()
			maxHeld = max(maxHeld, fa.held())
			mu.Unlock()
		}
	})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ := fa.counts(); a != 0 {
		t.Fatalf("試行の前に掛けている: %d", a)
	}
	e.runLive()
	if j := e.job(englishShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxHeld != 1 {
		t.Errorf("ライブの試行の間の掛かり数 = %d, want 1", maxHeld)
	}
	if a, r := fa.counts(); a != 1 || r != 1 {
		t.Errorf("acquired=%d released=%d, want 1 と 1", a, r)
	}
}

// ライブで録るジョブは期限間近を通知しない（録音期限は放送の終了の 5 分後なので、24 時間前はまだ放送の前）。
func TestLiveNoDeadlineNear(t *testing.T) {
	e := newLiveEnv(t, jst(2026, 10, 7, 12, 0))
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.checkNotifications()
	if got := e.notificationsOf(notify.KindDeadlineNear); len(got) != 0 {
		t.Errorf("期限間近を通知した: %q", got)
	}
}

// liveCounter は FetchLive を呼んだ回数を数える Fetcher。
type liveCounter struct {
	Fetcher
	mu sync.Mutex
	n  int
}

func (c *liveCounter) FetchLive(ctx context.Context, token radiko.Token, req radiko.LiveRequest, w io.Writer) (radiko.LiveResult, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Fetcher.FetchLive(ctx, token, req, w)
}

func (c *liveCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// countLive は Fetcher を liveCounter に差し替える。
func (e *env) countLive() *liveCounter {
	c := &liveCounter{Fetcher: e.app.Fetcher}
	e.app.Fetcher = c
	return c
}

// noJobEvents は、録音済み・欠落あり・録音できなかったの通知がまだ無いことを確かめる。
func (e *env) noJobEvents() {
	e.t.Helper()
	for _, k := range []string{notify.KindRecorded, notify.KindMissing, notify.KindExpired} {
		if got := e.notificationsOf(k); len(got) != 0 {
			e.t.Errorf("%s を通知した: %q", k, got)
		}
	}
}

// ライブ録音の録れた分を保存の段階で保存できない（または検証の段階で長さを測れない）なら、retrying にして作業用フォルダを残し、
// 次の試行（前の試行の開始から 15 分後。録音期限を過ぎていてよい）で入手せずに（FetchLive を呼ばずに）保存して done にする。
// 保存をやり直している間は通知しない。
func TestLiveSaveFailureRetriesSaveWithoutFetching(t *testing.T) {
	for _, tc := range []struct {
		name            string
		remux, duration error
		stage           domain.Stage
	}{
		{"保存の段階", errors.New("保存先が固まっている"), nil, domain.StageSave},
		{"検証の段階で長さを測れない", nil, errors.New("ffprobe が落ちた"), domain.StageVerify},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newLiveEnv(t, beforeEnglish)
			if err := e.app.Startup(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.media.fail(tc.remux, tc.duration)
			e.runLive()
			j := e.job(englishShow.Name)
			if j.State != domain.StateRetrying || !j.NextAttemptAt.Equal(beforeEnglish.Add(15*time.Minute)) {
				t.Fatalf("job = %+v（retrying で、次の試行は前の試行の開始から 15 分後のはず）", j)
			}
			if as := e.attempts(j.ID); len(as) != 1 || as[0].FailedStage != tc.stage {
				t.Fatalf("attempts = %+v", as)
			}
			aac := airingBase(e.app.jobDir(j.ID), j.Airings[0]) + ".aac"
			if got, want := segNames(t, aac), segRange(english.start, english.end); !slices.Equal(got, want) {
				t.Fatalf("作業用フォルダに残したセグメント = %v\nwant %v", got, want)
			}
			e.noJobEvents()

			e.media.fail(nil, nil)
			c := e.countLive()
			masters := len(e.fake.liveMasters)
			e.clock.Set(j.NextAttemptAt)
			if !e.clock.Now().After(j.Deadline) {
				t.Fatalf("次の試行 %v が録音期限 %v を過ぎていない（テストの前提）", e.clock.Now(), j.Deadline)
			}
			e.runLive()
			j = e.job(englishShow.Name)
			if j.State != domain.StateDone {
				t.Fatalf("保存をやり直した後の job = %+v", j)
			}
			if got, want := segNames(t, j.OutputPath), segRange(english.start, english.end); !slices.Equal(got, want) {
				t.Errorf("セグメント = %v\nwant %v", got, want)
			}
			if n := c.count(); n != 0 {
				t.Errorf("保存のやり直しで FetchLive を %d 回呼んだ", n)
			}
			if n := len(e.fake.liveMasters); n != masters {
				t.Errorf("保存のやり直しで radiko に繋いだ（マスタの要求 %d → %d）", masters, n)
			}
			if as := e.attempts(j.ID); len(as) != 2 || as[1].FailedStage != "" {
				t.Errorf("attempts = %+v", as)
			}
			if got := e.notificationsOf(notify.KindRecorded); len(got) != 1 {
				t.Errorf("録音済みの通知 = %q", got)
			}
			if _, err := os.Stat(e.app.jobDir(j.ID)); !os.IsNotExist(err) {
				t.Errorf("保存した後も作業用フォルダが残っている: %v", err)
			}
		})
	}
}

// 保存のやり直しは、タイムフリーの再試行と同じ間隔（前の試行の開始から 15 分 → 1 時間 → 3 時間 → 以降 6 時間ごと）で続け、
// 次の試行が打ち切り（放送の終了から 24 時間）より後になるなら打ち切りの時刻に最後の試行を置く。打ち切りの時刻以降に失敗したら
// expired にし、作業用フォルダを残して、録音できなかったの通知に残した場所・消す日時・rajikura retry を書く。
// その後の手動再試行は録音期限を過ぎていても受け付け、打ち切りを過ぎているので、保存がまた失敗すればすぐ expired にする。
// 原因を直してから手動再試行すれば、保存して done にする。
func TestLiveSaveRetryUntilCutoffThenKeeps(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.media.fail(errors.New("容量が足りない"), nil)
	e.runLive()
	j := e.job(englishShow.Name)
	cutoff := english.end.Add(24 * time.Hour)
	var starts []time.Time
	for j.State == domain.StateRetrying {
		if len(starts) > 20 {
			t.Fatalf("打ち切りで止まらない: %v", starts)
		}
		e.clock.Set(j.NextAttemptAt)
		starts = append(starts, j.NextAttemptAt)
		e.runLive()
		j = e.job(englishShow.Name)
	}
	first := beforeEnglish.Add(15 * time.Minute)
	want := []time.Time{first, first.Add(time.Hour), first.Add(4 * time.Hour), first.Add(10 * time.Hour),
		first.Add(16 * time.Hour), first.Add(22 * time.Hour), cutoff}
	if !slices.EqualFunc(starts, want, time.Time.Equal) {
		t.Errorf("保存をやり直した時刻 = %v\nwant %v", starts, want)
	}
	if j.State != domain.StateExpired {
		t.Fatalf("job = %+v", j)
	}
	dir := e.app.jobDir(j.ID)
	if got := segNames(t, airingBase(dir, j.Airings[0])+".aac"); !slices.Equal(got, segRange(english.start, english.end)) {
		t.Errorf("作業用フォルダに残したセグメント = %v", got)
	}
	keep := cutoff.Add(30 * 24 * time.Hour)
	got := e.notificationsOf(notify.KindExpired)
	if len(got) != 1 || !strings.Contains(got[0], dir) || !strings.Contains(got[0], fmtTime(keep)+" に消す") ||
		!strings.Contains(got[0], fmt.Sprintf("`rajikura retry %d`", j.ID)) || !strings.Contains(got[0], "容量が足りない") {
		t.Fatalf("録音できなかったの通知 = %q", got)
	}
	if n := e.notificationsOf(notify.KindRecorded); len(n) != 0 {
		t.Errorf("録音済みを通知した: %q", n)
	}

	// 録音期限も打ち切りも過ぎた後の手動再試行。保存がまた失敗すれば、すぐ expired にする。
	e.clock.Set(cutoff.Add(time.Hour))
	if err := e.store.ManualRetry(e.ctx, j.ID, e.clock.Now()); err != nil {
		t.Fatalf("録れた分を残している間の手動再試行 = %v", err)
	}
	e.runLive()
	if j := e.job(englishShow.Name); j.State != domain.StateExpired {
		t.Fatalf("打ち切りの後の手動再試行で保存が失敗した後の job = %+v（すぐ expired にするはず）", j)
	}
	if got := e.notificationsOf(notify.KindExpired); len(got) != 2 {
		t.Errorf("録音できなかったの通知 = %q", got)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("作業用フォルダが消えた: %v", err)
	}

	// 原因を直してから手動再試行すると、radiko に繋がずに保存する。
	e.media.fail(nil, nil)
	c := e.countLive()
	if err := e.store.ManualRetry(e.ctx, j.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.runLive()
	j = e.job(englishShow.Name)
	if j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if n := c.count(); n != 0 {
		t.Errorf("FetchLive を %d 回呼んだ", n)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("保存した後も作業用フォルダが残っている: %v", err)
	}
}

// keptLiveJob は、保存のやり直しを打ち切って、録れた分を残したまま expired にしたライブで録るジョブを作る（最後の試行は
// 打ち切りの時刻に終わる）。
func (e *env) keptLiveJob() domain.Job {
	e.t.Helper()
	if err := e.app.Startup(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	e.media.fail(errors.New("容量が足りない"), nil)
	e.runLive()
	e.clock.Set(english.end.Add(24 * time.Hour))
	e.runLive()
	j := e.job(englishShow.Name)
	if j.State != domain.StateExpired {
		e.t.Fatalf("job = %+v", j)
	}
	if _, err := os.Stat(e.app.jobDir(j.ID)); err != nil {
		e.t.Fatalf("作業用フォルダが残っていない: %v", err)
	}
	return j
}

// 残した作業用フォルダは、最後の試行の終わりから 30 日を過ぎたら、ライブの録音役の見回りで消える。30 日以内なら消えない。
func TestLiveSweepKeptAfter30Days(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	j := e.keptLiveJob()
	keep := english.end.Add(24 * time.Hour).Add(30 * 24 * time.Hour)
	e.clock.Set(keep.Add(-time.Minute))
	if err := e.app.sweepKeptLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.app.jobDir(j.ID)); err != nil {
		t.Fatalf("30 日以内に消えた: %v", err)
	}
	// 30 日を過ぎたら、ライブの録音役（runLive）が回るついでに消す（見回りは回り始めにも 1 回行う）。
	e.clock.Set(keep)
	e.app.LiveCheckInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.app.runLive(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(e.app.jobDir(j.ID)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("30 日を過ぎても、ライブの録音役が作業用フォルダを消さない")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if err := e.store.ManualRetry(e.ctx, j.ID, e.clock.Now()); err == nil {
		t.Error("30 日を過ぎた後の手動再試行を受け付けた")
	}
}

// 録音期限を過ぎた保存のやり直しは、同時に録る本数の枠に数えない（枠が埋まっていても走る）。前回のプロセスが保存の途中で
// 止まった（running のまま残った）ものの拾い直しも同じ。
func TestLiveSaveRetryIgnoresSlots(t *testing.T) {
	fill := func(a *App) {
		a.liveMu.Lock()
		defer a.liveMu.Unlock()
		a.liveRunning = map[int64]bool{}
		for i := range maxLiveJobs {
			a.liveRunning[int64(1001+i)] = true
		}
	}
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("拾い直し=%v", resume), func(t *testing.T) {
			e := newLiveEnv(t, beforeEnglish)
			if err := e.app.Startup(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.media.fail(errors.New("保存先が固まっている"), nil)
			e.runLive()
			j := e.job(englishShow.Name)
			if j.State != domain.StateRetrying {
				t.Fatalf("job = %+v", j)
			}
			e.media.fail(nil, nil)
			e.clock.Set(j.NextAttemptAt)
			if resume {
				// 保存の途中で止まった（running のまま残った）。
				if _, ok, err := e.store.ClaimDueLiveSave(e.ctx, e.clock.Now()); err != nil || !ok {
					t.Fatalf("ClaimDueLiveSave = %v, %v", ok, err)
				}
			}
			fill(e.app)
			if e.app.liveFree() != 0 {
				t.Fatal("枠が埋まっていない（テストの前提）")
			}
			e.runLive()
			if j := e.job(englishShow.Name); j.State != domain.StateDone {
				t.Fatalf("枠が埋まっている間の保存のやり直し: job = %+v", j)
			}
		})
	}
}

// ライブ録音で、保存した長さが #EXTINF の合計と lengthTolerance を超えてずれても、検証の失敗にせずに保存し、録音済みの通知に
// ずれを書く（タイムフリーからの録音では今のまま検証の失敗。TestVerifyFailure）。
func TestLiveLengthMismatchSavesAndNotifies(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.media.offset = 30 * time.Second
	e.runLive()
	j := e.job(englishShow.Name)
	if j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if as := e.attempts(j.ID); len(as) != 1 || as[0].FailedStage != "" || as[0].ProbeDuration != 3*time.Minute+30*time.Second {
		t.Errorf("attempts = %+v", as)
	}
	got := e.notificationsOf(notify.KindRecorded)
	if len(got) != 1 || !strings.Contains(got[0], "長さのずれ: 保存した長さ 3 分 30 秒 が #EXTINF の合計 3 分 と 30 秒 ずれている") {
		t.Errorf("録音済みの通知 = %q", got)
	}
}
