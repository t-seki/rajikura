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
// 保存した m4a を読み、チャプターだけを書き直す部品（WriteChapters。ADR 0038）と、アルバムの名前（album のタグ）だけを書き直す部品
// （RewriteAlbum。保存先の移し。ADR 0039）も、このファイルの後ろの節にある。
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

// ---- 保存した m4a を読む部品と、チャプターだけを書き直す WriteChapters（ADR 0038） ----
//
// 読めるのは、rajikura が保存した m4a（builtin が書いたものと、muxer が ffmpeg のときに ffmpeg が書いたもの）の箱の並び。
// 最上位は ftyp・moov・mdat（と free・skip・wide）、moov の trak は音声のトラック（hdlr が soun）1 つと、それが tref/chap で
// 指すチャプタートラック（hdlr が text）だけ。ほかの並び（別のトラック、chap 以外の tref、知らない最上位の箱）は読まずに
// エラーにする（書かずに失敗にする）。

// m4aBox は、メモリに読んだ箱 1 つ（ヘッダーを含むバイト列）。
type m4aBox struct {
	typ string
	hdr int
	b   []byte
}

func (x m4aBox) payload() []byte { return x.b[x.hdr:] }

// parseBoxes は、p に並ぶ箱を読む（大きさ 1 の 64 ビットの大きさと、大きさ 0 の終わりまでを読む）。
func parseBoxes(p []byte) ([]m4aBox, error) {
	var out []m4aBox
	for len(p) > 0 {
		if len(p) < 8 {
			return nil, errors.New("truncated box header")
		}
		size := uint64(binary.BigEndian.Uint32(p))
		typ := string(p[4:8])
		hdr := 8
		switch size {
		case 0:
			size = uint64(len(p))
		case 1:
			if len(p) < 16 {
				return nil, fmt.Errorf("truncated %q box header", typ)
			}
			size, hdr = binary.BigEndian.Uint64(p[8:]), 16
		}
		if size < uint64(hdr) || size > uint64(len(p)) {
			return nil, fmt.Errorf("bad size %d of %q box", size, typ)
		}
		out = append(out, m4aBox{typ: typ, hdr: hdr, b: p[:size]})
		p = p[size:]
	}
	return out, nil
}

// childBoxes は、箱 x の中身を子の箱として読む。
func childBoxes(x m4aBox) ([]m4aBox, error) {
	kids, err := parseBoxes(x.payload())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", x.typ, err)
	}
	return kids, nil
}

// findBox は bs の中の、型 typ の最初の箱を返す。
func findBox(bs []m4aBox, typ string) (m4aBox, bool) {
	for _, b := range bs {
		if b.typ == typ {
			return b, true
		}
	}
	return m4aBox{}, false
}

// boxAt は、x から型の並び path をたどった箱を返す（どの段も最初の箱）。
func boxAt(x m4aBox, path ...string) (m4aBox, error) {
	for _, typ := range path {
		kids, err := childBoxes(x)
		if err != nil {
			return m4aBox{}, err
		}
		var ok bool
		if x, ok = findBox(kids, typ); !ok {
			return m4aBox{}, fmt.Errorf("no %s box", typ)
		}
	}
	return x, nil
}

// makeBox は、型 typ と中身 parts から箱のバイト列を作る（大きさは 32 ビット）。
func makeBox(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n))
	copy(b[4:], typ)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// m4aChunk は、音声のトラックのチャンク 1 つ（ファイルの中の位置と、中のサンプルの大きさの合計）。
type m4aChunk struct {
	offset uint64
	size   uint64
}

// m4aSound は、音声のトラックの読んだ値。
type m4aSound struct {
	timescale uint32
	// samples は stts の長さの合計（timescale で）。
	samples uint64
	sizes   []uint32
	chunks  []m4aChunk
	co64    bool
}

// m4aFile は、読んだ m4a（ftyp と moov のバイト列と、書き直しに要る値）。
type m4aFile struct {
	ftyp []byte
	moov m4aBox
	// sound は音声のトラック、soundIdx は moov の子の中での位置、chapterIdx はチャプタートラックの位置（無ければ -1）。
	sound      m4aSound
	soundIdx   int
	chapterIdx int
	// trackIDs は、チャプタートラックを除くトラックの番号。nextTrackID は mvhd の値。
	trackIDs    []uint32
	nextTrackID uint32
	// cover は ilst に covr（カバー画像）があるか。
	cover bool
}

// readM4A は f の m4a を読む（この節の冒頭のコメントの箱の並びだけ）。
func readM4A(f *os.File) (*m4aFile, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	end := st.Size()
	m := &m4aFile{chapterIdx: -1}
	var moov []byte
	hdr := make([]byte, 16)
	for pos := int64(0); pos < end; {
		if end-pos < 8 {
			return nil, fmt.Errorf("truncated box header at %d", pos)
		}
		if _, err := f.ReadAt(hdr[:8], pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(hdr))
		typ := string(hdr[4:8])
		h := int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if _, err := f.ReadAt(hdr[8:16], pos+8); err != nil {
				return nil, err
			}
			size, h = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		}
		if size < h || size > end-pos {
			return nil, fmt.Errorf("bad size %d of %q box at %d", size, typ, pos)
		}
		switch typ {
		case "ftyp", "moov":
			if (typ == "ftyp" && m.ftyp != nil) || (typ == "moov" && moov != nil) {
				return nil, fmt.Errorf("more than one %s box", typ)
			}
			if size > 64<<20 {
				return nil, fmt.Errorf("%s box too large (%d bytes)", typ, size)
			}
			b := make([]byte, size)
			if _, err := f.ReadAt(b, pos); err != nil {
				return nil, err
			}
			if typ == "ftyp" {
				m.ftyp = b
			} else {
				moov = b
			}
		case "mdat", "free", "skip", "wide":
		default:
			return nil, fmt.Errorf("unexpected top-level %q box", typ)
		}
		pos += size
	}
	if m.ftyp == nil || moov == nil {
		return nil, errors.New("no ftyp or moov box")
	}
	boxes, err := parseBoxes(moov)
	if err != nil {
		return nil, err
	}
	m.moov = boxes[0]
	if err := m.parseMoov(); err != nil {
		return nil, err
	}
	return m, nil
}

// m4aTrak は、moov の trak 1 つの読んだ値。
type m4aTrak struct {
	idx     int
	id      uint32
	handler string
	chaps   []uint32 // tref/chap が指すトラックの番号
}

// parseMoov は moov を読み、音声のトラック・チャプタートラック・トラックの番号・カバー画像の有無を決める。
func (m *m4aFile) parseMoov() error {
	kids, err := childBoxes(m.moov)
	if err != nil {
		return err
	}
	var traks []m4aTrak
	sound := -1
	for i, k := range kids {
		switch k.typ {
		case "mvhd":
			p := k.payload()
			if len(p) < 4 {
				return errors.New("truncated mvhd box")
			}
			m.nextTrackID = binary.BigEndian.Uint32(p[len(p)-4:])
		case "trak":
			t, err := readTrak(k)
			if err != nil {
				return err
			}
			t.idx = i
			if t.handler == "soun" {
				if sound >= 0 {
					return errors.New("more than one sound track")
				}
				sound = len(traks)
			}
			traks = append(traks, t)
		case "udta":
			m.cover = hasCover(k)
		}
	}
	if sound < 0 {
		return errors.New("no sound track")
	}
	st := traks[sound]
	m.soundIdx = st.idx
	for i, t := range traks {
		switch {
		case i == sound:
			m.trackIDs = append(m.trackIDs, t.id)
		case t.handler == "text" && slices.Contains(st.chaps, t.id) && m.chapterIdx < 0:
			m.chapterIdx = t.idx
		default:
			return fmt.Errorf("unexpected track %d (handler %q)", t.id, t.handler)
		}
	}
	m.sound, err = readSound(kids[st.idx])
	return err
}

// readTrak は trak の番号（tkhd）・handler（mdia/hdlr）・tref/chap を読む。tref に chap 以外があればエラー。
func readTrak(trak m4aBox) (m4aTrak, error) {
	var t m4aTrak
	tkhd, err := boxAt(trak, "tkhd")
	if err != nil {
		return t, err
	}
	p := tkhd.payload()
	off := 12
	if len(p) > 0 && p[0] == 1 {
		off = 20
	}
	if len(p) < off+4 {
		return t, errors.New("truncated tkhd box")
	}
	t.id = binary.BigEndian.Uint32(p[off:])
	hdlr, err := boxAt(trak, "mdia", "hdlr")
	if err != nil {
		return t, err
	}
	hp := hdlr.payload()
	if len(hp) < 12 {
		return t, errors.New("truncated hdlr box")
	}
	t.handler = string(hp[8:12])
	kids, err := childBoxes(trak)
	if err != nil {
		return t, err
	}
	if tref, ok := findBox(kids, "tref"); ok {
		refs, err := childBoxes(tref)
		if err != nil {
			return t, err
		}
		for _, r := range refs {
			if r.typ != "chap" {
				return t, fmt.Errorf("unexpected tref/%s box", r.typ)
			}
			for p := r.payload(); len(p) >= 4; p = p[4:] {
				t.chaps = append(t.chaps, binary.BigEndian.Uint32(p))
			}
		}
	}
	return t, nil
}

// hasCover は udta/meta/ilst に covr があるか。
func hasCover(udta m4aBox) bool {
	meta, err := boxAt(udta, "meta")
	if err != nil || len(meta.payload()) < 4 {
		return false
	}
	// meta は FullBox（版とフラグの 4 バイトの後に子の箱）。
	kids, err := parseBoxes(meta.payload()[4:])
	if err != nil {
		return false
	}
	ilst, ok := findBox(kids, "ilst")
	if !ok {
		return false
	}
	items, err := childBoxes(ilst)
	if err != nil {
		return false
	}
	_, ok = findBox(items, "covr")
	return ok
}

// readSound は音声のトラックの mdhd・stts・stsz・stsc・stco/co64 を読み、チャンクごとの位置と大きさを決める。
func readSound(trak m4aBox) (m4aSound, error) {
	var s m4aSound
	mdhd, err := boxAt(trak, "mdia", "mdhd")
	if err != nil {
		return s, err
	}
	p := mdhd.payload()
	off := 12
	if len(p) > 0 && p[0] == 1 {
		off = 20
	}
	if len(p) < off+4 {
		return s, errors.New("truncated mdhd box")
	}
	s.timescale = binary.BigEndian.Uint32(p[off:])
	if s.timescale == 0 {
		return s, errors.New("mdhd timescale is 0")
	}
	stbl, err := boxAt(trak, "mdia", "minf", "stbl")
	if err != nil {
		return s, err
	}
	kids, err := childBoxes(stbl)
	if err != nil {
		return s, err
	}
	// table は FullBox の表（版とフラグ・件数・1 件 entry バイトの並び）の中身と件数を返す。
	table := func(typ string, entry int) ([]byte, int, error) {
		b, ok := findBox(kids, typ)
		if !ok {
			return nil, 0, fmt.Errorf("no %s box", typ)
		}
		p := b.payload()
		if len(p) < 8 {
			return nil, 0, fmt.Errorf("truncated %s box", typ)
		}
		n := binary.BigEndian.Uint32(p[4:])
		if uint64(len(p)-8) < uint64(n)*uint64(entry) {
			return nil, 0, fmt.Errorf("truncated %s box", typ)
		}
		return p[8:], int(n), nil
	}
	stts, n, err := table("stts", 8)
	if err != nil {
		return s, err
	}
	var sttsCount uint64
	for i := range n {
		e := stts[i*8:]
		c, d := uint64(binary.BigEndian.Uint32(e)), uint64(binary.BigEndian.Uint32(e[4:]))
		sttsCount += c
		s.samples += c * d
	}
	stsz, ok := findBox(kids, "stsz")
	if !ok {
		return s, errors.New("no stsz box")
	}
	zp := stsz.payload()
	if len(zp) < 12 {
		return s, errors.New("truncated stsz box")
	}
	size, count := binary.BigEndian.Uint32(zp[4:]), binary.BigEndian.Uint32(zp[8:])
	if size == 0 && uint64(len(zp)-12) < uint64(count)*4 {
		return s, errors.New("truncated stsz box")
	}
	s.sizes = make([]uint32, count)
	for i := range s.sizes {
		if size != 0 {
			s.sizes[i] = size
		} else {
			s.sizes[i] = binary.BigEndian.Uint32(zp[12+i*4:])
		}
	}
	if len(s.sizes) == 0 || uint64(len(s.sizes)) != sttsCount {
		return s, fmt.Errorf("%d samples in stsz but %d in stts", len(s.sizes), sttsCount)
	}
	var offsets []uint64
	if co, n, err := table("stco", 4); err == nil {
		for i := range n {
			offsets = append(offsets, uint64(binary.BigEndian.Uint32(co[i*4:])))
		}
	} else if co, n, err := table("co64", 8); err == nil {
		s.co64 = true
		for i := range n {
			offsets = append(offsets, binary.BigEndian.Uint64(co[i*8:]))
		}
	} else {
		return s, errors.New("no stco or co64 box")
	}
	stsc, n, err := table("stsc", 12)
	if err != nil {
		return s, err
	}
	perChunk := make([]uint32, len(offsets))
	for i := range n {
		e := stsc[i*12:]
		first, spc := binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[4:])
		last := uint32(len(offsets))
		if i+1 < n {
			last = binary.BigEndian.Uint32(stsc[(i+1)*12:]) - 1
		}
		if first == 0 || first > last+1 || last > uint32(len(offsets)) {
			return s, errors.New("bad stsc box")
		}
		for c := first; c <= last; c++ {
			perChunk[c-1] = spc
		}
	}
	next := 0
	for i, off := range offsets {
		c := m4aChunk{offset: off}
		for range perChunk[i] {
			if next >= len(s.sizes) {
				return s, errors.New("stsc has more samples than stsz")
			}
			c.size += uint64(s.sizes[next])
			next++
		}
		s.chunks = append(s.chunks, c)
	}
	if next != len(s.sizes) {
		return s, fmt.Errorf("stsc has %d samples but stsz has %d", next, len(s.sizes))
	}
	return s, nil
}

// durationMs は音声の長さ（ミリ秒。ffmpeg と同じく切り上げ）。
func (m *m4aFile) durationMs() uint64 { return durationMillis(m.sound.samples, m.sound.timescale) }

// place は、チャプター chs を、入力（パート）の長さ parts でファイルの中の時刻に置く（chapter.go の placeChapters）。
// 最後のパートの長さは、ファイルの長さから前のパートの長さを引いたもの（parts の最後の値は使わない）。parts が空なら 1 つのパート。
func (m *m4aFile) place(chs []Chapter, parts []time.Duration) []placedChapter {
	rate, total := m.sound.timescale, m.sound.samples
	n := max(len(parts), 1)
	samples := make([]uint64, n)
	var sum uint64
	for i := range n - 1 {
		s := uint64(0)
		if parts[i] > 0 {
			s = uint64(math.Round(parts[i].Seconds() * float64(rate)))
		}
		samples[i] = min(s, total-sum)
		sum += samples[i]
	}
	samples[n-1] = total - sum
	return placeChapters(chs, samples, rate, m.durationMs())
}

// chapterTrackID は、書き足すチャプタートラックの番号。builtin・ffmpeg が書くときと同じく、カバー画像があれば 3、無ければ 2
// （buildMoov）。ほかのトラックの番号と重なるなら、その後ろにする。
func (m *m4aFile) chapterTrackID() uint32 {
	id := uint32(2)
	if m.cover {
		id = 3
	}
	for _, t := range m.trackIDs {
		if t >= id {
			id = t + 1
		}
	}
	return id
}

// WriteChapters は、保存した m4a in のチャプターを chs に置き換えた m4a を out に書き、置いたチャプターの数を返す（ADR 0038）。
// parts はパート（連結したファイルの、もとの放送回）ごとの長さで、Chapter.Input はその番号。2 つ目以降のパートのチャプターは、
// 前のパートの長さだけずらす（最後のパートの長さはファイルから決める）。音声・タグ・画像は in のまま残す。
//
// in の moov の音声のトラックとタグはバイト列のまま写し、前のチャプター（チャプタートラック・tref/chap・chpl）を除いて、
// RemuxWithChapters と同じ形のチャプターを書き足す。音声のサンプルはチャンクの並びのまま mdat に写し、チャンクの位置だけを
// 書き直す（moov は mdat の前に置く）。builtin が書いたファイルなら、同じチャプターで RemuxWithChapters したのと同じバイト列に
// なる（テストで確かめる）。out は上書きされ、失敗したら消す。
func (Builtin) WriteChapters(ctx context.Context, in, out string, chs []Chapter, parts []time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	src, err := os.Open(in)
	if err != nil {
		return 0, fmt.Errorf("media: write chapters: %w", err)
	}
	defer src.Close()
	m, err := readM4A(src)
	if err != nil {
		return 0, fmt.Errorf("media: write chapters: %s: %w", in, err)
	}
	placed := m.place(chs, parts)
	if err := m.writeWithChapters(ctx, src, out, placed); err != nil {
		os.Remove(out)
		return 0, fmt.Errorf("media: write chapters: %w", err)
	}
	return len(placed), nil
}

// writeWithChapters は、m（src から読んだもの）にチャプター placed を書き足した m4a を out に書く。
func (m *m4aFile) writeWithChapters(ctx context.Context, src *os.File, out string, placed []placedChapter) error {
	var chapterData []byte
	for _, ch := range placed {
		chapterData = append(chapterData, chapterSample(ch.title)...)
	}
	var audio uint64
	for _, c := range m.sound.chunks {
		audio += c.size
	}
	dataSize := audio + uint64(len(chapterData))
	largeMdat := dataSize+8 > math.MaxUint32
	mdatHeader := uint64(8)
	if largeMdat {
		mdatHeader = 16
	}
	co64 := m.sound.co64
	trial, err := m.buildMoov(placed, 0, co64)
	if err != nil {
		return err
	}
	if !co64 && uint64(len(m.ftyp))+uint64(len(trial))+mdatHeader+dataSize > math.MaxUint32 {
		co64 = true
		if trial, err = m.buildMoov(placed, 0, co64); err != nil {
			return err
		}
	}
	dataStart := uint64(len(m.ftyp)) + uint64(len(trial)) + mdatHeader
	moov, err := m.buildMoov(placed, dataStart, co64)
	if err != nil {
		return err
	}
	if len(moov) != len(trial) {
		return errors.New("moov size changed")
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	err = func() error {
		if _, err := w.Write(m.ftyp); err != nil {
			return err
		}
		if _, err := w.Write(moov); err != nil {
			return err
		}
		if largeMdat {
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
		buf := make([]byte, 1<<16)
		for i, c := range m.sound.chunks {
			if i%64 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if _, err := io.CopyBuffer(w, io.NewSectionReader(src, int64(c.offset), int64(c.size)), buf); err != nil {
				return err
			}
		}
		if _, err := w.Write(chapterData); err != nil {
			return err
		}
		return w.Flush()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// buildMoov は、m の moov から前のチャプターを除き、チャプター placed（無ければ書かない）を書き足した moov を組む。dataStart は
// mdat の中身の先頭の位置（音声のチャンクをその順に置き、チャプターのサンプルをその後ろに置く）、co64 は 64 ビットの位置で書くか。
func (m *m4aFile) buildMoov(placed []placedChapter, dataStart uint64, co64 bool) ([]byte, error) {
	offsets := make([]uint64, len(m.sound.chunks))
	off := dataStart
	for i, c := range m.sound.chunks {
		offsets[i] = off
		off += c.size
	}
	var chapterTrak, tref, chpl []byte
	next := m.nextTrackID
	if len(placed) > 0 {
		id := m.chapterTrackID()
		next = max(next, id+1)
		ws := &memWS{}
		bw := &boxWriter{w: mp4.NewWriter(ws)}
		writeChapterTrak(bw, id, placed, m.durationMs(), off, co64)
		if bw.err != nil {
			return nil, bw.err
		}
		chapterTrak = ws.buf
		tref = makeBox("tref", makeBox("chap", binary.BigEndian.AppendUint32(nil, id)))
		chpl = makeBox("chpl", chplPayload(placed))
	}
	kids, err := childBoxes(m.moov)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	sawUdta := false
	for i, k := range kids {
		switch {
		case k.typ == "mvhd":
			b := slices.Clone(k.b)
			binary.BigEndian.PutUint32(b[len(b)-4:], next)
			parts = append(parts, b)
		case i == m.soundIdx:
			b, err := rebuildSoundTrak(k, tref, offsets, co64)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
			if chapterTrak != nil {
				parts = append(parts, chapterTrak)
			}
		case i == m.chapterIdx:
			// 前のチャプタートラックは除く（サンプルも mdat に写さない）。
		case k.typ == "udta":
			sawUdta = true
			b, err := rebuildUdta(k, chpl)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
		default:
			parts = append(parts, k.b)
		}
	}
	if !sawUdta && chpl != nil {
		parts = append(parts, makeBox("udta", chpl))
	}
	return makeBox("moov", parts...), nil
}

// rebuildSoundTrak は、音声のトラックの trak から前の tref を除いて tref（nil なら書かない）を edts の後ろ（edts が無ければ tkhd
// の後ろ）に置き、チャンクの位置を offsets で書き直す。
func rebuildSoundTrak(trak m4aBox, tref []byte, offsets []uint64, co64 bool) ([]byte, error) {
	kids, err := childBoxes(trak)
	if err != nil {
		return nil, err
	}
	_, hasEdts := findBox(kids, "edts")
	var parts [][]byte
	for _, k := range kids {
		switch k.typ {
		case "tref":
			continue
		case "mdia":
			b, err := rebuildOffsets(k, offsets, co64)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
		default:
			parts = append(parts, k.b)
		}
		if tref != nil && (k.typ == "edts" || k.typ == "tkhd" && !hasEdts) {
			parts = append(parts, tref)
		}
	}
	return makeBox("trak", parts...), nil
}

// rebuildOffsets は、mdia・minf・stbl をたどって stco / co64 を offsets で書き直す（co64 なら co64 で書く）。
func rebuildOffsets(x m4aBox, offsets []uint64, co64 bool) ([]byte, error) {
	kids, err := childBoxes(x)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	for _, k := range kids {
		switch {
		case x.typ == "mdia" && k.typ == "minf", x.typ == "minf" && k.typ == "stbl":
			b, err := rebuildOffsets(k, offsets, co64)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
		case x.typ == "stbl" && (k.typ == "stco" || k.typ == "co64"):
			body := binary.BigEndian.AppendUint32(make([]byte, 4), uint32(len(offsets)))
			typ := "stco"
			if co64 {
				typ = "co64"
				for _, o := range offsets {
					body = binary.BigEndian.AppendUint64(body, o)
				}
			} else {
				for _, o := range offsets {
					body = binary.BigEndian.AppendUint32(body, uint32(o))
				}
			}
			parts = append(parts, makeBox(typ, body))
		default:
			parts = append(parts, k.b)
		}
	}
	return makeBox(x.typ, parts...), nil
}

// rebuildUdta は、udta から前の chpl を除き、chpl（nil なら書かない）を最後に置く（ffmpeg と同じく meta の後ろ）。
func rebuildUdta(udta m4aBox, chpl []byte) ([]byte, error) {
	kids, err := childBoxes(udta)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	for _, k := range kids {
		if k.typ != "chpl" {
			parts = append(parts, k.b)
		}
	}
	if chpl != nil {
		parts = append(parts, chpl)
	}
	return makeBox("udta", parts...), nil
}

// InputLengths は、開始時刻の順に並んだ ADTS ファイル inputs ごとの長さを、フレームの数から出す（Remux がファイルの中で
// 入力ごとに占める長さ。連結したファイルのパートの長さとして DB に残す。ADR 0038）。
func (Builtin) InputLengths(ctx context.Context, inputs []string) ([]time.Duration, error) {
	return inputLengths(ctx, inputs)
}

// SameAudio は、m4a a と b の音声のトラックのサンプルが、数とバイト列で同じかを確かめる（chapter.go の sameAudio）。
func (Builtin) SameAudio(ctx context.Context, a, b string) error { return sameAudio(ctx, a, b) }

// ---- アルバムの名前だけを書き直す RewriteAlbum（保存先の移し。ADR 0039） ----

// albumBox は、ilst の中の album のタグの箱の型（©alb）。
const albumBox = "\xa9alb"

// albumFollowers は、ilst の中で album のタグより後ろに書く箱の型（writeIlst の順。album のタグが無いファイルに足すときに使う）。
var albumFollowers = []string{"\xa9day", "\xa9cmt", "\xa9gen", "covr", "trkn", "disk"}

// RewriteAlbum は、保存した m4a in の album のタグを album に置き換えた m4a を out に書く（保存先の移し。ADR 0039）。ほかのタグ・
// 画像・チャプター・音声は in のまま残す（in の箱の並びをそのまま写し、moov の udta/meta/ilst の album の箱だけを書き換える。moov の
// 大きさが変わってサンプルの位置がずれるなら、全部のトラックのチャンクの位置をずらす）。in に album のタグが無ければ、writeIlst と同じ
// 位置に足す。読めるのは readM4A が読める箱の並びで、udta/meta/ilst が無ければエラーにする。out は上書きされ、失敗したら消す。
func (Builtin) RewriteAlbum(ctx context.Context, in, out, album string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	src, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("media: rewrite album: %w", err)
	}
	defer src.Close()
	if _, err := readM4A(src); err != nil {
		return fmt.Errorf("media: rewrite album: %s: %w", in, err)
	}
	if err := rewriteAlbum(ctx, src, out, album); err != nil {
		os.Remove(out)
		return fmt.Errorf("media: rewrite album: %w", err)
	}
	return nil
}

// topBox は、ファイルの最上位の箱 1 つの位置と大きさ。
type topBox struct {
	typ       string
	pos, size int64
}

// topBoxes は、f の最上位の箱の並びを読む（readM4A と同じ大きさの読み方）。
func topBoxes(f *os.File) ([]topBox, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	end := st.Size()
	var out []topBox
	hdr := make([]byte, 16)
	for pos := int64(0); pos < end; {
		if end-pos < 8 {
			return nil, fmt.Errorf("truncated box header at %d", pos)
		}
		if _, err := f.ReadAt(hdr[:8], pos); err != nil {
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(hdr))
		h := int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if _, err := f.ReadAt(hdr[8:16], pos+8); err != nil {
				return nil, err
			}
			size, h = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		}
		if size < h || size > end-pos {
			return nil, fmt.Errorf("bad size %d of %q box at %d", size, string(hdr[4:8]), pos)
		}
		out = append(out, topBox{typ: string(hdr[4:8]), pos: pos, size: size})
		pos += size
	}
	return out, nil
}

// rewriteAlbum は RewriteAlbum の中身。src は readM4A で読めた m4a。
func rewriteAlbum(ctx context.Context, src *os.File, out, album string) error {
	tops, err := topBoxes(src)
	if err != nil {
		return err
	}
	moovIdx := -1
	mdatBefore, mdatAfter := false, false
	for i, b := range tops {
		switch b.typ {
		case "moov":
			moovIdx = i
		case "mdat":
			if moovIdx < 0 {
				mdatBefore = true
			} else {
				mdatAfter = true
			}
		}
	}
	if moovIdx < 0 {
		return errors.New("no moov box")
	}
	if mdatBefore && mdatAfter {
		return errors.New("mdat boxes on both sides of moov")
	}
	raw := make([]byte, tops[moovIdx].size)
	if _, err := src.ReadAt(raw, tops[moovIdx].pos); err != nil {
		return err
	}
	boxes, err := parseBoxes(raw)
	if err != nil {
		return err
	}
	moov := boxes[0]
	// moov が mdat の前にあれば、moov の大きさの差だけサンプルの位置がずれる。ずれは moov の大きさで決まり、ずらしても大きさは
	// 変わらない（チャンクの位置の表の幅を変えない）ので、ずらさずに組んだ大きさから決める。
	trial, err := albumMoov(moov, album, 0)
	if err != nil {
		return err
	}
	var shift int64
	if mdatAfter {
		shift = int64(len(trial)) - int64(len(raw))
	}
	newMoov, err := albumMoov(moov, album, shift)
	if err != nil {
		return err
	}
	if len(newMoov) != len(trial) {
		return errors.New("moov size changed")
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	err = func() error {
		buf := make([]byte, 1<<16)
		for i, b := range tops {
			if err := ctx.Err(); err != nil {
				return err
			}
			if i == moovIdx {
				if _, err := w.Write(newMoov); err != nil {
					return err
				}
				continue
			}
			if _, err := io.CopyBuffer(w, io.NewSectionReader(src, b.pos, b.size), buf); err != nil {
				return err
			}
		}
		return w.Flush()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// albumMoov は、moov の udta/meta/ilst の album の箱を album にし、全部のトラックのチャンクの位置を shift だけずらした moov を組む。
func albumMoov(moov m4aBox, album string, shift int64) ([]byte, error) {
	kids, err := childBoxes(moov)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	sawUdta := false
	for _, k := range kids {
		switch k.typ {
		case "trak":
			b, err := shiftOffsets(k, shift)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
		case "udta":
			sawUdta = true
			b, err := albumUdta(k, album)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
		default:
			parts = append(parts, k.b)
		}
	}
	if !sawUdta {
		return nil, errors.New("no udta box (no tags)")
	}
	return makeBox("moov", parts...), nil
}

// shiftOffsets は、trak・mdia・minf・stbl をたどって stco / co64 の位置を shift だけずらす（表の幅は変えない。stco で 32 ビットに
// 収まらなければエラー）。shift が 0 なら x をそのまま返す。
func shiftOffsets(x m4aBox, shift int64) ([]byte, error) {
	if shift == 0 {
		return x.b, nil
	}
	kids, err := childBoxes(x)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	for _, k := range kids {
		switch {
		case x.typ == "trak" && k.typ == "mdia", x.typ == "mdia" && k.typ == "minf", x.typ == "minf" && k.typ == "stbl":
			b, err := shiftOffsets(k, shift)
			if err != nil {
				return nil, err
			}
			parts = append(parts, b)
		case x.typ == "stbl" && (k.typ == "stco" || k.typ == "co64"):
			p := k.payload()
			entry := 4
			if k.typ == "co64" {
				entry = 8
			}
			if len(p) < 8 {
				return nil, fmt.Errorf("truncated %s box", k.typ)
			}
			n := binary.BigEndian.Uint32(p[4:])
			if uint64(len(p)-8) < uint64(n)*uint64(entry) {
				return nil, fmt.Errorf("truncated %s box", k.typ)
			}
			body := slices.Clone(p)
			for i := range int(n) {
				e := body[8+i*entry:]
				if entry == 4 {
					v := int64(binary.BigEndian.Uint32(e)) + shift
					if v < 0 || v > math.MaxUint32 {
						return nil, errors.New("chunk offset out of range of stco")
					}
					binary.BigEndian.PutUint32(e, uint32(v))
				} else {
					v := int64(binary.BigEndian.Uint64(e)) + shift
					if v < 0 {
						return nil, errors.New("negative chunk offset")
					}
					binary.BigEndian.PutUint64(e, uint64(v))
				}
			}
			parts = append(parts, makeBox(k.typ, body))
		default:
			parts = append(parts, k.b)
		}
	}
	return makeBox(x.typ, parts...), nil
}

// albumUdta は、udta の meta の ilst の album の箱を album にした udta を組む。meta・ilst が無ければエラー。
func albumUdta(udta m4aBox, album string) ([]byte, error) {
	kids, err := childBoxes(udta)
	if err != nil {
		return nil, err
	}
	var parts [][]byte
	sawMeta := false
	for _, k := range kids {
		if k.typ != "meta" || sawMeta {
			parts = append(parts, k.b)
			continue
		}
		sawMeta = true
		// meta は FullBox（版とフラグの 4 バイトの後に子の箱）。
		p := k.payload()
		if len(p) < 4 {
			return nil, errors.New("truncated meta box")
		}
		items, err := parseBoxes(p[4:])
		if err != nil {
			return nil, fmt.Errorf("meta: %w", err)
		}
		metaParts := [][]byte{p[:4]}
		sawIlst := false
		for _, it := range items {
			if it.typ != "ilst" || sawIlst {
				metaParts = append(metaParts, it.b)
				continue
			}
			sawIlst = true
			b, err := albumIlst(it, album)
			if err != nil {
				return nil, err
			}
			metaParts = append(metaParts, b)
		}
		if !sawIlst {
			return nil, errors.New("no ilst box (no tags)")
		}
		parts = append(parts, makeBox("meta", metaParts...))
	}
	if !sawMeta {
		return nil, errors.New("no meta box (no tags)")
	}
	return makeBox("udta", parts...), nil
}

// albumIlst は、ilst の album の箱を album の値（writeIlst と同じ UTF-8 の data）に置き換えた ilst を組む。album の箱が無ければ
// albumFollowers の最初の箱の前（どれも無ければ最後）に足す。album が空なら album の箱を除く。
func albumIlst(ilst m4aBox, album string) ([]byte, error) {
	items, err := childBoxes(ilst)
	if err != nil {
		return nil, err
	}
	var item []byte
	if album != "" {
		data := binary.BigEndian.AppendUint32(nil, dataTypeUTF8)
		data = binary.BigEndian.AppendUint32(data, 0)
		item = makeBox(albumBox, makeBox("data", data, []byte(album)))
	}
	var parts [][]byte
	placed := false
	for _, it := range items {
		if it.typ == albumBox {
			if !placed && item != nil {
				parts = append(parts, item)
			}
			placed = true
			continue
		}
		if !placed && slices.Contains(albumFollowers, it.typ) {
			if item != nil {
				parts = append(parts, item)
			}
			placed = true
		}
		parts = append(parts, it.b)
	}
	if !placed && item != nil {
		parts = append(parts, item)
	}
	return makeBox("ilst", parts...), nil
}
