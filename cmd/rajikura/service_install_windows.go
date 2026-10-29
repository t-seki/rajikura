//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/trayconf"
)

// Windows のサービスの名前とログオンのアカウント（仮想アカウント。docs/adr/0020-windows-service-with-virtual-account.md）。
const (
	serviceName    = "rajikura"
	serviceAccount = `NT SERVICE\` + serviceName
	// defaultServiceListen は、service install で入れるときの -listen の既定（同じ PC からだけ開ける）。
	defaultServiceListen = "127.0.0.1:8080"
)

// サービスを止める・待ち受けが応えるまでの待ち時間。
const (
	serviceStopTimeout  = 2 * time.Minute
	serviceReadyTimeout = time.Minute
)

// runServiceCommand は service install / uninstall を動かす。
// 足したら直す: service の操作
func runServiceCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		serviceUsage(stderr)
		return 2
	}
	in := newInstaller(stdout)
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("rajikura service install", flag.ContinueOnError)
		listen := fs.String("listen", defaultServiceListen, "サービスの serve が待ち受けるアドレス")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return 2
		}
		if fs.NArg() > 0 {
			serviceUsage(stderr)
			return 2
		}
		return exitCode("service install", in.install(ctx, *listen), stderr)
	case "uninstall":
		fs := flag.NewFlagSet("rajikura service uninstall", flag.ContinueOnError)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return 2
		}
		if fs.NArg() > 0 {
			serviceUsage(stderr)
			return 2
		}
		return exitCode("service uninstall", in.uninstall(ctx), stderr)
	default:
		fmt.Fprintf(stderr, "rajikura service: 不明な操作 %q\n", args[0])
		serviceUsage(stderr)
		return 2
	}
}

// 足したら直す: service の操作
func serviceUsage(w io.Writer) {
	fmt.Fprintf(w, "使い方: rajikura service install [-listen <アドレス>]   Windows のサービスとトレイを入れる・更新する（既定の -listen は %s）\n", defaultServiceListen)
	fmt.Fprintln(w, "        rajikura service uninstall                    サービスとトレイを消す（データのフォルダと tray.toml は残す）")
	fmt.Fprintln(w, "管理者で動かす。")
}

// serviceManager は SCM の上の rajikura のサービス（テストで差し替える）。
type serviceManager interface {
	// Installed はサービスが登録されているかを返す。
	Installed() (bool, error)
	// Stop はサービスを止め、止まるまで待つ。止まっていれば何もしない。
	Stop(ctx context.Context) error
	// Create は、exe を args で起動する自動起動のサービスを、仮想アカウントで登録する。
	Create(exe string, args []string) error
	// Update は、登録済みのサービスの実行ファイルと引数を直す（ログオンのアカウントは変えない）。
	Update(exe string, args []string) error
	Start() error
	Delete() error
	Close() error
}

// installer は service install / uninstall の手順。外とのやり取りは差し替えられる（テストは管理者の権限も SCM も使わない）。
type installer struct {
	out io.Writer
	// programDir は実行ファイルを写すフォルダ（%ProgramFiles%\rajikura）、dataDir はデータのフォルダ（%ProgramData%\rajikura）。
	programDir, dataDir string
	selfExe             func() (string, error)
	elevated            func() bool
	connect             func() (serviceManager, error)
	checkListen         func(addr string) error
	copyFile            func(src, dst string) error
	restrictACL         func(dir string) error
	waitReady           func(ctx context.Context, url string) error
	// trayExe は埋め込んだトレイの exe（無ければ nil。tray_windows.go）。userProgramDir は tray install がトレイを置くフォルダ
	// （%LocalAppData%\Programs\rajikura）、trayConfigPath は打ったユーザーの tray.toml（%AppData%\rajikura\tray.toml）。
	// getRun・setRun・deleteRun は打ったユーザーの HKCU の Run のトレイの登録を読む・書く・消す。deleteOnReboot は、動いていて
	// 消せない前の版のトレイの exe を、PC の次の再起動のときに消すよう頼む。stopTray は、同じセッションで動いている前のトレイ
	// （パスは問わない）に終了を頼んで待つ（stopRunningTray）。stopTrayAt は、exe のパスが合うトレイだけに頼んで待つ（stopTrayAtPath）。
	// startTray は、トレイの exe を command の行で、端末のセッションの shell の持ち主のトレイとして起動し、持ち主が打ったユーザーと
	// 違えばその名前を返す（startTrayAsShellUser）。
	trayExe        []byte
	userProgramDir string
	trayConfigPath string
	getRun         func() (string, bool, error)
	setRun         func(command string) error
	deleteRun      func() (bool, error)
	deleteOnReboot func(path string) error
	stopTray       func() trayStop
	stopTrayAt     func(exe string) trayStop
	startTray      func(exe, command string) (string, error)
}

func newInstaller(out io.Writer) *installer {
	return &installer{
		out:         out,
		programDir:  filepath.Join(envOr("ProgramFiles", `C:\Program Files`), serviceName),
		dataDir:     filepath.Join(envOr("ProgramData", `C:\ProgramData`), serviceName),
		selfExe:     os.Executable,
		elevated:    func() bool { return windows.GetCurrentProcessToken().IsElevated() },
		connect:     connectSCM,
		checkListen: checkListen,
		copyFile:    copyFile,
		restrictACL: restrictACL,
		waitReady:   waitReady,

		trayExe:        embeddedTray(),
		userProgramDir: userProgramDir(),
		trayConfigPath: userTrayConfigPath(),
		getRun:         getTrayRun,
		setRun:         setTrayRun,
		deleteRun:      deleteTrayRun,
		deleteOnReboot: deleteOnReboot,
		stopTray:       stopRunningTray,
		stopTrayAt:     stopTrayAtPath,
		startTray:      startTrayAsShellUser,
	}
}

// userProgramDir は、tray install がトレイを置くフォルダ（%LocalAppData%\Programs\rajikura）。分からなければ空。
func userProgramDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "Programs", serviceName)
}

// userTrayConfigPath は、打ったユーザーの tray.toml（%AppData%\rajikura\tray.toml）。分からなければ空。
func userTrayConfigPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return trayconf.Path(d)
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func (in *installer) exePath() string { return filepath.Join(in.programDir, serviceName+".exe") }

// errNotElevated は管理者の権限が無いときのエラー。
var errNotElevated = errors.New("管理者で動かす（スタートメニューの「ターミナル」を右クリックし、「管理者として実行」で開いた中で打つ）")

// install はサービスを入れる。すでに入っていれば更新する（止める → 差し替える → 引数を直す → 起動する。データは残す）。
// 実行ファイルを写した後に、同じセッションで動いている前のトレイを止め、埋め込んだトレイを %ProgramFiles%\rajikura に書き出し、
// 打ったユーザーの tray.toml に接続先（serviceURL）を書いて、HKCU の Run に登録する（installTray。tray install と同じ処理）。最後に（Web UI が応えた後に）、トレイを端末のセッションの shell の
// 持ち主のトレイとして起動する（launchTray。前のトレイが終わらなかったときと、トレイを埋め込んでいないときは起動しない）。
// トレイを起動できなくても、install は失敗にしない。
// 待ち受けられないアドレスなら、登録（更新）の前に止まる（トレイには触らない）。
// 更新のときは、止めた後のどの段で失敗しても（待ち受けの確かめ・実行ファイルを写す・トレイを入れる・フォルダを作る・引数を直す・ACL）、
// 止めたサービスを起動し直してからエラーを返す（止まったままにしない）。起動し直せなければ、両方のエラーを返す。
// 実行ファイルは隣の一時ファイルに書いてから置き換えるので、写せなかったときは前の実行ファイルが残る。
// 写した後に失敗したときは、新しい実行ファイルで起動し直す（引数は、直す前に失敗したなら止める前のまま）。
// 前のトレイを止めた後に失敗したときは（サービスの起動・応えの待ちの失敗も含む）、置いてあるトレイの exe を起動し直す（restartTray）。
func (in *installer) install(ctx context.Context, listen string) error {
	if !in.elevated() {
		return errNotElevated
	}
	m, err := in.connect()
	if err != nil {
		return fmt.Errorf("サービスの管理につなげない: %w", err)
	}
	defer m.Close()
	installed, err := m.Installed()
	if err != nil {
		return fmt.Errorf("サービスが入っているかを確かめられない: %w", err)
	}
	if installed {
		fmt.Fprintln(in.out, "すでに入っているので更新する。サービスを止める")
		if err := m.Stop(ctx); err != nil {
			return fmt.Errorf("サービスを止められない: %w", err)
		}
	}
	// copied は実行ファイルを新しい版に差し替え終えたか、updated は引数を直し終えたか
	// （起動し直したときに、どの実行ファイルとどの引数で起動したかを出し分ける）。
	copied, updated := false, false
	// tray は、前のトレイを止めた結果（installTray の前は trayNotRunning）。trayExe・trayURL は、置くトレイと tray.toml に書く接続先。
	tray := trayNotRunning
	trayExe, trayURL := in.trayPath(), trayconf.NormalizeURL(serviceURL(listen))
	// fail は、更新のときに止めたサービスを起動し直し、止めたトレイを起動し直してから err を返す（新しく入れるときは、
	// サービスには触らない）。
	fail := func(err error) error {
		if installed {
			if serr := m.Start(); serr != nil {
				err = errors.Join(err, fmt.Errorf("止めたサービスを起動し直せない: %w", serr))
			} else {
				fmt.Fprintln(in.out, restartedMessage(copied, updated))
			}
		}
		in.restartTray(trayExe, trayURL, tray)
		return err
	}
	if err := in.checkListen(listen); err != nil {
		return fail(fmt.Errorf("%s で待ち受けられない（ほかのアプリがポートを使っているなら、-listen で別のアドレスを渡す）: %w", listen, err))
	}

	exe := in.exePath()
	src, err := in.selfExe()
	if err != nil {
		return fail(fmt.Errorf("実行ファイルの場所が分からない: %w", err))
	}
	if !sameFile(src, exe) {
		if err := os.MkdirAll(in.programDir, 0o755); err != nil {
			return fail(fmt.Errorf("%s を作れない: %w", in.programDir, err))
		}
		if err := in.copyFile(src, exe); err != nil {
			return fail(fmt.Errorf("実行ファイルを %s に写せない: %w", exe, err))
		}
		copied = true
	}
	tray, err = in.installTray(trayPlan{exe: trayExe, url: trayURL, command: "service"})
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(in.dataDir, 0o755); err != nil {
		return fail(fmt.Errorf("データのフォルダ %s を作れない: %w", in.dataDir, err))
	}
	args := []string{"-data", in.dataDir, "-listen", listen, "serve"}
	if installed {
		err = m.Update(exe, args)
	} else {
		err = m.Create(exe, args)
	}
	if err != nil {
		return fail(fmt.Errorf("サービスを登録できない: %w", err))
	}
	updated = installed
	// 仮想アカウント（NT SERVICE\rajikura）は、サービスを登録した後でないと ACL で名指しできないので、登録の後に掛ける。
	if err := in.restrictACL(in.dataDir); err != nil {
		return fail(fmt.Errorf("データのフォルダ %s の ACL を掛けられない: %w", in.dataDir, err))
	}
	if err := m.Start(); err != nil {
		in.restartTray(trayExe, trayURL, tray)
		return fmt.Errorf("サービスを起動できない: %w", err)
	}

	url := serviceURL(listen)
	wctx, cancel := context.WithTimeout(ctx, serviceReadyTimeout)
	defer cancel()
	logPath := config.Boot{DataDir: in.dataDir}.LogPath()
	if err := in.waitReady(wctx, url+"healthz"); err != nil {
		in.restartTray(trayExe, trayURL, tray)
		return fmt.Errorf("サービスを起動したが、%s が応えない（ログ %s を見る）: %w", url, logPath, err)
	}
	fmt.Fprintf(in.out, "サービス %s を入れて起動した（実行ファイル %s、データのフォルダ %s、ログ %s）\n", serviceName, exe, in.dataDir, logPath)
	// serve は、待ち受けを開く前にセットアップコードのファイルを書く（パスワードが決まっていれば消す）。
	b, err := os.ReadFile(filepath.Join(in.dataDir, SetupCodeFile))
	switch {
	case err == nil:
		fmt.Fprintf(in.out, "ブラウザで %s を開き、セットアップコード %s を入れて Web UI のパスワードを決める\n", url, strings.TrimSpace(string(b)))
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(in.out, "Web UI: %s\n", url)
	default:
		fmt.Fprintf(in.out, "Web UI: %s（セットアップコードのファイルを読めない: %v）\n", url, err)
	}
	in.launchTray(trayExe, trayURL, tray)
	return nil
}

// restartedMessage は、更新の途中で失敗して止めたサービスを起動し直したときに出す文。copied は実行ファイルを新しい版に
// 差し替え終えたか、updated は引数を直し終えたか。
func restartedMessage(copied, updated bool) string {
	exe, args := "前の実行ファイル", "止める前の引数"
	if copied {
		exe = "新しい実行ファイル"
	}
	if updated {
		args = "直した引数"
	}
	return "更新を終えられなかった。止めたサービスを、" + exe + "と" + args + "で起動し直した"
}

// uninstall はサービスを止めて消す。%ProgramFiles%\rajikura の実行ファイルとトレイの exe も消し、打ったユーザーの HKCU の Run の
// トレイの登録は、%ProgramFiles%\rajikura のトレイの exe を指すときだけ消す（uninstallTray）。データのフォルダと tray.toml は残す。
func (in *installer) uninstall(ctx context.Context) error {
	if !in.elevated() {
		return errNotElevated
	}
	m, err := in.connect()
	if err != nil {
		return fmt.Errorf("サービスの管理につなげない: %w", err)
	}
	defer m.Close()
	installed, err := m.Installed()
	if err != nil {
		return fmt.Errorf("サービスが入っているかを確かめられない: %w", err)
	}
	if installed {
		if err := m.Stop(ctx); err != nil {
			return fmt.Errorf("サービスを止められない: %w", err)
		}
		if err := m.Delete(); err != nil {
			return fmt.Errorf("サービスを消せない: %w", err)
		}
		fmt.Fprintf(in.out, "サービス %s を消した\n", serviceName)
	} else {
		fmt.Fprintf(in.out, "サービス %s は入っていない\n", serviceName)
	}
	in.uninstallTray()
	exe := in.exePath()
	src, err := in.selfExe()
	switch {
	case err == nil && sameFile(src, exe):
		fmt.Fprintf(in.out, "動いている %s は消せない。手で消してよい\n", exe)
	default:
		if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(in.out, "%s を消せない（手で消してよい）: %v\n", exe, err)
		} else {
			_ = os.Remove(in.programDir) // 空なら消える
		}
	}
	fmt.Fprintf(in.out, "データのフォルダ %s は残した（DB・鍵のファイル・ログ。要らなければ手で消す）\n", in.dataDir)
	return nil
}

// serviceURL は listen で待ち受ける Web UI の URL（末尾は /）。ホストが空か全部のアドレスなら 127.0.0.1 にする。
func serviceURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// checkListen は addr で待ち受けられるかを、開いてすぐ閉じて確かめる。
func checkListen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

// sameFile は a と b が同じファイルかを返す（どちらかが無ければ false）。
func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

// copyFile は src を dst に写す（隣の一時ファイルに書いてから置き換える）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// restrictACL は dir の継承を切り、サービスのアカウント（変更）・SYSTEM・Administrators（フルコントロール）だけにする。
// 下のファイル・フォルダには継承で効く。SYSTEM と Administrators は、表示名が言語で変わるので SID で渡す。
func restrictACL(dir string) error {
	cmd := exec.Command("icacls", dir, "/inheritance:r",
		"/grant:r", serviceAccount+":(OI)(CI)M",
		"/grant:r", "*S-1-5-18:(OI)(CI)F",
		"/grant:r", "*S-1-5-32-544:(OI)(CI)F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitReady は url が HTTP で応えるまで待つ（ctx が切れるまで）。
func waitReady(ctx context.Context, url string) error {
	c := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w（最後の誤り: %v）", ctx.Err(), err)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// scmManager は SCM の上の rajikura のサービス。
type scmManager struct {
	m *mgr.Mgr
	s *mgr.Service
}

func connectSCM() (serviceManager, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	return &scmManager{m: m}, nil
}

func (c *scmManager) Installed() (bool, error) {
	s, err := c.m.OpenService(serviceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c.s = s
	return true, nil
}

func (c *scmManager) Stop(ctx context.Context) error {
	st, err := c.s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := c.s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, serviceStopTimeout)
	defer cancel()
	for {
		st, err := c.s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("止まるのを待ちきれない: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (c *scmManager) Create(exe string, args []string) error {
	s, err := c.m.CreateService(serviceName, exe, mgr.Config{
		StartType:        mgr.StartAutomatic,
		DisplayName:      serviceName,
		Description:      "radiko の放送を録音してライブラリへ保存する",
		ServiceStartName: serviceAccount,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, args...)
	if err != nil {
		return err
	}
	c.s = s
	return nil
}

func (c *scmManager) Update(exe string, args []string) error {
	cfg, err := c.s.Config()
	if err != nil {
		return err
	}
	cmdline := syscall.EscapeArg(exe)
	for _, a := range args {
		cmdline += " " + syscall.EscapeArg(a)
	}
	cfg.BinaryPathName = cmdline
	cfg.StartType = mgr.StartAutomatic
	// 空にすると変えない（NAS に保存するために人が切り替えたログオンのアカウントを残す）。
	cfg.ServiceStartName = ""
	cfg.Password = ""
	return c.s.UpdateConfig(cfg)
}

func (c *scmManager) Start() error {
	if c.s == nil {
		return errors.New("サービスが登録されていない")
	}
	return c.s.Start()
}

func (c *scmManager) Delete() error { return c.s.Delete() }

func (c *scmManager) Close() error {
	if c.s != nil {
		c.s.Close()
	}
	return c.m.Disconnect()
}
