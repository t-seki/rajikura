package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
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

// fakeChapterMark は、偽の WriteChapters が書いたファイルで、音声（入力をつないだもの）とチャプターの間に置く印。
const fakeChapterMark = "\nCHAPTERS:"

// fakeChapterWrite は、偽の WriteChapters に渡されたもの。
type fakeChapterWrite struct {
	in       string
	chapters []media.Chapter
	parts    []time.Duration
}

// fakeChapterWriter は、fakeMedia（fake_test.go）の ChapterWriter の分の記録と、起こす失敗。
type fakeChapterWriter struct {
	mu     sync.Mutex
	writes []fakeChapterWrite
	// writeErr があれば WriteChapters はそれを返す。corrupt なら、書いたファイルの音声を元と変える（SameAudio が一致しない）。
	writeErr error
	corrupt  bool
}

// fakeChapterWriters は fakeMedia ごとの fakeChapterWriter（fakeMedia のフィールドに持たせないので、ここで引く）。
var fakeChapterWriters sync.Map

// chapterWriter は m の fakeChapterWriter。
func (m *fakeMedia) chapterWriter() *fakeChapterWriter {
	v, _ := fakeChapterWriters.LoadOrStore(m, &fakeChapterWriter{})
	return v.(*fakeChapterWriter)
}

// fakeAudio は、偽の格納が書いたファイルの中身 b の、音声の部分（WriteChapters が書いたチャプターを除く）。
func fakeAudio(b []byte) []byte {
	if i := bytes.Index(b, []byte(fakeChapterMark)); i >= 0 {
		return b[:i]
	}
	return b
}

// InputLengths は、Duration と同じく、入力ごとに中身の "[" の数 × 5 秒を返す（ChapterWriter）。
func (m *fakeMedia) InputLengths(ctx context.Context, inputs []string) ([]time.Duration, error) {
	out := make([]time.Duration, len(inputs))
	for i, in := range inputs {
		d, err := os.ReadFile(in)
		if err != nil {
			return nil, err
		}
		out[i] = time.Duration(bytes.Count(d, []byte("["))) * 5 * time.Second
	}
	return out, nil
}

// WriteChapters は、in の音声の部分の後ろに印とチャプターの題を書き、渡されたものを記録する（ChapterWriter）。
func (m *fakeMedia) WriteChapters(ctx context.Context, in, out string, chapters []media.Chapter, parts []time.Duration) (int, error) {
	w := m.chapterWriter()
	w.mu.Lock()
	w.writes = append(w.writes, fakeChapterWrite{in: in, chapters: chapters, parts: parts})
	werr, corrupt := w.writeErr, w.corrupt
	w.mu.Unlock()
	if werr != nil {
		return 0, werr
	}
	d, err := os.ReadFile(in)
	if err != nil {
		return 0, err
	}
	audio := slices.Clone(fakeAudio(d))
	if corrupt {
		audio = append([]byte("壊れた"), audio...)
	}
	titles := make([]string, len(chapters))
	for i, ch := range chapters {
		titles[i] = ch.Title
	}
	return len(chapters), os.WriteFile(out, append(audio, []byte(fakeChapterMark+strings.Join(titles, "|"))...), 0o644)
}

// SameAudio は、a と b の音声の部分が同じか（ChapterWriter）。
func (m *fakeMedia) SameAudio(ctx context.Context, a, b string) error {
	x, err := os.ReadFile(a)
	if err != nil {
		return err
	}
	y, err := os.ReadFile(b)
	if err != nil {
		return err
	}
	if !bytes.Equal(fakeAudio(x), fakeAudio(y)) {
		return fmt.Errorf("%w: fake", media.ErrAudioMismatch)
	}
	return nil
}

// lastWrite は、最後に WriteChapters に渡されたもの（無ければ失敗）。
func (e *env) lastWrite() fakeChapterWrite {
	e.t.Helper()
	w := e.media.chapterWriter()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.writes) == 0 {
		e.t.Fatal("WriteChapters が呼ばれていない")
	}
	return w.writes[len(w.writes)-1]
}

// writeCount は WriteChapters が呼ばれた回数。
func (e *env) writeCount() int {
	w := e.media.chapterWriter()
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

// chapterWrites は、ジョブ id のチャプターの書き込みの履歴。
func (e *env) chapterWrites(id int64) []domain.ChapterWrite {
	e.t.Helper()
	ws, err := e.store.ChapterWrites(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ws
}

// partials は、dir の下に残った *.partial.m4a。
func partials(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".partial.m4a") {
			out = append(out, p)
		}
		return nil
	})
	return out
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
// 引き、できた時点のチャプターの取り直しで、保存したファイルにチャプターが書き込まれる（ADR 0033）。radiko から音声は取らず、
// 試行も起こさない（ジョブは done のまま、試行は増えない）。書き込みは保存した放送回の全部のチャプターを取り直し、連結した
// ファイルの 2 つ目のパートのチャプターは、保存のときに DB に残したパートの長さでずらす。置き換えたら再スキャンを頼み、
// 書き込みの履歴に 1 行残す。通知はしない。
func TestChapterRefetchWritesSavedFile(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	r := &fakeRescanner{}
	e.app.Rescanner = r
	prev := j.OutputPath
	before, err := os.ReadFile(prev)
	if err != nil {
		t.Fatal(err)
	}
	kinds := e.kinds()
	calls := f.count()
	attempts := len(e.attempts(j.ID))
	// 保存のときに、パート（放送回）ごとの長さを DB に残している。
	lengths, err := e.store.PartLengths(e.ctx, j.ID)
	if err != nil || len(lengths) != 2 || lengths[airingOf(flat1).Key()] <= 0 || lengths[airingOf(flat2).Key()] <= 0 {
		t.Fatalf("PartLengths = %v, %v", lengths, err)
	}

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

	// チャプターができた。次に引く時刻に、保存した放送回のチャプターを取り直して、保存したファイルに書き込む（試行は起こさない）。
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	e.clock.Set(afterBroadcast.Add(time.Hour))
	if e.runDue() {
		t.Fatal("チャプターの取り直しで試行を起こした")
	}
	if want := []string{"TBS 20261007 0830", "TBS 20261007 0830", "TBS 20261007 1000"}; !slices.Equal(f.calls[calls+1:], want) {
		t.Errorf("calls = %q, want %q", f.calls[calls+1:], want)
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersAdded || got.OutputPath != prev ||
		!got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 || got.Attempts != j.Attempts {
		t.Fatalf("取り直しの後: %+v", got)
	}
	if n := len(e.attempts(j.ID)); n != attempts {
		t.Errorf("試行の数 = %d, want %d（増えない）", n, attempts)
	}
	if n := e.fake.playlistCount(flat1.start); n != 1 {
		t.Errorf("flat1 の入手の回数 = %d, want 1（音声は取り直さない）", n)
	}
	w := e.lastWrite()
	want := []media.Chapter{{Input: 0, Start: 0, Title: "前半"}, {Input: 1, Start: 0, Title: "後半"}}
	wantParts := []time.Duration{lengths[airingOf(flat1).Key()], lengths[airingOf(flat2).Key()]}
	if w.in != prev || !slices.Equal(w.chapters, want) || !slices.Equal(w.parts, wantParts) {
		t.Errorf("WriteChapters = %+v, want chapters %+v parts %v", w, want, wantParts)
	}
	after, err := os.ReadFile(prev)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fakeAudio(after), before) || !strings.HasSuffix(string(after), fakeChapterMark+"前半|後半") {
		t.Errorf("保存したファイル = %q", after)
	}
	if p := partials(t, e.app.Library); len(p) != 0 {
		t.Errorf("partial が残った: %q", p)
	}
	ws := e.chapterWrites(j.ID)
	if len(ws) != 1 || ws[0].By != domain.ChapterWriteByRefetch || ws[0].Result != domain.ChapterWritten || ws[0].Count != 2 ||
		!ws[0].At.Equal(afterBroadcast.Add(time.Hour)) {
		t.Errorf("書き込みの履歴 = %+v", ws)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("チャプターの取り直しで通知した: %v → %v", kinds, after)
	}
	// 書き込んだ後は、もう取りに行かない。
	n := f.count()
	e.clock.Set(afterBroadcast.Add(3 * time.Hour))
	if e.runDue() || f.count() != n {
		t.Errorf("書き込んだ後に取りに行った: %q", f.calls[n:])
	}
}

// パートの長さが DB に残っていない（この決定より前に保存した）連結したファイルでは、2 つ目のパートのチャプターを、番組表の
// 放送時間（終了 − 開始）でずらす（ADR 0033）。
func TestChapterRefetchGuidePartsWithoutLengths(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	if err := e.store.SetPartLengths(e.ctx, j.ID, nil); err != nil {
		t.Fatal(err)
	}
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	e.clock.Set(afterBroadcast.Add(30 * time.Minute))
	e.runDue()
	if want := []time.Duration{flat1.end.Sub(flat1.start), flat2.end.Sub(flat2.start)}; !slices.Equal(e.lastWrite().parts, want) {
		t.Errorf("parts = %v, want %v", e.lastWrite().parts, want)
	}
	if got := e.job(flatShow.Name); got.Chapters != domain.ChaptersAdded {
		t.Errorf("job = %+v", got)
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

// 打ち切りの時点で一部の放送回だけ取れるようになっていれば、そのとき 1 回だけ、取れた分を保存したファイルに書き込む。一部だけ
// 入ったので、入らなかったにする。試行は起こさない。
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
	for now := afterBroadcast.Add(30 * time.Minute); !now.After(cutoff); now = now.Add(30 * time.Minute) {
		if now.Add(30 * time.Minute).After(cutoff) {
			now = cutoff
		}
		e.clock.Set(now)
		if e.runDue() {
			t.Fatalf("%v に試行した", now)
		}
		if n := e.writeCount(); n > 0 && !now.Equal(cutoff) {
			t.Fatalf("打ち切りの前（%v）に書き込んだ", now)
		}
	}
	if n := e.writeCount(); n != 1 {
		t.Fatalf("書き込みの回数 = %d, want 1", n)
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || got.Attempts != 1 {
		t.Fatalf("書き込んだ後: %+v", got)
	}
	if want := []media.Chapter{{Input: 1, Start: 0, Title: "後半"}}; !slices.Equal(e.lastWrite().chapters, want) {
		t.Errorf("chapters = %+v", e.lastWrite().chapters)
	}
	if ws := e.chapterWrites(got.ID); len(ws) != 1 || ws[0].Result != domain.ChapterWritten || ws[0].Count != 1 {
		t.Errorf("書き込みの履歴 = %+v", ws)
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

// 書き込めないとき（音声のサンプルが元と一致しない・書き込みが失敗する・保存先にファイルが無い）は、元のファイルをそのまま残し、
// partial を残さない。ジョブは done のまま入らなかったにし、書き込みの履歴に失敗と理由を残す。通知はしない。音声のサンプルが
// 一致しなかったときは、ログに Error で出す。再スキャンは頼まない（ADR 0033）。
func TestChapterRefetchWriteFailureKeepsFile(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(e *env, j domain.Job)
		reason string
		level  string
	}{
		{"音声のサンプルが元と一致しない", func(e *env, j domain.Job) { e.media.chapterWriter().corrupt = true }, "audio samples differ", "level=ERROR"},
		{"書き込みが失敗する", func(e *env, j domain.Job) { e.media.chapterWriter().writeErr = errors.New("書けない") }, "書けない", "level=WARN"},
		{"保存先にファイルが無い", func(e *env, j domain.Job) {
			if err := os.Remove(j.OutputPath); err != nil {
				e.t.Fatal(err)
			}
		}, "保存先にファイルが無い", "level=WARN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, f, j := newChapterWaitEnv(t)
			var buf lockedBuffer
			e.app.Log = slog.New(slog.NewTextHandler(&buf, nil))
			r := &fakeRescanner{}
			e.app.Rescanner = r
			kinds := e.kinds()
			tt.setup(e, j)
			before, _ := os.ReadFile(j.OutputPath) // 保存先にファイルが無いときは nil
			f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
			e.clock.Set(afterBroadcast.Add(30 * time.Minute))
			if e.runDue() {
				t.Fatal("試行を起こした")
			}
			got := e.job(flatShow.Name)
			if got.State != domain.StateDone || got.OutputPath != j.OutputPath || got.Chapters != domain.ChaptersNotAdded ||
				!got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 || got.Attempts != j.Attempts {
				t.Fatalf("書き込めなかった後: %+v", got)
			}
			after, err := os.ReadFile(j.OutputPath)
			if before != nil && (err != nil || !bytes.Equal(after, before)) {
				t.Errorf("元のファイルが変わった: %v", err)
			}
			if before == nil && err == nil {
				t.Error("無かった保存先にファイルができた")
			}
			if p := partials(t, e.app.Library); len(p) != 0 {
				t.Errorf("partial が残った: %q", p)
			}
			ws := e.chapterWrites(j.ID)
			if len(ws) != 1 || ws[0].Result != domain.ChapterWriteFailed || !strings.Contains(ws[0].Error, tt.reason) {
				t.Errorf("書き込みの履歴 = %+v", ws)
			}
			line := ""
			for _, l := range strings.Split(buf.String(), "\n") {
				if strings.Contains(l, "元のファイルを残す") {
					line = l
				}
			}
			if !strings.Contains(line, tt.level) {
				t.Errorf("ログの行に %s が無い: %q", tt.level, line)
			}
			if r.calls != 0 {
				t.Errorf("再スキャンを頼んだ: %d", r.calls)
			}
			if after := e.kinds(); !slices.Equal(after, kinds) {
				t.Errorf("通知した: %v → %v", kinds, after)
			}
		})
	}
}

// 書き込もうとしたときに、取れていたはずの放送回が一時的に取れなければ（引いたときは取れ、書き込む前に取ると取れない）、ファイルは
// 書かず、書き込みの履歴にも残さず、待ち（印・待っている放送回）を残して、次に引く時刻を 30 分後にする。取れるようになった次の
// 見回りで書き込む（書き込みのやり直し。ADR 0038）。
func TestChapterRefetchRetriesWhenTransientFailure(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	// 引いたときは取れ、書き込む前に取ると取れない。
	e.app.Fetcher = &flakyChapters{chapterFetcher: f, okCalls: 1}
	before, _ := os.ReadFile(j.OutputPath)
	at := afterBroadcast.Add(30 * time.Minute)
	e.clock.Set(at)
	e.runDue()
	if n := e.writeCount(); n != 0 {
		t.Errorf("WriteChapters が呼ばれた: %d", n)
	}
	if after, _ := os.ReadFile(j.OutputPath); !bytes.Equal(after, before) {
		t.Error("ファイルが変わった")
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersWaiting || !got.ChapterCheckAt.Equal(at.Add(30*time.Minute)) ||
		!maps.Equal(got.ChapterWaits, j.ChapterWaits) {
		t.Errorf("job = %+v（待ちを残す）", got)
	}
	if ws := e.chapterWrites(j.ID); len(ws) != 0 {
		t.Errorf("書き込みの履歴 = %+v（やり直しで書かなかった回は残さない）", ws)
	}
	// 取れるようになった次の見回りで書く。
	e.app.Fetcher = f
	e.clock.Set(at.Add(30 * time.Minute))
	e.runDue()
	got = e.job(flatShow.Name)
	if got.Chapters != domain.ChaptersAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
		t.Errorf("job = %+v（書いた後）", got)
	}
	if ws := e.chapterWrites(j.ID); len(ws) != 1 || ws[0].Result != domain.ChapterWritten || ws[0].Count != 2 {
		t.Errorf("書き込みの履歴 = %+v", ws)
	}
}

// 取れていたはずの放送回が一時的に取れないままやり直しの上限（最後の放送回の終了から 12 時間）になったら、書かずに諦める。ファイルは
// 前のまま、書き込みの履歴は取れなかったので書かなかった 1 行だけ、印は入らなかった、待ちは消える。上限の前は待ち続け、通知しない。
func TestChapterRefetchGivesUpAtRetryLimit(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	f.set(flat2.start, nil) // 保存のときに取れた flat2 が、書き込みの前の取得で 404 を返し続ける
	before, _ := os.ReadFile(j.OutputPath)
	kinds := e.kinds()
	limit := flat2.end.Add(12 * time.Hour)
	for now := afterBroadcast.Add(10 * time.Minute); now.Before(limit); now = now.Add(10 * time.Minute) {
		e.clock.Set(now)
		if e.runDue() {
			t.Fatalf("%v に試行した", now)
		}
		got := e.job(flatShow.Name)
		if got.Chapters != domain.ChaptersWaiting || e.writeCount() != 0 || len(e.chapterWrites(j.ID)) != 0 {
			t.Fatalf("上限の前（%v）に待ちをやめた: %+v, writes %d", now, got, e.writeCount())
		}
	}
	e.clock.Set(limit)
	e.runDue()
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
		t.Fatalf("上限の後: %+v", got)
	}
	if e.writeCount() != 0 {
		t.Errorf("WriteChapters が呼ばれた: %d", e.writeCount())
	}
	if after, _ := os.ReadFile(j.OutputPath); !bytes.Equal(after, before) {
		t.Error("ファイルが変わった")
	}
	if ws := e.chapterWrites(j.ID); len(ws) != 1 || ws[0].By != domain.ChapterWriteByRefetch || ws[0].Result != domain.ChapterNotFetched {
		t.Errorf("書き込みの履歴 = %+v", ws)
	}
	n := f.count()
	e.clock.Set(limit.Add(24 * time.Hour))
	e.runDue()
	if f.count() != n {
		t.Errorf("諦めた後に取りに行った: %q", f.calls[n:])
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("通知した: %v → %v", kinds, after)
	}
}

// flakyChapters は、最初の okCalls 回だけ chapterFetcher のとおりに返し、その後は全部 404 にする Fetcher。
type flakyChapters struct {
	*chapterFetcher
	mu      sync.Mutex
	okCalls int
}

func (f *flakyChapters) Chapters(ctx context.Context, stationID string, day, start time.Time) ([]radiko.Chapter, error) {
	f.mu.Lock()
	ok := f.okCalls > 0
	f.okCalls--
	f.mu.Unlock()
	if ok {
		return f.chapterFetcher.Chapters(ctx, stationID, day, start)
	}
	return nil, fmt.Errorf("%w: test", radiko.ErrNoChapters)
}

// 待っている間に人が録り直したら待ちが消え（入らなかったにする）、その保存でまた取れなければ待ち直す。人が頼んだ録り直しは
// 録音済みを通知する（ADR 0025 のまま）。
func TestChapterWaitClearedByRerecord(t *testing.T) {
	e, f, j := newChapterWaitEnv(t)
	recorded := countKind(e.kinds(), notify.KindRecorded)
	now := afterBroadcast.Add(10 * time.Minute)
	e.clock.Set(now)
	if err := e.store.Rerecord(e.ctx, j.ID, domain.MembershipFree, nil, now); err != nil {
		t.Fatal(err)
	}
	if got := e.job(flatShow.Name); got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
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
