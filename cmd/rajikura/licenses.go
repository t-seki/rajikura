package main

import (
	"embed"
	"fmt"
	"io"
	"io/fs"

	"github.com/t-seki/rajikura/web"
)

// rajikura licenses は、バイナリが含むソフトウェアのライセンスの一覧を標準出力に出す。version と同じく serve の API を呼ばず、DB にも触らない。
//
// 一覧は 2 つの部分からなり、どちらもビルドのたびに作って埋め込む（repo には置かない）。
//   - Go の分（licenses/licenses.txt）: rajikura 自身の LICENSE、Go 本体の LICENSE と PATENTS、依存の module ごとのライセンスの文と NOTICE。
//     Dockerfile の build と build-windows の段が、バイナリをビルドする前に scripts/gen-licenses.sh で作る（数える範囲は OS ごとのビルドの対象と同じ）
//   - Web UI の分（web/dist/.vite/license.md）: Vite の build.license の出力（web/vite.config.ts）。go:embed all:dist でバイナリに入る
//
// licenses には .gitkeep しか git に入れないので、一覧を作らずにビルドしても go:embed が通る（手元の go build・go test は一覧なしで通る）。
// そのときは、このビルドに一覧が入っていないことと、Release の配布物とコンテナイメージなら一覧を出せることを出して、終了コード 0 で終える。
//
//go:embed all:licenses
var licensesFS embed.FS

// goLicensesFile は、licensesFS の中の Go の分の一覧のパス（scripts/gen-licenses.sh の出力先。Dockerfile が渡す）。
const goLicensesFile = "licenses/licenses.txt"

// webLicensesFile は、Web UI の SPA の根（web.FS）から見た、Vite の build.license の出力のパス。
const webLicensesFile = ".vite/license.md"

// noLicenses は、Go の分の一覧が無いビルドの licenses が出す案内。
const noLicenses = "このビルドには、含むソフトウェアのライセンスの一覧が入っていない（一覧はリリースのビルドで作って埋め込む）。\n" +
	"Release の配布物（Windows・Linux のバイナリ）とコンテナイメージの rajikura licenses なら、一覧を出せる。"

// noWebLicenses は、Web UI の分が無いビルドの licenses が、Go の分の後に出す案内。
const noWebLicenses = "このビルドには、Web UI の依存のライセンスの一覧が入っていない（Web UI をビルドせずにビルドした）。"

// runLicenses は licenses を実行する。
func runLicenses(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "使い方: rajikura licenses")
		return 2
	}
	if err := writeLicenses(stdout, licensesFS, web.FS()); err != nil {
		fmt.Fprintf(stderr, "rajikura licenses: %v\n", err)
		return 1
	}
	return 0
}

// writeLicenses は、Go の分（goFS の goLicensesFile）と Web UI の分（webFS の webLicensesFile）を、この順に w に書く。
// Go の分が無ければ noLicenses だけを書く。Web UI の分が無ければ、Go の分の後に noWebLicenses を書く。
func writeLicenses(w io.Writer, goFS, webFS fs.FS) error {
	goPart, err := fs.ReadFile(goFS, goLicensesFile)
	if err != nil || len(goPart) == 0 {
		_, err := fmt.Fprintln(w, noLicenses)
		return err
	}
	if _, err := w.Write(goPart); err != nil {
		return err
	}
	webPart, err := fs.ReadFile(webFS, webLicensesFile)
	if err != nil || len(webPart) == 0 {
		_, err := fmt.Fprintln(w, noWebLicenses)
		return err
	}
	if _, err := fmt.Fprintf(w, "==> Web UI\n\n"); err != nil {
		return err
	}
	_, err = w.Write(webPart)
	return err
}
