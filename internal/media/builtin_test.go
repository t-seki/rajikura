package media

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	mp4 "github.com/abema/go-mp4"
)

// boxDigest は file の箱を順に読み、比べやすい行の並びにする（ADR 0013: ffprobe は寛容なので、箱の作りは
// ffmpeg 版とバイト列で比べて守る）。ffmpeg 版と builtin で違ってよい所は、行から外すか値を消す。
//
//   - 外す: free・mdat（builtin は moov の後に mdat を置く）、stsc・stco・co64（チャンクの切り方）、©too（encoder の名前）
//   - 値を消す: 音声のトラック（1 つ目の trak）の mdhd のタイムスケールと長さ、mp4a のサンプルレート、stts の 1 フレームの長さ
//     （ffmpeg は HE-AAC の出力の周波数、builtin はコアの周波数で書く）、btrt と esds の最大ビットレート（ffmpeg は見積もりの値）。
//     チャプタートラック（2 つ目の trak）の値は消さない
//
// 中身を持つ箱は、値を消した中身を mp4.Stringify で書く。そのほかの、子を持たない箱は中身のバイト列を書く。
func boxDigest(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	skip := map[mp4.BoxType]bool{
		mp4.BoxTypeFree(): true, mp4.BoxTypeMdat(): true, mp4.BoxTypeStsc(): true,
		mp4.BoxTypeStco(): true, mp4.BoxTypeCo64(): true, {0xA9, 't', 'o', 'o'}: true,
	}
	var lines []string
	traks := 0
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		if skip[h.BoxInfo.Type] {
			return nil, nil
		}
		if h.BoxInfo.Type == mp4.BoxTypeTrak() {
			traks++
		}
		path := make([]string, len(h.Path))
		for i, bt := range h.Path {
			path[i] = bt.String()
		}
		key := strings.Join(path, "/")
		if !h.BoxInfo.IsSupportedType() {
			var b bytes.Buffer
			if _, err := h.ReadData(&b); err != nil {
				return nil, err
			}
			lines = append(lines, key+" raw "+hex.EncodeToString(b.Bytes()))
			return nil, nil
		}
		box, n, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		// 値を消すのは 1 つ目の trak（音声のトラック）の中だけ。2 つ目（チャプタートラック）は消さない。
		normalized := traks == 1
		target := box
		if !normalized {
			target = nil
		}
		switch b := target.(type) {
		case *mp4.Mdhd:
			b.Timescale, b.DurationV0, b.DurationV1 = 0, 0, 0
		case *mp4.AudioSampleEntry:
			b.SampleRate = 0
		case *mp4.Stts:
			for i := range b.Entries {
				b.Entries[i].SampleDelta = 0
			}
		case *mp4.Btrt:
			b.MaxBitrate = 0
		case *mp4.Esds:
			for i := range b.Descriptors {
				if d := b.Descriptors[i].DecoderConfigDescriptor; d != nil {
					d.MaxBitrate = 0
				}
			}
		default:
			normalized = false
		}
		hasChildren := n < h.BoxInfo.Size-h.BoxInfo.HeaderSize
		if normalized || hasChildren {
			s, err := mp4.Stringify(box, h.BoxInfo.Context)
			if err != nil {
				return nil, err
			}
			lines = append(lines, key+" "+s)
		} else {
			var b bytes.Buffer
			if _, err := h.ReadData(&b); err != nil {
				return nil, err
			}
			lines = append(lines, key+" raw "+hex.EncodeToString(b.Bytes()))
		}
		if hasChildren {
			return h.Expand()
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return lines
}

// compareBoxes は、ffmpeg 版と builtin の boxDigest が同じかを確かめる。
func compareBoxes(t *testing.T, tl *Tools, ff, gm string) {
	t.Helper()
	a, b := boxDigest(t, ff), boxDigest(t, gm)
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			// 違い始める所の少し前から出す（長い行の後ろだけが違うことが多い）。
			j := 0
			for j < min(len(x), len(y)) && x[j] == y[j] {
				j++
			}
			from := max(0, j-80)
			t.Errorf("box %d differs at byte %d:\n ffmpeg:  %.80s ... %.200s\n builtin: %.80s ... %.200s", i, j, x, x[from:], y, y[from:])
			return
		}
	}
}

// probeSummary は、ffprobe で読める範囲の中身（長さ・ストリーム・タグ）を、比べやすい行の集まりにする。
// encoder のタグ（ffmpeg が書く Lavf...）は比べない。
// キーは、format の値なら "format:<名前>"、ストリームの値なら "stream<番号>:<名前>"。
func probeSummary(t *testing.T, tl *Tools, file string) map[string]string {
	t.Helper()
	m := map[string]string{}
	read := func(prefix func(n int) string, entries string) {
		out, err := run(context.Background(), tl.FFprobe, "-v", "error", "-show_entries", entries,
			"-of", "default=noprint_wrappers=1", file)
		if err != nil {
			t.Fatal(err)
		}
		n := -1
		for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
			k, v, ok := strings.Cut(l, "=")
			if !ok || k == "TAG:encoder" {
				continue
			}
			if k == "codec_name" || n < 0 {
				n++
			}
			m[prefix(n)+k] = v
		}
	}
	read(func(int) string { return "format:" }, "format=duration:format_tags")
	read(func(n int) string { return "stream" + strconv.Itoa(n) + ":" },
		"stream=codec_name,profile,sample_rate,channels,nb_frames,duration:stream_disposition=attached_pic")
	return m
}

// probeChapters は、ffprobe で読めるチャプター（開始・終わり・題）を 1 つ 1 行にする。
func probeChapters(t *testing.T, tl *Tools, file string) []string {
	t.Helper()
	out, err := run(context.Background(), tl.FFprobe, "-v", "error", "-show_chapters", "-of", "compact=p=0", file)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// compareWithFFmpeg は、同じ入力から ffmpeg 版と Go 版の m4a を作り、ffprobe で読める中身（チャプターを含む）を比べる。
// ffmpeg 版はタイムスケールとサンプルエントリーのサンプルレートを HE-AAC の出力の周波数で書き、Go 版は
// コアの周波数で書くが、ffprobe は音声をデコードして同じ値を読むので、ここでは差にならない。
// chapters は RemuxWithChapters に渡す（nil ならチャプター無し）。
func compareWithFFmpeg(t *testing.T, tl *Tools, dir string, inputs []string, cover string, chapters []Chapter) (ff, gm string) {
	t.Helper()
	ctx := context.Background()
	tags := Tags{
		Title: "2026-10-15", Album: "パンサー向井の#ふらっと", Artist: "向井慧（パンサー） / 横澤夏子", AlbumArtist: "TBSラジオ",
		Date: "2026-10-15", Genre: "ラジオ", Comment: "説明\n2 行目", Track: 10150, Disc: 2026,
	}
	ff, gm = filepath.Join(dir, "ffmpeg.m4a"), filepath.Join(dir, "go.m4a")
	start := time.Now()
	if err := tl.RemuxWithChapters(ctx, inputs, ff, tags, cover, chapters); err != nil {
		t.Fatal(err)
	}
	mid := time.Now()
	if err := (Builtin{}).RemuxWithChapters(ctx, inputs, gm, tags, cover, chapters); err != nil {
		t.Fatal(err)
	}
	t.Logf("remux took ffmpeg=%v go=%v", mid.Sub(start), time.Since(mid))
	a, b := probeSummary(t, tl, ff), probeSummary(t, tl, gm)
	for k, v := range a {
		if b[k] != v {
			t.Errorf("%s: ffmpeg=%q go=%q", k, v, b[k])
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("%s: only in go (%q)", k, v)
		}
	}
	if ca, cb := probeChapters(t, tl, ff), probeChapters(t, tl, gm); !slices.Equal(ca, cb) {
		t.Errorf("chapters differ:\n ffmpeg:  %q\n builtin: %q", ca, cb)
	}
	compareBoxes(t, tl, ff, gm)

	// builtin の Duration（読み直した長さ）は、ffprobe の長さと同じ。
	pd, err := tl.Duration(ctx, gm)
	if err != nil {
		t.Fatal(err)
	}
	bd, err := (Builtin{}).Duration(ctx, gm)
	if err != nil {
		t.Fatal(err)
	}
	if diff := (pd - bd).Abs(); diff > time.Millisecond {
		t.Errorf("Builtin.Duration = %v, ffprobe = %v", bd, pd)
	}
	return ff, gm
}

// makeStereoADTS は seconds 秒のステレオの正弦波を ADTS（AAC）で作る。
func makeStereoADTS(t *testing.T, tl *Tools, path string, seconds string) {
	t.Helper()
	_, err := run(context.Background(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=d="+seconds, "-ac", "2", "-c:a", "aac", "-f", "adts", path)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinMatchesFFmpeg(t *testing.T) {
	tl := requireFFmpeg(t)
	// RAJIKURA_MEDIA_KEEP を指せば、作った m4a をそこに残す（箱の中身を見比べるため）。
	dir := os.Getenv("RAJIKURA_MEDIA_KEEP")
	if dir == "" {
		dir = t.TempDir()
	}
	// radiko と同じステレオにする（モノラルの mp4a のチャンネル数は、ffmpeg 5 は 2、6 以降は 1 と書き、版で変わる）。
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac")
	makeStereoADTS(t, tl, a, "3")
	makeStereoADTS(t, tl, b, "4")
	compareWithFFmpeg(t, tl, dir, []string{a, b}, "", nil)

	d, err := adtsDuration([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	gd, err := tl.Duration(context.Background(), filepath.Join(dir, "go.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := d - gd; diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("adtsDuration = %v, ffprobe = %v", d, gd)
	}
}

// testADTSFrames は、中身が i で埋まった AAC-LC・24 kHz・ステレオの ADTS のフレームを n 個作る（i はフレームの番号の下位 8 ビット）。
func testADTSFrames(n, first int) []byte {
	const payload = 12
	frameLen := 7 + payload
	var b []byte
	for i := range n {
		b = append(b, 0xFF, 0xF1, 1<<6|6<<2, 2<<6|byte(frameLen>>11), byte(frameLen>>3), byte(frameLen&7)<<5|0x1F, 0xFC)
		b = append(b, bytes.Repeat([]byte{byte(first + i)}, payload)...)
	}
	return b
}

// testID3 は、中身が size バイトの ID3v2.4 のタグを作る（radiko のセグメントの先頭と同じ形。footer なら 10 バイトのフッターを付ける）。
func testID3(size int, footer bool) []byte {
	flags := byte(0)
	if footer {
		flags = 0x10
	}
	b := []byte{'I', 'D', '3', 4, 0, flags, byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F), byte(size >> 7 & 0x7F), byte(size & 0x7F)}
	b = append(b, bytes.Repeat([]byte{'P'}, size)...)
	if footer {
		b = append(b, '3', 'D', 'I', 4, 0, flags, b[6], b[7], b[8], b[9])
	}
	return b
}

// radiko のセグメントは先頭に ID3v2 のタグを持ち、入手はセグメントをつなぐので、ストリームの途中にもタグが来る。
// scanADTS はタグを読み飛ばし、フレームだけを拾う（ffmpeg の aac demuxer と同じ）。
func TestScanADTSSkipsID3(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.aac")
	var data []byte
	data = append(data, testID3(53, false)...)
	data = append(data, testADTSFrames(3, 0)...)
	data = append(data, testID3(200, true)...) // セグメントの継ぎ目（フッター付き）
	data = append(data, testADTSFrames(2, 3)...)
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	frames, cfg, err := scanADTS(context.Background(), []string{in})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 || cfg.sampleRate() != 24000 || cfg.channels != 2 {
		t.Fatalf("frames = %d, cfg = %+v", len(frames), cfg)
	}
	// 書いた m4a の中身が、タグを除いたフレームの中身だけになっている。
	out := filepath.Join(dir, "out.m4a")
	if err := (Builtin{}).Remux(context.Background(), []string{in}, out, Tags{Title: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	m4a, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for i := range 5 {
		want = append(want, bytes.Repeat([]byte{byte(i)}, 12)...)
	}
	if !bytes.HasSuffix(m4a, want) {
		t.Errorf("mdat does not end with the frame payloads")
	}
	if bytes.Contains(m4a, []byte("ID3")) {
		t.Errorf("m4a contains an ID3 tag")
	}
}

// 取り消した ctx では、入力を読む前に止まる。
func TestBuiltinRemuxCanceled(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.aac")
	if err := os.WriteFile(in, testADTSFrames(10, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (Builtin{}).Remux(ctx, []string{in}, filepath.Join(dir, "out.m4a"), Tags{}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestBuiltinESDescriptorSizes は、esds の入れ子の記述子が宣言する大きさが中身と合っていることを確かめる。
// 少なく宣言しても ffmpeg / ffprobe は読めるが、Android の MediaMetadataRetriever はトラックを読めない
// （試作で、kikidame の取り込みで長さ 0:00・タグ無しになった）。
func TestBuiltinESDescriptorSizes(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.aac")
	makeADTS(t, tl, a, "1")
	out := filepath.Join(dir, "go.m4a")
	if err := (Builtin{}).Remux(context.Background(), []string{a}, out, Tags{Title: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bis, err := mp4.ExtractBox(f, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a(), mp4.BoxTypeEsds()})
	if err != nil || len(bis) != 1 {
		t.Fatalf("esds: %v (%d boxes)", err, len(bis))
	}
	body := make([]byte, bis[0].Size-bis[0].HeaderSize)
	if _, err := f.ReadAt(body, int64(bis[0].Offset+bis[0].HeaderSize)); err != nil {
		t.Fatal(err)
	}
	// FullBox の 4 バイトの後に、ES 記述子（タグ・長さ・中身）が esds の終わりまで続く。
	checkDescriptor(t, body[4:], 0x03)
}

// checkDescriptor は、b の先頭の記述子のタグが tag で、宣言する長さが b の残りとちょうど同じかを確かめる。
// ES 記述子と DecoderConfig 記述子は、入れ子の記述子も確かめる。
func checkDescriptor(t *testing.T, b []byte, tag byte) {
	t.Helper()
	if len(b) == 0 || b[0] != tag {
		t.Fatalf("descriptor tag: got % x, want %#x", b[:min(len(b), 1)], tag)
	}
	n, i := 0, 1
	for ; i < len(b); i++ {
		n = n<<7 | int(b[i]&0x7F)
		if b[i]&0x80 == 0 {
			i++
			break
		}
	}
	if i+n != len(b) {
		t.Fatalf("descriptor %#x declares %d bytes, but %d follow", tag, n, len(b)-i)
	}
	body := b[i:]
	switch tag {
	case 0x03: // ES_ID(2) + flags(1)、続いて DecoderConfig と SLConfig
		rest := body[3:]
		dcdLen := descriptorLen(t, rest)
		checkDescriptor(t, rest[:dcdLen], 0x04)
		checkDescriptor(t, rest[dcdLen:], 0x06)
	case 0x04: // 13 バイトの後に DecSpecificInfo
		checkDescriptor(t, body[13:], 0x05)
	}
}

// descriptorLen は、b の先頭の記述子の、ヘッダーを含めた大きさを返す。
func descriptorLen(t *testing.T, b []byte) int {
	t.Helper()
	n, i := 0, 1
	for ; i < len(b); i++ {
		n = n<<7 | int(b[i]&0x7F)
		if b[i]&0x80 == 0 {
			i++
			break
		}
	}
	if i+n > len(b) {
		t.Fatalf("descriptor %#x overruns: %d + %d > %d", b[0], i, n, len(b))
	}
	return i + n
}
