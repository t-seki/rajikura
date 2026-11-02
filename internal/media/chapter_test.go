package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

// writeTestADTS は、testADTSFrames の n 個のフレームを dir/name に書く。
func writeTestADTS(t *testing.T, dir, name string, n, first int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, testADTSFrames(n, first), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// readFile は p の中身を返す。
func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// builtin が保存したチャプターの無い m4a に WriteChapters でチャプターを書くと、同じチャプターで RemuxWithChapters したのと
// 同じバイト列になる（音声・タグ・画像はそのまま、チャプターは同じ形。ADR 0033）。パートの長さ（InputLengths）で 2 つ目の
// パートのチャプターがずれる。チャプターのあるファイルに書き直すと、前のチャプターは残らない。どれも音声のサンプルは元と同じ。
func TestBuiltinWriteChaptersSameAsRemux(t *testing.T) {
	ctx := context.Background()
	for _, cover := range []bool{false, true} {
		t.Run(fmt.Sprintf("cover=%v", cover), func(t *testing.T) {
			dir := t.TempDir()
			a, b := writeTestADTS(t, dir, "a.aac", 47, 0), writeTestADTS(t, dir, "b.aac", 30, 47)
			inputs := []string{a, b}
			coverPath := ""
			if cover {
				coverPath = filepath.Join(dir, "cover.jpg")
				if err := os.WriteFile(coverPath, []byte("\xff\xd8 not really a jpeg \xff\xd9"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tags := Tags{Title: "t", Album: "番組", Track: 3}
			chs := []Chapter{{Input: 0, Start: 0, Title: "前半"}, {Input: 0, Start: time.Second, Title: "前半の 2"}, {Input: 1, Start: 0, Title: "後半"}}
			plain, want, got := filepath.Join(dir, "plain.m4a"), filepath.Join(dir, "want.m4a"), filepath.Join(dir, "got.m4a")
			if err := (Builtin{}).Remux(ctx, inputs, plain, tags, coverPath); err != nil {
				t.Fatal(err)
			}
			if err := (Builtin{}).RemuxWithChapters(ctx, inputs, want, tags, coverPath, chs); err != nil {
				t.Fatal(err)
			}
			parts, err := (Builtin{}).InputLengths(ctx, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if wantA := 47 * 1024 * time.Second / 24000; parts[0] != wantA {
				t.Errorf("InputLengths[0] = %v, want %v", parts[0], wantA)
			}
			n, err := (Builtin{}).WriteChapters(ctx, plain, got, chs, parts)
			if err != nil || n != 3 {
				t.Fatalf("WriteChapters = %d, %v", n, err)
			}
			if !bytes.Equal(readFile(t, got), readFile(t, want)) {
				t.Error("WriteChapters と RemuxWithChapters のファイルが違う")
			}
			if err := (Builtin{}).SameAudio(ctx, plain, got); err != nil {
				t.Errorf("SameAudio = %v", err)
			}

			// チャプターのあるファイルに、別のチャプターを書き直す。
			chs2 := []Chapter{{Input: 1, Start: 500 * time.Millisecond, Title: "後半だけ"}}
			want2, got2 := filepath.Join(dir, "want2.m4a"), filepath.Join(dir, "got2.m4a")
			if err := (Builtin{}).RemuxWithChapters(ctx, inputs, want2, tags, coverPath, chs2); err != nil {
				t.Fatal(err)
			}
			if n, err := (Builtin{}).WriteChapters(ctx, got, got2, chs2, parts); err != nil || n != 1 {
				t.Fatalf("WriteChapters (書き直し) = %d, %v", n, err)
			}
			if !bytes.Equal(readFile(t, got2), readFile(t, want2)) {
				t.Error("書き直したファイルが RemuxWithChapters と違う")
			}
			if err := (Builtin{}).SameAudio(ctx, plain, got2); err != nil {
				t.Errorf("SameAudio (書き直し) = %v", err)
			}
		})
	}
}

// パートの長さが分からず番組表の放送時間で代えたとき（実際より長い）も、2 つ目のパートのチャプターはその長さだけずれ、
// ファイルの長さを超えるものは捨てる。最後のパートの長さはファイルから決める。
func TestBuiltinWriteChaptersGuideParts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := writeTestADTS(t, dir, "in.aac", 141, 0) // 約 6.016 秒
	plain, got := filepath.Join(dir, "plain.m4a"), filepath.Join(dir, "got.m4a")
	if err := (Builtin{}).Remux(ctx, []string{in}, plain, Tags{}, ""); err != nil {
		t.Fatal(err)
	}
	n, err := (Builtin{}).WriteChapters(ctx, plain, got, []Chapter{
		{Input: 0, Start: 0, Title: "a"}, {Input: 1, Start: 0, Title: "b"}, {Input: 1, Start: 2 * time.Second, Title: "超える"},
	}, []time.Duration{5 * time.Second, time.Hour})
	if err != nil || n != 2 {
		t.Fatalf("WriteChapters = %d, %v", n, err)
	}
	f, err := os.Open(got)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := readM4A(f)
	if err != nil {
		t.Fatal(err)
	}
	if m.chapterIdx < 0 {
		t.Fatal("チャプタートラックが無い")
	}
	b := readFile(t, got)
	i := bytes.Index(b, []byte("chpl"))
	if i < 0 {
		t.Fatal("chpl が無い")
	}
	// chpl の中身: 版とフラグ・予約（8 バイト）の後に件数、続いて開始（100 ナノ秒単位）と題。
	p := b[i+4:]
	if p[8] != 2 || binary.BigEndian.Uint64(p[9:]) != 0 || binary.BigEndian.Uint64(p[9+8+1+1:]) != 5000*10000 {
		t.Errorf("chpl = % x", p[:30])
	}
}

// 音声のサンプルが元と違えば（中身・数）、SameAudio は ErrAudioMismatch を返す。
func TestSameAudioMismatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mk := func(name string, n, first int) string {
		in := writeTestADTS(t, dir, name+".aac", n, first)
		out := filepath.Join(dir, name+".m4a")
		if err := (Builtin{}).Remux(ctx, []string{in}, out, Tags{}, ""); err != nil {
			t.Fatal(err)
		}
		return out
	}
	base, other, short := mk("base", 10, 0), mk("other", 10, 1), mk("short", 9, 0)
	if err := sameAudio(ctx, base, base); err != nil {
		t.Errorf("同じファイル: %v", err)
	}
	for name, p := range map[string]string{"中身": other, "数": short} {
		if err := sameAudio(ctx, base, p); !errors.Is(err, ErrAudioMismatch) {
			t.Errorf("%s: err = %v, want ErrAudioMismatch", name, err)
		}
	}
}

// 読めない箱の並び（m4a でない、音声のトラックの無い、知らないトラックのある）なら、書かずにエラーにし、out を残さない。
func TestWriteChaptersUnreadable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := writeTestADTS(t, dir, "in.aac", 10, 0)
	good := filepath.Join(dir, "good.m4a")
	if err := (Builtin{}).Remux(ctx, []string{in}, good, Tags{}, ""); err != nil {
		t.Fatal(err)
	}
	gb := readFile(t, good)
	// 音声のトラックの hdlr を vide にする（音声のトラックが無く、知らないトラックがある）。
	noSound := bytes.Replace(gb, []byte("soun"), []byte("vide"), 1)
	for name, data := range map[string][]byte{
		"m4a でない":    []byte("not an mp4 file at all"),
		"音声のトラックが無い": noSound,
		"知らない最上位の箱":  append(slices.Clone(gb), makeBox("abcd", []byte{1, 2, 3})...),
	} {
		t.Run(name, func(t *testing.T) {
			src, out := filepath.Join(dir, "src.m4a"), filepath.Join(dir, "out.m4a")
			if err := os.WriteFile(src, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := (Builtin{}).WriteChapters(ctx, src, out, []Chapter{{Title: "a"}}, nil); err == nil {
				t.Error("エラーにならない")
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("out が残った: %v", err)
			}
		})
	}
}

// muxer が ffmpeg のとき、builtin が書いたファイルにも ffmpeg が書いたファイルにも、ffmpeg でチャプターだけを書き込め、
// 音声のサンプル（数とバイト列）は元と同じ。チャプターとタグ・カバー画像（とチャプタートラックのストリーム）は、同じチャプターで
// RemuxWithChapters したのと ffprobe で同じに読める。builtin の WriteChapters で ffmpeg が書いたファイルに書いても同じ（どちらの muxer で保存した
// ファイルかは分からないため）。
func TestWriteChaptersBothMuxers(t *testing.T) {
	tl := requireFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac")
	makeStereoADTS(t, tl, a, "3")
	makeStereoADTS(t, tl, b, "4")
	cover := filepath.Join(dir, "cover.jpg")
	if _, err := run(ctx, tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=16x16:d=1", "-frames:v", "1", cover); err != nil {
		t.Fatal(err)
	}
	inputs := []string{a, b}
	tags := Tags{Title: "2026-10-15", Album: "番組", Artist: "出演", Date: "2026-10-15", Genre: "ラジオ", Comment: "説明", Track: 10150, Disc: 2026}
	chs := testChapters()
	parts, err := inputLengths(ctx, inputs)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "want.m4a")
	if err := (Builtin{}).RemuxWithChapters(ctx, inputs, want, tags, cover, chs); err != nil {
		t.Fatal(err)
	}
	wantChapters, wantSummary := probeChapters(t, tl, want), probeSummary(t, tl, want)
	if len(wantChapters) == 0 {
		t.Fatal("RemuxWithChapters のファイルにチャプターが無い")
	}
	type writer interface {
		WriteChapters(ctx context.Context, in, out string, chs []Chapter, parts []time.Duration) (int, error)
		Remux(ctx context.Context, inputs []string, out string, tags Tags, cover string) error
	}
	for _, src := range []struct {
		name string
		w    writer
	}{{"builtin で保存", Builtin{}}, {"ffmpeg で保存", tl}} {
		plain := filepath.Join(dir, "plain-"+src.name+".m4a")
		if err := src.w.Remux(ctx, inputs, plain, tags, cover); err != nil {
			t.Fatal(err)
		}
		if got := probeChapters(t, tl, plain); len(got) != 0 {
			t.Fatalf("%s: 保存したファイルにチャプターがある: %q", src.name, got)
		}
		for _, w := range []struct {
			name string
			w    writer
		}{{"builtin で書き込む", Builtin{}}, {"ffmpeg で書き込む", tl}} {
			t.Run(src.name+"・"+w.name, func(t *testing.T) {
				out := filepath.Join(dir, "out-"+src.name+"-"+w.name+".m4a")
				n, err := w.w.WriteChapters(ctx, plain, out, chs, parts)
				if err != nil || n != 4 {
					t.Fatalf("WriteChapters = %d, %v", n, err)
				}
				if err := sameAudio(ctx, plain, out); err != nil {
					t.Errorf("音声のサンプルが元と違う: %v", err)
				}
				if got := probeChapters(t, tl, out); !slices.Equal(got, wantChapters) {
					t.Errorf("chapters:\n got:  %q\n want: %q", got, wantChapters)
				}
				got := probeSummary(t, tl, out)
				for k, v := range wantSummary {
					if got[k] != v {
						t.Errorf("%s: RemuxWithChapters %q、書き込んだ後 %q", k, v, got[k])
					}
				}
				for k, v := range got {
					if _, ok := wantSummary[k]; !ok {
						t.Errorf("%s: 書き込んだ後だけにある（%q）", k, v)
					}
				}
			})
		}
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
