package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// builtin が保存した m4a の album のタグを RewriteAlbum で書き換えると、新しいアルバムの名前で Remux（チャプターがあれば
// RemuxWithChapters）したのと同じバイト列になる（ほかのタグ・画像・チャプター・音声はそのまま。moov の大きさが変わってずれる
// サンプルの位置も、チャプタートラックを含めてずらす。ADR 0039）。名前が長くなっても短くなっても同じ。音声のサンプルは元と同じ。
func TestBuiltinRewriteAlbumSameAsRemux(t *testing.T) {
	ctx := context.Background()
	for _, cover := range []bool{false, true} {
		for _, withChapters := range []bool{false, true} {
			t.Run(fmt.Sprintf("cover=%v,chapters=%v", cover, withChapters), func(t *testing.T) {
				dir := t.TempDir()
				inputs := []string{writeTestADTS(t, dir, "a.aac", 47, 0), writeTestADTS(t, dir, "b.aac", 30, 47)}
				coverPath := ""
				if cover {
					coverPath = filepath.Join(dir, "cover.jpg")
					if err := os.WriteFile(coverPath, []byte("\xff\xd8 not really a jpeg \xff\xd9"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var chs []Chapter
				if withChapters {
					chs = []Chapter{{Input: 0, Start: 0, Title: "前半"}, {Input: 1, Start: 0, Title: "後半"}}
				}
				tags := Tags{Title: "2026-10-11 副題", Album: "VIVANT悪役会議室", Artist: "出演", AlbumArtist: "TBSラジオ",
					Date: "2026-10-11", Genre: "ラジオ", Comment: "説明", Track: 1011, Disc: 2026}
				saved := filepath.Join(dir, "saved.m4a")
				if err := (Builtin{}).RemuxWithChapters(ctx, inputs, saved, tags, coverPath, chs); err != nil {
					t.Fatal(err)
				}
				for _, album := range []string{"VIVANT 悪役会議室", "短い", "ずっと長い番組の名前に変えた VIVANT 悪役会議室"} {
					want, got := filepath.Join(dir, "want-"+album+".m4a"), filepath.Join(dir, "got-"+album+".m4a")
					wt := tags
					wt.Album = album
					if err := (Builtin{}).RemuxWithChapters(ctx, inputs, want, wt, coverPath, chs); err != nil {
						t.Fatal(err)
					}
					if err := (Builtin{}).RewriteAlbum(ctx, saved, got, album); err != nil {
						t.Fatalf("RewriteAlbum(%q) = %v", album, err)
					}
					if !bytes.Equal(readFile(t, got), readFile(t, want)) {
						t.Errorf("RewriteAlbum(%q) と、その名前で Remux したファイルが違う", album)
					}
					if err := (Builtin{}).SameAudio(ctx, saved, got); err != nil {
						t.Errorf("SameAudio(%q) = %v", album, err)
					}
				}
			})
		}
	}
}

// album のタグが無いファイルには、writeIlst と同じ位置に足す（album を書いて Remux したのと同じバイト列）。
func TestBuiltinRewriteAlbumAddsMissingAlbum(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inputs := []string{writeTestADTS(t, dir, "a.aac", 20, 0)}
	tags := Tags{Title: "t", Artist: "a", Date: "2026-10-11", Genre: "ラジオ", Track: 1011}
	saved, want, got := filepath.Join(dir, "saved.m4a"), filepath.Join(dir, "want.m4a"), filepath.Join(dir, "got.m4a")
	if err := (Builtin{}).Remux(ctx, inputs, saved, tags, ""); err != nil {
		t.Fatal(err)
	}
	wt := tags
	wt.Album = "番組"
	if err := (Builtin{}).Remux(ctx, inputs, want, wt, ""); err != nil {
		t.Fatal(err)
	}
	if err := (Builtin{}).RewriteAlbum(ctx, saved, got, "番組"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, got), readFile(t, want)) {
		t.Error("album を足したファイルが、album を書いて Remux したのと違う")
	}
}

// 読めない箱の並びなら、書かずにエラーにし、out を残さない。
func TestRewriteAlbumUnreadable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := writeTestADTS(t, dir, "in.aac", 10, 0)
	good := filepath.Join(dir, "good.m4a")
	if err := (Builtin{}).Remux(ctx, []string{in}, good, Tags{Album: "a"}, ""); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"m4a でない":   []byte("not an mp4 file at all"),
		"知らない最上位の箱": append(readFile(t, good), makeBox("abcd", []byte{1, 2, 3})...),
	} {
		t.Run(name, func(t *testing.T) {
			src, out := filepath.Join(dir, "src.m4a"), filepath.Join(dir, "out.m4a")
			if err := os.WriteFile(src, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := (Builtin{}).RewriteAlbum(ctx, src, out, "番組"); err == nil {
				t.Error("エラーにならない")
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("out が残った: %v", err)
			}
		})
	}
}

// muxer が builtin でも ffmpeg でも、builtin が書いたファイルにも ffmpeg が書いたファイルにも、album のタグだけを書き換えられ、
// 音声のサンプル（数とバイト列）は元と同じ。タグ（album だけが新しい名前）・カバー画像・チャプターは、新しい名前で
// RemuxWithChapters したのと ffprobe で同じに読める（ADR 0039）。
func TestRewriteAlbumBothMuxers(t *testing.T) {
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
	tags := Tags{Title: "2026-10-11 副題", Album: "VIVANT悪役会議室", Artist: "出演", AlbumArtist: "TBSラジオ", Date: "2026-10-11",
		Genre: "ラジオ", Comment: "説明", Track: 10110, Disc: 2026}
	chs := testChapters()
	newTags := tags
	newTags.Album = "VIVANT 悪役会議室"
	want := filepath.Join(dir, "want.m4a")
	if err := (Builtin{}).RemuxWithChapters(ctx, inputs, want, newTags, cover, chs); err != nil {
		t.Fatal(err)
	}
	wantChapters, wantSummary := probeChapters(t, tl, want), probeSummary(t, tl, want)
	if len(wantChapters) == 0 {
		t.Fatal("RemuxWithChapters のファイルにチャプターが無い")
	}
	type muxer interface {
		RemuxWithChapters(ctx context.Context, inputs []string, out string, tags Tags, cover string, chapters []Chapter) error
		RewriteAlbum(ctx context.Context, in, out, album string) error
	}
	for _, src := range []struct {
		name string
		m    muxer
	}{{"builtin で保存", Builtin{}}, {"ffmpeg で保存", tl}} {
		saved := filepath.Join(dir, "saved-"+src.name+".m4a")
		if err := src.m.RemuxWithChapters(ctx, inputs, saved, tags, cover, chs); err != nil {
			t.Fatal(err)
		}
		for _, w := range []struct {
			name string
			m    muxer
		}{{"builtin で書き換える", Builtin{}}, {"ffmpeg で書き換える", tl}} {
			t.Run(src.name+"・"+w.name, func(t *testing.T) {
				out := filepath.Join(dir, "out-"+src.name+"-"+w.name+".m4a")
				if err := w.m.RewriteAlbum(ctx, saved, out, newTags.Album); err != nil {
					t.Fatalf("RewriteAlbum = %v", err)
				}
				if err := sameAudio(ctx, saved, out); err != nil {
					t.Errorf("音声のサンプルが元と違う: %v", err)
				}
				if got := probeChapters(t, tl, out); !slices.Equal(got, wantChapters) {
					t.Errorf("chapters:\n got:  %q\n want: %q", got, wantChapters)
				}
				got := probeSummary(t, tl, out)
				for k, v := range wantSummary {
					if got[k] != v {
						t.Errorf("%s: 新しい名前で RemuxWithChapters %q、書き換えた後 %q", k, v, got[k])
					}
				}
				for k, v := range got {
					if _, ok := wantSummary[k]; !ok {
						t.Errorf("%s: 書き換えた後だけにある（%q）", k, v)
					}
				}
			})
		}
	}
}

// 取り消されたら、書かずに取り消しのエラーを返す。
func TestBuiltinRewriteAlbumCanceled(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "saved.m4a")
	if err := (Builtin{}).Remux(context.Background(), []string{writeTestADTS(t, dir, "a.aac", 10, 0)}, saved, Tags{Album: "a"}, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	out := filepath.Join(dir, "out.m4a")
	if err := (Builtin{}).RewriteAlbum(ctx, saved, out, "b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("out が残った: %v", err)
	}
}
