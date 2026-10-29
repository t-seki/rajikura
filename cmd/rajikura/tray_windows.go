//go:build windows

package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/t-seki/rajikura/internal/trayconf"
)

// トレイ（cmd/rajikura-tray。docs/adr/0023-windows-tray-embedded-and-linked-by-loopback.md）を埋め込み、書き出して、
// 打ったユーザーの HKCU の Run に登録する。入れる処理（installTray）は、service install と tray installの 2 つが通る 1 本で、
// 違うのは置く先と接続先の URL（と tray install の -token）だけ。
//   - service install: %ProgramFiles%\rajikura\rajikura-tray.exe に置き、接続先は -listen から決める（serviceURL）
//   - tray install: %LocalAppData%\Programs\rajikura\rajikura-tray.exe に置き、接続先は -url（管理者は要らない）
//
// 入れる処理は、同じセッションで動いている前のトレイを止め（stopRunningTray。置き場所は問わない）→ exe を置き（placeExe）→
// 打ったユーザーの tray.toml（%AppData%\rajikura\tray.toml）に接続先を書き（writeTrayConfig）→ Run に "<exe>" だけを登録する。
// 接続先は Run の行に付けず、tray.toml の url に置く。最後に、端末のセッションの shell（エクスプローラー）の持ち主のトレイとして
// 起動する（startTrayAsShellUser。service install は Web UI が応えた後に、tray install は入れた直後に）。
// service uninstall・tray uninstall は、Run の登録を、それぞれの置き場所の exe を指すときだけ消し（removeTrayRun）、exe を消す。
// tray uninstall は、自分の置き場所の exe で動いているトレイだけを止めてから消す（stopTrayAtPath）。service uninstall も同じく、%ProgramFiles%\rajikura の exe で動いているトレイだけを止めてから消す。
// tray.toml は、どちらの uninstall でも消さない。
// 足したら直す: service の操作
//
// トレイの exe は traybin/rajikura-tray.exe に置いてからビルドすると埋め込まれる（Release の Windows の exe は、
// Dockerfile の build-windows の段がトレイを先にビルドして置く）。traybin には .gitkeep しか git に入れないので、
// トレイをビルドしていなくても go:embed が通り、go build・go test・go vet にトレイは要らない。そのときは、
// service install はトレイを入れず、tray install は失敗にする。埋め込むのは Windows の exe だけ（このファイルは windows の build tag）。
//
//go:embed all:traybin
var trayBin embed.FS

// trayExeName は、埋め込むトレイの exe の名前（traybin の中と、書き出すときの名前）。
const trayExeName = "rajikura-tray.exe"

// トレイを登録する、打ったユーザーの HKCU のキーと値の名前。トレイのメニューの「ログイン時に起動する」
// （cmd/rajikura-tray/autostart_windows.go）と同じ名前を使う。service install と tray install も同じ値を書くので、
// 同じユーザーで両方を打てば、後に打った方が勝つ（トレイは 1 ユーザーに 1 つ）。
const (
	trayRunKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	trayRunValue = "rajikura-tray"
)

// defaultTrayURL は、トレイの接続先の既定（cmd/rajikura-tray/config.go の defaultURL と同じ。service install の既定の -listen）。
const defaultTrayURL = "http://127.0.0.1:8080"

// embeddedTray は、埋め込んだトレイの exe を返す（埋め込んでいなければ nil）。
func embeddedTray() []byte {
	b, err := fs.ReadFile(trayBin, "traybin/"+trayExeName)
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

// trayRunCommand は、HKCU の Run に登録するトレイの起動の行（exe を引用符で囲んだだけ。接続先は tray.toml の url に置く）。
// install の最後にトレイを起動する行も同じ。
func trayRunCommand(exe string) string {
	return `"` + exe + `"`
}

// runTarget は、Run の行が起動する exe のパス（引用符で囲んでいれば、その中。囲んでいなければ最初の空白まで）。
func runTarget(command string) string {
	c := strings.TrimSpace(command)
	if strings.HasPrefix(c, `"`) {
		if i := strings.Index(c[1:], `"`); i >= 0 {
			return c[1 : 1+i]
		}
		return c[1:]
	}
	if i := strings.IndexAny(c, " \t"); i >= 0 {
		return c[:i]
	}
	return c
}

// samePath は、2 つのパスが同じ場所を指すか（Windows のパスなので大文字小文字は問わない）。
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// getTrayRun は、打ったユーザーの HKCU の Run のトレイの登録を読む（無ければ ok が false）。
func getTrayRun() (command string, ok bool, err error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, trayRunKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf(`HKCU\%s を開けない: %w`, trayRunKey, err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(trayRunValue)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf(`HKCU\%s の %s を読めない: %w`, trayRunKey, trayRunValue, err)
	}
	return v, true, nil
}

// setTrayRun は、打ったユーザーの HKCU の Run に command を登録する（あれば書き直す）。
func setTrayRun(command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, trayRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf(`HKCU\%s を開けない: %w`, trayRunKey, err)
	}
	defer k.Close()
	if err := k.SetStringValue(trayRunValue, command); err != nil {
		return fmt.Errorf(`HKCU\%s に %s を書けない: %w`, trayRunKey, trayRunValue, err)
	}
	return nil
}

// deleteTrayRun は、打ったユーザーの HKCU の Run からトレイの登録を消す。登録があったかを返す。
func deleteTrayRun() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, trayRunKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(`HKCU\%s を開けない: %w`, trayRunKey, err)
	}
	defer k.Close()
	err = k.DeleteValue(trayRunValue)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(`HKCU\%s の %s を消せない: %w`, trayRunKey, trayRunValue, err)
	}
	return true, nil
}

// deleteOnReboot は、p を PC の次の再起動のときに消すよう Windows に頼む（管理者の権限が要る）。
func deleteOnReboot(p string) error {
	from, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

// trayPath は、service install が置くトレイの exe（%ProgramFiles%\rajikura\rajikura-tray.exe）。
func (in *installer) trayPath() string { return filepath.Join(in.programDir, trayExeName) }

// userTrayPath は、tray install が置くトレイの exe（%LocalAppData%\Programs\rajikura\rajikura-tray.exe）。
func (in *installer) userTrayPath() string { return filepath.Join(in.userProgramDir, trayExeName) }

// trayPlan は、入れる処理（installTray）に渡す、置く先と接続先。
type trayPlan struct {
	// exe はトレイの exe を置くパス。
	exe string
	// url は tray.toml の url に書く接続先（末尾の / は落としたもの）。
	url string
	// token は tray install -token で渡した API トークン（渡していなければ空）。
	token string
	// command は、打ち直すときのサブコマンド（よけた exe の案内に出す。「service」か「tray」）。
	command string
}

// installTray は、埋め込んだトレイを p.exe に置き、打ったユーザーの tray.toml に接続先を書いて、HKCU の Run に "<exe>" を登録する。
// 埋め込んでいなければ、そう出して何もしない（trayNotRunning を返し、前のトレイにも触らない）。
// 置く前に、同じセッションで動いている前のトレイを止め（stopTray）、止めた結果を返す（エラーのときも返す。install の失敗の道で、
// 止めたトレイを起動し直すため）。前のトレイが終わらなかったか、終わった直後でまだ exe が使われていれば、動いている exe をよけて
// 差し替える（placeExe）。Run に前の登録があって今の行と違えば、前の行を出す。起動はしない（呼ぶ側が launchTray で起動する）。
func (in *installer) installTray(p trayPlan) (trayStop, error) {
	if len(in.trayExe) == 0 {
		fmt.Fprintln(in.out, "この rajikura.exe にはトレイが入っていないので、トレイは入れない（Release の rajikura.exe には入っている）")
		return trayNotRunning, nil
	}
	stop := in.stopTray()
	switch stop {
	case trayStopped:
		fmt.Fprintln(in.out, "動いていた前のトレイを終了した")
	case trayStillRunning:
		fmt.Fprintf(in.out, "前のトレイが %d 秒待っても終わらなかった\n", int(trayStopTimeout/time.Second))
	}
	dir := filepath.Dir(p.exe)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return stop, fmt.Errorf("%s を作れない: %w", dir, err)
	}
	aside, err := placeExe(in.trayExe, p.exe)
	if err != nil {
		return stop, fmt.Errorf("トレイを %s に書き出せない: %w", p.exe, err)
	}
	if aside != "" {
		fmt.Fprintln(in.out, "前のトレイの exe がまだ使われていたので、よけて差し替えた")
		in.deleteLater(aside, p.command)
	}
	if err := in.writeTrayConfig(p.url, p.token); err != nil {
		return stop, err
	}
	cmd := trayRunCommand(p.exe)
	prev, hadPrev, perr := in.getRun()
	if err := in.setRun(cmd); err != nil {
		return stop, fmt.Errorf("ログイン時にトレイを起動する登録ができない: %w", err)
	}
	fmt.Fprintf(in.out, "トレイを %s に入れ、このユーザーのログイン時に起動するよう登録した（HKCU\\%s の %s: %s）\n",
		p.exe, trayRunKey, trayRunValue, cmd)
	switch {
	case perr != nil:
		fmt.Fprintf(in.out, "前の登録は読めなかった: %v\n", perr)
	case hadPrev && prev != cmd:
		fmt.Fprintf(in.out, "前の登録 %s を書き換えた\n", prev)
	}
	return stop, nil
}

// writeTrayConfig は、打ったユーザーの tray.toml（trayConfigPath）に接続先 url を書く。
//   - token を渡したら、url と token を書く
//   - token を渡さず、tray.toml の url が url と同じ（末尾の / を落として比べる）なら、触らない
//   - token を渡さず、tray.toml が無いか url が違えば、url を書いて token を消す（前の接続先で連携したトークンを持ち越さない）
//
// tray.toml を読めない（TOML の誤り、知らないキー）ときは、失敗にせず上書きし、そのことを出す。書けなければエラーを返す。
func (in *installer) writeTrayConfig(url, token string) error {
	path := in.trayConfigPath
	if path == "" {
		return errors.New("tray.toml を置くフォルダ（%AppData%）が分からない")
	}
	cur, rerr := trayconf.Read(path)
	if rerr != nil {
		fmt.Fprintf(in.out, "%s を読めないので、上書きする（%v）\n", path, rerr)
		cur = trayconf.Config{}
	}
	if token == "" && rerr == nil && cur.URL != "" && trayconf.NormalizeURL(cur.URL) == url {
		return nil
	}
	if err := trayconf.Write(path, trayconf.Config{URL: url, Token: token}); err != nil {
		return fmt.Errorf("トレイの接続先を書けない: %w", err)
	}
	switch {
	case token != "":
		fmt.Fprintf(in.out, "トレイの接続先 %s と API トークンを %s に書いた\n", url, path)
	case cur.URL != "":
		fmt.Fprintf(in.out, "トレイの接続先 %s を %s に書いた。接続先が変わった（前は %s）ので、前に連携した API トークンは消した。トレイのメニューの「Web UI と連携する」で連携し直す\n", url, path, cur.URL)
	default:
		fmt.Fprintf(in.out, "トレイの接続先 %s を %s に書いた。トレイのメニューの「Web UI と連携する」で連携する\n", url, path)
	}
	return nil
}

// launchTray は、置いたトレイの exe を起動する（service install は Web UI が応えた後、tray install は入れた直後）。
// トレイを埋め込んでいなければ何もしない。前のトレイが終わらなかったときは起動せず、手での終え方と起動の仕方を出す。
// url は tray.toml に書いた接続先（ほかのユーザーのトレイとして起動したときの案内に出す）。
func (in *installer) launchTray(exe, url string, stop trayStop) {
	if len(in.trayExe) == 0 {
		return
	}
	if stop == trayStillRunning {
		fmt.Fprintf(in.out, "前のトレイが終わらなかったので、新しいトレイは起動しない。通知領域のトレイのメニューの「終了」で前のトレイを終えてから、エクスプローラーで %s をダブルクリックして起動する\n", exe)
		return
	}
	in.startTrayAt(exe, url)
}

// restartTray は、前のトレイを止めた後に install が失敗したときに、置いてある exe（新しい版か前の版）を起動し直す。
// 前のトレイが動いていなかったときは何もしない。終わらなかったときは起動せず、launchTray と同じく手での終え方と起動の仕方を出す。
func (in *installer) restartTray(exe, url string, stop trayStop) {
	if stop == trayStillRunning {
		in.launchTray(exe, url, stop)
		return
	}
	if stop != trayStopped {
		return
	}
	if _, err := os.Stat(exe); err != nil {
		fmt.Fprintf(in.out, "止めたトレイを起動し直せない（%s が無い: %v）。install を打ち直して入れ直す\n", exe, err)
		return
	}
	fmt.Fprintln(in.out, "止めたトレイを起動し直す")
	in.startTrayAt(exe, url)
}

// startTrayAt は exe を、Run に書く行（trayRunCommand）で起動し、結果を出す。起動できなくても install は失敗にしない。
// 端末のセッションの shell の持ち主が打ったユーザーと違えば（別の管理者の資格情報で昇格したとき）、そのユーザーには Run も
// tray.toml も書いていないので、ログイン時の起動の登録と、既定でない接続先への向け方（-url を付けて起動し直して連携する）を出す。
func (in *installer) startTrayAt(exe, url string) {
	owner, err := in.startTray(exe, trayRunCommand(exe))
	switch {
	case err != nil:
		fmt.Fprintf(in.out, "トレイを起動できなかった（%v）。エクスプローラーで %s をダブルクリックして起動する\n", err, exe)
	case owner != "":
		fmt.Fprintf(in.out, "トレイを、画面の前のユーザー %s のトレイとして起動した。このユーザーにはログイン時の起動を登録していない（登録は昇格した管理者の HKCU に入った）。トレイのメニューの「ログイン時に起動する」で登録する\n", owner)
		fmt.Fprintf(in.out, "このユーザーの tray.toml も書いていない（書いたのは昇格した管理者のもの）。接続先が既定の %s でないとき（service install の -listen を既定から変えたときなど）は、起動したトレイをメニューの「終了」で終えてから、このユーザーで \"%s\" -url %s を起動し、メニューの「Web UI と連携する」で連携する\n", defaultTrayURL, exe, url)
	default:
		fmt.Fprintln(in.out, "トレイを起動した（通知領域に出る）")
	}
}

// trayStop は、前のトレイを止めた結果。
type trayStop int

const (
	// trayNotRunning は、止める前のトレイが無かった（トレイを埋め込んでいないときも）。
	trayNotRunning trayStop = iota
	// trayStopped は、動いていた前のトレイが終わった。
	trayStopped
	// trayStillRunning は、trayStopTimeout 待っても前のトレイが終わらなかった。
	trayStillRunning
)

const (
	// trayWindowClass は、fyne systray が作る隠れた窓の class（fyne.io/systray v1.12.2 の Windows の実装の固定の名前）。
	// ほかの fyne systray のアプリも同じ class の窓を持つので、exe の名前（かパス）でも絞る。
	trayWindowClass = "SystrayClass"
	// trayMutexName は、トレイの二重起動を防ぐ名前付き mutex（cmd/rajikura-tray/single_windows.go の singleMutexName と同じ名前）。
	trayMutexName = `Local\rajikura-tray`
	// trayStopTimeout は、前のトレイに終了を頼んでから、mutex が無くなるのを待つ長さ。
	trayStopTimeout = 10 * time.Second
	wmClose         = 0x0010
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procFindWindowEx = user32.NewProc("FindWindowExW")
	procPostMessage  = user32.NewProc("PostMessageW")
)

// stopRunningTray は、install と同じセッションで動いている前のトレイ（パスは問わない）に終了を頼み、終わるまで待つ。
func stopRunningTray() trayStop {
	return stopTrayWindows(trayWindowClass, trayMutexName, trayStopTimeout, exeNamed(trayExeName), false)
}

// stopTrayAtPath は、同じセッションで動いているトレイのうち、exe のパスが exe のものだけに終了を頼み、終わるまで待つ
// （tray uninstall が service install の入れたトレイを、service uninstall が tray install の入れたトレイを止めないため）。
func stopTrayAtPath(exe string) trayStop {
	return stopTrayWindows(trayWindowClass, trayMutexName, trayStopTimeout, func(img string) bool { return samePath(img, exe) }, true)
}

// exeNamed は、exe の名前（パスの最後。大文字小文字は問わない）が name かを見る関数を返す。
func exeNamed(name string) func(string) bool {
	return func(img string) bool { return strings.EqualFold(filepath.Base(img), name) }
}

// stopTrayWindows は、同じセッションで動いていて、class の窓を持ち、exe のパスが match に合うプロセスの窓に WM_CLOSE を送り、
// 名前付き mutex が開けなくなるまで最大 timeout 待つ。WM_CLOSE で、fyne systray は窓を壊し、通知領域のアイコンを消して終わる
// （メニューの「終了」と同じ道。TerminateProcess は onExit を通らずアイコンが残るので使わない）。
// 合う窓が無いとき、onlyMatched なら trayNotRunning を返す（mutex は、合わないトレイが持ちうるので見ない）。onlyMatched でなければ、
// mutex も無いときだけ trayNotRunning を返し、mutex があれば無くなるまで待つ。
func stopTrayWindows(class, mutex string, timeout time.Duration, match func(string) bool, onlyMatched bool) trayStop {
	hwnds := findTrayWindows(class, match)
	if len(hwnds) == 0 && (onlyMatched || !mutexExists(mutex)) {
		return trayNotRunning
	}
	for _, h := range hwnds {
		procPostMessage.Call(uintptr(h), wmClose, 0, 0)
	}
	deadline := time.Now().Add(timeout)
	for mutexExists(mutex) {
		if time.Now().After(deadline) {
			return trayStillRunning
		}
		time.Sleep(100 * time.Millisecond)
	}
	return trayStopped
}

// findTrayWindows は、このプロセスと同じセッションで動き、exe のパスが match に合うプロセスが持つ、class のトップレベルの窓を返す。
func findTrayWindows(class string, match func(string) bool) []windows.HWND {
	cls, err := windows.UTF16PtrFromString(class)
	if err != nil {
		return nil
	}
	var self uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &self); err != nil {
		return nil
	}
	var found []windows.HWND
	var prev windows.HWND
	// 窓が尽きると FindWindowExW は 0 を返す。上限は、壊れた窓の並びで回り続けないための保険。
	for i := 0; i < 10000; i++ {
		r, _, _ := procFindWindowEx.Call(0, uintptr(prev), uintptr(unsafe.Pointer(cls)), 0)
		if r == 0 {
			break
		}
		prev = windows.HWND(r)
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(prev, &pid); err != nil || pid == 0 {
			continue
		}
		var sess uint32
		if err := windows.ProcessIdToSessionId(pid, &sess); err != nil || sess != self {
			continue
		}
		if img := processImage(pid); img == "" || !match(img) {
			continue
		}
		found = append(found, prev)
	}
	return found
}

// processImage は pid の exe のパスを返す（開けなければ空）。
func processImage(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// mutexExists は、名前付き mutex があるかを返す。開けない理由が「無い」以外（権限が無いなど）なら、あるとみなす。
func mutexExists(name string) bool {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, p)
	if err == nil {
		windows.CloseHandle(h)
		return true
	}
	return !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

// errNoShell は、端末のセッションに shell の窓が無い（SSH からの実行など）ときのエラー。
var errNoShell = errors.New("このセッションにエクスプローラーの窓が無い（SSH などから打った）")

// startTrayAsShellUser は、端末のセッションの shell（GetShellWindow の窓の持ち主のエクスプローラー）を親に指定して、
// exe を command の行で起動する（CreateProcess の PROC_THREAD_ATTRIBUTE_PARENT_PROCESS）。トレイは、管理者の端末の子ではなく、
// エクスプローラーの子として動く。環境変数はエクスプローラーの持ち主のもの（Token.Environ）を渡す。
// エクスプローラーの持ち主が install を打ったユーザーと違えば（標準ユーザーが別の管理者の資格情報で昇格したとき）、その名前を返す。
// エクスプローラーの持ち主と打ったユーザーが同じとき（Windows 11 の管理者でない端末）に、windowsgui の exe をこの形で起動すると、
// 親がエクスプローラーになり、そのユーザーの環境変数と引数が渡り、カレントディレクトリが exe のフォルダになることは実機で確かめた
// （2026-10-26）。この場面では、持ち主の環境変数と打ったユーザーの環境変数が同じ値になるので、どちらが渡ったかは見分けていない。
// 持ち主が打ったユーザーと違うときに、持ち主の環境変数が渡ることは未確認。コンソールの exe は、同じ形では 0xC0000142 で終わった
// （同じ実機の確かめ。トレイは windowsgui なので当たらない）。
// 管理者の端末から起動したときに、親に指定したプロセスの token を子が受け継ぐこと（トレイがふつうの権限で動くこと）は、
// 実機では未確認。
func startTrayAsShellUser(exe, command string) (string, error) {
	shell := windows.GetShellWindow()
	if shell == 0 {
		return "", errNoShell
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(shell, &pid); err != nil {
		return "", fmt.Errorf("エクスプローラーのプロセスが分からない: %w", err)
	}
	parent, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("エクスプローラー（PID %d）を開けない: %w", pid, err)
	}
	defer windows.CloseHandle(parent)
	var tok windows.Token
	if err := windows.OpenProcessToken(parent, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE, &tok); err != nil {
		return "", fmt.Errorf("エクスプローラー（PID %d）の token を開けない: %w", pid, err)
	}
	defer tok.Close()
	owner, err := otherTokenUser(tok)
	if err != nil {
		return "", fmt.Errorf("エクスプローラーの持ち主が分からない: %w", err)
	}
	env, err := tok.Environ(false)
	if err != nil {
		return "", fmt.Errorf("エクスプローラーの持ち主の環境変数を作れない: %w", err)
	}
	if err := startWithParent(parent, exe, command, env); err != nil {
		return "", fmt.Errorf("起動できない: %w", err)
	}
	return owner, nil
}

// otherTokenUser は、tok のユーザーがこのプロセスのユーザーと違えば、その名前（ドメイン\名前。引けなければ SID）を返す。同じなら空。
func otherTokenUser(tok windows.Token) (string, error) {
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	if windows.EqualSid(self.User.Sid, u.User.Sid) {
		return "", nil
	}
	account, domain, _, err := u.User.Sid.LookupAccount("")
	if err != nil {
		return u.User.Sid.String(), nil
	}
	return domain + `\` + account, nil
}

// startWithParent は、parent を親に指定して exe を cmdline の行と env で起動する（終わるのは待たない）。作業フォルダは exe のフォルダ。
// 標準の入出力は渡さない（ハンドルを親に写さないよう、Files は 0 にする）。
func startWithParent(parent windows.Handle, exe, cmdline string, env []string) error {
	_, h, err := syscall.StartProcess(exe, []string{exe}, &syscall.ProcAttr{
		Dir:   filepath.Dir(exe),
		Env:   env,
		Files: []uintptr{0, 0, 0},
		Sys:   &syscall.SysProcAttr{CmdLine: cmdline, ParentProcess: syscall.Handle(parent)},
	})
	if err != nil {
		return err
	}
	return windows.CloseHandle(windows.Handle(h))
}

// removeTrayRun は、打ったユーザーの HKCU の Run のトレイの登録を、exe を指すとき（runTarget が exe と同じパス）だけ消す。
// ほかの exe を指していれば（service install と tray install の後に打った方の登録、トレイのメニューで登録した別の exe）、
// 消さずに、そのことを出す。ほかのユーザーの登録には届かない。
func (in *installer) removeTrayRun(exe string) {
	cmd, ok, err := in.getRun()
	switch {
	case err != nil:
		fmt.Fprintf(in.out, "ログイン時にトレイを起動する登録を読めない（HKCU\\%s の %s。手で消してよい）: %v\n", trayRunKey, trayRunValue, err)
		return
	case !ok:
		return
	case !samePath(runTarget(cmd), exe):
		fmt.Fprintf(in.out, "ログイン時にトレイを起動する登録（HKCU\\%s の %s: %s）は %s を指していないので、消さなかった\n", trayRunKey, trayRunValue, cmd, exe)
		return
	}
	existed, err := in.deleteRun()
	switch {
	case err != nil:
		fmt.Fprintf(in.out, "ログイン時にトレイを起動する登録を消せない（HKCU\\%s の %s。手で消してよい）: %v\n", trayRunKey, trayRunValue, err)
	case existed:
		fmt.Fprintf(in.out, "このユーザーのログイン時にトレイを起動する登録（HKCU\\%s の %s）を消した。ほかのユーザーの登録は、そのユーザーがトレイのメニューで外す\n", trayRunKey, trayRunValue)
	}
}

// uninstallTray は、service uninstall の分のトレイを消す。打ったユーザーの HKCU の Run の登録を、%ProgramFiles%\rajikura の
// トレイの exe を指すときだけ消し（removeTrayRun）、同じセッションで動いているトレイのうち、その exe のものだけを止めてから
// （tray install の入れたトレイには触らない）、その exe を消す。止まらなければ、名前を変えてよけ、PC の次の再起動のときに
// 消すよう頼み、メニューの「終了」で終えることを出す。止められなくても失敗にはしない。ほかのユーザーのセッションのトレイと
// 登録には届かない。
func (in *installer) uninstallTray() {
	exe := in.trayPath()
	in.removeTrayRun(exe)
	if _, err := os.Stat(exe); err == nil {
		switch in.stopTrayAt(exe) {
		case trayStopped:
			fmt.Fprintln(in.out, "動いていたトレイを終了した")
		case trayStillRunning:
			fmt.Fprintf(in.out, "トレイが %d 秒待っても終わらなかった\n", int(trayStopTimeout/time.Second))
		}
	}
	in.removeTrayExe(exe, "service", "トレイを止められなかったので")
}

// removeTrayExe は、トレイの exe を消す。動いていて消せなければ、名前を変えてよけ、PC の次の再起動のときに消すよう頼み、
// メニューの「終了」で終えることを出す（why はその前に添える理由）。消せたか、無かったかを返す（よけたときは false）。
func (in *installer) removeTrayExe(exe, command, why string) bool {
	removeStale(exe)
	err := os.Remove(exe)
	switch {
	case err == nil:
		fmt.Fprintf(in.out, "トレイ %s を消した\n", exe)
		return true
	case errors.Is(err, os.ErrNotExist):
		return true
	}
	// 動いている exe は消せないが、名前は変えられる。
	aside := asideName(exe)
	if rerr := os.Rename(exe, aside); rerr != nil {
		fmt.Fprintf(in.out, "トレイ %s を消せない（トレイを終了してから手で消してよい）: %v\n", exe, err)
		return false
	}
	fmt.Fprintf(in.out, "トレイ %s が動いていたので消せず、名前を変えてよけた。%s、通知領域のトレイのメニューの「終了」で終える\n", exe, why)
	in.deleteLater(aside, command)
	return false
}

// deleteLater は、よけた前の版の exe を PC の次の再起動のときに消すよう頼み、そのことを出す。command は、よけたファイルを
// 次に消してみるサブコマンド（「service」か「tray」）。
func (in *installer) deleteLater(aside, command string) {
	if err := in.deleteOnReboot(aside); err != nil {
		fmt.Fprintf(in.out, "よけた %s は、トレイを終了してから手で消してよい（再起動のときに消すよう頼めない: %v）\n", aside, err)
		return
	}
	fmt.Fprintf(in.out, "よけた %s は、PC の次の再起動のときに消える（次の %s install・uninstall でも消す）\n", aside, command)
}

// placeExe は data を dst に書く（隣の一時ファイルに書いてから置き換える）。dst が動いている exe で置き換えられないときは、
// dst の名前を変えてよけ（動いている exe は、上書きも削除もできないが、名前は変えられる。実機で確かめた）、
// その後に置き換える。よけたファイルを消せなければ（動いていれば）、そのパスを返す（消せたか、よけなかったなら空）。
// 前によけて残ったファイルは、先に消してみる（removeStale）。
func placeExe(data []byte, dst string) (string, error) {
	removeStale(dst)
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		os.Remove(tmp)
		return "", err
	}
	err := os.Rename(tmp, dst)
	if err == nil {
		return "", nil
	}
	aside := asideName(dst)
	if rerr := os.Rename(dst, aside); rerr != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		// 戻せなければ前の版も無くなるが、次の install で書き直せる。
		_ = os.Rename(aside, dst)
		os.Remove(tmp)
		return "", err
	}
	if os.Remove(aside) == nil {
		return "", nil
	}
	return aside, nil
}

// asideName は、動いている exe をよけるときの名前（<exe>.<時刻>.old）。removeStale が消す。
func asideName(exe string) string {
	return fmt.Sprintf("%s.%d.old", exe, time.Now().UnixNano())
}

// removeStale は、前によけて残った exe（<exe>.*.old）を消してみる（まだ動いていれば残る）。
func removeStale(exe string) {
	matches, _ := filepath.Glob(exe + ".*.old")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}
