package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	tl := New("", "")
	if tl.FFmpeg != "ffmpeg" || tl.FFprobe != "ffprobe" {
		t.Fatalf("defaults = %+v", tl)
	}
	tl = New("/x/ffmpeg", "/x/ffprobe")
	if tl.FFmpeg != "/x/ffmpeg" || tl.FFprobe != "/x/ffprobe" {
		t.Fatalf("explicit = %+v", tl)
	}
}

func TestTagsMetadata(t *testing.T) {
	got := Tags{
		Title: "2026-10-11", Album: "番組", Artist: "出演者", AlbumArtist: "局",
		Date: "2026-10-11", Genre: "ラジオ", Comment: "説明", Track: 1011, Disc: 2026,
	}.metadata()
	want := []string{
		"title=2026-10-11", "album=番組", "artist=出演者", "album_artist=局",
		"date=2026-10-11", "genre=ラジオ", "comment=説明", "track=1011", "disc=2026",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("metadata = %q, want %q", got, want)
	}
	if got := (Tags{}).metadata(); len(got) != 0 {
		t.Fatalf("empty tags metadata = %q", got)
	}
}

func TestConcatList(t *testing.T) {
	got := concatList([]string{`C:\a\b.aac`, "/tmp/it's.aac"})
	want := "file 'C:/a/b.aac'\n" + `file '/tmp/it'\''s.aac'` + "\n"
	// Linux では filepath.ToSlash は何もしないので、バックスラッシュの行は Windows のときだけ比べる。
	if os.PathSeparator != '\\' {
		want = `file 'C:\a\b.aac'` + "\n" + `file '/tmp/it'\''s.aac'` + "\n"
	}
	if got != want {
		t.Fatalf("concatList = %q, want %q", got, want)
	}
}

func TestRemuxWithoutInputs(t *testing.T) {
	if err := New("", "").Remux(context.Background(), nil, "x.m4a", Tags{}, ""); err == nil {
		t.Fatal("want error")
	}
}

func TestRunMissingCommand(t *testing.T) {
	_, err := run(context.Background(), "definitely-not-a-command-rajikura")
	if err == nil {
		t.Fatal("want error")
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 4}
	b.Write([]byte("abc"))
	b.Write([]byte("defg"))
	if b.String() != "defg" {
		t.Fatalf("tail = %q", b.String())
	}
}

// requireFFmpeg は ffmpeg / ffprobe が見つからなければテストを飛ばす。
// Windows で chocolatey の shim（PATH 上の ffmpeg.exe）が見つかったときは、本体を使う。
// shim を止めても本体が残り、入力ファイルを掴んだままになるため（media.go の doc コメント）。
func requireFFmpeg(t *testing.T) *Tools {
	t.Helper()
	var paths [2]string
	for i, n := range []string{"ffmpeg", "ffprobe"} {
		p, err := exec.LookPath(n)
		if err != nil {
			t.Skipf("%s not found", n)
		}
		paths[i] = chocolateyBody(p)
	}
	return New(paths[0], paths[1])
}

// chocolateyBody は、p が chocolatey の shim なら本体のパスを返す。そうでなければ p を返す。
func chocolateyBody(p string) string {
	if runtime.GOOS != "windows" {
		return p
	}
	dir := filepath.Dir(p)
	if !strings.EqualFold(filepath.Base(dir), "bin") || !strings.EqualFold(filepath.Base(filepath.Dir(dir)), "chocolatey") {
		return p
	}
	body := filepath.Join(filepath.Dir(dir), "lib", "ffmpeg", "tools", "ffmpeg", "bin", filepath.Base(p))
	if _, err := os.Stat(body); err != nil {
		return p
	}
	return body
}

// makeADTS は duration 秒の正弦波を ADTS（AAC）で作る。
func makeADTS(t *testing.T, tl *Tools, path string, seconds string) {
	t.Helper()
	_, err := run(context.Background(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=d="+seconds, "-c:a", "aac", "-f", "adts", path)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemuxAndDuration(t *testing.T) {
	tl := requireFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac")
	makeADTS(t, tl, a, "3")
	makeADTS(t, tl, b, "3")

	out := filepath.Join(dir, "out.m4a")
	tags := Tags{
		Title: "2026-10-11", Album: "番組", Artist: "出演者", AlbumArtist: "局",
		Date: "2026-10-11", Genre: "ラジオ", Comment: "説明", Track: 1011, Disc: 2026,
	}
	if err := tl.Remux(ctx, []string{a, b}, out, tags, ""); err != nil {
		t.Fatal(err)
	}

	d, err := tl.Duration(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if d < 5500*time.Millisecond || d > 6500*time.Millisecond {
		t.Fatalf("duration = %v, want about 6s", d)
	}

	probed, err := run(ctx, tl.FFprobe, "-v", "error", "-show_entries", "format_tags",
		"-of", "default=noprint_wrappers=1", out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(probed)
	for _, want := range []string{
		"TAG:title=2026-10-11", "TAG:album=番組", "TAG:artist=出演者", "TAG:album_artist=局",
		"TAG:date=2026-10-11", "TAG:genre=ラジオ", "TAG:comment=説明", "TAG:disc=2026",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ffprobe output lacks %q:\n%s", want, got)
		}
	}
	if !hasLine(got, "TAG:track=1011") {
		t.Errorf("ffprobe output lacks exact track=1011:\n%s", got)
	}
	if !hasLine(got, "TAG:disc=2026") {
		t.Errorf("ffprobe output lacks exact disc=2026:\n%s", got)
	}
}

// hasLine は out に want と完全に一致する行があるかを返す。
func hasLine(out, want string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

func TestRemuxSingleInput(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.aac")
	makeADTS(t, tl, a, "2")
	out := filepath.Join(dir, "out.m4a")
	if err := tl.Remux(context.Background(), []string{a}, out, Tags{Title: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	d, err := tl.Duration(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if d < 1500*time.Millisecond || d > 2500*time.Millisecond {
		t.Fatalf("duration = %v, want about 2s", d)
	}
}

// makeSkewedADTS は、無音と雑音を silence 秒・noise 秒ずつつないだ ADTS を作る（silenceFirst なら無音が先）。
// 無音のフレームは小さく雑音のフレームは大きいので、ffprobe が先頭のビットレートから推定する長さは
// 実際の長さからずれる（放送回ごとの推定長が実長とずれる条件を作る）。
func makeSkewedADTS(t *testing.T, tl *Tools, path string, silenceFirst bool, silence, noise string) {
	t.Helper()
	s := "anullsrc=r=44100:cl=mono:d=" + silence
	n := "anoisesrc=r=44100:a=0.5:d=" + noise
	first, second := n, s
	if silenceFirst {
		first, second = s, n
	}
	_, err := run(context.Background(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", first, "-f", "lavfi", "-i", second,
		"-filter_complex", "[0][1]concat=n=2:v=0:a=1", "-c:a", "aac", "-f", "adts", path)
	if err != nil {
		t.Fatal(err)
	}
}

// countPackets は、file の音声のパケットの数とサンプリングレートを返す。
func countPackets(t *testing.T, tl *Tools, file string) (packets, rate int) {
	t.Helper()
	out, err := run(context.Background(), tl.FFprobe, "-v", "error", "-count_packets", "-select_streams", "a:0",
		"-show_entries", "stream=nb_read_packets,sample_rate", "-of", "csv=p=0", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d,%d", &rate, &packets); err != nil {
		t.Fatalf("ffprobe output %q: %v", out, err)
	}
	return packets, rate
}

// packetsDuration は、AAC のパケット数から長さを返す（1 フレームは 1024 サンプル）。
func packetsDuration(packets, rate int) time.Duration {
	return time.Duration(float64(packets) * 1024 / float64(rate) * float64(time.Second))
}

// TestRemuxJoinKeepsActualLength は、ビットレートからの推定長が実長とずれる ADTS をつないでも、
// m4a の長さが各ファイルの実長の和に合うことを確かめる。
func TestRemuxJoinKeepsActualLength(t *testing.T) {
	tl := requireFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	// 雑音が先のものは推定が実長より短く、無音が先のものは長くなる。
	inputs := []string{filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac"), filepath.Join(dir, "c.aac")}
	makeSkewedADTS(t, tl, inputs[0], false, "6", "4")
	makeSkewedADTS(t, tl, inputs[1], true, "3", "7")
	makeSkewedADTS(t, tl, inputs[2], false, "5", "5")

	var wantPackets, rate int
	for _, in := range inputs {
		p, r := countPackets(t, tl, in)
		if rate != 0 && r != rate {
			t.Fatalf("%s: sample rate %d, want %d", filepath.Base(in), r, rate)
		}
		rate = r
		actual := packetsDuration(p, r)
		est, err := tl.Duration(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		// 前提: 推定が実長から 1 秒以上ずれている。ずれていなければ、このテストは何も確かめていない。
		if diff := (est - actual).Abs(); diff < time.Second {
			t.Fatalf("%s: estimated %v vs actual %v; want the estimate to be off by 1s or more", filepath.Base(in), est, actual)
		}
		wantPackets += p
	}
	want := packetsDuration(wantPackets, rate)

	out := filepath.Join(dir, "out.m4a")
	if err := tl.Remux(ctx, inputs, out, Tags{Title: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	d, err := tl.Duration(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if diff := (d - want).Abs(); diff > 100*time.Millisecond {
		t.Fatalf("duration = %v, want %v (sum of actual lengths); off by %v", d, want, diff)
	}
	if got, _ := countPackets(t, tl, out); got != wantPackets {
		t.Fatalf("packets in output = %d, want %d", got, wantPackets)
	}
}

// TestRemuxMissingFirstInputWhileJoining は、連結で先頭の入力が無いとき、ffmpeg のエラーだけでなく
// 欠けたファイルのパスがエラーに入ることを確かめる。
func TestRemuxMissingFirstInputWhileJoining(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	b := filepath.Join(dir, "b.aac")
	makeADTS(t, tl, b, "1")
	err := tl.Remux(context.Background(), []string{filepath.Join(dir, "missing.aac"), b}, filepath.Join(dir, "o.m4a"), Tags{}, "")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "missing.aac") {
		t.Fatalf("error lacks the missing input: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist in the chain", err)
	}
}

// helperEnv は、テストのバイナリを子プロセスとして使うときの役割を渡す環境変数。
const helperEnv = "RAJIKURA_MEDIA_TEST_HELPER"

// TestHelperProcess は子プロセスとして呼ばれたときだけ動く（helperEnv が無ければ何もしない）。
//   - shim: chocolatey の shim に見立てる。標準入力を引き継いだ孫プロセス（hold）を起動して待つ。
//   - hold: 標準入力を読まずに居続ける。PID を RAJIKURA_MEDIA_TEST_PIDFILE に書く。
func TestHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "shim":
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), helperEnv+"=hold")
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "hold":
		if err := os.WriteFile(os.Getenv("RAJIKURA_MEDIA_TEST_PIDFILE"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// TestRunWithStdinReturnsWhileReaderIsHeld は、取り消した後に標準入力の読み側を別のプロセスが
// 握り続けても、runWithStdin が戻り、入力のファイルを掴んでいないことを確かめる
// （Windows の chocolatey の shim を取り消すと本体が残る。パッケージの doc コメント）。
func TestRunWithStdinReturnsWhileReaderIsHeld(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	t.Setenv(helperEnv, "shim")
	t.Setenv("RAJIKURA_MEDIA_TEST_PIDFILE", pidFile)
	old := stdinWaitDelay
	stdinWaitDelay = 500 * time.Millisecond
	t.Cleanup(func() { stdinWaitDelay = old })

	// パイプのバッファより大きい入力にして、書き込みが止まるようにする。
	in := filepath.Join(dir, "in.aac")
	if err := os.WriteFile(in, make([]byte, 16<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runWithStdin(ctx, &filesReader{files: []string{in}}, os.Args[0], "-test.run=^TestHelperProcess$")
		done <- err
	}()
	t.Cleanup(func() { killHelper(t, pidFile) })

	// 孫プロセスが起動するのを待ってから取り消す。
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(waitDelay + 10*time.Second):
		t.Fatal("runWithStdin did not return while the reader is held by another process")
	}
	// 戻った時点で入力を掴んでいない。Windows では掴まれていると消せない。
	if err := os.Remove(in); err != nil {
		t.Fatalf("input still in use: %v", err)
	}
}

// blockingReader は、Close されるまで Read が戻らない io.ReadCloser。
type blockingReader struct {
	once   sync.Once
	closed chan struct{}
}

func (r *blockingReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, os.ErrClosed
}

func (r *blockingReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

// TestRunWithStdinStopsStuckWriter は、子プロセスが終わっても標準入力への書き込みの goroutine が
// 戻らないとき、stdinWaitDelay の後に stdin を閉じて戻り、失敗にすることを確かめる。
// TestRunWithStdinReturnsWhileReaderIsHeld は、手元の Windows（Go 1.27）では Wait がパイプを
// 閉じた時点で書き込みが戻り、待つ上限が無くても通る。こちらは待つ上限そのものを確かめる。
func TestRunWithStdinStopsStuckWriter(t *testing.T) {
	old := stdinWaitDelay
	stdinWaitDelay = 500 * time.Millisecond
	t.Cleanup(func() { stdinWaitDelay = old })

	r := &blockingReader{closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		// helperEnv が無いので、子プロセスはすぐに終わる。
		_, err := runWithStdin(context.Background(), r, os.Args[0], "-test.run=^TestHelperProcess$")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not stop") {
			t.Fatalf("err = %v, want the stuck writer reported", err)
		}
	case <-time.After(waitDelay + 10*time.Second):
		r.Close()
		t.Fatal("runWithStdin did not return while writing stdin is stuck")
	}
}

// killHelper は、PID ファイルに書かれた孫プロセスを止める。
func killHelper(t *testing.T, pidFile string) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	p.Kill()
	p.Wait() // Windows では、止まるまで待たないとテストのバイナリを消せない。Linux では孫は子でないのでエラーになるが構わない。
}

func TestRemuxMissingInputWhileJoining(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.aac")
	makeADTS(t, tl, a, "1")
	err := tl.Remux(context.Background(), []string{a, filepath.Join(dir, "missing.aac")}, filepath.Join(dir, "o.m4a"), Tags{}, "")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "missing.aac") {
		t.Fatalf("error lacks the missing input: %v", err)
	}
}

func TestRemuxFailureCarriesStderr(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	err := tl.Remux(context.Background(), []string{filepath.Join(dir, "missing.aac")}, filepath.Join(dir, "o.m4a"), Tags{}, "")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "missing.aac") {
		t.Fatalf("error lacks ffmpeg stderr: %v", err)
	}
}

func TestRemuxCanceledWhileRunning(t *testing.T) {
	tl := requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.aac")
	makeADTS(t, tl, a, "1")
	// 同じ入力を大量に並べて、取り消すまで ffmpeg が動き続けるようにする。
	inputs := make([]string, 20000)
	for i := range inputs {
		inputs[i] = a
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tl.Remux(ctx, inputs, filepath.Join(dir, "o.m4a"), Tags{}, "") }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		// 取り消す前に正常に終わっていたら、取り消しの確認にならない。
		if err == nil {
			t.Fatal("remux finished before cancel; make the input longer")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		// Remux が返った時点で ffmpeg は入力を掴んでいない。Windows では掴まれていると消せない。
		if err := os.Remove(a); err != nil {
			t.Fatalf("input still in use after cancel: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("remux did not stop after cancel")
	}
}
