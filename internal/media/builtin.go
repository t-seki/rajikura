package media

// builtin.go は、ffmpeg を使わずに ADTS（AAC）から m4a を作る [Builtin]（ADR 0013。設定 muxer = "builtin"）。
// [Tools] と同じ入力（開始時刻の順に並んだ ADTS のファイル）から、同じタグとカバー画像とチャプターを持つ
// m4a を作る。箱の読み書きには github.com/abema/go-mp4 を使う（go-mp4 が型を知らない箱は、バイト列で書く）。
//
// 作りは次のとおり。
//
//  1. 入力の ADTS を一度読み、フレームごとの大きさ（ADTS のヘッダーを除いたもの）を集める
//  2. moov（サンプルの表とタグ。チャプターがあればチャプタートラックと chpl も。chapter.go）を先に組み、mdat の中身の
//     位置を決める（ffmpeg の +faststart と同じく、moov を mdat の前に置く）
//  3. ADTS のヘッダーを外したフレームを、mdat に順に書く。チャプターがあれば、チャプタートラックのサンプルをその後ろに書く
//
// HE-AAC（SBR）の扱い: ADTS のヘッダーには、コア（AAC-LC）のサンプリング周波数しか書かれていない
// （radiko は 24 kHz）。AudioSpecificConfig・タイムスケール・サンプルエントリーのサンプルレートは、
// どれもヘッダーの値のまま（24 kHz、1 フレーム 1024）書き、SBR はデコーダーに見つけさせる
// （implicit signaling。AudioSpecificConfig は ffmpeg の aac_adtstoasc と同じ）。
// ffmpeg は音声を試しにデコードして SBR を見つけ、タイムスケールとサンプルレートを 2 倍
// （48 kHz、1 フレーム 2048）で書くが、この実装は音声をデコードしないので、推測で 2 倍にはしない。
// 2 倍にしない形でも、ffprobe・kikidame（Android）・Jellyfin・Windows（プロパティとメディアプレーヤー）は、
// 2 倍の形と同じに表示・再生した（試作での確かめ。1 フレームの長さは 1024/24000 秒と 2048/48000 秒で同じ）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"time"

	mp4 "github.com/abema/go-mp4"
)

// samplesPerFrame は AAC の 1 フレームのサンプル数（コアのサンプリング周波数で）。
const samplesPerFrame = 1024

// samplesPerChunk は、m4a の 1 チャンクに入れるフレームの数。ffmpeg の出力（約 1 MB ごと）に近い値。
const samplesPerChunk = 4096

// adtsSampleRates は ADTS の sampling_frequency_index から周波数への表（ISO/IEC 14496-3）。
var adtsSampleRates = [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// adtsConfig は ADTS のヘッダーから読む、ストリーム全体で変わらない値。
type adtsConfig struct {
	objectType uint8 // audio object type（ADTS の profile + 1）
	freqIndex  uint8
	channels   uint8
}

func (c adtsConfig) sampleRate() uint32 { return adtsSampleRates[c.freqIndex] }

// audioSpecificConfig は ADTS の値から AudioSpecificConfig（2 バイト）を作る。
func (c adtsConfig) audioSpecificConfig() []byte {
	v := uint16(c.objectType)<<11 | uint16(c.freqIndex)<<7 | uint16(c.channels)<<3
	return []byte{byte(v >> 8), byte(v)}
}

// adtsFrame は 1 フレームの位置。
type adtsFrame struct {
	file   int   // inputs の何番目か
	offset int64 // ファイルの中の、ヘッダーを除いた中身の先頭
	size   uint32
}

// id3HeaderLen は ID3v2 のタグのヘッダーの大きさ（"ID3"・版 2 バイト・フラグ・syncsafe の大きさ 4 バイト）。
const id3HeaderLen = 10

// id3TagLen は、ID3v2 のタグのヘッダー h（id3HeaderLen バイト）から、ヘッダーとフッターを含めたタグ全体の大きさを返す。
// 大きさの 4 バイトは syncsafe（各バイトの下位 7 ビット）。フラグの 0x10 はフッター（10 バイト）がある印。
func id3TagLen(h []byte) (int64, error) {
	if h[6]|h[7]|h[8]|h[9] >= 0x80 {
		return 0, errors.New("bad ID3v2 size")
	}
	n := int64(h[6])<<21 | int64(h[7])<<14 | int64(h[8])<<7 | int64(h[9])
	n += id3HeaderLen
	if h[5]&0x10 != 0 {
		n += id3HeaderLen
	}
	return n, nil
}

// scanADTS は inputs の全フレームの位置と大きさを集める。全フレームの profile・周波数・チャンネル数が
// 揃っていることを確かめる。1 フレームに複数の raw_data_block を持つ ADTS は扱わない。
//
// フレームの切れ目にある ID3v2 のタグは読み飛ばす。radiko のタイムフリーの音声は、HLS のセグメントごとに
// 先頭に ID3v2.4 のタグ（PRIV、時刻の印）を持ち、入手はセグメントをそのままつなぐので、ストリームの途中にも現れる
// （2026-10-17 に入手したままの 5 分で確かめた）。ffmpeg の aac demuxer も読み飛ばしている。
//
// ctx が取り消されたら、4096 フレームごとの区切りで止める（ffmpeg 版が子プロセスを止めるのと同じく、取り消しの後に戻る）。
func scanADTS(ctx context.Context, inputs []string) ([]adtsFrame, adtsConfig, error) {
	var frames []adtsFrame
	var cfg adtsConfig
	for i, in := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, cfg, err
		}
		f, err := os.Open(in)
		if err != nil {
			return nil, cfg, err
		}
		r := bufio.NewReaderSize(f, 1<<16)
		var pos int64
		hdr := make([]byte, id3HeaderLen)
		for {
			if _, err := io.ReadFull(r, hdr[:7]); err != nil {
				if err == io.EOF {
					break
				}
				f.Close()
				if err == io.ErrUnexpectedEOF {
					return nil, cfg, fmt.Errorf("%s: truncated ADTS header at %d", in, pos)
				}
				return nil, cfg, err
			}
			if hdr[0] == 'I' && hdr[1] == 'D' && hdr[2] == '3' {
				if _, err := io.ReadFull(r, hdr[7:]); err != nil {
					f.Close()
					return nil, cfg, fmt.Errorf("%s: truncated ID3 header at %d", in, pos)
				}
				n, err := id3TagLen(hdr)
				if err != nil {
					f.Close()
					return nil, cfg, fmt.Errorf("%s: %w at %d", in, err, pos)
				}
				if _, err := r.Discard(int(n - id3HeaderLen)); err != nil {
					f.Close()
					return nil, cfg, fmt.Errorf("%s: truncated ID3 tag at %d", in, pos)
				}
				pos += n
				continue
			}
			if hdr[0] != 0xFF || hdr[1]&0xF0 != 0xF0 {
				f.Close()
				return nil, cfg, fmt.Errorf("%s: no ADTS sync word at %d", in, pos)
			}
			protectionAbsent := hdr[1]&0x01 == 1
			c := adtsConfig{
				objectType: hdr[2]>>6 + 1,
				freqIndex:  hdr[2] >> 2 & 0x0F,
				channels:   (hdr[2]&0x01)<<2 | hdr[3]>>6,
			}
			frameLen := int64(hdr[3]&0x03)<<11 | int64(hdr[4])<<3 | int64(hdr[5])>>5
			blocks := hdr[6] & 0x03
			if int(c.freqIndex) >= len(adtsSampleRates) {
				f.Close()
				return nil, cfg, fmt.Errorf("%s: bad sampling frequency index %d at %d", in, c.freqIndex, pos)
			}
			if blocks != 0 {
				f.Close()
				return nil, cfg, fmt.Errorf("%s: %d raw data blocks in one ADTS frame at %d (not supported)", in, blocks+1, pos)
			}
			hdrLen := int64(7)
			if !protectionAbsent {
				hdrLen = 9
			}
			if frameLen <= hdrLen {
				f.Close()
				return nil, cfg, fmt.Errorf("%s: bad ADTS frame length %d at %d", in, frameLen, pos)
			}
			if len(frames) == 0 {
				cfg = c
			} else if c != cfg {
				f.Close()
				return nil, cfg, fmt.Errorf("%s: ADTS config changed at %d (%+v -> %+v)", in, pos, cfg, c)
			}
			frames = append(frames, adtsFrame{file: i, offset: pos + hdrLen, size: uint32(frameLen - hdrLen)})
			if len(frames)%4096 == 0 {
				if err := ctx.Err(); err != nil {
					f.Close()
					return nil, cfg, err
				}
			}
			if _, err := r.Discard(int(frameLen - 7)); err != nil {
				f.Close()
				return nil, cfg, fmt.Errorf("%s: truncated ADTS frame at %d", in, pos)
			}
			pos += frameLen
		}
		f.Close()
	}
	if len(frames) == 0 {
		return nil, cfg, errors.New("no ADTS frames")
	}
	return frames, cfg, nil
}

// adtsDuration は inputs のフレームの数から長さを出す。
func adtsDuration(inputs []string) (time.Duration, error) {
	frames, cfg, err := scanADTS(context.Background(), inputs)
	if err != nil {
		return 0, err
	}
	return time.Duration(float64(len(frames)) * samplesPerFrame / float64(cfg.sampleRate()) * float64(time.Second)), nil
}

// Builtin は、ffmpeg・ffprobe を使わずに m4a への格納と長さの測定を行う。[Tools] と同じメソッドを持つ。
type Builtin struct{}

// Duration は、file の m4a を読み直し、音声のトラック（soundMdhd）の mdhd の長さとタイムスケールから長さを出す。
// mdhd の長さは、Remux が入力のフレームの数から書いた値なので、これで確かめられるのは「入手した音声の長さが
// #EXTINF の合計と合うか」（検証の本来の役目）までで、箱の作りの誤りは見ない（ADR 0013。箱の作りは、
// テストで ffmpeg 版と箱のバイト列を比べて守る）。
func (Builtin) Duration(ctx context.Context, file string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f, err := os.Open(file)
	if err != nil {
		return 0, fmt.Errorf("media: duration: %w", err)
	}
	defer f.Close()
	mdhd, err := soundMdhd(f)
	if err != nil {
		return 0, fmt.Errorf("media: duration: %w", err)
	}
	if mdhd.Timescale == 0 {
		return 0, errors.New("media: duration: mdhd timescale is 0")
	}
	d := uint64(mdhd.DurationV0)
	if mdhd.GetVersion() == 1 {
		d = mdhd.DurationV1
	}
	return time.Duration(float64(d) / float64(mdhd.Timescale) * float64(time.Second)), nil
}

// soundMdhd は、f の m4a の音声のトラック（hdlr が soun のもの。チャプタートラックを持つファイルではトラックが 2 つある）の mdhd を返す。
// 音声のトラックがちょうど 1 つでなければエラー。
func soundMdhd(f io.ReadSeeker) (*mp4.Mdhd, error) {
	mdia := mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia()}
	mdhds, err := mp4.ExtractBoxWithPayload(f, nil, append(slices.Clone(mdia), mp4.BoxTypeMdhd()))
	if err != nil {
		return nil, err
	}
	hdlrs, err := mp4.ExtractBoxWithPayload(f, nil, append(slices.Clone(mdia), mp4.BoxTypeHdlr()))
	if err != nil {
		return nil, err
	}
	if len(mdhds) != len(hdlrs) {
		return nil, fmt.Errorf("%d mdhd boxes but %d hdlr boxes", len(mdhds), len(hdlrs))
	}
	var found *mp4.Mdhd
	for i, h := range hdlrs {
		if h.Payload.(*mp4.Hdlr).HandlerType != [4]byte{'s', 'o', 'u', 'n'} {
			continue
		}
		if found != nil {
			return nil, errors.New("more than one sound track")
		}
		found = mdhds[i].Payload.(*mp4.Mdhd)
	}
	if found == nil {
		return nil, errors.New("no sound track")
	}
	return found, nil
}

// Remux は [Tools.Remux] と同じく、開始時刻の順に並んだ ADTS ファイル inputs を 1 本の m4a out にし、
// tags を書く。cover が空でなければ、その JPEG をカバー画像（ilst の covr）として書く。out は上書きされる。
func (b Builtin) Remux(ctx context.Context, inputs []string, out string, tags Tags, cover string) error {
	return b.RemuxWithChapters(ctx, inputs, out, tags, cover, nil)
}

// RemuxWithChapters は [Tools.RemuxWithChapters] と同じく、Remux に加えて chapters を、ffmpeg 版と同じ形で書く（chapter.go）。
func (Builtin) RemuxWithChapters(ctx context.Context, inputs []string, out string, tags Tags, cover string, chs []Chapter) error {
	if len(inputs) == 0 {
		return errors.New("media: remux: no inputs")
	}
	frames, cfg, err := scanADTS(ctx, inputs)
	if err != nil {
		return fmt.Errorf("media: remux: %w", err)
	}
	var coverData []byte
	if cover != "" {
		if coverData, err = os.ReadFile(cover); err != nil {
			return fmt.Errorf("media: remux: %w", err)
		}
	}

	var dataSize uint64
	for _, fr := range frames {
		dataSize += uint64(fr.size)
	}
	// チャプターのサンプルは、mdat の音声の後ろに置く。
	rate := cfg.sampleRate()
	chapters := placeChapters(chs, inputSampleCounts(frames, len(inputs)), rate, durationMillis(uint64(len(frames))*samplesPerFrame, rate))
	var chapterData []byte
	for _, ch := range chapters {
		chapterData = append(chapterData, chapterSample(ch.title)...)
	}
	dataSize += uint64(len(chapterData))
	ftyp := ftypBytes()
	largeMdat := dataSize+8 > math.MaxUint32
	mdatHeader := uint64(8)
	if largeMdat {
		mdatHeader = 16
	}
	// moov の大きさはまだ分からないので、カバー画像と、サンプルの表の上限の見積もり（4 MB）を足して決める。
	co64 := uint64(len(ftyp))+dataSize+mdatHeader+uint64(len(coverData))+(4<<20) > math.MaxUint32

	// moov の大きさは chunk の位置の値によらないので、仮の位置で一度組んで大きさを測り、本当の位置で組み直す。
	trial, err := buildMoov(frames, cfg, tags, coverData, chapters, 0, co64)
	if err != nil {
		return fmt.Errorf("media: remux: %w", err)
	}
	dataStart := uint64(len(ftyp)) + uint64(len(trial)) + mdatHeader
	moov, err := buildMoov(frames, cfg, tags, coverData, chapters, dataStart, co64)
	if err != nil {
		return fmt.Errorf("media: remux: %w", err)
	}
	if len(moov) != len(trial) {
		return errors.New("media: remux: moov size changed")
	}

	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("media: remux: %w", err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	err = writeFile(ctx, w, inputs, frames, ftyp, moov, dataSize, largeMdat)
	if err == nil {
		_, err = w.Write(chapterData)
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(out)
		return fmt.Errorf("media: remux: %w", err)
	}
	return nil
}

func writeFile(ctx context.Context, w io.Writer, inputs []string, frames []adtsFrame, ftyp, moov []byte, dataSize uint64, large bool) error {
	if _, err := w.Write(ftyp); err != nil {
		return err
	}
	if _, err := w.Write(moov); err != nil {
		return err
	}
	if large {
		var h [16]byte
		binary.BigEndian.PutUint32(h[0:], 1)
		copy(h[4:], "mdat")
		binary.BigEndian.PutUint64(h[8:], dataSize+16)
		if _, err := w.Write(h[:]); err != nil {
			return err
		}
	} else {
		var h [8]byte
		binary.BigEndian.PutUint32(h[0:], uint32(dataSize+8))
		copy(h[4:], "mdat")
		if _, err := w.Write(h[:]); err != nil {
			return err
		}
	}
	// フレームはファイルの中で連続しているので、ファイルごとに順に読み、ヘッダーを読み飛ばす。
	buf := make([]byte, 8192)
	cur := -1
	var r *bufio.Reader
	var f *os.File
	var pos int64
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for i, fr := range frames {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if fr.file != cur {
			if f != nil {
				f.Close()
			}
			var err error
			if f, err = os.Open(inputs[fr.file]); err != nil {
				return err
			}
			r = bufio.NewReaderSize(f, 1<<16)
			cur, pos = fr.file, 0
		}
		if _, err := r.Discard(int(fr.offset - pos)); err != nil {
			return err
		}
		if _, err := io.CopyBuffer(w, io.LimitReader(r, int64(fr.size)), buf); err != nil {
			return err
		}
		pos = fr.offset + int64(fr.size)
	}
	return nil
}

func ftypBytes() []byte {
	var b bytes.Buffer
	// ffmpeg の -f mp4 と同じ brand。
	b.Write([]byte{0, 0, 0, 28})
	b.WriteString("ftyp")
	b.WriteString("isom")
	b.Write([]byte{0, 0, 2, 0})
	b.WriteString("isomiso2mp41")
	return b.Bytes()
}

// memWS はメモリの上の io.WriteSeeker（go-mp4 の Writer が箱の大きさを後から書き戻すため）。
type memWS struct {
	buf []byte
	pos int
}

func (m *memWS) Write(p []byte) (int, error) {
	if end := m.pos + len(p); end > len(m.buf) {
		m.buf = append(m.buf, make([]byte, end-len(m.buf))...)
	}
	copy(m.buf[m.pos:], p)
	m.pos += len(p)
	return len(p), nil
}

func (m *memWS) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = int(offset)
	case io.SeekCurrent:
		m.pos += int(offset)
	case io.SeekEnd:
		m.pos = len(m.buf) + int(offset)
	}
	return int64(m.pos), nil
}

// boxWriter は、箱を入れ子で書く手助け。
type boxWriter struct {
	w   *mp4.Writer
	err error
}

func (bw *boxWriter) box(b mp4.IImmutableBox, ctx mp4.Context, children func()) {
	if bw.err != nil {
		return
	}
	if _, bw.err = bw.w.StartBox(&mp4.BoxInfo{Type: b.GetType()}); bw.err != nil {
		return
	}
	if _, bw.err = mp4.Marshal(bw.w, b, ctx); bw.err != nil {
		return
	}
	if children != nil {
		children()
	}
	if bw.err != nil {
		return
	}
	_, bw.err = bw.w.EndBox()
}

// rawBox は、go-mp4 が型を知らない箱（tref・chpl など）を書く。中身は children の中で raw か rawBox で書く。
func (bw *boxWriter) rawBox(typ string, children func()) {
	if bw.err != nil {
		return
	}
	if _, bw.err = bw.w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(typ)}); bw.err != nil {
		return
	}
	children()
	if bw.err != nil {
		return
	}
	_, bw.err = bw.w.EndBox()
}

// raw は、箱の中身のバイト列をそのまま書く。
func (bw *boxWriter) raw(b []byte) {
	if bw.err != nil {
		return
	}
	_, bw.err = bw.w.Write(b)
}

var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

// buildMoov は moov の箱を組む。dataStart は mdat の中身の先頭のファイルの中の位置。
// タイムスケールとサンプルレートは ADTS のヘッダーの周波数のまま書く（ファイルの冒頭のコメント）。
// chapters があれば、チャプタートラック（トラック 2。サンプルは mdat の音声の後ろ）と、音声のトラックからそれを指す tref/chap と、
// udta/chpl を書く（chapter.go）。
func buildMoov(frames []adtsFrame, cfg adtsConfig, tags Tags, cover []byte, chapters []placedChapter, dataStart uint64, co64 bool) ([]byte, error) {
	rate := cfg.sampleRate()
	delta := uint32(samplesPerFrame)
	mediaDuration := uint64(len(frames)) * uint64(delta)
	// ffmpeg と同じく切り上げる。
	durationMs := (mediaDuration*1000 + uint64(rate) - 1) / uint64(rate)

	// ビットレート: 平均は全体から、最大は 1 秒ごとの区切りの最大。ffmpeg の最大は、入力を読んだときの
	// 見積もりと平均の大きいほうで（movenc.c の calculate_mpeg4_bit_rates。「1 秒の区切りの最大にすべき」
	// という FIXME がある）、値は揃わない。どちらも参考の値で、再生には使われない。
	var total uint64
	var maxRate, window uint64
	perSec := int(math.Ceil(float64(rate) / float64(delta)))
	for i, fr := range frames {
		total += uint64(fr.size)
		window += uint64(fr.size)
		if (i+1)%perSec == 0 || i == len(frames)-1 {
			if window*8 > maxRate {
				maxRate = window * 8
			}
			window = 0
		}
	}
	// ffmpeg（movenc.c の compute_avg_bitrate）と同じく、整数の割り算で切り捨てる。
	avgRate := uint32(total * 8 * uint64(rate) / mediaDuration)

	sizes := make([]uint32, len(frames))
	for i, fr := range frames {
		sizes[i] = fr.size
	}
	var offsets []uint64
	var stsc []mp4.StscEntry
	off := dataStart
	for i := 0; i < len(frames); i += samplesPerChunk {
		n := min(samplesPerChunk, len(frames)-i)
		offsets = append(offsets, off)
		for _, s := range sizes[i : i+n] {
			off += uint64(s)
		}
		if len(stsc) == 0 || stsc[len(stsc)-1].SamplesPerChunk != uint32(n) {
			stsc = append(stsc, mp4.StscEntry{FirstChunk: uint32(len(offsets)), SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1})
		}
	}

	// チャプターのサンプルは音声の後ろ（off は音声の終わり）。
	chapterOffset := off

	ws := &memWS{}
	bw := &boxWriter{w: mp4.NewWriter(ws)}
	var c mp4.Context
	asc := cfg.audioSpecificConfig()
	// トラックの番号は ffmpeg と同じく、入力のストリームの順に振る。カバー画像は ffmpeg ではストリーム（2 番）に数えるが、
	// trak は書かず ilst の covr になるので、チャプタートラックはカバー画像があれば 3 番、無ければ 2 番になる。
	nextTrackID := uint32(2)
	chapterTrackID := uint32(2)
	if len(cover) > 0 {
		chapterTrackID = 3
	}
	if len(chapters) > 0 {
		nextTrackID = chapterTrackID + 1
	}
	bw.box(&mp4.Moov{}, c, func() {
		bw.box(&mp4.Mvhd{Timescale: 1000, DurationV0: uint32(durationMs), Rate: 0x00010000, Volume: 0x0100, Matrix: identityMatrix, NextTrackID: nextTrackID}, c, nil)
		bw.box(&mp4.Trak{}, c, func() {
			tkhd := &mp4.Tkhd{TrackID: 1, DurationV0: uint32(durationMs), AlternateGroup: 1, Volume: 0x0100, Matrix: identityMatrix}
			tkhd.SetFlags(0x000003)
			bw.box(tkhd, c, nil)
			bw.box(&mp4.Edts{}, c, func() {
				bw.box(&mp4.Elst{EntryCount: 1, Entries: []mp4.ElstEntry{{SegmentDurationV0: uint32(durationMs), MediaTimeV0: 0, MediaRateInteger: 1}}}, c, nil)
			})
			if len(chapters) > 0 {
				// tref/chap: チャプタートラックを指す。
				bw.rawBox("tref", func() { bw.rawBox("chap", func() { bw.raw(binary.BigEndian.AppendUint32(nil, chapterTrackID)) }) })
			}
			bw.box(&mp4.Mdia{}, c, func() {
				bw.box(&mp4.Mdhd{Timescale: rate, DurationV0: uint32(mediaDuration), Language: [3]byte{'u' - 0x60, 'n' - 0x60, 'd' - 0x60}}, c, nil)
				bw.box(&mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"}, c, nil)
				bw.box(&mp4.Minf{}, c, func() {
					bw.box(&mp4.Smhd{}, c, nil)
					bw.box(&mp4.Dinf{}, c, func() {
						bw.box(&mp4.Dref{EntryCount: 1}, c, func() {
							url := &mp4.Url{}
							url.SetFlags(mp4.UrlSelfContained)
							bw.box(url, c, nil)
						})
					})
					bw.box(&mp4.Stbl{}, c, func() {
						bw.box(&mp4.Stsd{EntryCount: 1}, c, func() {
							bw.box(&mp4.AudioSampleEntry{
								SampleEntry: mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()}, DataReferenceIndex: 1},
								// チャンネル数（1 か 2）。ffmpeg 6 以降と同じ（ffmpeg 5 はモノラルでも 2 と書いていた）。
								// 3 以上や PCE（0）なら、MP4 の既定値の 2 を書く（本当の構成は AudioSpecificConfig にある）。
								ChannelCount: sampleEntryChannels(cfg.channels),
								SampleSize:   16,
								SampleRate:   rate << 16,
							}, c, func() {
								// 記述子の大きさは、入れ子の記述子のヘッダー（タグ 1 バイトと、長さの欄）を含む。
								// go-mp4 は長さの欄を常に 4 バイト（0x80 0x80 0x80 xx）で書くので、それで数える。
								// 少なく書くと、ffmpeg は読めるが Android（MediaMetadataRetriever）はトラックを読めない。
								const descHeader = 1 + 4
								dsi := mp4.Descriptor{Tag: mp4.DecSpecificInfoTag, Size: uint32(len(asc)), Data: asc}
								dcdSize := uint32(13 + descHeader + len(asc))
								slSize := uint32(1)
								bw.box(&mp4.Esds{Descriptors: []mp4.Descriptor{
									{Tag: mp4.ESDescrTag, Size: 3 + (descHeader + dcdSize) + (descHeader + slSize), ESDescriptor: &mp4.ESDescriptor{ESID: 1}},
									{Tag: mp4.DecoderConfigDescrTag, Size: dcdSize, DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
										ObjectTypeIndication: 0x40, StreamType: 5, Reserved: true, MaxBitrate: uint32(maxRate), AvgBitrate: avgRate,
									}},
									dsi,
									{Tag: mp4.SLConfigDescrTag, Size: slSize, Data: []byte{0x02}},
								}}, c, nil)
								bw.box(&mp4.Btrt{MaxBitrate: uint32(maxRate), AvgBitrate: avgRate}, c, nil)
							})
						})
						bw.box(&mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: uint32(len(frames)), SampleDelta: delta}}}, c, nil)
						bw.box(&mp4.Stsc{EntryCount: uint32(len(stsc)), Entries: stsc}, c, nil)
						bw.box(&mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}, c, nil)
						if co64 {
							bw.box(&mp4.Co64{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}, c, nil)
						} else {
							o32 := make([]uint32, len(offsets))
							for i, o := range offsets {
								o32[i] = uint32(o)
							}
							bw.box(&mp4.Stco{EntryCount: uint32(len(o32)), ChunkOffset: o32}, c, nil)
						}
						sgpd := &mp4.Sgpd{GroupingType: [4]byte{'r', 'o', 'l', 'l'}, DefaultLength: 2, EntryCount: 1, RollDistances: []int16{-1}}
						sgpd.SetVersion(1)
						bw.box(sgpd, c, nil)
						// ffmpeg と同じく、全サンプルを roll のグループ（1 フレーム前から復号すれば戻れる）に入れる。
						bw.box(&mp4.Sbgp{GroupingType: binary.BigEndian.Uint32([]byte("roll")), EntryCount: 1,
							Entries: []mp4.SbgpEntry{{SampleCount: uint32(len(frames)), GroupDescriptionIndex: 1}}}, c, nil)
					})
				})
			})
		})
		if len(chapters) > 0 {
			writeChapterTrak(bw, chapterTrackID, chapters, durationMs, chapterOffset, co64)
		}
		bw.box(&mp4.Udta{}, c, func() {
			bw.box(&mp4.Meta{}, c, func() {
				// iTunes 形式のタグの hdlr。ffmpeg と同じく、予約の欄の先頭に "appl" を書く。
				bw.box(&mp4.Hdlr{HandlerType: [4]byte{'m', 'd', 'i', 'r'}, Reserved: [3]uint32{0x6170706c, 0, 0}}, c, nil)
				bw.box(&mp4.Ilst{}, c, func() { writeIlst(bw, tags, cover) })
			})
			if len(chapters) > 0 {
				bw.rawBox("chpl", func() { bw.raw(chplPayload(chapters)) })
			}
		})
	})
	if bw.err != nil {
		return nil, bw.err
	}
	return ws.buf, nil
}

// sampleEntryChannels は、mp4a のサンプルエントリーに書くチャンネル数を返す。
func sampleEntryChannels(channels uint8) uint16 {
	if channels == 1 {
		return 1
	}
	return 2
}

// iTunes 形式のタグの data の型（Apple の QuickTime File Format の well-known types）。
// go-mp4 の DataTypeStringJPEG は 14（Apple では PNG）なので使わない。
const (
	dataTypeBinary = 0
	dataTypeUTF8   = 1
	dataTypeJPEG   = 13
)

// writeIlst は ffmpeg の mp4 muxer と同じ順でタグを書く。
func writeIlst(bw *boxWriter, t Tags, cover []byte) {
	item := func(typ mp4.BoxType, dataType uint32, data []byte) {
		bw.box(&mp4.IlstMetaContainer{AnyTypeBox: mp4.AnyTypeBox{Type: typ}}, mp4.Context{UnderIlst: true}, func() {
			bw.box(&mp4.Data{DataType: dataType, Data: data}, mp4.Context{UnderIlst: true, UnderIlstMeta: true}, nil)
		})
	}
	text := func(code string, v string) {
		if v != "" {
			item(mp4.BoxType{0xA9, code[0], code[1], code[2]}, dataTypeUTF8, []byte(v))
		}
	}
	text("nam", t.Title)
	text("ART", t.Artist)
	if t.AlbumArtist != "" {
		item(mp4.StrToBoxType("aART"), dataTypeUTF8, []byte(t.AlbumArtist))
	}
	text("alb", t.Album)
	text("day", t.Date)
	text("cmt", t.Comment)
	text("gen", t.Genre)
	if len(cover) > 0 {
		item(mp4.StrToBoxType("covr"), dataTypeJPEG, cover)
	}
	// trkn と disk は ffmpeg と同じく 8 バイト（0, 番号, 総数, 0）。
	pair := func(code string, n int) {
		if n > 0 {
			b := make([]byte, 8)
			binary.BigEndian.PutUint16(b[2:], uint16(n))
			item(mp4.StrToBoxType(code), dataTypeBinary, b)
		}
	}
	pair("trkn", t.Track)
	pair("disk", t.Disc)
}
