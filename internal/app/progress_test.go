package app

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// progressEvent は進捗を置く・変える・消すの 1 回（ok が false なら消した）。
type progressEvent struct {
	p  Progress
	ok bool
}

// progressLog は、ジョブ 1 つの進捗の移り方を記録する。
type progressLog struct {
	mu   sync.Mutex
	seen []progressEvent
	// check があれば、置く・変えるたびに呼ぶ（その時点のファイルと比べる）。
	check func(p Progress)
}

// watchProgress は、ジョブ id の進捗の移り方を a で記録する（試行を始める前に呼ぶ）。
func watchProgress(a *App, id int64) *progressLog {
	l := &progressLog{}
	a.progressSeen = func(jid int64, p Progress, ok bool) {
		if jid != id {
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		l.seen = append(l.seen, progressEvent{p, ok})
		if ok && l.check != nil {
			l.check(p)
		}
	}
	return l
}

func (l *progressLog) events() []progressEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// fetched は、入手の段階の進捗の録れた長さを、続けて同じ値を 1 つにまとめて並べる。
func fetched(evs []progressEvent) []time.Duration {
	var out []time.Duration
	for _, e := range evs {
		if e.ok && e.p.Counted && (len(out) == 0 || out[len(out)-1] != e.p.Fetched) {
			out = append(out, e.p.Fetched)
		}
	}
	return out
}

// laterStages は、長さを出さない進捗の段階を並べる。
func laterStages(evs []progressEvent) []domain.Stage {
	var out []domain.Stage
	for _, e := range evs {
		if e.ok && !e.p.Counted {
			out = append(out, e.p.Stage)
		}
	}
	return out
}

// タイムフリーのジョブの進捗は、試行の始まりに前の試行で入手し終えた放送回の分を録れた長さに置き、入手している放送回の
// チャンク（5 分）を書くごとに進め、格納・検証・保存の段階に移り、試行が終わると消える。合計はジョブの放送回の長さの合計。
func TestProgressTimefree(t *testing.T) {
	// 2 つ目の放送回は 11 分（チャンク 300/300/60 秒）。
	long2 := prog{id: "P1", start: jst(2026, 10, 7, 10, 0), end: jst(2026, 10, 7, 10, 11), title: "パンサー向井のふらっと (2)", pfm: "パンサー向井"}
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, long2)
	e.putShows(flatShow)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	// 1 回目の試行は 2 つ目の放送回で落ち、1 つ目の放送回（1 分）を入手し終えた分として残す。
	e.fake.set(func(f *fakeRadiko) { f.failSeek[long2.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	if _, ok := e.app.JobProgress(j.ID); ok {
		t.Error("失敗した試行の後に進捗が残っている")
	}

	e.fake.set(func(f *fakeRadiko) { delete(f.failSeek, long2.start.Format(radikoTime)) })
	e.clock.Set(j.NextAttemptAt)
	l := watchProgress(e.app, j.ID)
	var verifying Progress
	e.media.onDuration = func() { verifying, _ = e.app.JobProgress(j.ID) }
	if !e.runDue() {
		t.Fatal("再試行されない")
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	evs := l.events()
	if len(evs) == 0 || !evs[0].ok || evs[0].p != (Progress{Stage: domain.StageFetch, Counted: true, Fetched: time.Minute, Total: 12 * time.Minute}) {
		t.Fatalf("試行の始まりの進捗 = %+v", evs)
	}
	for _, ev := range evs {
		if ev.p.Counted && (ev.p.Stage != domain.StageFetch || ev.p.Total != 12*time.Minute) {
			t.Errorf("入手の段階の進捗 = %+v", ev.p)
		}
	}
	if got, want := fetched(evs), []time.Duration{time.Minute, 6 * time.Minute, 11 * time.Minute, 12 * time.Minute}; !slices.Equal(got, want) {
		t.Errorf("録れた長さの移り方 = %v, want %v", got, want)
	}
	if got, want := laterStages(evs), []domain.Stage{domain.StageSave, domain.StageVerify, domain.StageSave}; !slices.Equal(got, want) {
		t.Errorf("入手の後の段階 = %v, want %v", got, want)
	}
	if verifying != (Progress{Stage: domain.StageVerify}) {
		t.Errorf("長さを測っている間の進捗 = %+v", verifying)
	}
	if last := evs[len(evs)-1]; last.ok {
		t.Errorf("試行の終わりに進捗を消していない: %+v", last)
	}
	if _, ok := e.app.JobProgress(j.ID); ok {
		t.Error("試行が終わった後に進捗が残っている")
	}
}

// ライブで録るジョブの入手の間の録れた長さは、記録（.live）の届いた音の長さ（Extinf）で、記録を書くたびに進む。
// 合計は放送回の長さ。試行が終わると消える。
func TestProgressLive(t *testing.T) {
	e := newLiveEnv(t, beforeEnglish)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	j := e.job(englishShow.Name)
	l := watchProgress(e.app, j.ID)
	base := airingBase(e.app.jobDir(j.ID), j.Airings[0])
	var mismatch []string
	l.check = func(p Progress) {
		if !p.Counted {
			return
		}
		cp, err := readLiveCheckpoint(base)
		if err != nil {
			t.Error(err)
			return
		}
		if want := time.Duration(cp.Extinf * float64(time.Second)); p.Fetched != want {
			mismatch = append(mismatch, p.Fetched.String()+" != "+want.String())
		}
	}
	e.runLive()
	if j := e.job(englishShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	evs := l.events()
	l.mu.Lock()
	if len(mismatch) > 0 {
		t.Errorf("録れた長さが記録の Extinf と違う: %v", mismatch)
	}
	l.mu.Unlock()
	if len(evs) == 0 || evs[0].p != (Progress{Stage: domain.StageFetch, Counted: true, Total: 3 * time.Minute}) {
		t.Fatalf("試行の始まりの進捗 = %+v", evs)
	}
	got := fetched(evs)
	if len(got) < 3 || got[len(got)-1] != 3*time.Minute || !slices.IsSorted(got) {
		t.Errorf("録れた長さの移り方 = %v（0 から 3 分まで、記録を書くたびに増えるはず）", got)
	}
	if got, want := laterStages(evs), []domain.Stage{domain.StageSave, domain.StageVerify, domain.StageSave}; !slices.Equal(got, want) {
		t.Errorf("入手の後の段階 = %v, want %v", got, want)
	}
	if _, ok := e.app.JobProgress(j.ID); ok {
		t.Error("試行が終わった後に進捗が残っている")
	}
}

// 前の起動から続けるライブ録音は、読んだ記録の届いた音の長さから録れた長さを始める。
func TestProgressLiveResumeFromCheckpoint(t *testing.T) {
	long := prog{id: "E1", start: jst(2026, 10, 7, 13, 0), end: jst(2026, 10, 7, 13, 10), title: "ラジオ英会話", ng: true}
	e := newLiveEnv(t, beforeEnglish, long)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			if strings.Contains(r.URL.Path, "20261007130200") {
				cancel() // 13:02:00 のセグメントを取ろうとしたところで止める
			}
		}
	})
	if err := e.app.startDueLive(ctx); err != nil {
		t.Fatal(err)
	}
	e.app.liveWG.Wait()
	j := e.job(englishShow.Name)
	if j.State != domain.StateRunning {
		t.Fatalf("止めた後の job = %+v", j)
	}
	cp, err := readLiveCheckpoint(airingBase(e.app.jobDir(j.ID), j.Airings[0]))
	if err != nil || cp.Extinf <= 0 {
		t.Fatalf("止めた後の記録 = %+v, %v", cp, err)
	}
	e.fake.set(func(f *fakeRadiko) { f.onSegment = nil })

	// 同じ DB と作業ディレクトリで起動し直す。
	e.stopExpander()
	e.clock.Set(jst(2026, 10, 7, 13, 3))
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
	l := watchProgress(b, j.ID)
	if err := b.startDueLive(e.ctx); err != nil {
		t.Fatal(err)
	}
	b.liveWG.Wait()
	if j := e.job(englishShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	evs := l.events()
	want := Progress{Stage: domain.StageFetch, Counted: true, Fetched: time.Duration(cp.Extinf * float64(time.Second)), Total: 10 * time.Minute}
	if len(evs) == 0 || evs[0].p != want {
		t.Fatalf("起動し直した試行の始まりの進捗 = %+v, want %+v", evs, want)
	}
}

// 録音期限を過ぎて保存だけをやり直すライブ録音は、入手しないので録れた長さを出さず、段階（保存・検証）だけを出す。
func TestProgressLiveSaveOnly(t *testing.T) {
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
	if !e.clock.Now().After(j.Deadline) {
		t.Fatalf("次の試行 %v が録音期限 %v を過ぎていない（テストの前提）", e.clock.Now(), j.Deadline)
	}
	l := watchProgress(e.app, j.ID)
	e.runLive()
	if j := e.job(englishShow.Name); j.State != domain.StateDone {
		t.Fatalf("保存をやり直した後の job = %+v", j)
	}
	evs := l.events()
	if got := fetched(evs); len(got) != 0 {
		t.Errorf("保存だけのやり直しで録れた長さを出した: %v（%+v）", got, evs)
	}
	if got, want := laterStages(evs), []domain.Stage{domain.StageSave, domain.StageVerify, domain.StageSave}; !slices.Equal(got, want) {
		t.Errorf("段階 = %v, want %v", got, want)
	}
	if len(evs) == 0 || evs[len(evs)-1].ok {
		t.Errorf("試行の終わりに進捗を消していない: %+v", evs)
	}
}

// 録り直しの試行（done から retrying に戻したジョブ）も、ほかの試行と同じく、録れた長さを 0 から始めて（前に録ったときの
// 作業用フォルダは消してある）入手の間に進め、試行が終わると消える。
func TestProgressRerecord(t *testing.T) {
	e, j := newRerecordEnv(t)
	now := afterBroadcast.Add(time.Hour)
	e.clock.Set(now)
	if err := e.store.Rerecord(e.ctx, j.ID, now); err != nil {
		t.Fatal(err)
	}
	l := watchProgress(e.app, j.ID)
	if !e.runDue() {
		t.Fatal("録り直しが実行されない")
	}
	if got := e.job(suppinShow.Name); got.State != domain.StateDone {
		t.Fatalf("job = %+v", got)
	}
	evs := l.events()
	if got, want := fetched(evs), []time.Duration{0, time.Minute}; !slices.Equal(got, want) {
		t.Errorf("録れた長さの移り方 = %v, want %v（%+v）", got, want, evs)
	}
	if _, ok := e.app.JobProgress(j.ID); ok {
		t.Error("試行が終わった後に進捗が残っている")
	}
}
