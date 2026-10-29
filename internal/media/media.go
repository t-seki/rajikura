// Package media は、入手した ADTS（AAC）を再エンコードせずに m4a へ格納し（タグ・カバー画像・チャプターも書く）、長さを測る。
// 作り方は 2 つあり、設定 muxer で選ぶ（ADR 0013）。どちらも同じ形の箱を書く（チャプターの形は chapter.go。ADR 0027）。
//
//   - [Tools]: ffmpeg / ffprobe を子プロセスとして呼ぶ（ADR 0001: ffmpeg にネットワークを触らせない。
//     手元のファイルから m4a を作る 1 回だけに使う）。このファイル
//   - [Builtin]: ffmpeg / ffprobe を使わず、Go で m4a を作る（builtin.go）
//
// # ffmpeg / ffprobe のパス
//
// パスは [New] の引数（設定）で受け取る。空文字なら、PATH の上の "ffmpeg" / "ffprobe" を使う。
//
// Windows で chocolatey が入れた PATH 上の ffmpeg.exe は、本体を子プロセスで起動する
// 中継（shim）である。shim を止めても本体は残る（事前検証で起きた）。context の取り消しで
// 止められるのは shim だけなので、取り消した後も ffmpeg 本体が残りうる。プロセスツリーまで
// 止める仕組みは作らない。確実に止めたいときは、設定で shim ではなく本体
// （例: C:\ProgramData\chocolatey\lib\ffmpeg\tools\ffmpeg\bin\ffmpeg.exe）のパスを指定する。
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// stderrTail はエラーに含める stderr の末尾のバイト数。
	stderrTail = 4096
	// waitDelay は context の取り消し後、子プロセスの I/O の終了を待つ時間。
	waitDelay = 5 * time.Second
)

// Tags は m4a に書くタグ。空文字（Track / Disc は 0）のものは書かない。
// 名前は ffmpeg の mp4 muxer のキーに対応する。
type Tags struct {
	Title       string // title
	Album       string // album
	Artist      string // artist
	AlbumArtist string // album_artist
	Date        string // date
	Genre       string // genre
	Comment     string // comment
	Track       int    // track
	Disc        int    // disc
}

// metadata は -metadata に渡す key=value の並び。
func (t Tags) metadata() []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	add("title", t.Title)
	add("album", t.Album)
	add("artist", t.Artist)
	add("album_artist", t.AlbumArtist)
	add("date", t.Date)
	add("genre", t.Genre)
	add("comment", t.Comment)
	if t.Track > 0 {
		add("track", strconv.Itoa(t.Track))
	}
	if t.Disc > 0 {
		add("disc", strconv.Itoa(t.Disc))
	}
	return out
}

// Tools は ffmpeg / ffprobe のパスを持つ。
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// New は Tools を返す。空文字のパスは PATH 上の既定の名前になる。
func New(ffmpeg, ffprobe string) *Tools {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	return &Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// Remux は、開始時刻の順に並んだ ADTS ファイル inputs を -c copy で 1 本の m4a out にし、
// 同じ呼び出しで tags を書く。out は上書きされる。
// cover が空でなければ、その JPEG をカバー画像（attached_pic）として埋め込む。チャプターは書かない（RemuxWithChapters）。
func (t *Tools) Remux(ctx context.Context, inputs []string, out string, tags Tags, cover string) error {
	return t.RemuxWithChapters(ctx, inputs, out, tags, cover, nil)
}

// RemuxWithChapters は Remux と同じく m4a を作り、chapters（chapter.go）も書く。chapters があれば、入力の ADTS を一度読んで
// 入力ごとの長さを数え、ファイルの中の時刻に置いたチャプターを ffmetadata のファイルにして渡す（ffmpeg は chpl と
// チャプタートラックの両方を書く）。chapters が空（または全部捨てられる）なら Remux と同じファイルになる。
//
// 入力が 1 つなら、concat demuxer にそのファイルだけを並べたリストを渡す。
//
// 入力が 2 つ以上（分割番組の連結）なら、ADTS をバイト列のまま順に ffmpeg の標準入力へ流し、
// 1 本の ADTS として読ませる。concat demuxer で並べると、ADTS には長さの情報が無いので、
// ffmpeg は各ファイルの長さをビットレートから推定し、それを次のファイルの開始時刻にする。
// 可変ビットレートの HE-AAC では推定が実際とずれ、短く推定した継ぎ目では次のファイルの
// タイムスタンプが重なって DTS が詰められ、m4a が短くなる（3 つの放送回をつないだ実例で約 30 秒）。
// バイト列のままつなげば、タイムスタンプはフレームの数から決まるので継ぎ目でずれない
// （放送回の中のチャンクも、入手がバイト列でつないでいる）。
func (t *Tools) RemuxWithChapters(ctx context.Context, inputs []string, out string, tags Tags, cover string, chs []Chapter) error {
	if len(inputs) == 0 {
		return errors.New("media: remux: no inputs")
	}
	var chapters []placedChapter
	var durationMs uint64
	if len(chs) > 0 {
		frames, cfg, err := scanADTS(ctx, inputs)
		if err != nil {
			return fmt.Errorf("media: remux: %w", err)
		}
		durationMs = durationMillis(uint64(len(frames))*samplesPerFrame, cfg.sampleRate())
		chapters = placeChapters(chs, inputSampleCounts(frames, len(inputs)), cfg.sampleRate(), durationMs)
	}
	dir, err := os.MkdirTemp("", "rajikura-concat-")
	if err != nil {
		return fmt.Errorf("media: remux: %w", err)
	}
	defer os.RemoveAll(dir)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
	var stdin io.ReadCloser
	if len(inputs) == 1 {
		abs, err := filepath.Abs(inputs[0])
		if err != nil {
			return fmt.Errorf("media: remux: %w", err)
		}
		list := filepath.Join(dir, "list.txt")
		if err := os.WriteFile(list, []byte(concatList([]string{abs})), 0o600); err != nil {
			return fmt.Errorf("media: remux: %w", err)
		}
		args = append(args, "-f", "concat", "-safe", "0", "-i", list)
	} else {
		args = append(args, "-f", "aac", "-i", "pipe:0")
		stdin = &filesReader{files: inputs}
	}
	if cover != "" {
		args = append(args, "-i", cover)
	}
	if len(chapters) > 0 {
		meta := filepath.Join(dir, "chapters.txt")
		if err := os.WriteFile(meta, []byte(ffmetadata(chapters, durationMs)), 0o600); err != nil {
			return fmt.Errorf("media: remux: %w", err)
		}
		idx := 1
		if cover != "" {
			idx = 2
		}
		args = append(args, "-f", "ffmetadata", "-i", meta, "-map_chapters", strconv.Itoa(idx))
	}
	args = append(args, "-map", "0:a")
	if cover != "" {
		args = append(args, "-map", "1:v")
	}
	args = append(args, "-c", "copy", "-bsf:a", "aac_adtstoasc")
	if cover != "" {
		args = append(args, "-disposition:v:0", "attached_pic")
	}
	for _, m := range tags.metadata() {
		args = append(args, "-metadata", m)
	}
	args = append(args, "-f", "mp4", out)

	if _, err := runWithStdin(ctx, stdin, t.FFmpeg, args...); err != nil {
		return fmt.Errorf("media: remux: %w", err)
	}
	return nil
}

// filesReader は files の中身を順に読む io.ReadCloser。ファイルは 1 つずつ開いて閉じる。
// Close は、別の goroutine が Write で止まっている間にも呼べる（開いているファイルを閉じ、
// それ以降の Read は失敗する）。開く・読むときの失敗は [inputError] で返す。
type filesReader struct {
	mu     sync.Mutex
	files  []string
	cur    *os.File
	closed bool
}

func (r *filesReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if r.closed {
			return 0, os.ErrClosed
		}
		if r.cur == nil {
			if len(r.files) == 0 {
				return 0, io.EOF
			}
			f, err := os.Open(r.files[0])
			if err != nil {
				return 0, &inputError{err: err}
			}
			r.cur = f
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			r.cur.Close()
			r.cur = nil
			r.files = r.files[1:]
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, &inputError{err: fmt.Errorf("%s: %w", r.cur.Name(), err)}
		}
		return n, nil
	}
}

func (r *filesReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.cur != nil {
		r.cur.Close()
		r.cur = nil
	}
	return nil
}

// inputError は、標準入力に流す入力そのものの失敗（開けない・読めない）。
// ffmpeg が先に閉じたことで書き込みが失敗したのとは分けて扱う。
type inputError struct{ err error }

func (e *inputError) Error() string { return e.err.Error() }
func (e *inputError) Unwrap() error { return e.err }

// concatList は concat demuxer のリストファイルの中身を作る。
// パスはスラッシュに直し、単一引用符は「引用を閉じ、\ で逃がした ' を置き、再び開く」形にする。
func concatList(inputs []string) string {
	var b strings.Builder
	for _, in := range inputs {
		p := strings.ReplaceAll(filepath.ToSlash(in), "'", `'\''`)
		b.WriteString("file '" + p + "'\n")
	}
	return b.String()
}

// Duration は ffprobe で file の長さを測る。
func (t *Tools) Duration(ctx context.Context, file string) (time.Duration, error) {
	out, err := run(ctx, t.FFprobe,
		"-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", file)
	if err != nil {
		return 0, fmt.Errorf("media: duration: %w", err)
	}
	s := strings.TrimSpace(string(out))
	sec, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("media: duration: unexpected ffprobe output %q", s)
	}
	return time.Duration(sec * float64(time.Second)), nil
}

// run は子プロセスを走らせ、stdout を返す。失敗したら stderr の末尾をエラーに含める。
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runWithStdin(ctx, nil, name, args...)
}

// stdinWaitDelay は、子プロセスが終わった後、標準入力への書き込みが止まるのを待つ上限。
// テストで短くするために変数にしている。
var stdinWaitDelay = waitDelay

// runWithStdin は run と同じだが、stdin が nil でなければ、別の goroutine で stdin を読んで
// 子プロセスの標準入力に書き、読み終えたら標準入力を閉じる（子プロセスには EOF が届く）。
// 戻るときには stdin は閉じられている（stdin が開いていたファイルも閉じている）。
//
// 子プロセスが成功しても、書き込みが失敗したら失敗にする（途中までの入力で作った出力を成功にしないため）。
// 子プロセスが失敗したときは、入力そのものの失敗（[inputError]）なら子プロセスのエラーと合わせて返す。
// それ以外の書き込みの失敗（子プロセスが先に閉じた）は捨てる。
//
// 子プロセスが終わっても、標準入力の読み側を別のプロセスが握っていると、書き込みが戻らないことがある
// （Windows の chocolatey の shim を取り消したとき、本体が残る。パッケージの doc コメント）。
// WaitDelay は標準入力の書き込み側には効かないので、Wait の後 stdinWaitDelay だけ待っても
// 書き込みが止まらなければ、stdin と標準入力のパイプを閉じて戻る。Windows ではパイプへの
// 同期的な書き込みを閉じるだけでは止められないことがあり、その goroutine は読み側のプロセスが
// 終わるまで残りうる（入力のファイルはもう閉じているので掴まない）。
func runWithStdin(ctx context.Context, stdin io.ReadCloser, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	var stdout bytes.Buffer
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	var (
		pw   io.WriteCloser
		done chan error
	)
	if stdin != nil {
		defer stdin.Close()
		var err error
		if pw, err = cmd.StdinPipe(); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
		}
	}
	err := cmd.Start()
	if err == nil && stdin != nil {
		done = make(chan error, 1)
		go func() {
			_, err := io.Copy(pw, stdin)
			if cerr := pw.Close(); err == nil && !errors.Is(cerr, os.ErrClosed) {
				err = cerr
			}
			done <- err
		}()
	}
	if err == nil {
		err = cmd.Wait()
	} else if pw != nil {
		pw.Close()
	}
	var inErr error
	if done != nil {
		timer := time.NewTimer(stdinWaitDelay)
		select {
		case inErr = <-done:
		case <-timer.C:
			stdin.Close()
			pw.Close()
			inErr = errors.New("writing stdin did not stop after the process exited")
		}
		timer.Stop()
	}
	var ie *inputError
	switch {
	case err == nil && inErr != nil:
		err = fmt.Errorf("stdin: %w", inErr)
	case err != nil && errors.As(inErr, &ie):
		err = errors.Join(err, fmt.Errorf("stdin: %w", inErr))
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = fmt.Errorf("%w (%v)", ctxErr, err)
		}
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, tail)
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.Bytes(), nil
}

// tailBuffer は書かれたデータの末尾 max バイトだけを保持する。
type tailBuffer struct {
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
