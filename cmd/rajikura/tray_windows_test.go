//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/t-seki/rajikura/internal/trayconf"
)

// TestTrayHelperProcess は、トレイの代わりに起動される（startHelper）。ふつうのテストの実行では何もしない。
//   - RAJIKURA_TRAY_HELPER=1: 1 分眠る（動いている exe の代わり）
//   - RAJIKURA_TRAY_HELPER=window: RAJIKURA_TRAY_HELPER_MUTEX の mutex を取り、RAJIKURA_TRAY_HELPER_CLASS の隠れた窓を作って、
//     窓が壊れるまで（WM_CLOSE を受けるまで）メッセージを回す（fyne systray の窓の代わり）
//   - RAJIKURA_TRAY_HELPER=report: 親のプロセスの PID と、環境変数 RAJIKURA_TRAY_HELPER_ENV を RAJIKURA_TRAY_HELPER_OUT に書く
func TestTrayHelperProcess(t *testing.T) {
	switch os.Getenv("RAJIKURA_TRAY_HELPER") {
	case "1":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "window":
		os.Exit(helperWindow(os.Getenv("RAJIKURA_TRAY_HELPER_CLASS"), os.Getenv("RAJIKURA_TRAY_HELPER_MUTEX")))
	case "report":
		s := fmt.Sprintf("%d %s", os.Getppid(), os.Getenv("RAJIKURA_TRAY_HELPER_ENV"))
		if err := os.WriteFile(os.Getenv("RAJIKURA_TRAY_HELPER_OUT"), []byte(s), 0o644); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

var (
	procRegisterClassEx  = user32.NewProc("RegisterClassExW")
	procCreateWindowEx   = user32.NewProc("CreateWindowExW")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procGetMessage       = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessage  = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}

type winMsg struct {
	Hwnd           windows.HWND
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             struct{ X, Y int32 }
}

// helperWindow は、mutex を取り、class の隠れたトップレベルの窓（親は 0）を作って、窓が壊れるまでメッセージを回す。
// WM_CLOSE は DefWindowProc が窓を壊し、WM_DESTROY で PostQuitMessage する（fyne systray の窓と同じ道）。
func helperWindow(class, mutex string) int {
	runtime.LockOSThread()
	mp, _ := windows.UTF16PtrFromString(mutex)
	if _, err := windows.CreateMutex(nil, false, mp); err != nil {
		return 2
	}
	cls, _ := windows.UTF16PtrFromString(class)
	wc := wndClassEx{
		WndProc: windows.NewCallback(func(hwnd windows.HWND, m uint32, w, l uintptr) uintptr {
			if m == 0x0002 { // WM_DESTROY
				procPostQuitMessage.Call(0)
				return 0
			}
			r, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(m), w, l)
			return r
		}),
		ClassName: cls,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return 3
	}
	if r, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0); r == 0 {
		return 4
	}
	var m winMsg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return 0
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// startHelper は、このテストの exe を path に写して、mode の TestTrayHelperProcess として起動する。止めるのは t.Cleanup。
func startHelper(t *testing.T, path, mode string, env ...string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := copyFile(self, path); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestTrayHelperProcess$")
	cmd.Env = append(append(os.Environ(), "RAJIKURA_TRAY_HELPER="+mode), env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProcess(cmd) })
	return cmd
}

// startRunningExe は、動いている exe の代わりに、このテストの exe を path に写して起動する。
func startRunningExe(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	return startHelper(t, path, "1")
}

func stopProcess(cmd *exec.Cmd) {
	if cmd.ProcessState != nil {
		return
	}
	cmd.Process.Kill()
	cmd.Wait()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// trayFakes は、trayInstaller が差し替えたトレイの操作の記録と、返す値。
type trayFakes struct {
	// runs は Run の登録の操作、reboot は再起動のときに消すよう頼んだパス。
	runs, reboot []string
	// run・hasRun は、偽の HKCU の Run の値 rajikura-tray（setRun が書き、deleteRun が消し、getRun が返す）。
	run    string
	hasRun bool
	// events は、前のトレイを止める（そのときのトレイの exe の中身と tray.toml の url）・Run に登録する（同じ）・Web UI の応えを待つ・
	// トレイを起動する（そのときのトレイの exe の中身と行）・パスで絞って止める（そのパス）の順。
	events []string
	// stop は stopTray・stopTrayAt が返す結果、owner と startErr は startTray が返す値。
	stop     trayStop
	owner    string
	startErr error
}

// trayInstaller は、トレイを埋め込んだ形の fakeInstaller。Run への登録・前のトレイを止める・トレイを起動するのは記録するだけ
// （HKCU にも、動いているトレイにも触らない）。tray.toml は一時のフォルダに書く。
func trayInstaller(t *testing.T, m *fakeManager, out io.Writer, tray string) (*installer, *trayFakes) {
	t.Helper()
	in, steps := fakeInstaller(t, m, out)
	in.trayExe = []byte(tray)
	f := &trayFakes{}
	// state は、置いたトレイの exe（service と tray のどちらか、あるほう）の中身と、tray.toml の url。
	state := func() string {
		b, err := os.ReadFile(in.trayPath())
		if err != nil {
			b, _ = os.ReadFile(in.userTrayPath())
		}
		c, _ := readConfig(in.trayConfigPath)
		return string(b) + "," + c.URL
	}
	in.getRun = func() (string, bool, error) { return f.run, f.hasRun, nil }
	in.setRun = func(cmd string) error {
		f.runs = append(f.runs, "set "+cmd)
		f.events = append(f.events, "set-run("+state()+")")
		f.run, f.hasRun = cmd, true
		return nil
	}
	in.deleteRun = func() (bool, error) {
		f.runs = append(f.runs, "delete")
		had := f.hasRun
		f.run, f.hasRun = "", false
		return had, nil
	}
	in.deleteOnReboot = func(p string) error { f.reboot = append(f.reboot, p); return nil }
	in.waitReady = func(context.Context, string) error {
		*steps = append(*steps, "ready")
		f.events = append(f.events, "ready")
		return nil
	}
	in.stopTray = func() trayStop {
		f.events = append(f.events, "stop-tray("+state()+")")
		return f.stop
	}
	in.stopTrayAt = func(exe string) trayStop {
		f.events = append(f.events, "stop-tray-at("+exe+")")
		return f.stop
	}
	in.startTray = func(exe, cmd string) (string, error) {
		b, _ := os.ReadFile(exe)
		f.events = append(f.events, "start-tray("+string(b)+") "+cmd)
		return f.owner, f.startErr
	}
	return in, f
}

// readConfig は tray.toml を読む（無ければ空）。
func readConfig(path string) (trayconf.Config, error) { return trayconf.Read(path) }

// Run に登録する行と起動の行は、exe を引用符で囲んだだけ（-url を付けない。接続先は tray.toml の url に置く）。
func TestTrayRunCommand(t *testing.T) {
	exe := `C:\Program Files\rajikura\rajikura-tray.exe`
	if got, want := trayRunCommand(exe), `"`+exe+`"`; got != want {
		t.Errorf("trayRunCommand = %q, want %q", got, want)
	}
}

// Run の行が起動する exe のパスを読む（前の版の -url 付きの行も、トレイのメニューで登録した行も）。
func TestRunTarget(t *testing.T) {
	exe := `C:\Program Files\rajikura\rajikura-tray.exe`
	for cmd, want := range map[string]string{
		`"` + exe + `"`: exe,
		`"` + exe + `" -url http://127.0.0.1:8080`:    exe,
		`  "` + exe + `"  `:                           exe,
		`C:\rajikura\rajikura-tray.exe -url http://x`: `C:\rajikura\rajikura-tray.exe`,
		`C:\rajikura\rajikura-tray.exe`:               `C:\rajikura\rajikura-tray.exe`,
		`"` + exe:                                     exe,
	} {
		if got := runTarget(cmd); got != want {
			t.Errorf("runTarget(%q) = %q, want %q", cmd, got, want)
		}
	}
	if !samePath(`C:\PROGRAM FILES\rajikura\RAJIKURA-TRAY.EXE`, exe) || samePath(`C:\Users\a\AppData\Local\Programs\rajikura\rajikura-tray.exe`, exe) {
		t.Error("samePath は大文字小文字を問わず、別のパスは別とみなす")
	}
}

// service install は、前のトレイを止め → 埋め込んだトレイを %ProgramFiles%\rajikura に書き出し → 打ったユーザーの tray.toml に
// -listen の URL を書き → Run に "<exe>" だけを登録し → Web UI が応えた後に、Run と同じ行でトレイを起動する。
func TestInstallPlacesTrayAndRegistersRun(t *testing.T) {
	m := &fakeManager{}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray-v1")
	addr := freeAddr(t)
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(in.trayPath()); err != nil || string(b) != "tray-v1" {
		t.Errorf("トレイの exe = %q, %v", b, err)
	}
	cmd := `"` + in.trayPath() + `"`
	if strings.Join(f.runs, ",") != "set "+cmd {
		t.Errorf("Run = %v, want %q", f.runs, cmd)
	}
	// 初めて入れたときも（前のトレイが無くても）起動する。止めるときにはまだ exe も tray.toml も無く、Run に登録するときには両方ある。
	url := "http://" + addr
	want := []string{"stop-tray(,)", "set-run(tray-v1," + url + ")", "ready", "start-tray(tray-v1) " + cmd}
	if strings.Join(f.events, "|") != strings.Join(want, "|") {
		t.Errorf("順 = %q, want %q", f.events, want)
	}
	if c, err := readConfig(in.trayConfigPath); err != nil || c != (trayconf.Config{URL: url}) {
		t.Errorf("tray.toml = %+v, %v", c, err)
	}
	for _, s := range []string{"トレイを " + in.trayPath() + " に入れ", trayRunValue, "トレイを起動した", "トレイの接続先 " + url + " を " + in.trayConfigPath + " に書いた", "「Web UI と連携する」で連携する"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
	// 起動の文は、Web UI の文の後に出る。
	if strings.Index(out.String(), "トレイを起動した") < strings.Index(out.String(), "Web UI") {
		t.Errorf("トレイの起動が Web UI の文より前: %q", out.String())
	}
	if _, err := os.Stat(in.trayPath() + ".new"); !os.IsNotExist(err) {
		t.Errorf("一時ファイルが残っている（err = %v）", err)
	}
}

// トレイを埋め込んでいない rajikura.exe（開発のビルド）では、トレイを入れず、前のトレイにも触らず、起動もしない
// （fakeInstaller の stopTray・startTray は、呼ばれるとテストを落とす）。
func TestInstallWithoutEmbeddedTray(t *testing.T) {
	m := &fakeManager{}
	var out bytes.Buffer
	in, _ := fakeInstaller(t, m, &out)
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in.trayPath()); !os.IsNotExist(err) {
		t.Errorf("トレイの exe ができた（err = %v）", err)
	}
	if !strings.Contains(out.String(), "トレイが入っていない") || strings.Contains(out.String(), "トレイを起動") {
		t.Errorf("stdout = %q", out.String())
	}
}

// 打ち直し（更新）では、前のトレイを止めてから新しい版を置き、最後に新しい版を起動する。
func TestInstallStopsPreviousTrayAndStartsNew(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray-v2")
	os.MkdirAll(in.programDir, 0o755)
	os.WriteFile(in.trayPath(), []byte("tray-v1"), 0o644)
	f.stop = trayStopped
	addr := freeAddr(t)
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	cmd := `"` + in.trayPath() + `"`
	want := []string{"stop-tray(tray-v1,)", "set-run(tray-v2,http://" + addr + ")", "ready", "start-tray(tray-v2) " + cmd}
	if strings.Join(f.events, "|") != strings.Join(want, "|") {
		t.Errorf("順 = %q, want %q", f.events, want)
	}
	if !strings.Contains(out.String(), "動いていた前のトレイを終了した") || !strings.Contains(out.String(), "トレイを起動した") {
		t.Errorf("stdout = %q", out.String())
	}
}

// 前のトレイが終わった直後で exe がまだ使われていれば、よけて差し替え、再起動のときに消すよう頼む。
func TestInstallReplacesRunningTray(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f, cmd := runningTrayInstaller(t, m, &out)
	f.stop = trayStopped
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(in.trayPath()); err != nil || string(b) != "tray-v2" {
		t.Errorf("差し替えたトレイの exe = %.20q, %v", b, err)
	}
	if len(f.runs) != 1 {
		t.Errorf("Run = %v", f.runs)
	}
	aside, _ := filepath.Glob(in.trayPath() + ".*.old")
	if len(aside) != 1 || strings.Join(f.reboot, ",") != aside[0] {
		t.Fatalf("よけた exe = %v、再起動で消す = %v", aside, f.reboot)
	}
	if !strings.Contains(out.String(), "まだ使われていたので、よけて差し替えた") {
		t.Errorf("stdout = %q", out.String())
	}
	if last := f.events[len(f.events)-1]; !strings.HasPrefix(last, "start-tray(tray-v2) ") {
		t.Errorf("最後に新しい版を起動していない: %q", f.events)
	}

	// トレイが止まった後の打ち直しで、よけた exe を消す。
	stopProcess(cmd)
	out.Reset()
	f.stop = trayNotRunning
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(in.trayPath() + ".*.old"); len(left) != 0 {
		t.Errorf("よけた exe が残っている: %v", left)
	}
	if strings.Contains(out.String(), "よけて差し替えた") {
		t.Errorf("動いていないのに、よけた: %q", out.String())
	}
}

// runningTrayInstaller は、トレイの置き場で exe が動いている（消せない）形の trayInstaller。埋め込むトレイは tray-v2。
func runningTrayInstaller(t *testing.T, m *fakeManager, out io.Writer) (*installer, *trayFakes, *exec.Cmd) {
	t.Helper()
	in, f := trayInstaller(t, m, out, "tray-v2")
	cmd := startRunningExe(t, in.trayPath())
	// 動いている exe は消せない（この前提が崩れたら、よける形のテストになっていない）。
	if err := os.Remove(in.trayPath()); err == nil {
		t.Fatal("動いている exe を消せた")
	}
	return in, f, cmd
}

// 前のトレイが 10 秒で終わらなければ、exe はよけて置き、新しいトレイは起動せずに、手での終え方と起動の仕方を出す。
func TestInstallDoesNotStartTrayWhenPreviousKeepsRunning(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f, _ := runningTrayInstaller(t, m, &out)
	f.stop = trayStillRunning
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(in.trayPath()); err != nil || string(b) != "tray-v2" {
		t.Errorf("差し替えたトレイの exe = %.20q, %v", b, err)
	}
	if aside, _ := filepath.Glob(in.trayPath() + ".*.old"); len(aside) != 1 {
		t.Errorf("よけた exe = %v", aside)
	}
	for _, e := range f.events {
		if strings.HasPrefix(e, "start-tray") {
			t.Errorf("前のトレイが終わらないのに起動した: %q", f.events)
		}
	}
	for _, s := range []string{"10 秒待っても終わらなかった", "新しいトレイは起動しない", "メニューの「終了」", "エクスプローラーで " + in.trayPath() + " をダブルクリック"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
}

// 前のトレイを止めた後に失敗したら、置いてある exe を起動し直す（止めたトレイを止めたままにしない）。
func TestInstallFailureAfterStoppingTrayRestartsTray(t *testing.T) {
	boom := errors.New("boom")
	t.Run("Run に登録できない（新しい版は置いた）", func(t *testing.T) {
		m := &fakeManager{installed: true}
		var out bytes.Buffer
		in, f := trayInstaller(t, m, &out, "tray-v2")
		f.stop = trayStopped
		in.setRun = func(string) error { return boom }
		err := in.install(context.Background(), freeAddr(t))
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "トレイを起動する登録ができない") {
			t.Fatalf("err = %v", err)
		}
		if strings.Join(m.calls, ",") != "stop,start" {
			t.Errorf("SCM = %v（stop,start だけのはず）", m.calls)
		}
		if len(f.events) != 2 || f.events[0] != "stop-tray(,)" || !strings.HasPrefix(f.events[1], "start-tray(tray-v2) ") {
			t.Errorf("順 = %q", f.events)
		}
		for _, s := range []string{"新しい実行ファイルと止める前の引数で起動し直した", "止めたトレイを起動し直す", "トレイを起動した"} {
			if !strings.Contains(out.String(), s) {
				t.Errorf("stdout に %q が無い: %q", s, out.String())
			}
		}
	})
	t.Run("ACL を掛けられない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		var out bytes.Buffer
		in, f := trayInstaller(t, m, &out, "tray-v2")
		f.stop = trayStopped
		in.restrictACL = func(string) error { return boom }
		if err := in.install(context.Background(), freeAddr(t)); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if last := f.events[len(f.events)-1]; !strings.HasPrefix(last, "start-tray(tray-v2) ") {
			t.Errorf("起動し直していない: %q", f.events)
		}
	})
	t.Run("Web UI が応えない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		var out bytes.Buffer
		in, f := trayInstaller(t, m, &out, "tray-v2")
		f.stop = trayStopped
		in.waitReady = func(context.Context, string) error { return boom }
		if err := in.install(context.Background(), freeAddr(t)); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if last := f.events[len(f.events)-1]; !strings.HasPrefix(last, "start-tray(tray-v2) ") {
			t.Errorf("起動し直していない: %q", f.events)
		}
	})
	t.Run("前のトレイが動いていなければ起動しない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		in, f := trayInstaller(t, m, io.Discard, "tray-v2")
		in.restrictACL = func(string) error { return boom }
		if err := in.install(context.Background(), freeAddr(t)); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		for _, e := range f.events {
			if strings.HasPrefix(e, "start-tray") {
				t.Errorf("止めていないトレイを起動した: %q", f.events)
			}
		}
	})
}

// 前のトレイを止める前に失敗したら（待ち受けの確かめ・実行ファイルを写す）、トレイには触らない。
func TestInstallFailureBeforeStoppingTrayLeavesTray(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	t.Run("待ち受けられない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		in, f := trayInstaller(t, m, io.Discard, "tray-v2")
		f.stop = trayStopped
		if err := in.install(context.Background(), busy.Addr().String()); err == nil {
			t.Fatal("通った")
		}
		if len(f.events) != 0 || len(f.runs) != 0 {
			t.Errorf("トレイに触った: %q、Run %v", f.events, f.runs)
		}
	})
	t.Run("実行ファイルを写せない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		in, f := trayInstaller(t, m, io.Discard, "tray-v2")
		f.stop = trayStopped
		in.copyFile = func(string, string) error { return errors.New("boom") }
		if err := in.install(context.Background(), freeAddr(t)); err == nil {
			t.Fatal("通った")
		}
		if len(f.events) != 0 {
			t.Errorf("トレイに触った: %q", f.events)
		}
	})
}

// トレイを起動できなくても install は失敗にせず、理由と手での起動の仕方を出す。
func TestInstallTrayStartFailureIsNotFatal(t *testing.T) {
	m := &fakeManager{}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray-v1")
	f.startErr = errNoShell
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatalf("err = %v（起動できなくても失敗にしない）", err)
	}
	for _, s := range []string{"トレイを起動できなかった", errNoShell.Error(), "エクスプローラーで " + in.trayPath() + " をダブルクリックして起動する"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
}

// 端末のセッションの shell の持ち主が打ったユーザーと違えば（別の管理者の資格情報で昇格したとき）、そのユーザーのトレイとして
// 起動したことと、そのユーザーにはログイン時の起動を登録していないことを出す。
func TestInstallTrayStartedForOtherUser(t *testing.T) {
	m := &fakeManager{}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray-v1")
	f.owner = `PC\standard`
	addr := freeAddr(t)
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	// そのユーザーの tray.toml は書かれず、起動の行に -url も無いので、-listen を既定から変えたときの向け方も出す。
	for _, s := range []string{`画面の前のユーザー PC\standard のトレイとして起動した`, "ログイン時の起動を登録していない", "「ログイン時に起動する」で登録する",
		"このユーザーの tray.toml も書いていない", "-listen を既定から変えたとき", `このユーザーで "` + in.trayPath() + `" -url http://` + addr + " を起動し", "「Web UI と連携する」で連携する"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
}

// service uninstall は、トレイの exe と、打ったユーザーの Run の登録（%ProgramFiles%\rajikura のトレイを指すもの。前の版の
// -url 付きの行も）を消す。tray.toml は残す。
func TestUninstallRemovesTray(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray")
	os.MkdirAll(in.programDir, 0o755)
	os.WriteFile(in.exePath(), []byte("exe"), 0o644)
	os.WriteFile(in.trayPath(), []byte("tray"), 0o644)
	if err := trayconf.Write(in.trayConfigPath, trayconf.Config{URL: "http://127.0.0.1:8080", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	f.run, f.hasRun = `"`+strings.ToUpper(in.trayPath())+`" -url http://127.0.0.1:8080`, true
	if err := in.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.runs, ",") != "delete" || f.hasRun {
		t.Errorf("Run = %v（残り %q）", f.runs, f.run)
	}
	if c, err := readConfig(in.trayConfigPath); err != nil || c.Token != "tok" {
		t.Errorf("tray.toml が消えた・変わった: %+v, %v", c, err)
	}
	// 止めるのは %ProgramFiles%\rajikura のトレイだけ（パスで絞る）。起動はしない。
	if want := "stop-tray-at(" + in.trayPath() + ")"; strings.Join(f.events, ",") != want {
		t.Errorf("events = %q, want %q", f.events, want)
	}
	if _, err := os.Stat(in.programDir); !os.IsNotExist(err) {
		t.Errorf("%s が残っている（err = %v）", in.programDir, err)
	}
	for _, want := range []string{"トレイ " + in.trayPath() + " を消した", "起動する登録（HKCU"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout に %q が無い: %q", want, out.String())
		}
	}
}

// service uninstall は、Run の登録が %ProgramFiles%\rajikura のトレイを指していなければ（後に tray install を打った）、消さずにそう出す。
func TestUninstallKeepsRunOfOtherTray(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray")
	other := `"` + in.userTrayPath() + `"`
	f.run, f.hasRun = other, true
	if err := in.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 0 || f.run != other {
		t.Errorf("ほかのトレイの Run を消した: %v（残り %q）", f.runs, f.run)
	}
	if !strings.Contains(out.String(), other+"）は "+in.trayPath()+" を指していないので、消さなかった") {
		t.Errorf("stdout = %q", out.String())
	}
}

// 動いていたトレイを止められたときは、そう出す。
func TestUninstallStopsRunningTray(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray")
	os.MkdirAll(in.programDir, 0o755)
	os.WriteFile(in.trayPath(), []byte("tray"), 0o644)
	f.stop = trayStopped
	if err := in.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "動いていたトレイを終了した") || !strings.Contains(out.String(), "トレイ "+in.trayPath()+" を消した") {
		t.Errorf("stdout = %q", out.String())
	}
}

// トレイの exe が無ければ、止めようとしない。
func TestUninstallNoTrayExeDoesNotStop(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray")
	if err := in.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 0 {
		t.Errorf("events = %q", f.events)
	}
}

// トレイを止められなくても uninstall は失敗にせず、動いていて消せないときは、よけて、再起動のときに消すよう頼む。
func TestUninstallRunningTray(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, f := trayInstaller(t, m, &out, "tray")
	startRunningExe(t, in.trayPath())
	f.stop = trayStillRunning
	if err := in.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "秒待っても終わらなかった") {
		t.Errorf("stdout に待っても終わらなかった旨が無い: %q", out.String())
	}
	if _, err := os.Stat(in.trayPath()); !os.IsNotExist(err) {
		t.Errorf("トレイの exe が元の名前で残っている（err = %v）", err)
	}
	aside, _ := filepath.Glob(in.trayPath() + ".*.old")
	if len(aside) != 1 || strings.Join(f.reboot, ",") != aside[0] {
		t.Errorf("よけた exe = %v、再起動で消す = %v", aside, f.reboot)
	}
	if !strings.Contains(out.String(), "動いていたので消せず") || !strings.Contains(out.String(), "メニューの「終了」で終える") {
		t.Errorf("stdout = %q", out.String())
	}
}

// testTrayNames は、本物のトレイ（SystrayClass・Local\rajikura-tray）に触らないよう、テストごとに別の窓の class と mutex の名前を返す。
func testTrayNames(t *testing.T) (class, mutex string) {
	t.Helper()
	id := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	return "RajikuraTestTray-" + id, `Local\rajikura-test-tray-` + id
}

// startHelperWindow は、path に写したこのテストの exe で、class の窓を持ち mutex を取ったトレイの代わりを起動し、窓ができるまで待つ。
func startHelperWindow(t *testing.T, path, class, mutex string) *exec.Cmd {
	t.Helper()
	cmd := startHelper(t, path, "window", "RAJIKURA_TRAY_HELPER_CLASS="+class, "RAJIKURA_TRAY_HELPER_MUTEX="+mutex)
	deadline := time.Now().Add(20 * time.Second)
	for len(findTrayWindows(class, exeNamed(filepath.Base(path)))) == 0 || !mutexExists(mutex) {
		if time.Now().After(deadline) {
			t.Fatal("トレイの代わりの窓と mutex ができない")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cmd
}

// waitExit は cmd が timeout のうちに終わるかを返す。
func waitExit(cmd *exec.Cmd, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// 同じセッションで、class の窓を持ち exe の名前が合うプロセスに WM_CLOSE を送り、mutex が無くなるまで待つ。パスは問わない。
func TestStopTrayWindows(t *testing.T) {
	class, mutex := testTrayNames(t)
	if got := stopTrayWindows(class, mutex, time.Second, exeNamed(trayExeName), false); got != trayNotRunning {
		t.Errorf("トレイが無いのに %v", got)
	}
	// exe の名前は大文字小文字を問わない。
	cmd := startHelperWindow(t, filepath.Join(t.TempDir(), "anywhere", "Rajikura-Tray.EXE"), class, mutex)
	if got := stopTrayWindows(class, mutex, 10*time.Second, exeNamed(trayExeName), false); got != trayStopped {
		t.Fatalf("stopTrayWindows = %v, want trayStopped", got)
	}
	if !waitExit(cmd, 10*time.Second) {
		t.Fatal("WM_CLOSE で終わらない")
	}
	if mutexExists(mutex) {
		t.Error("mutex が残っている")
	}
}

// exe の名前が違うプロセスの窓（ほかの fyne systray のアプリ）には触らない。mutex が残れば、timeout の後に trayStillRunning を返す。
func TestStopTrayWindowsIgnoresOtherExe(t *testing.T) {
	class, mutex := testTrayNames(t)
	path := filepath.Join(t.TempDir(), "other-app.exe")
	cmd := startHelperWindow(t, path, class, mutex)
	if got := findTrayWindows(class, exeNamed(trayExeName)); len(got) != 0 {
		t.Errorf("名前の違う exe の窓を拾った: %v", got)
	}
	if got := stopTrayWindows(class, mutex, 300*time.Millisecond, exeNamed(trayExeName), false); got != trayStillRunning {
		t.Errorf("stopTrayWindows = %v, want trayStillRunning", got)
	}
	if waitExit(cmd, 500*time.Millisecond) {
		t.Error("名前の違う exe が終わった（WM_CLOSE を送った）")
	}
}

// パスで絞るとき（tray uninstall）は、同じ名前でも別のパスのトレイ（service install の入れたトレイ）には触らず、mutex が
// あっても待たずに trayNotRunning を返す。パスが合えば止める。
func TestStopTrayWindowsByPath(t *testing.T) {
	class, mutex := testTrayNames(t)
	dir := t.TempDir()
	running := filepath.Join(dir, "Program Files", "rajikura", trayExeName)
	cmd := startHelperWindow(t, running, class, mutex)
	mine := filepath.Join(dir, "LocalAppData", "Programs", "rajikura", trayExeName)
	isMine := func(img string) bool { return samePath(img, mine) }
	if got := stopTrayWindows(class, mutex, 10*time.Second, isMine, true); got != trayNotRunning {
		t.Errorf("別のパスのトレイで %v, want trayNotRunning", got)
	}
	if waitExit(cmd, 500*time.Millisecond) {
		t.Fatal("別のパスのトレイが終わった（WM_CLOSE を送った）")
	}
	// 一時のフォルダが短い名前（8.3）で渡ることがあるので、比べる先は、動いているプロセスの exe のパス（長い名前）にする。
	img := processImage(uint32(cmd.Process.Pid))
	if !strings.EqualFold(filepath.Base(img), trayExeName) {
		t.Fatalf("トレイの代わりの exe のパス = %q", img)
	}
	isRunning := func(p string) bool { return samePath(p, strings.ToUpper(img)) }
	if got := stopTrayWindows(class, mutex, 10*time.Second, isRunning, true); got != trayStopped {
		t.Fatalf("パスが合うトレイで %v, want trayStopped", got)
	}
	if !waitExit(cmd, 10*time.Second) {
		t.Fatal("WM_CLOSE で終わらない")
	}
}

// 開けない理由が「無い」なら mutex は無い、開ければある。
func TestMutexExists(t *testing.T) {
	_, mutex := testTrayNames(t)
	if mutexExists(mutex) {
		t.Fatal("作る前からある")
	}
	p, _ := windows.UTF16PtrFromString(mutex)
	h, err := windows.CreateMutex(nil, false, p)
	if err != nil {
		t.Fatal(err)
	}
	if !mutexExists(mutex) {
		t.Error("作ったのに無い")
	}
	windows.CloseHandle(h)
	if mutexExists(mutex) {
		t.Error("閉じたのにある")
	}
}

// startWithParent は、指定したプロセスを親にして、行と環境変数を渡して起動する。
func TestStartWithParent(t *testing.T) {
	dir := t.TempDir()
	parentCmd := startRunningExe(t, filepath.Join(dir, "parent.exe"))
	parent, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS, false, uint32(parentCmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(parent)
	child := filepath.Join(dir, "child.exe")
	self, _ := os.Executable()
	if err := copyFile(self, child); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.txt")
	env := append(os.Environ(), "RAJIKURA_TRAY_HELPER=report", "RAJIKURA_TRAY_HELPER_OUT="+outFile, "RAJIKURA_TRAY_HELPER_ENV=from-parent")
	if err := startWithParent(parent, child, `"`+child+`" -test.run=^TestTrayHelperProcess$`, env); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var b []byte
	for {
		b, err = os.ReadFile(outFile)
		if err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("起動した子が書かない: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	want := strconv.Itoa(parentCmd.Process.Pid) + " from-parent"
	if string(b) != want {
		t.Errorf("子の親と環境変数 = %q, want %q（親はテストではなく指定したプロセス）", b, want)
	}
}

// 同じユーザーの token なら、持ち主が違うとはみなさない。
func TestOtherTokenUserSameUser(t *testing.T) {
	if got, err := otherTokenUser(windows.GetCurrentProcessToken()); err != nil || got != "" {
		t.Errorf("otherTokenUser(自分) = %q, %v", got, err)
	}
}
