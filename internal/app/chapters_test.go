package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
)

// chapterResult は、偽のチャプターの取得元が放送回に返すもの。
type chapterResult struct {
	chapters []radiko.Chapter
	err      error
}

// chapterFetcher は、*radiko.Client（偽のサーバーに繋ぐ）の Chapters だけを差し替えた Fetcher（ChapterSource を満たす）。
type chapterFetcher struct {
	*radiko.Client
	mu sync.Mutex
	// results は放送回の開始時刻 → 返すもの。無い放送回は ErrNoChapters。
	results map[time.Time]chapterResult
	// calls は呼ばれた「局 放送日 開始時刻」。
	calls []string
}

// set は放送回の開始時刻 start に返すものを r にする（nil なら ErrNoChapters に戻す）。
func (f *chapterFetcher) set(start time.Time, r *chapterResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r == nil {
		delete(f.results, start)
		return
	}
	f.results[start] = *r
}

// count は呼ばれた回数。
func (f *chapterFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *chapterFetcher) Chapters(ctx context.Context, stationID string, day, start time.Time) ([]radiko.Chapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s %s %s", stationID, day.Format("20060102"), start.In(domain.JST).Format("1504")))
	r, ok := f.results[start]
	if !ok {
		return nil, fmt.Errorf("%w: test", radiko.ErrNoChapters)
	}
	return r.chapters, r.err
}

// useChapters は設定 chapters をオンにし、チャプターの取得元を results を返す偽のものにする。ログは buf にも書く。
func (e *env) useChapters(results map[time.Time]chapterResult, buf *lockedBuffer) *chapterFetcher {
	e.t.Helper()
	if results == nil {
		results = map[time.Time]chapterResult{}
	}
	f := &chapterFetcher{Client: e.app.Fetcher.(*radiko.Client), results: results}
	e.app.Fetcher = f
	e.app.Chapters = true
	if buf != nil {
		e.app.Log = slog.New(slog.NewTextHandler(buf, nil))
	}
	return f
}

// 設定 chapters がオンでも、最初の試行は遅らせない（ADR 0027）。保存のときに放送回ごとにチャプターを取り、
// 分割番組をつないだファイルでは、放送回（パート）ごとに格納の入力の番号を付けて渡す（ファイルの中の位置は media が
// 前のパートの長さだけずらす）。題は、曲の場面なら「♪ 曲名 / アーティスト」、それ以外は titleLong。
func TestChaptersJoined(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	f := e.useChapters(map[time.Time]chapterResult{
		flat1.start: {chapters: []radiko.Chapter{{From: 0, To: 30 * time.Second, Title: "オープニング", TitleLong: "オープニング : 挨拶"}}},
		flat2.start: {chapters: []radiko.Chapter{
			{From: 0, To: 10 * time.Second, Title: "トーク", TitleLong: ""},
			{From: 10 * time.Second, To: 50 * time.Second, Title: "曲", TitleLong: "曲 : 何か", IsMusic: true, MusicTitle: "歌", MusicArtist: "歌手"},
		}},
	}, nil)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	fj := e.job(flatShow.Name)
	if want := flat2.end.Add(150*time.Second + 2*time.Minute); !fj.NextAttemptAt.Equal(want) {
		t.Fatalf("NextAttemptAt = %v, want %v", fj.NextAttemptAt, want)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	if fj = e.job(flatShow.Name); fj.State != domain.StateDone || fj.Chapters != domain.ChaptersAdded || !fj.ChapterCheckAt.IsZero() {
		t.Fatalf("job = %+v", fj)
	}
	if want := []string{"TBS 20261007 0830", "TBS 20261007 1000"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	want := []media.Chapter{
		{Input: 0, Start: 0, Title: "オープニング : 挨拶"},
		{Input: 1, Start: 0, Title: "トーク"},
		{Input: 1, Start: 10 * time.Second, Title: "♪ 歌 / 歌手"},
	}
	if len(e.media.chapters) != 1 || !slices.Equal(e.media.chapters[0], want) {
		t.Errorf("chapters = %+v, want %+v", e.media.chapters, want)
	}
}

// チャプターが取れなくても（404・空・eventId が無い・通信の失敗）、チャプター無しで保存して録音は成功になり、Info のログに
// 局・放送回・理由が出る。取れたパートの分は入れる。通信の失敗で取れなかった放送回があるので、保存から 30 分後にチャプターの
// API を引くまで待つ（ADR 0027）。
func TestChaptersUnavailable(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	var buf lockedBuffer
	e.useChapters(map[time.Time]chapterResult{
		flat1.start: {err: fmt.Errorf("%w: boom", radiko.ErrTemporary)},
		flat2.start: {chapters: []radiko.Chapter{{From: 0, TitleLong: "取れたパート"}}},
	}, &buf)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	fj := e.job(flatShow.Name)
	if fj.State != domain.StateDone || fj.Chapters != domain.ChaptersWaiting || !fj.ChapterCheckAt.Equal(afterBroadcast.Add(30*time.Minute)) {
		t.Fatalf("job = %+v", fj)
	}
	if want := map[string]domain.AiringChapterWait{airingOf(flat1).Key(): domain.AiringChapterPending}; !maps.Equal(fj.ChapterWaits, want) {
		t.Errorf("ChapterWaits = %v, want %v", fj.ChapterWaits, want)
	}
	if as := e.attempts(fj.ID); len(as) != 1 || as[0].FailedStage != "" {
		t.Errorf("attempts = %+v", as)
	}
	if want := []media.Chapter{{Input: 1, Start: 0, Title: "取れたパート"}}; len(e.media.chapters) != 1 || !slices.Equal(e.media.chapters[0], want) {
		t.Errorf("chapters = %+v", e.media.chapters)
	}
	log := buf.String()
	line := ""
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "チャプターを取れない") {
			line = l
		}
	}
	for _, s := range []string{"level=INFO", "station=TBS", "start=2026-10-07T08:30:00", "reason=", "通信の失敗", fmt.Sprintf("job=%d", fj.ID)} {
		if !strings.Contains(line, s) {
			t.Errorf("ログの行に %q が無い: %q", s, line)
		}
	}
}

// 取れない理由ごとに、Info のログの理由を分ける。どれもチャプター無し（nil）。404 と通信の失敗・読めない応答は待つ（ChapterPending）、
// 空と eventId が無いのは待たない（ChapterAbsent）。
func TestFetchChaptersReasons(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	var buf lockedBuffer
	a1 := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 8, 30), End: jst(2026, 10, 7, 9, 0)}
	a2 := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 9, 0), End: jst(2026, 10, 7, 9, 30)}
	a3 := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 9, 30), End: jst(2026, 10, 7, 10, 0)}
	a4 := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 8, 1, 0), End: jst(2026, 10, 8, 2, 0)} // 放送日は 10-07
	f := e.useChapters(map[time.Time]chapterResult{
		a2.Start: {chapters: []radiko.Chapter{}},
		a3.Start: {err: fmt.Errorf("%w: x", radiko.ErrNoEvent)},
		a4.Start: {err: errors.New("読めない")},
	}, &buf)
	got, fetched := e.app.fetchChapters(e.ctx, e.app.log(), []domain.Airing{a1, a2, a3, a4})
	if got != nil {
		t.Errorf("chapters = %+v", got)
	}
	if want := []domain.ChapterFetch{domain.ChapterPending, domain.ChapterAbsent, domain.ChapterAbsent, domain.ChapterPending}; !slices.Equal(fetched, want) {
		t.Errorf("fetched = %v, want %v", fetched, want)
	}
	if want := []string{"TBS 20261007 0830", "TBS 20261007 0900", "TBS 20261007 0930", "TBS 20261007 0100"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q（深夜の放送回は前の日の放送日）", f.calls, want)
	}
	log := buf.String()
	for _, reason := range []string{"404", "reason=空", "eventId", "通信の失敗"} {
		if !strings.Contains(log, reason) {
			t.Errorf("ログに %q が無い: %s", reason, log)
		}
	}
}

// 設定 chapters がオフなら、チャプターを取りに行かず、最初の試行も遅らせない。
func TestChaptersOff(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	f := e.useChapters(nil, nil)
	e.app.Chapters = false
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if fj := e.job(flatShow.Name); !fj.NextAttemptAt.Equal(flat2.end.Add(150*time.Second + 2*time.Minute)) {
		t.Fatalf("NextAttemptAt = %v", fj.NextAttemptAt)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	if len(f.calls) != 0 || len(e.media.chapters) != 1 || e.media.chapters[0] != nil {
		t.Errorf("calls = %q, chapters = %+v", f.calls, e.media.chapters)
	}
	if fj := e.job(flatShow.Name); fj.Chapters != domain.ChaptersNone || !fj.ChapterCheckAt.IsZero() {
		t.Errorf("取りに行っていないジョブのチャプターの値は値なし: %+v", fj)
	}
}

// airingOf は偽の番組表の項目 p の放送回（TBS）。Key を引くのに使う。
func airingOf(p prog) domain.Airing {
	return domain.Airing{StationID: "TBS", Start: p.start, End: p.end}
}

// newChapterWaitEnv は、ふらっと（2 つの放送回をつなぐ）を録り終えた環境を返す。flat1 のチャプターは 404（まだ無い）、flat2 は
// 取れる。ジョブはチャプターを待っている。
func newChapterWaitEnv(t *testing.T) (*env, *chapterFetcher, domain.Job) {
	t.Helper()
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	f := e.useChapters(map[time.Time]chapterResult{
		flat2.start: {chapters: []radiko.Chapter{{From: 0, TitleLong: "後半"}}},
	}, nil)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone || j.Chapters != domain.ChaptersWaiting || j.OutputPath == "" {
		t.Fatalf("job = %+v", j)
	}
	return e, f, j
}

// 最初の試行でチャプターが 404 だった放送回は、チャプター無しで保存された後、待っている放送回のチャプターの API だけを 30 分おきに
// 引き、できた時点のチャプターの取り直しでファイルにチャプターが入る。取り直しは radiko から取り直して前の保存先を
// 上書きし、通知しない。
func TestChapterRefetchAddsChapters(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	prev := j.OutputPath
	kinds := e.kinds()
	calls := f.count()

	// 引く時刻の前は引かない。
	e.clock.Set(afterBroadcast.Add(29 * time.Minute))
	if e.runDue() || f.count() != calls {
		t.Fatalf("引く時刻の前に引いた: calls %d → %d", calls, f.count())
	}
	// 引く時刻に、待っている放送回（flat1）だけを引く。まだ 404 なので待ち続ける。
	e.clock.Set(afterBroadcast.Add(30 * time.Minute))
	if e.runDue() {
		t.Fatal("まだ 404 なのに試行した")
	}
	if f.count() != calls+1 || f.calls[len(f.calls)-1] != "TBS 20261007 0830" {
		t.Fatalf("calls = %q", f.calls)
	}
	if got := e.job(flatShow.Name); got.State != domain.StateDone || got.Chapters != domain.ChaptersWaiting ||
		!got.ChapterCheckAt.Equal(afterBroadcast.Add(time.Hour)) {
		t.Fatalf("待ち続ける: %+v", got)
	}

	// チャプターができた。次に引く時刻に、チャプターの取り直しの試行を起こして録り直す。
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	e.clock.Set(afterBroadcast.Add(time.Hour))
	if !e.runDue() {
		t.Fatal("チャプターの取り直しが実行されない")
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersAdded || got.ChapterRefetch || got.OutputPath != prev ||
		!got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
		t.Fatalf("取り直しの後: %+v", got)
	}
	if n := e.fake.playlistCount(flat1.start); n != 2 {
		t.Errorf("flat1 の入手の回数 = %d, want 2（取り直す）", n)
	}
	want := []media.Chapter{{Input: 0, Start: 0, Title: "前半"}, {Input: 1, Start: 0, Title: "後半"}}
	if last := e.media.chapters[len(e.media.chapters)-1]; !slices.Equal(last, want) {
		t.Errorf("chapters = %+v, want %+v", last, want)
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("チャプターの取り直しで通知した: %v → %v", kinds, after)
	}
}

// チャプターの無い局の放送回は、最後の放送回の終了から 6 時間で打ち切り、入らなかったにして、それ以上取りに行かない。
// 打ち切りも通知しない。
func TestChapterWaitGivesUpAtCutoff(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, flatShow.Name)
	f := e.useChapters(nil, nil) // どの放送回も 404
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	kinds := e.kinds()
	cutoff := suppin.end.Add(6 * time.Hour)
	for now := afterBroadcast; now.Before(cutoff.Add(2 * time.Hour)); now = now.Add(10 * time.Minute) {
		e.clock.Set(now)
		if e.runDue() {
			t.Fatalf("%v に試行した", now)
		}
	}
	got := e.job(suppinShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
		t.Fatalf("打ち切りの後: %+v", got)
	}
	// 保存のとき 1 回と、12:30 から 17:00 まで 30 分おきの 10 回と、打ち切りの 17:01 の 1 回。
	if n := f.count(); n != 12 {
		t.Errorf("calls = %d, want 12: %q", n, f.calls)
	}
	n := f.count()
	e.clock.Set(cutoff.Add(24 * time.Hour))
	e.runDue()
	if f.count() != n {
		t.Errorf("打ち切りの後に取りに行った: %q", f.calls[n:])
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("打ち切りで通知した: %v → %v", kinds, after)
	}
}

// 打ち切りの時点で一部の放送回だけ取れるようになっていれば、そのとき 1 回だけチャプターの取り直しを起こす。取り直しの保存は
// 打ち切りを過ぎているので待たず、入らなかったにする（一部だけ入った）。
func TestChapterRefetchPartialAtCutoff(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	f := e.useChapters(nil, nil) // 最初はどの放送回も 404
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	f.set(flat2.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "後半"}}})
	cutoff := flat2.end.Add(6 * time.Hour)
	refetched := 0
	for now := afterBroadcast.Add(30 * time.Minute); !now.After(cutoff); now = now.Add(30 * time.Minute) {
		if now.Add(30 * time.Minute).After(cutoff) {
			now = cutoff
		}
		e.clock.Set(now)
		if e.runDue() {
			refetched++
			if !now.Equal(cutoff) {
				t.Errorf("打ち切りの前（%v）に取り直した", now)
			}
		}
	}
	if refetched != 1 {
		t.Fatalf("取り直しの回数 = %d, want 1", refetched)
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() {
		t.Fatalf("取り直しの後: %+v", got)
	}
	if want := []media.Chapter{{Input: 1, Start: 0, Title: "後半"}}; !slices.Equal(e.media.chapters[len(e.media.chapters)-1], want) {
		t.Errorf("chapters = %+v", e.media.chapters)
	}
}

// 空（200 で 0 件）と eventId が無いときは待たず、入らなかったにする。
func TestChaptersNoWaitWhenEmptyOrNoEvent(t *testing.T) {
	for name, r := range map[string]chapterResult{
		"空":           {chapters: []radiko.Chapter{}},
		"eventId が無い": {err: fmt.Errorf("%w: x", radiko.ErrNoEvent)},
	} {
		t.Run(name, func(t *testing.T) {
			e := newFlowEnv(t)
			e.store.DeleteShow(e.ctx, flatShow.Name)
			f := e.useChapters(map[time.Time]chapterResult{suppin.start: r}, nil)
			if err := e.app.Startup(e.ctx); err != nil {
				t.Fatal(err)
			}
			if !e.runDue() {
				t.Fatal("実行されない")
			}
			got := e.job(suppinShow.Name)
			if got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
				t.Fatalf("job = %+v", got)
			}
			n := f.count()
			e.clock.Set(afterBroadcast.Add(time.Hour))
			e.runDue()
			if f.count() != n {
				t.Errorf("待たないはずが引いた: %q", f.calls)
			}
		})
	}
}

// チャプターの取り直しで起こした試行が録れなければ（恒久的に取れない）、録音できなかったにせず録音済みに戻し、前のファイル・
// 保存先・欠落の印を前のまま残す。通知はしない。
func TestChapterRefetchFailureRevertsToDone(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	prev := j.OutputPath
	before, err := os.ReadFile(prev)
	if err != nil {
		t.Fatal(err)
	}
	kinds := e.kinds()
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.clock.Set(afterBroadcast.Add(30 * time.Minute))
	if !e.runDue() {
		t.Fatal("チャプターの取り直しが実行されない")
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.OutputPath != prev || len(got.Missing) != 0 || got.ChapterRefetch ||
		got.Chapters != domain.ChaptersNotAdded {
		t.Fatalf("録れなかった取り直しの後: %+v", got)
	}
	if after, err := os.ReadFile(prev); err != nil || string(after) != string(before) {
		t.Errorf("前のファイルが変わった: %v", err)
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("通知した: %v → %v", kinds, after)
	}
	// 録音済みに戻した後は、もう取りに行かない。
	n := f.count()
	e.clock.Set(afterBroadcast.Add(2 * time.Hour))
	if e.runDue() || f.count() != n {
		t.Errorf("戻した後に取りに行った: %q", f.calls[n:])
	}
}

// チャプターの取り直しで起こした試行が、録音期限の時点で一部の放送回しか取れていなくても、録音済みに戻して前のファイル・保存先・
// 欠落の印を残す（人が頼んだ録り直しなら録音できなかったにするところ。TestRerecordPartialAtDeadlineKeepsPrev）。再試行の間も、
// 期限間近・欠落ありを含めて通知しない。
func TestChapterRefetchPartialAtDeadlineRevertsToDone(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	prev := j.OutputPath
	before, err := os.ReadFile(prev)
	if err != nil {
		t.Fatal(err)
	}
	kinds := e.kinds()
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	e.clock.Set(afterBroadcast.Add(30 * time.Minute))
	if !e.runDue() {
		t.Fatal("チャプターの取り直しが実行されない")
	}
	if got := e.job(flatShow.Name); got.State != domain.StateRetrying || !got.ChapterRefetch {
		t.Fatalf("1 回目の試行の後: %+v", got)
	}
	e.clock.Set(j.Deadline.Add(-time.Hour))
	if err := e.app.CheckNotifications(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.OutputPath != prev || len(got.Missing) != 0 || got.ChapterRefetch ||
		got.Chapters != domain.ChaptersNotAdded {
		t.Fatalf("録音期限の後: %+v", got)
	}
	if after, err := os.ReadFile(prev); err != nil || string(after) != string(before) {
		t.Errorf("前のファイルが変わった: %v", err)
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("通知した: %v → %v", kinds, after)
	}
}

// 待っている間に人が録り直したら待ちが消え（入らなかったにする）、その保存でまた取れなければ待ち直す。人が頼んだ録り直しは
// 録音済みを通知する（ADR 0025 のまま）。
func TestChapterWaitClearedByRerecord(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	recorded := countKind(e.kinds(), notify.KindRecorded)
	now := afterBroadcast.Add(10 * time.Minute)
	e.clock.Set(now)
	if err := e.store.Rerecord(e.ctx, j.ID, now); err != nil {
		t.Fatal(err)
	}
	if got := e.job(flatShow.Name); got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 || got.ChapterRefetch {
		t.Fatalf("録り直しを受け付けた後: %+v", got)
	}
	n := f.count()
	if !e.runDue() {
		t.Fatal("録り直しが実行されない")
	}
	if f.count() != n+2 {
		t.Errorf("録り直しの保存でチャプターを取り直していない: %q", f.calls[n:])
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersWaiting || !got.ChapterCheckAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("録り直しの後: %+v", got)
	}
	if c := countKind(e.kinds(), notify.KindRecorded); c != recorded+1 {
		t.Errorf("録音済みの通知 = %d, want %d", c, recorded+1)
	}
}

// API を引くときに設定 chapters がオフなら、引かずに待ちを消す（入らなかったにする）。
func TestChapterWaitClearedBySettingOff(t *testing.T) {
	e, f, _ := newChapterWaitEnv(t)
	e.app.Chapters = false
	n := f.count()
	e.clock.Set(afterBroadcast.Add(30 * time.Minute))
	if e.runDue() || f.count() != n {
		t.Fatalf("設定がオフなのに引いた: %q", f.calls[n:])
	}
	if got := e.job(flatShow.Name); got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
		t.Fatalf("job = %+v", got)
	}
}

// 曲の場面の題は「♪ 曲名 / アーティスト」。アーティストが空なら「♪ 曲名」、曲名が空なら titleLong。それ以外は titleLong（空なら title）。
func TestChapterTitle(t *testing.T) {
	tests := []struct {
		ch   radiko.Chapter
		want string
	}{
		{radiko.Chapter{Title: "t", TitleLong: "long"}, "long"},
		{radiko.Chapter{Title: "t"}, "t"},
		{radiko.Chapter{TitleLong: "long", IsMusic: true, MusicTitle: "歌", MusicArtist: "歌手"}, "♪ 歌 / 歌手"},
		{radiko.Chapter{TitleLong: "long", IsMusic: true, MusicTitle: "歌"}, "♪ 歌"},
		{radiko.Chapter{TitleLong: "long", IsMusic: true, MusicArtist: "歌手"}, "long"},
	}
	for _, tt := range tests {
		if got := chapterTitle(tt.ch); got != tt.want {
			t.Errorf("chapterTitle(%+v) = %q, want %q", tt.ch, got, tt.want)
		}
	}
}
