package media

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// placeChapters は、入力ごとのチャプターを、前の入力の長さだけずらしてファイルの中の時刻に置き、開始の順に並べる。
// 入力の外・題が空・その入力の長さ以上・ファイルの長さ以上のものは捨て、同じ開始は最初の 1 つだけを残す。
func TestPlaceChapters(t *testing.T) {
	// 入力 0 は 24000 サンプル（1 秒）、入力 1 は 48000 サンプル（2 秒）。ファイルの長さは 3000 ms。
	samples := []uint64{24000, 48000}
	got := placeChapters([]Chapter{
		{Input: 1, Start: 500 * time.Millisecond, Title: "後半の 2 つ目"},
		{Input: 0, Start: 0, Title: " 前半\nの頭 "},
		{Input: 1, Start: 0, Title: "後半の頭"},
		{Input: 1, Start: 0, Title: "同じ開始（捨てる）"},
		{Input: 0, Start: time.Second, Title: "入力の長さ（捨てる）"},
		{Input: 1, Start: 2 * time.Second, Title: "ファイルの長さ（捨てる）"},
		{Input: 2, Start: 0, Title: "入力の外（捨てる）"},
		{Input: 0, Start: 300 * time.Millisecond, Title: "  "},
	}, samples, 24000, 3000)
	want := []placedChapter{{0, "前半 の頭"}, {1000, "後半の頭"}, {1500, "後半の 2 つ目"}}
	if !slices.Equal(got, want) {
		t.Errorf("placeChapters = %v, want %v", got, want)
	}
	if got := chapterDurations(want, 3000); !slices.Equal(got, []uint64{1000, 500, 1500}) {
		t.Errorf("chapterDurations = %v", got)
	}
	// 最初のチャプターが 0 から始まらなくても、チャプタートラックの最初のサンプルは 0 から（ffmpeg と同じ）。
	if got := chapterDurations([]placedChapter{{400, "a"}, {1000, "b"}}, 3000); !slices.Equal(got, []uint64{1000, 2000}) {
		t.Errorf("chapterDurations (first not at 0) = %v", got)
	}
}

// 題は 255 バイトまで（chpl は長さを 1 バイトで持つ）。UTF-8 の文字の途中では切らない。
func TestChapterTitleTruncated(t *testing.T) {
	long := strings.Repeat("あ", 100) // 300 バイト
	got := chapterTitle(long)
	if len(got) != 255 || got != strings.Repeat("あ", 85) {
		t.Errorf("chapterTitle: %d bytes %q", len(got), got)
	}
}

func TestEscapeFFmetadata(t *testing.T) {
	if got, want := escapeFFmetadata(`a=b;c#d\e`), `a\=b\;c\#d\\e`; got != want {
		t.Errorf("escapeFFmetadata = %q, want %q", got, want)
	}
}

// testChapters は、2 つの入力（3 秒と 4 秒）に置くチャプター。特別な文字・長い題・ファイルの長さを超えるもの・同じ開始を含む。
func testChapters() []Chapter {
	return []Chapter{
		{Input: 0, Start: 0, Title: "オープニング"},
		{Input: 0, Start: 1500 * time.Millisecond, Title: "♪ 曲 / 歌手"},
		{Input: 1, Start: 0, Title: `後半 = ; # \ の頭`},
		{Input: 1, Start: 2250 * time.Millisecond, Title: strings.Repeat("長い題", 30)},
		{Input: 1, Start: 2250 * time.Millisecond, Title: "同じ開始（捨てる）"},
		{Input: 1, Start: 10 * time.Second, Title: "ファイルの長さを超える（捨てる）"},
	}
}

// チャプターのあるファイルでも、builtin は ffmpeg 版と同じ箱（chpl・チャプタートラック・tref/chap）を書く（ADR 0013・0027）。
// 2 つ目の入力のチャプターは、1 つ目の入力の長さだけずれる。
func TestBuiltinMatchesFFmpegWithChapters(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := os.Getenv("RAJIKURA_MEDIA_KEEP")
	if dir == "" {
		dir = t.TempDir()
	}
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac")
	makeStereoADTS(t, tl, a, "3")
	makeStereoADTS(t, tl, b, "4")
	_, gm := compareWithFFmpeg(t, tl, dir, []string{a, b}, "", testChapters())

	// 1 つ目の入力の長さ（フレームの数から）だけ、2 つ目の入力のチャプターがずれる。
	frames, cfg, err := scanADTS(context.Background(), []string{a})
	if err != nil {
		t.Fatal(err)
	}
	first := time.Duration(len(frames)) * samplesPerFrame * time.Second / time.Duration(cfg.sampleRate())
	starts := chapterStarts(t, tl, gm)
	want := []int64{0, 1500, (first + 0).Milliseconds(), (first + 2250*time.Millisecond).Milliseconds()}
	if !slices.Equal(starts, want) {
		t.Errorf("chapter starts = %v ms, want %v ms (first input %v)", starts, want, first)
	}
}

// 最初のチャプターが 0 から始まらないとき（分割番組の 1 つ目のパートのチャプターが取れなかったときなど）も、ffmpeg 版と同じ。
func TestBuiltinMatchesFFmpegWithLateFirstChapter(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac")
	makeStereoADTS(t, tl, a, "2")
	makeStereoADTS(t, tl, b, "2")
	compareWithFFmpeg(t, tl, dir, []string{a, b}, "", []Chapter{
		{Input: 1, Start: 0, Title: "後半だけ"},
		{Input: 1, Start: time.Second, Title: "後半の 2 つ目"},
	})
}

// カバー画像とチャプターを一緒に書いても、ffmpeg 版と同じ（ffmetadata は 3 つ目の入力になる）。
func TestBuiltinMatchesFFmpegWithChaptersAndCover(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.aac")
	makeStereoADTS(t, tl, a, "2")
	cover := filepath.Join(dir, "cover.jpg")
	if _, err := run(context.Background(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=16x16:d=1", "-frames:v", "1", cover); err != nil {
		t.Fatal(err)
	}
	compareWithFFmpeg(t, tl, dir, []string{a}, cover, []Chapter{{Input: 0, Start: 0, Title: "1 つだけ"}})
}

// chapterStarts は、ffprobe で読めるチャプターの開始（ミリ秒）を返す。
func chapterStarts(t *testing.T, tl *Tools, file string) []int64 {
	t.Helper()
	out, err := run(context.Background(), tl.FFprobe, "-v", "error", "-show_entries", "chapter=start_time", "-of", "csv=p=0", file)
	if err != nil {
		t.Fatal(err)
	}
	var starts []int64
	for _, l := range strings.Fields(string(out)) {
		sec, err := strconv.ParseFloat(strings.TrimSuffix(l, ","), 64)
		if err != nil {
			t.Fatalf("ffprobe start_time %q: %v", l, err)
		}
		starts = append(starts, int64(math.Round(sec*1000)))
	}
	return starts
}

// チャプターが 0 件なら、チャプタートラックも chpl も書かない（今までと同じファイル）。
func TestBuiltinNoChaptersSameFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.aac")
	if err := os.WriteFile(in, testADTSFrames(10, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, empty := filepath.Join(dir, "plain.m4a"), filepath.Join(dir, "empty.m4a")
	ctx := context.Background()
	if err := (Builtin{}).Remux(ctx, []string{in}, plain, Tags{Title: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	// 全部捨てられるチャプター（題が空・入力の外）だけを渡しても同じ。
	if err := (Builtin{}).RemuxWithChapters(ctx, []string{in}, empty, Tags{Title: "t"}, "", []Chapter{{Title: " "}, {Input: 3, Title: "x"}}); err != nil {
		t.Fatal(err)
	}
	x, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(empty)
	if err != nil {
		t.Fatal(err)
	}
	if string(x) != string(y) {
		t.Error("files differ")
	}
	if strings.Contains(string(x), "chpl") || strings.Contains(string(x), "tref") {
		t.Error("file has chapter boxes")
	}
}

// チャプターのあるファイルでも、Builtin.Duration は音声のトラックの長さを読む。
func TestBuiltinDurationWithChapters(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.aac")
	// 24 kHz で 1024 サンプルのフレームを 47 個（約 2.005 秒）。
	if err := os.WriteFile(in, testADTSFrames(47, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.m4a")
	if err := (Builtin{}).RemuxWithChapters(context.Background(), []string{in}, out, Tags{}, "", []Chapter{{Title: "a"}, {Start: time.Second, Title: "b"}}); err != nil {
		t.Fatal(err)
	}
	d, err := (Builtin{}).Duration(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if want := 47 * 1024 * time.Second / 24000; d != want {
		t.Errorf("Duration = %v, want %v", d, want)
	}
}
