#!/usr/bin/env bash
# scripts/check-go-comment-refs.sh の例文テスト。テスト用の置き場を GO_COMMENT_REFS_ROOT で渡し、
# 1 つの Go のファイルの中身ごとに「当たる（失敗する）」「当たらない（通る）」を確かめる。
# 各例で、スクリプトの stderr に perl の警告（at -e line を含む行など）が出たら落とす。
# 使い方: scripts/check-go-comment-refs_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/code/internal/store" "$tmp/code/cmd/app"
: > "$tmp/code/internal/store/jobs.go"
: > "$tmp/code/internal/store/jobs_windows.go"
: > "$tmp/code/cmd/app/main.go"

fail=0
passed=0
failed=0
check() { # check <hit|miss> <store/x.go の中身> [hit のとき、出力に含まれるはずの文字列]
  local want=$1 body=$2 expect=${3:-} rc=0 out
  printf '%s\n' "$body" > "$tmp/code/internal/store/x.go"
  out=$(GO_COMMENT_REFS_ROOT="$tmp/code" "$here/check-go-comment-refs.sh" 2>"$tmp/stderr") || rc=$?
  out="$out$(cat "$tmp/stderr")"
  if grep -qE 'at -e line|Use of uninitialized' "$tmp/stderr"; then
    echo "FAIL: perl の警告が stderr に出た（$(head -n1 "$tmp/stderr")）: $body"; fail=1; failed=$((failed + 1)); return 0
  fi
  if [ "$want" = hit ] && [ "$rc" -ne 1 ]; then
    echo "FAIL: 当たるはずが rc=$rc: $body"; fail=1
  elif [ "$want" = miss ] && [ "$rc" -ne 0 ]; then
    echo "FAIL: 当たらないはずが rc=$rc: $body ($out)"; fail=1
  elif [ -n "$expect" ] && [[ "$out" != *"$expect"* ]]; then
    echo "FAIL: 出力に「$expect」が無い: $body"; fail=1
  else
    echo "ok: ${body//$'\n'/⏎}"; passed=$((passed + 1)); return 0
  fi
  failed=$((failed + 1))
}

# 同じディレクトリのファイル名
check miss '// jobs.go を見る'
check miss '// `jobs_windows.go` を見る'
check hit  '// gone.go を見る' 'store/x.go:1: 実在しないファイルの名指し: gone.go'
check hit  '// `main.go` を見る' 'main.go'
check hit  $'package store\n\nfunc f() {} // gone.go を見る' 'x.go:3'
# internal・cmd で始まるパス
check miss '// internal/store/jobs.go を見る'
check miss '// cmd/app/main.go を見る'
check hit  '// internal/store/gone.go を見る' 'internal/store/gone.go'
check hit  '// cmd/app/gone.go を見る' 'cmd/app/gone.go'
# 対象外: ほかのパス、パターン、断片、URL
check miss '// web/src/gone.go を見る'
check miss '// *_test.go と *.go'
check miss '// {a,b}.go'
check miss '// _test.go の断片'
check miss '// http://example.com/gone.go'
check miss '// jobs.gone.go.bak'
# コメントでない行は読まない
check miss $'var s = "gone.go"\nvar t = `gone.go`'
check miss '/* gone.go */'
# 免除の印: 書いた名指しだけを外す
check miss '// gone.go を見る code-ref-ok: gone.go'
check miss '// gone.go と other.go code-ref-ok: gone.go、other.go'
check hit  '// gone.go と other.go code-ref-ok: gone.go' 'other.go'
check hit  '// jobs.go code-ref-ok: jobs.go' 'この行で当たらない'
check hit  '// 何もない code-ref-ok: gone.go' 'この行で当たらない'
check hit  '// code-ref-ok:' '名指しを書かない'
# 複数ファイルのうち 1 つが落ちれば失敗する
printf '%s\n' '// jobs.go' > "$tmp/code/internal/store/y.go"
check hit  '// gone.go' 'x.go:1'

# 日本語が空白なしで直前・直後に付いても、名前だけを拾う
check hit  '// gone.goを見る' '名指し: gone.go'
check hit  '// のgone.go' '名指し: gone.go'
check hit  '// 「gone.go」を見る' '名指し: gone.go'
# .（英字だけでない名前も）で始まるディレクトリと node_modules の配下は読まない。vendor は読む
for d in .hidden .v2 .dir-1 node_modules; do
  mkdir -p "$tmp/code/$d"; printf '%s\n' '// gone.go' > "$tmp/code/$d/z.go"
done
check miss '// jobs.go'
rm -rf "$tmp/code/.hidden" "$tmp/code/.v2" "$tmp/code/.dir-1" "$tmp/code/node_modules"
mkdir -p "$tmp/code/vendor"; printf '%s\n' '// gone.go' > "$tmp/code/vendor/z.go"
check hit  '// jobs.go' 'vendor/z.go:1'
rm -rf "$tmp/code/vendor"
check miss '// jobs.goを見る'
check miss '// のjobs.go'
check miss '// 「jobs.go」と「jobs_windows.go」'
# CRLF の行も LF と同じ結果になる
check hit  $'// gone.go\r' 'gone.go'
check miss $'// jobs.go\r'
check miss $'// gone.go code-ref-ok: gone.go\r'
check hit  $'// jobs.go code-ref-ok: jobs.go\r' 'この行で当たらない'
# 対象の .go が 1 つも無ければ落とす
rm -rf "$tmp/empty"; mkdir -p "$tmp/empty"
rc=0; out=$(GO_COMMENT_REFS_ROOT="$tmp/empty" "$here/check-go-comment-refs.sh" 2>&1) || rc=$?
if [ "$rc" -eq 1 ] && [[ "$out" == *"1 つも無い"* ]]; then
  echo "ok: .go のファイルが 0 件"; passed=$((passed + 1))
else
  echo "FAIL: .go のファイルが 0 件で rc=$rc ($out)"; fail=1; failed=$((failed + 1))
fi

echo "通った: $passed 件、失敗: $failed 件"
if [ "$fail" -ne 0 ]; then exit 1; fi
