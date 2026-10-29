//go:build realrecording

package media

// 実物の radiko の音声で、ffmpeg 版と builtin を比べるテスト（ADR 0013）。録音は repo に置かないので、
// ふだんの go test では作らない（CI は skip されたテストを失敗にするので、t.Skip にもしない）。手元で次のように流す。
//
//	RAJIKURA_REAL_DIR=<フォルダ> go test -tags realrecording -run TestBuiltinRealRecording ./internal/media/
//
// フォルダには、入手したままの ADTS（radiko の ID3 のタグが付いたもの）を *.aac で置く。名前の順につなぐ
// （2 つ以上なら分割番組の連結の確かめになる）。cover.jpg があればカバー画像に使う。作った m4a は同じフォルダに
// ffmpeg.m4a・go.m4a として残す（Jellyfin・kikidame で確かめるため）。

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestBuiltinRealRecording(t *testing.T) {
	src := os.Getenv("RAJIKURA_REAL_DIR")
	if src == "" {
		t.Fatal("RAJIKURA_REAL_DIR を指す")
	}
	inputs, err := filepath.Glob(filepath.Join(src, "*.aac"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("%s に *.aac が無い（%v）", src, err)
	}
	sort.Strings(inputs)
	cover := filepath.Join(src, "cover.jpg")
	if _, err := os.Stat(cover); err != nil {
		cover = ""
	}
	tl := requireFFmpeg(t)
	compareWithFFmpeg(t, tl, src, inputs, cover, nil)

	d, err := adtsDuration(inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("inputs = %d, adtsDuration = %v", len(inputs), d)
}
