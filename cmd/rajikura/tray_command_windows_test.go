//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/trayconf"
)

// tray install は、service install と同じ処理で、前のトレイを止め → %LocalAppData%\Programs\rajikura に置き → tray.toml に -url を
// 書き → Run に "<exe>" だけを登録し → その場で、Run と同じ行で起動する（管理者は見ない）。
func TestTrayInstallPlacesTrayInUserDir(t *testing.T) {
	var out bytes.Buffer
	in, f := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	in.elevated = func() bool { t.Error("tray install が管理者かを見た"); return false }
	url := "https://rajikura.example.com"
	if err := in.trayInstall(url, ""); err != nil {
		t.Fatal(err)
	}
	exe := in.userTrayPath()
	if b, err := os.ReadFile(exe); err != nil || string(b) != "tray-v1" {
		t.Errorf("トレイの exe = %q, %v", b, err)
	}
	if _, err := os.Stat(in.trayPath()); !os.IsNotExist(err) {
		t.Errorf("%%ProgramFiles%% にも置いた（err = %v）", err)
	}
	cmd := `"` + exe + `"`
	want := []string{"stop-tray(,)", "set-run(tray-v1," + url + ")", "start-tray(tray-v1) " + cmd}
	if strings.Join(f.events, "|") != strings.Join(want, "|") {
		t.Errorf("順 = %q, want %q", f.events, want)
	}
	if f.run != cmd {
		t.Errorf("Run = %q, want %q", f.run, cmd)
	}
	if c, err := readConfig(in.trayConfigPath); err != nil || c != (trayconf.Config{URL: url}) {
		t.Errorf("tray.toml = %+v, %v", c, err)
	}
	for _, s := range []string{"トレイを " + exe + " に入れ", "トレイを起動した", "「Web UI と連携する」で連携する"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
}

// tray install -token を渡すと、tray.toml に url と token を書く（連携しなくてよい）。
func TestTrayInstallWithToken(t *testing.T) {
	var out bytes.Buffer
	in, _ := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	if err := in.trayInstall("https://rajikura.example.com", "tok-1"); err != nil {
		t.Fatal(err)
	}
	if c, err := readConfig(in.trayConfigPath); err != nil || c != (trayconf.Config{URL: "https://rajikura.example.com", Token: "tok-1"}) {
		t.Errorf("tray.toml = %+v, %v", c, err)
	}
	if !strings.Contains(out.String(), "トレイの接続先 https://rajikura.example.com と API トークンを "+in.trayConfigPath+" に書いた") || strings.Contains(out.String(), "連携する") {
		t.Errorf("stdout = %q", out.String())
	}
}

// tray.toml の書き方: -token を渡したら url と token を書く。渡さず url が同じなら触らない。渡さず、無いか url が違えば url を書いて
// token を消す。読めなければ上書きする。
func TestWriteTrayConfigTable(t *testing.T) {
	const url = "http://127.0.0.1:18080"
	cases := []struct {
		name    string
		before  string // tray.toml の中身（空なら置かない）
		token   string
		want    trayconf.Config
		touched bool
		say     []string
		notSay  []string
	}{
		{name: "-token を渡した", before: "url = \"http://127.0.0.1:8080\"\ntoken = \"old\"\n", token: "new",
			want: trayconf.Config{URL: url, Token: "new"}, touched: true, say: []string{"と API トークンを"}, notSay: []string{"連携"}},
		{name: "-token を渡さず url が同じ（末尾の / を落として比べる）", before: "url = \"" + url + "/\"\ntoken = \"keep\"\n",
			want: trayconf.Config{URL: url + "/", Token: "keep"}, touched: false},
		{name: "-token を渡さず url が違う", before: "url = \"http://127.0.0.1:8080\"\ntoken = \"old\"\n",
			want: trayconf.Config{URL: url}, touched: true, say: []string{"接続先が変わった（前は http://127.0.0.1:8080）", "トークンは消した", "連携し直す"}},
		{name: "-token を渡さず tray.toml が無い",
			want: trayconf.Config{URL: url}, touched: true, say: []string{"「Web UI と連携する」で連携する"}, notSay: []string{"変わった"}},
		{name: "tray.toml を読めない（知らないキー）", before: "url = \"" + url + "\"\ntokn = \"x\"\n",
			want: trayconf.Config{URL: url}, touched: true, say: []string{"を読めないので、上書きする", "tokn", "で連携する"}},
		{name: "tray.toml を読めない（TOML の誤り）で -token を渡した", before: "url = \n", token: "t",
			want: trayconf.Config{URL: url, Token: "t"}, touched: true, say: []string{"を読めないので、上書きする", "と API トークンを"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			in, _ := fakeInstaller(t, &fakeManager{}, &out)
			if c.before != "" {
				os.MkdirAll(filepath.Dir(in.trayConfigPath), 0o700)
				if err := os.WriteFile(in.trayConfigPath, []byte(c.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := in.writeTrayConfig(url, c.token); err != nil {
				t.Fatal(err)
			}
			got, err := readConfig(in.trayConfigPath)
			if err != nil || got != c.want {
				t.Errorf("tray.toml = %+v, %v, want %+v", got, err, c.want)
			}
			b, _ := os.ReadFile(in.trayConfigPath)
			if touched := string(b) != c.before; touched != c.touched {
				t.Errorf("書き直した = %v, want %v（中身 %q）", touched, c.touched, b)
			}
			if !c.touched && out.Len() != 0 {
				t.Errorf("触らないときは何も出さない: %q", out.String())
			}
			for _, s := range c.say {
				if !strings.Contains(out.String(), s) {
					t.Errorf("stdout に %q が無い: %q", s, out.String())
				}
			}
			for _, s := range c.notSay {
				if strings.Contains(out.String(), s) {
					t.Errorf("stdout に %q がある: %q", s, out.String())
				}
			}
		})
	}
}

// service install も、tray.toml を -token を渡さない tray install と同じく書く（-listen の URL が変われば、前のトークンを消す）。
func TestServiceInstallRewritesTrayConfigWhenURLChanges(t *testing.T) {
	var out bytes.Buffer
	in, _ := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	if err := trayconf.Write(in.trayConfigPath, trayconf.Config{URL: "http://127.0.0.1:8080", Token: "old"}); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	if c, _ := readConfig(in.trayConfigPath); c != (trayconf.Config{URL: "http://" + addr}) {
		t.Errorf("tray.toml = %+v", c)
	}
	// 打ち直しで URL が同じなら、トークンを残す。
	if err := trayconf.Write(in.trayConfigPath, trayconf.Config{URL: "http://" + addr, Token: "linked"}); err != nil {
		t.Fatal(err)
	}
	in.connect = func() (serviceManager, error) { return &fakeManager{installed: true}, nil }
	if err := in.install(context.Background(), addr); err != nil {
		t.Fatal(err)
	}
	if c, _ := readConfig(in.trayConfigPath); c.Token != "linked" {
		t.Errorf("同じ URL の打ち直しでトークンが消えた: %+v", c)
	}
}

// Run に前の登録があって今の行と違えば（前の版の -url 付きの行、tray install の行）、前の行を出す。同じなら出さない。
func TestInstallTrayShowsPreviousRun(t *testing.T) {
	var out bytes.Buffer
	in, f := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	prev := `"` + in.trayPath() + `" -url http://127.0.0.1:8080`
	f.run, f.hasRun = prev, true
	if err := in.trayInstall("http://127.0.0.1:8080", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "前の登録 "+prev+" を書き換えた") {
		t.Errorf("stdout = %q", out.String())
	}
	out.Reset()
	if err := in.trayInstall("http://127.0.0.1:8080", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "前の登録") {
		t.Errorf("同じ行なのに前の登録を出した: %q", out.String())
	}
}

// 同じユーザーで service install と tray install の両方を打つと、後に打った方の Run が残る。
func TestLaterInstallWinsRun(t *testing.T) {
	in, f := trayInstaller(t, &fakeManager{}, &bytes.Buffer{}, "tray-v1")
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatal(err)
	}
	if err := in.trayInstall("https://rajikura.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if f.run != `"`+in.userTrayPath()+`"` {
		t.Errorf("Run = %q", f.run)
	}
	if err := in.install(context.Background(), freeAddr(t)); err != nil {
		t.Fatal(err)
	}
	if f.run != `"`+in.trayPath()+`"` {
		t.Errorf("Run = %q", f.run)
	}
}

// トレイを埋め込んでいない rajikura.exe の tray install は、何もせずに失敗にする。
func TestTrayInstallWithoutEmbeddedTray(t *testing.T) {
	in, _ := fakeInstaller(t, &fakeManager{}, &bytes.Buffer{})
	if err := in.trayInstall("http://127.0.0.1:8080", ""); !errors.Is(err, errNoEmbeddedTray) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(in.trayConfigPath); !os.IsNotExist(err) {
		t.Errorf("tray.toml を書いた（err = %v）", err)
	}
	if _, err := os.Stat(in.userTrayPath()); !os.IsNotExist(err) {
		t.Errorf("トレイを置いた（err = %v）", err)
	}
}

// 本物の newInstaller を通る run でも、トレイを埋め込んでいなければ終了コード 1 と、Release の exe には入っている旨を出す。
// （トレイを埋め込んだ開発のビルドでは、本物の %LocalAppData% と HKCU に書くので、飛ばす。）
func TestTrayInstallCommandWithoutEmbeddedTray(t *testing.T) {
	if embeddedTray() != nil {
		t.Skip("トレイを埋め込んだビルドでは、本物の HKCU に書くので流さない")
	}
	code, out, errOut := runCmd(t, context.Background(), "tray", "install", "-url", "http://127.0.0.1:8080")
	if code != 1 || out != "" || !strings.Contains(errOut, "rajikura tray install: この rajikura.exe にはトレイが入っていない（Release の rajikura.exe には入っている）") {
		t.Errorf("code = %d, stdout = %q, stderr = %q", code, out, errOut)
	}
}

// tray の使い方の誤りは、何もする前に終了コード 2 にする。
func TestTrayCommandUsage(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"tray"}, "rajikura tray install -url"},
		{[]string{"tray", "nope"}, "不明な操作"},
		{[]string{"tray", "install"}, "-url を渡す"},
		{[]string{"tray", "install", "-token", "x"}, "-url を渡す"},
		{[]string{"tray", "install", "-url", "127.0.0.1:8080"}, "URL として読めない"},
		{[]string{"tray", "install", "-url", "ftp://example.com"}, "URL として読めない"},
		{[]string{"tray", "install", "-url", "http://127.0.0.1:8080", "x"}, "rajikura tray install -url"},
		{[]string{"tray", "install", "-nope"}, "-nope"},
		{[]string{"tray", "uninstall", "x"}, "rajikura tray uninstall"},
	} {
		code, out, errOut := runCmd(t, context.Background(), c.args...)
		if code != 2 || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: code = %d, stdout = %q, stderr = %q", c.args, code, out, errOut)
		}
	}
}

// tray uninstall は、Run の登録が自分の exe を指すときだけ消し、自分の exe のトレイだけを止めてから exe を消す。tray.toml は残す。
func TestTrayUninstall(t *testing.T) {
	var out bytes.Buffer
	in, f := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	if err := in.trayInstall("https://rajikura.example.com", "tok"); err != nil {
		t.Fatal(err)
	}
	f.events = nil
	f.stop = trayStopped
	out.Reset()
	if err := in.trayUninstall(); err != nil {
		t.Fatal(err)
	}
	exe := in.userTrayPath()
	if f.hasRun {
		t.Errorf("Run が残っている: %q", f.run)
	}
	if strings.Join(f.events, "|") != "stop-tray-at("+exe+")" {
		t.Errorf("止め方 = %q（自分の exe のパスで絞って止める）", f.events)
	}
	if _, err := os.Stat(in.userProgramDir); !os.IsNotExist(err) {
		t.Errorf("%s が残っている（err = %v）", in.userProgramDir, err)
	}
	if c, err := readConfig(in.trayConfigPath); err != nil || c.Token != "tok" {
		t.Errorf("tray.toml が消えた・変わった: %+v, %v", c, err)
	}
	for _, s := range []string{"動いていたトレイを終了した", "トレイ " + exe + " を消した", in.trayConfigPath + " は残した", "「API トークン」の画面で失効させる"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
}

// tray uninstall は、Run の登録が自分の exe を指していなければ（後に service install を打った）消さない。
func TestTrayUninstallKeepsRunOfServiceTray(t *testing.T) {
	var out bytes.Buffer
	in, f := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	if err := in.trayInstall("https://rajikura.example.com", ""); err != nil {
		t.Fatal(err)
	}
	svc := `"` + in.trayPath() + `"`
	f.run = svc
	out.Reset()
	if err := in.trayUninstall(); err != nil {
		t.Fatal(err)
	}
	if !f.hasRun || f.run != svc {
		t.Errorf("service install の Run を消した: %v %q", f.hasRun, f.run)
	}
	if !strings.Contains(out.String(), "を指していないので、消さなかった") {
		t.Errorf("stdout = %q", out.String())
	}
}

// tray uninstall は、exe が無ければ、そう出してエラーにしない。
func TestTrayUninstallWithoutTray(t *testing.T) {
	var out bytes.Buffer
	in, f := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	if err := in.trayUninstall(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "トレイ "+in.userTrayPath()+" は入っていない") {
		t.Errorf("stdout = %q", out.String())
	}
	if len(f.events) != 0 {
		t.Errorf("入っていないのにトレイを止めた: %q", f.events)
	}
}

// tray uninstall で、トレイが止まらず exe が使われていれば、よけて、メニューの「終了」で終えることを出す。
func TestTrayUninstallRunningTray(t *testing.T) {
	var out bytes.Buffer
	in, f := trayInstaller(t, &fakeManager{}, &out, "tray-v1")
	f.stop = trayStillRunning
	startRunningExe(t, in.userTrayPath())
	if err := in.trayUninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in.userTrayPath()); !os.IsNotExist(err) {
		t.Errorf("トレイの exe が元の名前で残っている（err = %v）", err)
	}
	aside, _ := filepath.Glob(in.userTrayPath() + ".*.old")
	if len(aside) != 1 || strings.Join(f.reboot, ",") != aside[0] {
		t.Errorf("よけた exe = %v、再起動で消す = %v", aside, f.reboot)
	}
	for _, s := range []string{"10 秒待っても終わらなかった", "動いていたので消せず", "トレイを止められなかったので、通知領域のトレイのメニューの「終了」で終える", "次の tray install・uninstall でも消す"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("stdout に %q が無い: %q", s, out.String())
		}
	}
}

// 前のトレイを止めた後に tray install が失敗したら、置いてある exe を起動し直す。
func TestTrayInstallFailureRestartsTray(t *testing.T) {
	in, f := trayInstaller(t, &fakeManager{}, &bytes.Buffer{}, "tray-v2")
	f.stop = trayStopped
	boom := errors.New("boom")
	in.setRun = func(string) error { return boom }
	if err := in.trayInstall("http://127.0.0.1:8080", ""); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if last := f.events[len(f.events)-1]; !strings.HasPrefix(last, "start-tray(tray-v2) ") {
		t.Errorf("起動し直していない: %q", f.events)
	}
}
