#!/usr/bin/env bash
# scripts/check-fix-markers.sh の例文テスト。例ごとに一時ディレクトリに小さな git の repo を作り、FIX_MARKERS_ROOT で渡して、
# 「通る」「落ちる」を確かめる。各例で、スクリプトの出力に perl の警告（at -e line を含む行など）が出たら落とす。
# 使い方: scripts/check-fix-markers_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
r="$tmp/r"

passed=0
failed=0

# setup は空の repo を作り、項が 2 つ（「項 A」と、字下げした箇条の「項 B」）の列挙を持つ docs/development.md を置く。
setup() {
  rm -rf "$r"
  mkdir -p "$r/docs"
  git -C "$r" init -q
  # 作業ツリーの改行を書き換えない（Windows の既定の core.autocrlf=true でも、CRLF の例をそのまま読ませる）
  git -C "$r" config core.autocrlf false
  cat > "$r/docs/development.md" <<'M'
# dev

## 足したら直す列挙

段落の説明の印 `足したら直す: <項の名前>` は項の名前に数えない。

- A を足す: 印 `足したら直す: 項 A` を repo 全体で grep する。
  - B を足す: 印 `足したら直す: 項 B` を repo 全体で grep する。

## 次の節

本文。
M
  git -C "$r" add docs/development.md
}

# put <パス> <中身> は、ファイルを書いて git に追わせる。
put() {
  mkdir -p "$(dirname "$r/$1")"
  printf '%s\n' "$2" > "$r/$1"
  git -C "$r" add "$1"
}

# check <ok|fail> <名前> [出力に含まれるはずの文字列] は、今の repo でスクリプトを流して確かめる。
check() {
  local want=$1 name=$2 expect=${3:-} rc=0 out
  out=$(FIX_MARKERS_ROOT="$r" "$here/check-fix-markers.sh" 2>&1) || rc=$?
  if grep -qE 'at -e line|Use of uninitialized' <<<"$out"; then
    echo "FAIL: perl の警告が出た（$(head -n1 <<<"$out")）: $name"
  elif [ "$want" = ok ] && [ "$rc" -ne 0 ]; then
    echo "FAIL: 通るはずが rc=$rc（$out）: $name"
  elif [ "$want" = fail ] && [ "$rc" -ne 1 ]; then
    echo "FAIL: 落ちるはずが rc=$rc（$out）: $name"
  elif [ -n "$expect" ] && [[ "$out" != *"$expect"* ]]; then
    echo "FAIL: 出力に「$expect」が無い（$out）: $name"
  else
    echo "ok: $name"
    passed=$((passed + 1))
    return 0
  fi
  failed=$((failed + 1))
}

setup
put a.go '// 足したら直す: 項 A'
put Dockerfile '# 足したら直す: 項 B'
check ok 'Go の // と Dockerfile の # の印で、項ごとに 1 つずつあれば通る' '項 A: 1'

setup
put web/x.ts '  // 足したら直す: 項 A'
put docs/x.md '文。 <!-- 足したら直す: 項 A --> <!-- 足したら直す: 項 B --> <!-- glossary-ok: 回 -->'
check ok 'md の行末に印が 2 つと glossary-ok があれば、印を 2 つとも読む' '項 A: 2'

setup
put a.go '// 足したら直す: 項 A'
check fail '列挙の節の中の箇条は数えないので、項 B の印が無ければ落ちる' '項「項 B」の印が 1 つも無い'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す: 項 B'
put c.go '// 足したら直す: 項 C'
check fail '列挙に無い名前の印があれば、ファイルと行を出して落ちる' 'c.go:1: 列挙に無い項の名前の印: 「項 C」'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す: 項Ｂ'
check fail '打ち間違えた名前の印は数えず落ちる' '列挙に無い項の名前の印: 「項Ｂ」'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す:'
put c.go '// 足したら直す: 項 B'
check fail '名前が空の印は落ちる' 'b.go:1: 列挙に無い項の名前の印: 「」'

setup
put a.go '// 足したら直す: 項 A'
printf '%s\n' '| 行 | 中身 <!-- 足したら直す: 項 B --> |' >> "$r/docs/development.md"
git -C "$r" add docs/development.md
check ok 'development.md のほかの節（列挙の節の後）の印は数える' '項 B: 1'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す: 項 B'
put docs/adr/0001-x.md '古い <!-- 足したら直す: 項 Z -->'
check ok 'docs/adr/ の印は読まない（列挙に無い名前でも落ちない）'

setup
put a.go '// 足したら直す: 項 A'
put docs/adr/0001-x.md '古い <!-- 足したら直す: 項 B -->'
check fail 'docs/adr/ の印は数えないので、そこにしか無い項は落ちる' '項「項 B」の印が 1 つも無い'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す: 項 B'
printf '%s\n' '// 足したら直す: 項 Z' > "$r/untracked.go"
check ok 'git が追わないファイルは読まない'

setup
put a.go '// 足したら直す: 項 A'
printf '// 足したら直す: 項 B\r\nfunc f() {}\r\n' > "$r/b.go"
git -C "$r" add b.go
check ok 'CRLF の行の印も名前を読める' '項 B: 1'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す: 項 B'
put scripts/check-fix-markers.sh '# 足したら直す: <名前>'
put scripts/check-fix-markers_test.sh '# 足したら直す: 項 Z'
check ok '検査のスクリプトと例文テストは読まない'

setup
put a.go '// 足したら直す: 項 A'
put b.go '// 足したら直す: 項 B'
printf '\0\0足したら直す: 項 Z\n' > "$r/bin.dat"
git -C "$r" add bin.dat
check ok 'バイナリのファイルは読まない'

setup
put b.go '// 足したら直す: 項 B'
put a.go '// 1 文目。
// 足したら直す: 項 A
// 2 文目。
package a'
check fail 'Go のコメントのブロックの途中に挟んだ印は、ファイルと行を出して落ちる' 'a.go:2: 印がコメントのブロックの途中にある'

setup
put b.go '// 足したら直す: 項 B'
put a.ts '/** x */
  // 本文。
  // 足したら直す: 項 A
  // 続き。
export {}'
check fail 'TS の字下げしたコメントのブロックの途中に挟んだ印も落ちる' 'a.ts:3: 印がコメントのブロックの途中にある'

setup
put a.go '// 本文。
// 足したら直す: 項 A
// 足したら直す: 項 B
// 続き。
package a'
check fail '印が続けて並んだ後に文字のある行が続けば落ちる' 'a.go:2: 印がコメントのブロックの途中にある'

setup
put a.go '// 本文。
// 足したら直す: 項 A
// 足したら直す: 項 B
package a'
check ok 'ブロックの最後に印が並ぶのは通る' '項 B: 1'

setup
put a.go '// 本文。
//
// 足したら直す: 項 A
//
// 続きの段落。
func f() {}
// 足したら直す: 項 B
func g() {}'
check ok '// だけの行で区切った段落の印と、コードの直前の印は通る' '項 A: 1'

setup
put b.go '// 足したら直す: 項 B'
printf '// 本文。\r\n// 足したら直す: 項 A\r\n// 続き。\r\npackage a\r\n' > "$r/a.go"
git -C "$r" add a.go
check fail 'CRLF のコメントのブロックの途中に挟んだ印も落ちる' 'a.go:2: 印がコメントのブロックの途中にある'

setup
put b.go '// 足したら直す: 項 B'
put a.md '本文 <!-- 足したら直す: 項 A -->
// 続き'
check ok 'md の印は、次の行が // でも対象外' '項 A: 1'

setup
cat > "$r/docs/development.md" <<'M'
# dev

## 足したら直す列挙

- A を足す: a.go を直す。
M
git -C "$r" add docs/development.md
put a.go '// 足したら直す: 項 A'
check fail '列挙の箇条から項の名前が読めなければ落ちる' '項の名前が 1 つも読めない'

setup
printf '# dev\n' > "$r/docs/development.md"
git -C "$r" add docs/development.md
check fail '列挙の節が無ければ落ちる' '節が無い'

echo "通った: $passed 件、失敗: $failed 件"
[ "$failed" -eq 0 ]
