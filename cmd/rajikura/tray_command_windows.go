//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/t-seki/rajikura/internal/trayconf"
)

// runTrayCommand は tray install / uninstall を動かす。serve を Windows のサービスにしていない PC（serve を k3s などで
// 動かしている人）に、埋め込んだトレイだけを入れ、-url の serve に向ける。管理者は要らない（書くのは打ったユーザーの
// %LocalAppData%・%AppData%・HKCU）。入れる処理は service install と同じ installTray（tray_windows.go）。
// 足したら直す: service の操作
func runTrayCommand(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		trayUsage(stderr)
		return 2
	}
	in := newInstaller(stdout)
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("rajikura tray install", flag.ContinueOnError)
		urlFlag := fs.String("url", "", "トレイがつなぐ rajikura の Web UI の URL（必須。例 https://rajikura.example.com）")
		token := fs.String("token", "", "トレイが使う API トークン（渡せば Web UI と連携しなくてよい）")
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return 2
		}
		if fs.NArg() > 0 {
			trayUsage(stderr)
			return 2
		}
		if *urlFlag == "" {
			fmt.Fprintln(stderr, "rajikura tray install: -url を渡す（トレイがつなぐ rajikura の Web UI の URL）")
			trayUsage(stderr)
			return 2
		}
		u, err := trayconf.ParseURL(*urlFlag)
		if err != nil {
			fmt.Fprintf(stderr, "rajikura tray install: -url: %v\n", err)
			return 2
		}
		return exitCode("tray install", in.trayInstall(trayconf.NormalizeURL(u.String()), *token), stderr)
	case "uninstall":
		fs := flag.NewFlagSet("rajikura tray uninstall", flag.ContinueOnError)
		if err := parseFlags(fs, args[1:], stderr); err != nil {
			return 2
		}
		if fs.NArg() > 0 {
			trayUsage(stderr)
			return 2
		}
		return exitCode("tray uninstall", in.trayUninstall(), stderr)
	default:
		fmt.Fprintf(stderr, "rajikura tray: 不明な操作 %q\n", args[0])
		trayUsage(stderr)
		return 2
	}
}

// 足したら直す: service の操作
func trayUsage(w io.Writer) {
	fmt.Fprintln(w, "使い方: rajikura tray install -url <URL> [-token <トークン>]   トレイだけを入れる・更新する（serve をこの PC のサービスにしていないとき）")
	fmt.Fprintln(w, "        rajikura tray uninstall                            tray install で入れたトレイを消す（tray.toml は残す）")
	fmt.Fprintln(w, "管理者は要らない。")
}

// errNoEmbeddedTray は、トレイを埋め込んでいない rajikura.exe で tray install を打ったときのエラー。
var errNoEmbeddedTray = errors.New("この rajikura.exe にはトレイが入っていない（Release の rajikura.exe には入っている）")

// trayInstall は、埋め込んだトレイを %LocalAppData%\Programs\rajikura に置き、打ったユーザーの tray.toml に url（と token）を書いて、
// HKCU の Run に登録し、その場で起動する（installTray → launchTray。service install と同じ処理で、置く先と接続先だけが違う）。
// トレイを埋め込んでいなければ、何もせずにエラーを返す。打ち直すと、前のトレイを止めて差し替える（更新）。
// 前のトレイを止めた後に失敗したら、置いてある exe を起動し直してからエラーを返す（restartTray）。
func (in *installer) trayInstall(url, token string) error {
	if len(in.trayExe) == 0 {
		return errNoEmbeddedTray
	}
	if in.userProgramDir == "" {
		return errors.New("トレイを置くフォルダ（%LocalAppData%）が分からない")
	}
	exe := in.userTrayPath()
	stop, err := in.installTray(trayPlan{exe: exe, url: url, token: token, command: "tray"})
	if err != nil {
		in.restartTray(exe, url, stop)
		return err
	}
	in.launchTray(exe, url, stop)
	return nil
}

// trayUninstall は、tray install で入れたトレイを消す。打ったユーザーの HKCU の Run の登録は、%LocalAppData%\Programs\rajikura の
// トレイの exe を指すときだけ消す（removeTrayRun）。動いているトレイは、その exe のものだけを止めてから（service install の入れた
// トレイには触らない）、exe を消す。止まらなければ exe をよけ、メニューの「終了」で終えることを出す。exe が無ければ、そう出して
// 終える（エラーにしない）。tray.toml は残し、その場所と、トークンを Web UI で失効させることを出す。
func (in *installer) trayUninstall() error {
	if in.userProgramDir == "" {
		return errors.New("トレイを置くフォルダ（%LocalAppData%）が分からない")
	}
	exe := in.userTrayPath()
	in.removeTrayRun(exe)
	if _, err := os.Stat(exe); errors.Is(err, os.ErrNotExist) {
		removeStale(exe)
		fmt.Fprintf(in.out, "トレイ %s は入っていない\n", exe)
	} else {
		switch in.stopTrayAt(exe) {
		case trayStopped:
			fmt.Fprintln(in.out, "動いていたトレイを終了した")
		case trayStillRunning:
			fmt.Fprintf(in.out, "トレイが %d 秒待っても終わらなかった\n", int(trayStopTimeout/time.Second))
		}
		if in.removeTrayExe(exe, "tray", "トレイを止められなかったので") {
			_ = os.Remove(in.userProgramDir) // 空なら消える
		}
	}
	if in.trayConfigPath != "" {
		fmt.Fprintf(in.out, "トレイの接続先と API トークンの %s は残した（要らなければ手で消す）。連携した API トークンは、Web UI の「API トークン」の画面で失効させる\n", in.trayConfigPath)
	}
	return nil
}
