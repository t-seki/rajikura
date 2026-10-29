// rajikura は radiko タイムフリーの放送を録音してライブラリへ保存するサービスの入口。
//
//	rajikura [-data <フォルダ>] [-listen <アドレス>] [-allowed-hosts <名前,...>] serve [-reset-password] 常駐して番組表の同期と録音を回す。DB を開くのは serve だけ
//	rajikura [-url <URL>] [-token <トークン>] jobs [-state <状態>]   ジョブを一覧する
//	rajikura [-url <URL>] [-token <トークン>] retry <ID>...          retrying / expired / skipped（録らなかった）のジョブの次の試行をすぐに行わせる
//	rajikura [-url <URL>] [-token <トークン>] skip <ID>...           試行がまだ無い waiting のジョブを録らないにする
//	rajikura [-url <URL>] [-token <トークン>] record <ID>...         skipped のジョブを録るに戻す
//	rajikura [-url <URL>] [-token <トークン>] rerecord <ID>...       録音済み（done）のタイムフリーのジョブを録り直す（録音期限の内側）
//	rajikura [-url <URL>] [-token <トークン>] backup [-o <file>]     serve から DB のバックアップを受け取り、ファイルに書く
//	rajikura service install [-listen <アドレス>]   Windows のサービスとトレイを入れる・更新する（Windows だけ。管理者で動かす）
//	rajikura service uninstall                     Windows のサービスとトレイを消す（データのフォルダと tray.toml は残す）
//	rajikura tray install -url <URL> [-token <トークン>]   Windows のトレイだけを入れる・更新する（Windows だけ。管理者は要らない）
//	rajikura tray uninstall                        tray install で入れたトレイを消す（tray.toml は残す）
//	rajikura version                                 版を表示する
//	rajikura licenses                                含むソフトウェアのライセンスの一覧を出す（licenses.go）
//
// serve の起動の設定は、フラグ -data（データのフォルダ。DB・鍵のファイル・作業用フォルダと、Web UI のパスワードが決まるまでの間だけ setup-code.txt を置く。
// Windows のサービスとして動くときは、ログのフォルダ logs も置く。既定は実行ファイルと同じフォルダ）と -listen（既定は :8080）と
// -allowed-hosts（IP のアドレスと localhost のほかに、Web UI・API を名前で開くときの名前。カンマ区切り。既定は空。
// docs/adr/0021-web-ui-origin-host-and-login-limit.md）の 3 つだけ。serve の前にも後にも書ける（`rajikura -data /data serve` と
// `rajikura serve -data /data` は同じ）。設定（保存先・通知先など）は DB に置き、Web UI で変える
// （docs/adr/0015-settings-and-secrets-in-db.md）。0.x の -config は無くなったので、付けたら入れ方の docs を添えて止める。
//
// Web UI のパスワードは、初めて Web UI を開いた人が、serve が作ったセットアップコード（データのフォルダの
// setup-code.txt とログに出す）を入れて決める（docs/adr/0016-first-password-by-setup-code.md）。忘れたときは
// serve -reset-password で、パスワード・ログインのセッション・API トークンを消して、セットアップコードからやり直す。
//
// jobs・retry・skip・record・rerecord・backup は serve の HTTP API を呼ぶ（internal/client。docs/adr/0009-cli-over-http.md）。
// 接続先と API トークンは、フラグ → 環境変数（RAJIKURA_URL・RAJIKURA_TOKEN）→ 設定ファイル（os.UserConfigDir の下の
// rajikura/client.toml）の順に読む。書き方は docs/cli.md にある。
//
// service は Windows のサービス（仮想アカウント NT SERVICE\rajikura）として入れる・消す（docs/adr/0020-windows-service-with-virtual-account.md、
// 手順は docs/windows.md）。serve の HTTP API は呼ばず、-url・-token・-data は使わない。install は自分を %ProgramFiles%\rajikura に写し、
// データのフォルダ %ProgramData%\rajikura の ACL を絞って、-data・-listen を付けた serve をサービスとして登録する。
// install は、埋め込んだトレイ（cmd/rajikura-tray）も %ProgramFiles%\rajikura に書き出し、打ったユーザーの tray.toml に接続先を書いて、
// HKCU の Run に登録する。uninstall は、トレイの exe と、その exe を指す Run の登録も消す（tray_windows.go）。
//
// tray は、serve を Windows のサービスにしていない PC（serve を k3s などで動かしている人）に、埋め込んだトレイだけを入れる・消す
// （tray_command_windows.go）。service と同じく serve の HTTP API は呼ばない。install は、トレイを
// %LocalAppData%\Programs\rajikura に書き出し、-url（と -token）を打ったユーザーの tray.toml に書いて、HKCU の Run に登録し、
// その場で起動する。管理者は要らない。
//
// serve は、サービスとして起動されたかを自分で見分け、サービスなら停止と PC の終了の要求で止まり、ログをデータのフォルダの
// logs\rajikura.log に出す（大きさで回す）。
//
// 足したら直す: CLI のサブコマンド
// 足したら直す: service の操作
// 足したら直す: データのフォルダ
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/t-seki/rajikura/internal/client"
	"github.com/t-seki/rajikura/internal/config"
)

// version は版。リリースのビルドで -ldflags "-X main.version=..." により上書きする。
var version = "dev"

// userConfigDir は CLI の設定ファイル（rajikura/client.toml）を置くフォルダの親。テストで差し替える。
var userConfigDir = os.UserConfigDir

// bootHook は、serve に渡す起動の設定を書き換える（テストが偽の radiko のホストを差す。人が使う口は無い）。
var bootHook func(*config.Boot)

func main() {
	// SIGINT / SIGTERM（Windows では Ctrl+C）で ctx を取り消し、実行中の格納（ffmpeg、または builtin）と HTTP を止めて終わる。
	// Windows のサービスとして動くときの停止と PC の終了の要求は、serveHost（service_windows.go）が同じく ctx の取り消しにする。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// errUsage は使い方の誤り（終了コード 2）。
var errUsage = errors.New("使い方の誤り")

// configRemoved は -config を付けたときに出す案内。
const configRemoved = "-config は無くなった。データのフォルダは -data で渡し、設定は Web UI で決め直す" +
	"（docs/getting-started.md の「初めて入れるとき」）"

// clientCommand は serve の HTTP API を呼ぶサブコマンド。引数を確かめてから connect でクライアントを作る
// （使い方の誤りは、接続先とトークンが無くても先に知らせる）。
type clientCommand func(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error

// bootFlags は serve の起動の設定のフラグ（-data・-listen・-allowed-hosts と、無くなった -config）を fs に足す。
type bootFlags struct {
	data, listen, allowedHosts, config *string
}

// 足したら直す: データのフォルダ
// 足したら直す: 待ち受ける口
func addBootFlags(fs *flag.FlagSet) bootFlags {
	return bootFlags{
		data:         fs.String("data", "", "serve のデータのフォルダ（DB・鍵のファイル・作業用フォルダと、Web UI のパスワードが決まるまでの間だけ setup-code.txt を置く。Windows のサービスとして動くときは logs も。既定は実行ファイルと同じフォルダ）"),
		listen:       fs.String("listen", "", "serve が /healthz・/metrics・Web UI・API を待ち受けるアドレス（既定は "+config.DefaultListen+"）"),
		allowedHosts: fs.String("allowed-hosts", "", "serve の Web UI・API を名前で開くときの名前（カンマ区切り。IP のアドレスと localhost は書かなくても開ける）"),
		config:       fs.String("config", "", "無くなった（-data を使う）"),
	}
}

// run はサブコマンドを実行し、終了コードを返す。
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rajikura", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }
	boot := addBootFlags(fs)
	// 足したら直す: CLI のサブコマンド
	urlFlag := fs.String("url", "", "jobs・retry・skip・record・rerecord・backup がつなぐ serve の URL")
	tokenFlag := fs.String("token", "", "jobs・retry・skip・record・rerecord・backup が使う API トークン")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["config"] {
		fmt.Fprintln(stderr, "rajikura: "+configRemoved)
		return 2
	}
	if fs.NArg() == 0 {
		usage(stderr)
		return 2
	}

	cmd, rest := fs.Arg(0), fs.Args()[1:]
	var csub clientCommand
	// 足したら直す: CLI のサブコマンド
	// 足したら直す: service の操作
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "rajikura", version)
		return 0
	case "licenses":
		return runLicenses(rest, stdout, stderr)
	case "serve":
		// 足したら直す: CLI のサブコマンド
		if set["url"] || set["token"] {
			fmt.Fprintln(stderr, "rajikura serve: -url と -token は jobs・retry・skip・record・rerecord・backup のもの（serve は使わない）")
			return 2
		}
		return runServe(ctx, boot, set, rest, stdout, stderr)
	case "service":
		// service は serve に HTTP で話さず、-url・-token を使わない。-listen は service install の後に書く。
		// -allowed-hosts は service install に渡せない（LAN からは IP のアドレスで開く。docs/adr/0021-web-ui-origin-host-and-login-limit.md）。
		for _, name := range []string{"data", "listen", "allowed-hosts", "url", "token"} {
			if set[name] {
				fmt.Fprintf(stderr, "rajikura service: -%s は使わない（待ち受けるアドレスは service install -listen で渡す。-allowed-hosts は渡せない）\n", name)
				return 2
			}
		}
		return runServiceCommand(ctx, rest, stdout, stderr)
	case "tray":
		// tray も serve に HTTP で話さない。トレイの接続先とトークンは tray install の後に書く（-url・-token）。
		for _, name := range []string{"data", "listen", "allowed-hosts", "url", "token"} {
			if set[name] {
				fmt.Fprintf(stderr, "rajikura tray: -%s は tray の前に書かない（トレイの接続先と API トークンは tray install -url・-token で渡す）\n", name)
				return 2
			}
		}
		return runTrayCommand(ctx, rest, stdout, stderr)
	case "jobs":
		csub = listJobs
	case "retry":
		csub = retryJobs
	case "skip":
		csub = skipJobs
	case "record":
		csub = recordJobs
	case "rerecord":
		csub = rerecordJobs
	case "backup":
		csub = backupCommand
	default:
		fmt.Fprintf(stderr, "rajikura: 不明なサブコマンド %q\n", cmd)
		usage(stderr)
		return 2
	}

	if set["data"] || set["listen"] || set["allowed-hosts"] {
		fmt.Fprintf(stderr, "rajikura %s: -data と -listen と -allowed-hosts は serve だけが使う（%s は -url と -token で serve につなぐ）\n", cmd, cmd)
		return 2
	}
	connect := func() (*client.Client, error) {
		settings, warnings, err := client.Resolve(client.Sources{FlagURL: *urlFlag, FlagToken: *tokenFlag, ConfigDir: userConfigDir})
		for _, w := range warnings {
			fmt.Fprintf(stderr, "rajikura %s: 警告: %s\n", cmd, w)
		}
		if err != nil {
			return nil, err
		}
		return client.New(settings)
	}
	return exitCode(cmd, csub(ctx, connect, rest, stdout, stderr), stderr)
}

// runServe は serve の起動の設定（-data・-listen・-allowed-hosts。serve の前に書いたものと後に書いたもの）を決めて serve を動かす。
// 同じフラグを serve の前と後の両方に書いたら、使い方の誤りにする。
func runServe(ctx context.Context, global bootFlags, globalSet map[string]bool, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rajikura serve", flag.ContinueOnError)
	local := addBootFlags(fs)
	resetPassword := fs.Bool("reset-password", false, "Web UI のパスワード・ログインのセッション・API トークンを消して、セットアップコードからやり直す（起動は続ける）")
	if err := parseFlags(fs, args, stderr); err != nil {
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["config"] {
		fmt.Fprintln(stderr, "rajikura serve: "+configRemoved)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "使い方: rajikura serve [-data <フォルダ>] [-listen <アドレス>] [-allowed-hosts <名前,...>] [-reset-password]")
		return 2
	}
	pick := func(name string, g, l *string) (string, bool) {
		if globalSet[name] && set[name] {
			fmt.Fprintf(stderr, "rajikura serve: -%s を serve の前と後の両方に書いている（どちらか一方にする）\n", name)
			return "", false
		}
		if set[name] {
			return *l, true
		}
		return *g, true
	}
	data, ok := pick("data", global.data, local.data)
	if !ok {
		return 2
	}
	listen, ok := pick("listen", global.listen, local.listen)
	if !ok {
		return 2
	}
	hostsFlag, ok := pick("allowed-hosts", global.allowedHosts, local.allowedHosts)
	if !ok {
		return 2
	}
	allowedHosts, err := parseAllowedHosts(hostsFlag)
	if err != nil {
		fmt.Fprintf(stderr, "rajikura serve: -allowed-hosts: %v\n", err)
		return 2
	}
	if data == "" {
		d, err := config.DefaultDataDir()
		if err != nil {
			fmt.Fprintf(stderr, "rajikura serve: %v\n", err)
			return 1
		}
		data = d
	}
	if listen == "" {
		listen = config.DefaultListen
	}
	boot := config.Boot{DataDir: data, Listen: listen, AllowedHosts: allowedHosts}
	if bootHook != nil {
		bootHook(&boot)
	}
	return exitCode("serve", serveHost(ctx, boot, serveOptions{resetPassword: *resetPassword}, stdout, stderr), stderr)
}

// parseAllowedHosts は -allowed-hosts の値（カンマで区切った名前の並び）を読む。空白と空の要素は除く。
// ポート・スキーム・パスを書いた名前は Host と合わない（Host の検査はポートを見ない）ので、誤りにする。
func parseAllowedHosts(s string) ([]string, error) {
	var hosts []string
	for _, h := range strings.Split(s, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if strings.ContainsAny(h, ":/ ") {
			return nil, fmt.Errorf("%q は名前だけを書く（ポート・スキーム・パスは書かない）", h)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// exitCode はサブコマンドのエラーを終了コードにする（使い方の誤りは 2、ほかのエラーは 1）。
func exitCode(cmd string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errUsage) {
		return 2
	}
	fmt.Fprintf(stderr, "rajikura %s: %v\n", cmd, err)
	return 1
}

// parseFlags はサブコマンドのフラグを読む。誤りなら errUsage を返す（メッセージは fs が出す）。
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	return nil
}

// 足したら直す: CLI のサブコマンド
// 足したら直す: service の操作
// 足したら直す: データのフォルダ
func usage(w io.Writer) {
	fmt.Fprintln(w, "使い方: rajikura [-data <フォルダ>] [-listen <アドレス>] [-allowed-hosts <名前,...>] serve [-reset-password]")
	fmt.Fprintln(w, "        rajikura [-url <URL>] [-token <トークン>] <jobs | retry | skip | record | rerecord | backup> ...")
	fmt.Fprintln(w, "        rajikura service <install [-listen <アドレス>] | uninstall>")
	fmt.Fprintln(w, "        rajikura tray <install -url <URL> [-token <トークン>] | uninstall>")
	fmt.Fprintln(w, "        rajikura version")
	fmt.Fprintln(w, "        rajikura licenses")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "サブコマンド:")
	fmt.Fprintln(w, "  serve                 常駐して番組表の同期と録音を回す（DB を開くのは serve だけ）")
	fmt.Fprintln(w, "                        -reset-password: Web UI のパスワード・ログインのセッション・API トークンを消して、セットアップコードからやり直す")
	fmt.Fprintln(w, "  jobs [-state 状態]    ジョブを一覧する（状態はカンマ区切りで複数指定できる）")
	fmt.Fprintln(w, "  retry ID...           retrying / expired / skipped（録らなかった）のジョブの次の試行をすぐに行わせる（serve がすぐ拾う）")
	fmt.Fprintln(w, "  skip ID...            試行がまだ無い waiting のジョブを録らないにする（放送の前なら録らない予定、後なら録らなかった）")
	fmt.Fprintln(w, "  record ID...          skipped のジョブを録るに戻す（録らない予定は録音予定に、録らなかったは手動再試行と同じく retrying に）")
	fmt.Fprintln(w, "  rerecord ID...        done のタイムフリーのジョブを録り直す（録音期限の内側だけ。前の保存先と同じ名前なら上書きする）")
	fmt.Fprintln(w, "  backup [-o FILE]      DB のバックアップを受け取ってファイルに書く（既定は今のフォルダの rajikura-<日時>.db。秘密の鍵は含まない）")
	fmt.Fprintln(w, "  service install       Windows のサービスとトレイを入れる・更新する（Windows だけ。管理者で動かす。-listen の既定は 127.0.0.1:8080）")
	fmt.Fprintln(w, "  service uninstall     Windows のサービスとトレイを消す（データのフォルダと tray.toml は残す）")
	fmt.Fprintln(w, "  tray install          Windows のトレイだけを入れる・更新する（Windows だけ。管理者は要らない。-url は必須、-token は任意）")
	fmt.Fprintln(w, "  tray uninstall        tray install で入れたトレイを消す（tray.toml は残す）")
	fmt.Fprintln(w, "  version               版を表示する")
	fmt.Fprintln(w, "  licenses              含むソフトウェアのライセンスの一覧を出す（一覧はリリースのビルドにだけ入る）")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "-data は serve のデータのフォルダ（DB・鍵のファイル・作業用フォルダと、Web UI のパスワードが決まるまでの間だけ setup-code.txt を置く。")
	fmt.Fprintln(w, "Windows のサービスとして動くときは、ログのフォルダ logs も置く）。既定は実行ファイルと同じフォルダ。")
	fmt.Fprintf(w, "-listen は serve が待ち受けるアドレス。既定は %s。\n", config.DefaultListen)
	fmt.Fprintln(w, "-allowed-hosts は、Web UI・API を名前で開くときの名前（カンマ区切り）。IP のアドレスと localhost は書かなくても開ける。")
	fmt.Fprintln(w, "設定（保存先・通知先など）は Web UI で変える。")
	fmt.Fprintf(w, "jobs・retry・skip・record・rerecord・backup は serve の HTTP API を呼ぶ。接続先と API トークンは、-url・-token → 環境変数 %s・%s →\n", client.EnvURL, client.EnvToken)
	fmt.Fprintln(w, "設定ファイル（Windows は %AppData%\\rajikura\\client.toml、Linux は ~/.config/rajikura/client.toml。キーは url と token）の順に読む。")
}
