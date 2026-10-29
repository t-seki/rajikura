#!/bin/sh
# rajikura のバイナリに埋め込む、Go の分のライセンスの一覧を作る（rajikura licenses が出す。cmd/rajikura/licenses.go）。
# Dockerfile の build と build-windows の段（golang の alpine。busybox の sh）で、バイナリをビルドする前に流す。
# 一覧は repo に置かない（出力先の cmd/rajikura/licenses/ は .gitkeep だけを git に入れる）。手元の go build は一覧なしで通る。
#
# 使い方: GOOS=<OS> GOARCH=<arch> CGO_ENABLED=0 sh scripts/gen-licenses.sh <出力のファイル> <パッケージ>...
#   数える範囲は、その OS でビルドする対象と揃える（Linux は ./cmd/rajikura、Windows はトレイも埋め込むので ./cmd/rajikura ./cmd/rajikura-tray）。
#   repo の根で流す（rajikura 自身の LICENSE を ./LICENSE から読む）。依存の module は module cache にあること（go mod download の後）。
#
# 一覧の並び:
#   1. rajikura 自身の LICENSE
#   2. Go 本体（GOROOT）の LICENSE と PATENTS
#   3. go list -deps で数えた module（rajikura 自身を除く）を module のパスの順に。module ごとに、パスと版の見出しと、
#      module の根にある LICENSE*・LICENCE*・COPYING*・NOTICE*・PATENTS*・AUTHORS*（大文字小文字を問わない）の全文
# Web UI の分（web/dist/.vite/license.md）は、rajikura licenses がこの一覧の後に出す（ここでは扱わない）。
# 一覧の文に cmd/rajikura-tray のパスを書かない（release.yml がトレイの埋め込みをその文字列で確かめるため）。
set -eu

if [ "$#" -lt 2 ]; then
  echo "使い方: sh scripts/gen-licenses.sh <出力のファイル> <パッケージ>..." >&2
  exit 2
fi
out=$1
shift

if [ ! -f LICENSE ]; then
  echo "gen-licenses.sh: ./LICENSE が無い（repo の根で流す）" >&2
  exit 1
fi

goroot=$(go env GOROOT)
goversion=$(go env GOVERSION)
goos=$(go env GOOS)
goarch=$(go env GOARCH)

tmp=$(mktemp)
mods=$(mktemp)
listed=$(mktemp)
trap 'rm -f "$tmp" "$mods" "$listed"' EXIT

# 1 つのファイルを、見出しを付けて書く
emit_file() {
  printf '%s %s\n\n' '---' "$2" >>"$tmp"
  cat "$1" >>"$tmp"
  # 最後に改行の無いファイルでも、次の見出しが行の頭から始まるようにする
  printf '\n\n' >>"$tmp"
}

{
  printf 'rajikura が含むソフトウェアのライセンス（%s/%s）\n\n' "$goos" "$goarch"
  printf '==> rajikura\n\n'
} >"$tmp"
emit_file LICENSE LICENSE

printf '==> Go %s\n\n' "$goversion" >>"$tmp"
for name in LICENSE PATENTS; do
  if [ ! -f "$goroot/$name" ]; then
    echo "gen-licenses.sh: $goroot/$name が無い" >&2
    exit 1
  fi
  emit_file "$goroot/$name" "$name"
done

# module のパス・版・置き場所（rajikura 自身と標準ライブラリは除く）。
# busybox の sh には pipefail が無く、パイプでつなぐと go list の失敗を拾えない（module の欠けた一覧のまま通りうる）ので、
# いったんファイルに書いて終了コードを見てから並べる。
if ! go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' "$@" >"$listed"; then
  echo "gen-licenses.sh: go list -deps が失敗した（上の go list のエラーを見る）" >&2
  exit 1
fi
LC_ALL=C sort -u "$listed" >"$mods"

if [ ! -s "$mods" ]; then
  echo "gen-licenses.sh: 依存の module が 1 つも数えられなかった" >&2
  exit 1
fi

while read -r path version dir; do
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    echo "gen-licenses.sh: $path $version の置き場所が無い（go mod download の後に流す）" >&2
    exit 1
  fi
  printf '==> %s %s\n\n' "$path" "$version" >>"$tmp"
  found=0
  for f in "$dir"/*; do
    [ -f "$f" ] || continue
    name=${f##*/}
    # 大文字小文字を問わない（ファイルごとに tr を起こさず、case の文字の組で書く）
    case $name in
      [Ll][Ii][Cc][Ee][Nn][Ss][Ee]* | [Ll][Ii][Cc][Ee][Nn][Cc][Ee]* | [Cc][Oo][Pp][Yy][Ii][Nn][Gg]* | \
        [Nn][Oo][Tt][Ii][Cc][Ee]* | [Pp][Aa][Tt][Ee][Nn][Tt][Ss]* | [Aa][Uu][Tt][Hh][Oo][Rr][Ss]*)
        emit_file "$f" "$name"
        found=1
        ;;
    esac
  done
  if [ "$found" = 0 ]; then
    echo "gen-licenses.sh: $path $version にライセンスのファイルが無い" >&2
    exit 1
  fi
done <"$mods"

mkdir -p "$(dirname "$out")"
mv "$tmp" "$out"
echo "gen-licenses.sh: $(wc -l <"$mods" | tr -d ' ') 個の module を $out に書いた" >&2
