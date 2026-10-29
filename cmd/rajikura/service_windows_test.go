//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// runHandler は、SCM の代わりに h.Execute を動かし、要求 cmd を送って、戻り値と SCM に返した状態を返す。
func runHandler(t *testing.T, h svc.Handler, cmd svc.Cmd, wait <-chan struct{}) (uint32, []svc.Status) {
	t.Helper()
	r := make(chan svc.ChangeRequest)
	s := make(chan svc.Status, 100)
	type result struct {
		code uint32
	}
	done := make(chan result, 1)
	go func() {
		_, code := h.Execute(nil, r, s)
		done <- result{code}
	}()
	// Running を返してから要求を送る。
	deadline := time.After(10 * time.Second)
	var got []svc.Status
	for running := false; !running; {
		select {
		case st := <-s:
			got = append(got, st)
			running = st.State == svc.Running
		case <-deadline:
			t.Fatal("Running にならない")
		}
	}
	if wait != nil {
		<-wait
	}
	r <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: got[len(got)-1]}
	r <- svc.ChangeRequest{Cmd: cmd}
	select {
	case res := <-done:
		close(s)
		for st := range s {
			got = append(got, st)
		}
		return res.code, got
	case <-deadline:
		t.Fatal("要求を送っても止まらない")
		return 0, nil
	}
}

// サービスとして動くとき、Stop と Shutdown（PC の終了）の要求で serve の ctx が取り消され、止まるまで停止中を返す。
func TestServiceHandlerStopsOnRequest(t *testing.T) {
	for _, cmd := range []svc.Cmd{svc.Stop, svc.Shutdown} {
		t.Run(fmt.Sprint(cmd), func(t *testing.T) {
			var logBuf bytes.Buffer
			canceled := make(chan struct{})
			h := &serviceHandler{
				ctx: context.Background(),
				log: slog.New(slog.NewTextHandler(&logBuf, nil)),
				// 取り消されてから少し待って戻る（格納を止めるまでの間に、停止中を知らせ直すことを見る）。
				checkpointEvery: 10 * time.Millisecond,
				serve: func(ctx context.Context) error {
					<-ctx.Done()
					close(canceled)
					time.Sleep(50 * time.Millisecond)
					return ctx.Err()
				},
			}
			code, statuses := runHandler(t, h, cmd, nil)
			select {
			case <-canceled:
			default:
				t.Fatal("ctx が取り消されていない")
			}
			if code != 0 || h.err != nil {
				t.Errorf("終了コード = %d、err = %v（取り消しで止まったのは成功）", code, h.err)
			}
			var running svc.Status
			pending := 0
			for _, st := range statuses {
				switch st.State {
				case svc.Running:
					running = st
				case svc.StopPending:
					pending++
					if st.WaitHint == 0 || st.CheckPoint != uint32(pending) {
						t.Errorf("停止中の %d 回目: WaitHint = %d、CheckPoint = %d", pending, st.WaitHint, st.CheckPoint)
					}
				}
			}
			if running.Accepts&svc.AcceptStop == 0 || running.Accepts&svc.AcceptShutdown == 0 {
				t.Errorf("Running で受ける要求 = %v（Stop と Shutdown を受ける）", running.Accepts)
			}
			if pending < 2 {
				t.Errorf("停止中を返したのが %d 回（止まるまで知らせ直す）", pending)
			}
		})
	}
}

// serve が自分でエラーで止まったら、失敗の終了コードを SCM に返す。
func TestServiceHandlerServeFails(t *testing.T) {
	h := &serviceHandler{
		ctx:   context.Background(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		serve: func(context.Context) error { return errors.New("listen を開けない") },
	}
	r := make(chan svc.ChangeRequest)
	s := make(chan svc.Status, 10)
	_, code := h.Execute(nil, r, s)
	if code != 1 || h.err == nil {
		t.Errorf("終了コード = %d、err = %v", code, h.err)
	}
}

// サービスとして動くとき、ログはファイルに出て、大きさで回る。
func TestRunAsServiceLogsToRotatingFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "rajikura.log")
	// serve の出力を書き終えてから停止を送る（停止のログが今のファイルに入るように）。
	written := make(chan struct{})
	runner := func(name string, h svc.Handler) error {
		if name != serviceName {
			t.Errorf("サービスの名前 = %q", name)
		}
		code, _ := runHandler(t, h, svc.Stop, written)
		if code != 0 {
			t.Errorf("終了コード = %d", code)
		}
		return nil
	}
	line := strings.Repeat("x", 99) + "\n"
	err := runAsService(context.Background(), logPath, 300, 3, runner, func(ctx context.Context, w io.Writer) error {
		for i := 0; i < 10; i++ {
			io.WriteString(w, line)
		}
		close(written)
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 今のファイルには、serve の出力の後に受けた停止の要求のログがある（ハンドラのログも同じファイルに出る）。
	cur, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "停止の要求を受けた") {
		t.Errorf("rajikura.log に停止のログが無い: %q", cur)
	}
	for _, name := range []string{"rajikura.log.1", "rajikura.log.2"} {
		b, err := os.ReadFile(filepath.Join(dir, "logs", name))
		if err != nil {
			t.Fatalf("%s が無い（回っていない）: %v", name, err)
		}
		if len(b) > 300 {
			t.Errorf("%s が %d バイト（上限 300）", name, len(b))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "rajikura.log.3")); !os.IsNotExist(err) {
		t.Errorf("3 本より多く残っている（err = %v）", err)
	}
}

// fakeManager は SCM の代わり。呼ばれた操作を順に記録する。
type fakeManager struct {
	installed bool
	calls     []string
	// startErr は Start が返すエラー。
	startErr error
	// updateErr は Update が返すエラー。
	updateErr error
}

func (f *fakeManager) Installed() (bool, error)   { return f.installed, nil }
func (f *fakeManager) Stop(context.Context) error { f.calls = append(f.calls, "stop"); return nil }
func (f *fakeManager) Create(exe string, args []string) error {
	f.calls = append(f.calls, "create "+exe+" "+strings.Join(args, " "))
	return nil
}
func (f *fakeManager) Update(exe string, args []string) error {
	f.calls = append(f.calls, "update "+exe+" "+strings.Join(args, " "))
	return f.updateErr
}
func (f *fakeManager) Start() error  { f.calls = append(f.calls, "start"); return f.startErr }
func (f *fakeManager) Delete() error { f.calls = append(f.calls, "delete"); return nil }
func (f *fakeManager) Close() error  { return nil }

// fakeInstaller は、管理者の権限も SCM も使わない installer を作る。
func fakeInstaller(t *testing.T, m *fakeManager, out io.Writer) (*installer, *[]string) {
	t.Helper()
	root := t.TempDir()
	self := filepath.Join(root, "dl", "rajikura.exe")
	os.MkdirAll(filepath.Dir(self), 0o755)
	os.WriteFile(self, []byte("exe"), 0o644)
	var steps []string
	in := &installer{
		out:         out,
		programDir:  filepath.Join(root, "Program Files", "rajikura"),
		dataDir:     filepath.Join(root, "ProgramData", "rajikura"),
		selfExe:     func() (string, error) { return self, nil },
		elevated:    func() bool { return true },
		connect:     func() (serviceManager, error) { return m, nil },
		checkListen: checkListen,
		copyFile: func(src, dst string) error {
			steps = append(steps, "copy")
			return copyFile(src, dst)
		},
		restrictACL: func(string) error { steps = append(steps, "acl"); return nil },
		waitReady:   func(context.Context, string) error { steps = append(steps, "ready"); return nil },
		// トレイは埋め込んでいない形にする（トレイのテストは tray_windows_test.go。HKCU にも、本物の tray.toml にも触らない）。
		userProgramDir: filepath.Join(root, "LocalAppData", "Programs", "rajikura"),
		trayConfigPath: filepath.Join(root, "AppData", "rajikura", "tray.toml"),
		getRun:         func() (string, bool, error) { return "", false, nil },
		setRun: func(string) error {
			t.Error("トレイを埋め込んでいないのに Run に登録した")
			return nil
		},
		deleteRun:      func() (bool, error) { return false, nil },
		deleteOnReboot: func(string) error { return nil },
		// 前のトレイを止める・トレイを起動するのも、トレイを埋め込んでいなければ呼ばない（本物は動いているトレイに触る）。
		stopTray: func() trayStop {
			t.Error("トレイを埋め込んでいないのに前のトレイを止めた")
			return trayNotRunning
		},
		startTray: func(string, string) (string, error) {
			t.Error("トレイを埋め込んでいないのにトレイを起動した")
			return "", nil
		},
		// tray uninstall の止め方も、本物は動いているトレイに触るので、差し替える（呼ばれたら trayInstaller が記録する）。
		stopTrayAt: func(string) trayStop { return trayNotRunning },
	}
	return in, &steps
}

// 待ち受けられないアドレスなら、理由を言って登録の前に止まる。
func TestInstallStopsWhenListenUnavailable(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	addr := busy.Addr().String()

	t.Run("初めて入れる", func(t *testing.T) {
		m := &fakeManager{}
		in, steps := fakeInstaller(t, m, io.Discard)
		err := in.install(context.Background(), addr)
		if err == nil || !strings.Contains(err.Error(), addr+" で待ち受けられない") {
			t.Fatalf("err = %v", err)
		}
		if len(m.calls) != 0 || len(*steps) != 0 {
			t.Errorf("登録の前に止まっていない: SCM %v、手順 %v", m.calls, *steps)
		}
		if _, err := os.Stat(in.exePath()); !os.IsNotExist(err) {
			t.Errorf("実行ファイルを写した（err = %v）", err)
		}
	})
	t.Run("更新", func(t *testing.T) {
		m := &fakeManager{installed: true}
		var out bytes.Buffer
		in, steps := fakeInstaller(t, m, &out)
		err := in.install(context.Background(), addr)
		if err == nil || !strings.Contains(err.Error(), "で待ち受けられない") {
			t.Fatalf("err = %v", err)
		}
		// 止めたサービスは、直さずに起動し直す。
		if strings.Join(m.calls, ",") != "stop,start" || len(*steps) != 0 {
			t.Errorf("SCM %v、手順 %v（stop,start だけのはず）", m.calls, *steps)
		}
		if !strings.Contains(out.String(), "前の実行ファイルと止める前の引数で起動し直した") {
			t.Errorf("stdout = %q", out.String())
		}
	})
}

// 更新のとき、止めた後の段で失敗しても、止めたサービスを起動し直してからエラーを返す（止まったままにしない）。
func TestInstallUpdateRestartsOnFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	boom := errors.New("boom")

	t.Run("実行ファイルを写せない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		var out bytes.Buffer
		in, _ := fakeInstaller(t, m, &out)
		in.copyFile = func(string, string) error { return boom }
		err := in.install(context.Background(), addr)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "実行ファイルを") {
			t.Fatalf("err = %v", err)
		}
		if strings.Join(m.calls, ",") != "stop,start" {
			t.Errorf("SCM = %v（stop,start だけのはず）", m.calls)
		}
		if !strings.Contains(out.String(), "前の実行ファイルと止める前の引数で起動し直した") {
			t.Errorf("stdout = %q", out.String())
		}
	})
	t.Run("ACL を掛けられない", func(t *testing.T) {
		m := &fakeManager{installed: true}
		var out bytes.Buffer
		in, _ := fakeInstaller(t, m, &out)
		in.restrictACL = func(string) error { return boom }
		err := in.install(context.Background(), addr)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "ACL を掛けられない") {
			t.Fatalf("err = %v", err)
		}
		calls := strings.Join(m.calls, ",")
		if !strings.HasPrefix(calls, "stop,update ") || !strings.HasSuffix(calls, ",start") || len(m.calls) != 3 {
			t.Errorf("SCM = %v（stop,update,start のはず）", m.calls)
		}
		if !strings.Contains(out.String(), "新しい実行ファイルと直した引数で起動し直した") {
			t.Errorf("stdout = %q", out.String())
		}
	})
	t.Run("写した後に引数を直せない", func(t *testing.T) {
		m := &fakeManager{installed: true, updateErr: boom}
		var out bytes.Buffer
		in, _ := fakeInstaller(t, m, &out)
		err := in.install(context.Background(), addr)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "サービスを登録できない") {
			t.Fatalf("err = %v", err)
		}
		calls := strings.Join(m.calls, ",")
		if !strings.HasPrefix(calls, "stop,update ") || !strings.HasSuffix(calls, ",start") || len(m.calls) != 3 {
			t.Errorf("SCM = %v（stop,update,start のはず）", m.calls)
		}
		// 実行ファイルは差し替え済みなので、新しい実行ファイルを止める前の引数で起動し直している。
		if !strings.Contains(out.String(), "新しい実行ファイルと止める前の引数で起動し直した") {
			t.Errorf("stdout = %q", out.String())
		}
	})
	t.Run("起動し直しも失敗する", func(t *testing.T) {
		startErr := errors.New("start failed")
		m := &fakeManager{installed: true, startErr: startErr}
		in, _ := fakeInstaller(t, m, io.Discard)
		in.copyFile = func(string, string) error { return boom }
		err := in.install(context.Background(), addr)
		if !errors.Is(err, boom) || !errors.Is(err, startErr) || !strings.Contains(err.Error(), "起動し直せない") {
			t.Fatalf("err = %v（両方のエラーが分かるはず）", err)
		}
	})
	t.Run("新しく入れるときは起動しない", func(t *testing.T) {
		m := &fakeManager{}
		in, _ := fakeInstaller(t, m, io.Discard)
		in.copyFile = func(string, string) error { return boom }
		if err := in.install(context.Background(), addr); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if len(m.calls) != 0 {
			t.Errorf("SCM = %v", m.calls)
		}
	})
}

// 待ち受けられるなら、写す → 登録 → ACL → 起動 → 応えるのを待つ、の順に進み、セットアップコードと URL を出す。
func TestInstallRegistersService(t *testing.T) {
	m := &fakeManager{}
	var out bytes.Buffer
	in, steps := fakeInstaller(t, m, &out)
	// serve の代わりに、セットアップコードのファイルを置く。
	os.MkdirAll(in.dataDir, 0o755)
	os.WriteFile(filepath.Join(in.dataDir, SetupCodeFile), []byte("ABCD-EFGH\n"), 0o600)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	wantCreate := "create " + in.exePath() + " -data " + in.dataDir + " -listen " + addr + " serve"
	if strings.Join(m.calls, ",") != wantCreate+",start" {
		t.Errorf("SCM = %v", m.calls)
	}
	if strings.Join(*steps, ",") != "copy,acl,ready" {
		t.Errorf("手順 = %v", *steps)
	}
	if b, err := os.ReadFile(in.exePath()); err != nil || string(b) != "exe" {
		t.Errorf("写した実行ファイル = %q, %v", b, err)
	}
	if !strings.Contains(out.String(), "セットアップコード ABCD-EFGH") || !strings.Contains(out.String(), "http://"+addr+"/") {
		t.Errorf("stdout = %q", out.String())
	}

	// 打ち直すと更新する（止める → 差し替える → 引数を直す → 起動する）。パスワードが決まっていれば URL だけを出す。
	m2 := &fakeManager{installed: true}
	in.connect = func() (serviceManager, error) { return m2, nil }
	os.Remove(filepath.Join(in.dataDir, SetupCodeFile))
	out.Reset()
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m2.calls, ","); got != "stop,"+strings.Replace(wantCreate, "create", "update", 1)+",start" {
		t.Errorf("更新の SCM = %v", got)
	}
	if strings.Contains(out.String(), "セットアップコード") || !strings.Contains(out.String(), "Web UI: http://"+addr+"/") {
		t.Errorf("更新の stdout = %q", out.String())
	}
}

func TestInstallNeedsElevation(t *testing.T) {
	m := &fakeManager{}
	in, _ := fakeInstaller(t, m, io.Discard)
	in.elevated = func() bool { return false }
	if err := in.install(context.Background(), "127.0.0.1:0"); !errors.Is(err, errNotElevated) {
		t.Errorf("install: err = %v", err)
	}
	if err := in.uninstall(context.Background()); !errors.Is(err, errNotElevated) {
		t.Errorf("uninstall: err = %v", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("SCM = %v", m.calls)
	}
}

// uninstall は止めて消し、実行ファイルを消して、データのフォルダは残す。
func TestUninstallKeepsDataDir(t *testing.T) {
	m := &fakeManager{installed: true}
	var out bytes.Buffer
	in, _ := fakeInstaller(t, m, &out)
	os.MkdirAll(in.programDir, 0o755)
	os.WriteFile(in.exePath(), []byte("exe"), 0o644)
	os.MkdirAll(in.dataDir, 0o755)
	os.WriteFile(filepath.Join(in.dataDir, "rajikura.db"), []byte("db"), 0o644)
	if err := in.uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.calls, ",") != "stop,delete" {
		t.Errorf("SCM = %v", m.calls)
	}
	if _, err := os.Stat(in.programDir); !os.IsNotExist(err) {
		t.Errorf("%s が残っている（err = %v）", in.programDir, err)
	}
	if _, err := os.Stat(filepath.Join(in.dataDir, "rajikura.db")); err != nil {
		t.Errorf("データのフォルダが消えた: %v", err)
	}
	if !strings.Contains(out.String(), "データのフォルダ "+in.dataDir+" は残した") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestServiceURL(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:8080": "http://127.0.0.1:8080/",
		":8080":          "http://127.0.0.1:8080/",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/",
		"[::]:9000":      "http://127.0.0.1:9000/",
		"192.168.1.5:80": "http://192.168.1.5:80/",
		"[::1]:8080":     "http://[::1]:8080/",
	} {
		if got := serviceURL(listen); got != want {
			t.Errorf("serviceURL(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestServiceCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"service"}, {"service", "nope"}, {"service", "install", "x"}, {"service", "uninstall", "x"}, {"service", "install", "-nope"}} {
		code, _, errOut := runCmd(t, context.Background(), args...)
		if code != 2 || !strings.Contains(errOut, "rajikura service") {
			t.Errorf("%v: code = %d, stderr = %q", args, code, errOut)
		}
	}
}
