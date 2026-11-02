package app

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
	"github.com/t-seki/rajikura/internal/radiko"
)

// newRedoEnv は、ふらっと（2 つの放送回をつなぐ）を設定 chapters がオフのまま録り終えた環境を返す（チャプターの値は値なし。
// チャプターを埋め込むようになる前に録ったファイルに当たる）。設定はオフのままにする。
func newRedoEnv(t *testing.T) (*env, *chapterFetcher, domain.Job) {
	t.Helper()
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	f := e.useChapters(nil, nil)
	e.app.Chapters = false
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone || j.Chapters != domain.ChaptersNone || j.OutputPath == "" || f.count() != 0 {
		t.Fatalf("job = %+v, calls = %q", j, f.calls)
	}
	return e, f, j
}

// 人が頼んだチャプターの入れ直しは、録るジョブが無いときに録音のループが 1 件ずつ行い、radiko から音声を取らずに、保存したファイルに
// 保存した放送回のチャプターを書き込む（ADR 0033）。設定 chapters がオフでも行う。試行は起こさず（done のまま、試行も増えない）、
// 書き込みの履歴に「チャプターの入れ直し」で 1 行残し、頼みを消す。置き換えたら再スキャンを頼み、通知はしない。
func TestChapterRedoWritesSavedFile(t *testing.T) {
	e, f, j := newRedoEnv(t)
	r := &fakeRescanner{}
	e.app.Rescanner = r
	before, err := os.ReadFile(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	kinds := e.kinds()
	attempts := len(e.attempts(j.ID))
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "前半"}}})
	f.set(flat2.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "後半"}, {From: time.Minute, TitleLong: "終わり"}}})

	// 頼むまでは何もしない。
	if e.runDue() || f.count() != 0 {
		t.Fatalf("頼む前に取りに行った: %q", f.calls)
	}
	at := afterBroadcast.Add(time.Hour)
	e.clock.Set(at)
	if err := e.store.RequestChapterRedo(e.ctx, j.ID, at); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("チャプターの入れ直しをしない")
	}
	if want := []string{"TBS 20261007 0830", "TBS 20261007 1000"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	got := e.job(flatShow.Name)
	if got.State != domain.StateDone || got.Chapters != domain.ChaptersAdded || got.OutputPath != j.OutputPath || got.Attempts != j.Attempts ||
		!got.ChapterRedoAt.IsZero() || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
		t.Fatalf("入れ直しの後: %+v", got)
	}
	if n := len(e.attempts(j.ID)); n != attempts {
		t.Errorf("試行の数 = %d, want %d（増えない）", n, attempts)
	}
	if n := e.fake.playlistCount(flat1.start); n != 1 {
		t.Errorf("flat1 の入手の回数 = %d, want 1（音声は取り直さない）", n)
	}
	w := e.lastWrite()
	want := []media.Chapter{{Input: 0, Start: 0, Title: "前半"}, {Input: 1, Start: 0, Title: "後半"}, {Input: 1, Start: time.Minute, Title: "終わり"}}
	if w.in != j.OutputPath || !slices.Equal(w.chapters, want) {
		t.Errorf("WriteChapters = %+v, want %+v", w, want)
	}
	after, err := os.ReadFile(j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fakeAudio(after), before) || !strings.HasSuffix(string(after), fakeChapterMark+"前半|後半|終わり") {
		t.Errorf("保存したファイル = %q", after)
	}
	ws := e.chapterWrites(j.ID)
	if len(ws) != 1 || ws[0].By != domain.ChapterWriteByRedo || ws[0].Result != domain.ChapterWritten || ws[0].Count != 3 || !ws[0].At.Equal(at) {
		t.Errorf("書き込みの履歴 = %+v", ws)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
	if after := e.kinds(); !slices.Equal(after, kinds) {
		t.Errorf("チャプターの入れ直しで通知した: %v → %v", kinds, after)
	}
	// 頼みは 1 回で消える。
	n := f.count()
	if e.runDue() || f.count() != n {
		t.Errorf("頼みが消えていない: %q", f.calls[n:])
	}

	// ジョブ 1 件の指定なら、入ったのジョブでもファイルのチャプターを置き換える。
	f.set(flat1.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "新しい前半"}}})
	if err := e.store.RequestChapterRedo(e.ctx, j.ID, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !e.runDue() {
		t.Fatal("入ったのジョブの入れ直しをしない")
	}
	if after, _ := os.ReadFile(j.OutputPath); !strings.HasSuffix(string(after), fakeChapterMark+"新しい前半|後半|終わり") || !bytes.Equal(fakeAudio(after), before) {
		t.Errorf("置き換えた後のファイル = %q", after)
	}
	if got := e.job(flatShow.Name); got.Chapters != domain.ChaptersAdded || len(e.chapterWrites(j.ID)) != 2 {
		t.Errorf("置き換えた後: %+v", got)
	}
}

// 入れ直しで、保存した放送回の全部でチャプターを取れなければ、ファイルは書かず、取れなかったので書かなかったと履歴に残す。
// 一部だけ取れたら、取れた分を書き込む。どちらも、取れなかった放送回が 404（まだできていないかもしれない）でもチャプターの待ちは
// 起こさず、チャプターの値は ADR 0027 の決まりで付け直す（値なしの古いジョブにも入らなかったが付く。ADR 0033）。
func TestChapterRedoNotFetchedOrPartial(t *testing.T) {
	t.Run("全部取れない", func(t *testing.T) {
		e, f, j := newRedoEnv(t)
		before, _ := os.ReadFile(j.OutputPath)
		if err := e.store.RequestChapterRedo(e.ctx, j.ID, afterBroadcast); err != nil {
			t.Fatal(err)
		}
		if !e.runDue() {
			t.Fatal("入れ直しをしない")
		}
		if f.count() != 2 || e.writeCount() != 0 {
			t.Errorf("calls = %q, WriteChapters = %d", f.calls, e.writeCount())
		}
		if after, _ := os.ReadFile(j.OutputPath); !bytes.Equal(after, before) {
			t.Error("ファイルが変わった")
		}
		got := e.job(flatShow.Name)
		if got.State != domain.StateDone || got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() ||
			len(got.ChapterWaits) != 0 || !got.ChapterRedoAt.IsZero() {
			t.Errorf("job = %+v", got)
		}
		if ws := e.chapterWrites(j.ID); len(ws) != 1 || ws[0].By != domain.ChapterWriteByRedo || ws[0].Result != domain.ChapterNotFetched {
			t.Errorf("書き込みの履歴 = %+v", ws)
		}
	})
	t.Run("一部だけ取れた", func(t *testing.T) {
		e, f, j := newRedoEnv(t)
		f.set(flat2.start, &chapterResult{chapters: []radiko.Chapter{{From: 0, TitleLong: "後半"}}})
		if err := e.store.RequestChapterRedo(e.ctx, j.ID, afterBroadcast); err != nil {
			t.Fatal(err)
		}
		if !e.runDue() {
			t.Fatal("入れ直しをしない")
		}
		if w := e.lastWrite(); !slices.Equal(w.chapters, []media.Chapter{{Input: 1, Start: 0, Title: "後半"}}) {
			t.Errorf("WriteChapters = %+v", w)
		}
		got := e.job(flatShow.Name)
		if got.Chapters != domain.ChaptersNotAdded || !got.ChapterCheckAt.IsZero() || len(got.ChapterWaits) != 0 {
			t.Errorf("job = %+v", got)
		}
		if ws := e.chapterWrites(j.ID); len(ws) != 1 || ws[0].Result != domain.ChapterWritten || ws[0].Count != 1 {
			t.Errorf("書き込みの履歴 = %+v", ws)
		}
		// 待ちを起こしていないので、時刻が過ぎても取りに行かない。
		n := f.count()
		e.clock.Set(afterBroadcast.Add(time.Hour))
		if e.runDue() || f.count() != n {
			t.Errorf("待ちを起こした: %q", f.calls[n:])
		}
	})
}
