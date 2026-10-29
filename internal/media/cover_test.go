package media

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeJPEG は ffmpeg で小さな JPEG を作る。
func makeJPEG(t *testing.T, tl *Tools, path string) {
	t.Helper()
	if _, err := run(context.Background(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=64x64", "-frames:v", "1", path); err != nil {
		t.Fatal(err)
	}
}

// カバー画像を埋め込んでも、音声のストリームは 1 本で、長さは変わらない。
// 画像は attached_pic として入る。入力が 1 つのとき（concat）も 2 つ以上のとき（標準入力）も同じ。
func TestRemuxWithCover(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a, b, cover := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac"), filepath.Join(dir, "c.jpg")
	makeADTS(t, tl, a, "2")
	makeADTS(t, tl, b, "2")
	makeJPEG(t, tl, cover)
	for name, inputs := range map[string][]string{"in1": {a}, "in2": {a, b}} {
		out := filepath.Join(dir, name+".m4a")
		if err := tl.Remux(context.Background(), inputs, out, Tags{Title: "t"}, cover); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := run(context.Background(), tl.FFprobe, "-v", "error", "-show_entries",
			"stream=codec_type,codec_name:stream_disposition=attached_pic", "-of", "default=nw=1", out)
		if err != nil {
			t.Fatal(err)
		}
		s := string(got)
		if strings.Count(s, "codec_type=audio") != 1 || strings.Count(s, "codec_type=video") != 1 ||
			!strings.Contains(s, "codec_name=mjpeg") || !strings.Contains(s, "DISPOSITION:attached_pic=1") {
			t.Errorf("%s: streams = %q", name, s)
		}
		d, err := tl.Duration(context.Background(), out)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Duration(len(inputs)) * 2 * time.Second
		if (d - want).Abs() > 500*time.Millisecond {
			t.Errorf("%s: duration = %v, want about %v", name, d, want)
		}
	}
}
