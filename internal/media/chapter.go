package media

// chapter.go は、m4a に入れるチャプター（用語集の「チャプター」。ADR 0027）を、[Tools] と [Builtin] で同じ形に並べる。
//
// 形は ffmpeg に ffmetadata でチャプターを渡したときの既定の出力と同じで、次の 2 つを書く。
//
//   - Nero の moov/udta/chpl: 開始の時刻（100 ナノ秒単位）と題。255 件まで（件数を 1 バイトで持つ）
//   - QuickTime のチャプタートラック: 音声のトラックの tref/chap が指す、テキストのトラック。サンプル 1 つがチャプター 1 つで、
//     サンプルの長さは次のチャプターの開始まで（最後はファイルの終わりまで）。ffmpeg は最初のサンプルを 0 から始める
//     （最初のチャプターの開始が 0 でなくても。ffmpeg 5.0 の出力で確かめた）ので、ここでも同じにする
//
// チャプターが 0 件なら、どちらも書かない（今までと同じファイル）。

import (
	"cmp"
	"encoding/binary"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mp4 "github.com/abema/go-mp4"
)

// Chapter は m4a に入れるチャプター 1 つ。
type Chapter struct {
	// Input は、Remux の inputs の何番目の入力の中のチャプターか。
	Input int
	// Start は、その入力の始まりからの開始の時刻。ファイルの中での時刻は、前の入力の長さ（フレームの数から出す）を足したもの。
	Start time.Duration
	// Title は題。改行は空白にし、前後の空白を落とす。空の題のチャプターは書かない。
	Title string
}

const (
	// maxChapterTitle は題の上限のバイト数（chpl は題の長さを 1 バイトで持つ）。超える題は、UTF-8 の文字の切れ目で切り詰める。
	maxChapterTitle = 255
	// maxChpl は chpl に書くチャプターの数の上限（件数を 1 バイトで持つ。ffmpeg と同じく、チャプタートラックには全部書く）。
	maxChpl = 255
)

// placedChapter は、ファイルの中での開始の時刻（ミリ秒）を決めたチャプター。
type placedChapter struct {
	startMs uint64
	title   string
}

// placeChapters は、チャプター chs をファイルの中の時刻に置き、開始の順に並べる。
// inputSamples は入力ごとのサンプルの数（コアのサンプリング周波数 rate で）、durationMs はファイルの長さ（ミリ秒）。
//
//   - 入力の番号が inputs の外のもの、題が空のもの、開始がその入力の長さ以上のもの、開始がファイルの長さ以上のものは捨てる
//   - 開始の時刻（ミリ秒）が同じものは、最初の 1 つだけを残す（チャプタートラックのサンプルの長さが 0 にならないように）
func placeChapters(chs []Chapter, inputSamples []uint64, rate uint32, durationMs uint64) []placedChapter {
	if len(chs) == 0 || rate == 0 {
		return nil
	}
	offsets := make([]time.Duration, len(inputSamples))
	lengths := make([]time.Duration, len(inputSamples))
	var total uint64
	for i, n := range inputSamples {
		offsets[i] = time.Duration(total) * time.Second / time.Duration(rate)
		lengths[i] = time.Duration(n) * time.Second / time.Duration(rate)
		total += n
	}
	var out []placedChapter
	for _, ch := range chs {
		if ch.Input < 0 || ch.Input >= len(inputSamples) || ch.Start < 0 || ch.Start >= lengths[ch.Input] {
			continue
		}
		title := chapterTitle(ch.Title)
		if title == "" {
			continue
		}
		ms := uint64((offsets[ch.Input] + ch.Start).Milliseconds())
		if ms >= durationMs {
			continue
		}
		out = append(out, placedChapter{startMs: ms, title: title})
	}
	slices.SortStableFunc(out, func(a, b placedChapter) int { return cmp.Compare(a.startMs, b.startMs) })
	return slices.CompactFunc(out, func(a, b placedChapter) bool { return a.startMs == b.startMs })
}

// chapterTitle は題を整える。改行（と制御文字）を空白にし、前後の空白を落とし、maxChapterTitle バイトまでに切り詰める。
func chapterTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if len(s) <= maxChapterTitle {
		return s
	}
	n := maxChapterTitle
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
}

// chapterDurations は、チャプタートラックのサンプルの長さ（ミリ秒）を返す。最初のサンプルは 0 から、各サンプルは次のチャプターの
// 開始まで、最後はファイルの長さ durationMs まで（ffmpeg と同じ。chapter.go の冒頭のコメント）。
func chapterDurations(chs []placedChapter, durationMs uint64) []uint64 {
	out := make([]uint64, len(chs))
	for i := range chs {
		start := chs[i].startMs
		if i == 0 {
			start = 0
		}
		end := durationMs
		if i+1 < len(chs) {
			end = chs[i+1].startMs
		}
		out[i] = end - start
	}
	return out
}

// chapterSample は、チャプタートラックのサンプル 1 つ（題の長さ 2 バイト・題・文字コードを UTF-8 と示す encd の箱）。
func chapterSample(title string) []byte {
	b := make([]byte, 2, 2+len(title)+12)
	binary.BigEndian.PutUint16(b, uint16(len(title)))
	b = append(b, title...)
	return append(b, 0x00, 0x00, 0x00, 0x0C, 'e', 'n', 'c', 'd', 0x00, 0x00, 0x01, 0x00)
}

// ffmetadata は、ffmpeg に渡す ffmetadata のファイルの中身（チャプターだけ）を作る。時刻はミリ秒で書き、各チャプターの終わりは
// 次のチャプターの開始（最後はファイルの長さ durationMs）にする。
func ffmetadata(chs []placedChapter, durationMs uint64) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	for i, ch := range chs {
		end := durationMs
		if i+1 < len(chs) {
			end = chs[i+1].startMs
		}
		b.WriteString("[CHAPTER]\nTIMEBASE=1/1000\n")
		b.WriteString("START=" + strconv.FormatUint(ch.startMs, 10) + "\n")
		b.WriteString("END=" + strconv.FormatUint(end, 10) + "\n")
		b.WriteString("title=" + escapeFFmetadata(ch.title) + "\n")
	}
	return b.String()
}

// escapeFFmetadata は ffmetadata の値の特別な文字（=・;・#・\・改行）を \ で逃がす。
func escapeFFmetadata(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '=', ';', '#', '\\', '\n':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// durationMillis は、サンプルの数 samples（周波数 rate で）の長さのミリ秒（ffmpeg と同じく切り上げ）。
func durationMillis(samples uint64, rate uint32) uint64 {
	return (samples*1000 + uint64(rate) - 1) / uint64(rate)
}

// inputSampleCounts は、フレームの並びから入力ごとのサンプルの数を数える。
func inputSampleCounts(frames []adtsFrame, inputs int) []uint64 {
	out := make([]uint64, inputs)
	for _, fr := range frames {
		out[fr.file] += samplesPerFrame
	}
	return out
}

// textStubHeader は、チャプタートラックのサンプルエントリー（text）の中身のうち、予約の欄とデータの参照の番号の後ろに続く
// 部分（TextSampleEntry・BoxRecord・StyleRecord・FontTableBox）。ffmpeg（movenc.c の mov_create_chapter_track）が書くものと同じ。
var textStubHeader = []byte{
	0x00, 0x00, 0x00, 0x01, // displayFlags
	0x00, 0x00, // horizontal + vertical justification
	0x00, 0x00, 0x00, 0x00, // bgColourRed/Green/Blue/Alpha
	0x00, 0x00, 0x00, 0x00, // defTextBoxTop/Left
	0x00, 0x00, 0x00, 0x00, // defTextBoxBottom/Right
	0x00, 0x00, 0x00, 0x00, // startChar + endChar
	0x00, 0x01, // fontID
	0x00, 0x00, // fontStyleFlags + fontSize
	0x00, 0x00, 0x00, 0x00, // fgColourRed/Green/Blue/Alpha
	0x00, 0x00, 0x00, 0x0D, 'f', 't', 'a', 'b', // FontTableBox
	0x00, 0x01, // entry count
	0x00, 0x01, // font ID
	0x00, // font name length
}

// gmhdPayload は、チャプタートラックの minf/gmhd の中身（gmin と text の箱）。ffmpeg（movenc.c の mov_write_gmhd_tag）と同じ。
var gmhdPayload = []byte{
	0x00, 0x00, 0x00, 0x18, 'g', 'm', 'i', 'n',
	0x00, 0x00, 0x00, 0x00, // version & flags
	0x00, 0x40, // graphics mode
	0x80, 0x00, 0x80, 0x00, 0x80, 0x00, // opColor
	0x00, 0x00, // balance
	0x00, 0x00, // reserved
	0x00, 0x00, 0x00, 0x2C, 't', 'e', 'x', 't',
	0x00, 0x01,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x01,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x40, 0x00,
	0x00, 0x00,
}

// writeChapterTrak は、チャプタートラック（番号 trackID）の trak を書く（ffmpeg と同じ箱の並びと値）。
// サンプルはファイルの中の offset から続けて置いた 1 つのチャンクにする。時刻の単位はミリ秒（ムービーのタイムスケール）。
func writeChapterTrak(bw *boxWriter, trackID uint32, chs []placedChapter, durationMs, offset uint64, co64 bool) {
	var c mp4.Context
	durations := chapterDurations(chs, durationMs)
	var stts []mp4.SttsEntry
	for _, d := range durations {
		if n := len(stts); n > 0 && stts[n-1].SampleDelta == uint32(d) {
			stts[n-1].SampleCount++
			continue
		}
		stts = append(stts, mp4.SttsEntry{SampleCount: 1, SampleDelta: uint32(d)})
	}
	sizes := make([]uint32, len(chs))
	var total uint64
	same := true
	for i, ch := range chs {
		sizes[i] = uint32(len(chapterSample(ch.title)))
		total += uint64(sizes[i])
		same = same && sizes[i] == sizes[0]
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}
	if same {
		// ffmpeg と同じく、サンプルの大きさが全部同じなら 1 つの値で書く。
		stsz.SampleSize, stsz.EntrySize = sizes[0], nil
	}
	// btrt は ffmpeg と同じく、平均（全体の大きさ × 8 ÷ 長さの秒）を最大にも書く。平均が 0 なら書かない。
	avgRate := uint32(total * 8 * 1000 / durationMs)

	bw.box(&mp4.Trak{}, c, func() {
		tkhd := &mp4.Tkhd{TrackID: trackID, DurationV0: uint32(durationMs), Matrix: identityMatrix}
		tkhd.SetFlags(0x000002) // ムービーの中にあるが、再生には使わない
		bw.box(tkhd, c, nil)
		bw.box(&mp4.Edts{}, c, func() {
			bw.box(&mp4.Elst{EntryCount: 1, Entries: []mp4.ElstEntry{{SegmentDurationV0: uint32(durationMs), MediaTimeV0: 0, MediaRateInteger: 1}}}, c, nil)
		})
		bw.box(&mp4.Mdia{}, c, func() {
			bw.box(&mp4.Mdhd{Timescale: 1000, DurationV0: uint32(durationMs)}, c, nil)
			bw.box(&mp4.Hdlr{HandlerType: [4]byte{'t', 'e', 'x', 't'}, Name: "SubtitleHandler"}, c, nil)
			bw.box(&mp4.Minf{}, c, func() {
				bw.rawBox("gmhd", func() { bw.raw(gmhdPayload) })
				bw.box(&mp4.Dinf{}, c, func() {
					bw.box(&mp4.Dref{EntryCount: 1}, c, func() {
						url := &mp4.Url{}
						url.SetFlags(mp4.UrlSelfContained)
						bw.box(url, c, nil)
					})
				})
				bw.box(&mp4.Stbl{}, c, func() {
					bw.box(&mp4.Stsd{EntryCount: 1}, c, func() {
						bw.rawBox("text", func() {
							bw.raw([]byte{0, 0, 0, 0, 0, 0, 0, 1}) // 予約の 6 バイトと、データの参照の番号 1
							bw.raw(textStubHeader)
							if avgRate > 0 {
								bw.box(&mp4.Btrt{MaxBitrate: avgRate, AvgBitrate: avgRate}, c, nil)
							}
						})
					})
					bw.box(&mp4.Stts{EntryCount: uint32(len(stts)), Entries: stts}, c, nil)
					bw.box(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(chs)), SampleDescriptionIndex: 1}}}, c, nil)
					bw.box(stsz, c, nil)
					if co64 {
						bw.box(&mp4.Co64{EntryCount: 1, ChunkOffset: []uint64{offset}}, c, nil)
					} else {
						bw.box(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(offset)}}, c, nil)
					}
				})
			})
		})
	})
}

// chplPayload は moov/udta/chpl の中身（箱のヘッダーを除く）を作る。
func chplPayload(chs []placedChapter) []byte {
	n := min(len(chs), maxChpl)
	b := []byte{0x01, 0, 0, 0, 0, 0, 0, 0, byte(n)}
	for _, ch := range chs[:n] {
		b = binary.BigEndian.AppendUint64(b, ch.startMs*10000)
		b = append(b, byte(len(ch.title)))
		b = append(b, ch.title...)
	}
	return b
}
