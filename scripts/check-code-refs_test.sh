#!/usr/bin/env bash
# scripts/check-code-refs.sh の例文テスト。テスト用のコードの置き場を CODE_REFS_ROOT で渡し、
# 1 行ずつ「当たる（失敗する）」「当たらない（通る）」を確かめる。
# 各例で、スクリプトの stderr に perl の警告（at -e line を含む行など）が出たら落とす。
# 使い方: scripts/check-code-refs_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/code/internal/store" "$tmp/code/cmd/app"
: > "$tmp/code/internal/store/jobs.go"
: > "$tmp/code/cmd/app/main.go"
cat > "$tmp/code/internal/store/store.go" <<'G'
package store

type Store struct{}

func (s *Store) ListJobs(ctx context.Context) error { return nil }

func (Store) Close() {}

type Airing struct {
	// Title は番組名。
	Title string
	A, B  int
}
G

fail=0
passed=0
failed=0
# stderr（$tmp/stderr）に perl の警告が出ていたら、落ちた例として数えて 0 を返す（呼び出し側は return 0 で抜ける）
perl_warned() {
  grep -qE 'at -e line|Use of uninitialized' "$tmp/stderr" || return 1
  echo "FAIL: perl の警告が stderr に出た（$(head -n1 "$tmp/stderr")）: $1"; fail=1; failed=$((failed + 1))
}
check() { # check <hit|miss> <行> [hit のとき、出力に含まれるはずの文字列]
  local want=$1 line=$2 expect=${3:-} rc=0 out
  printf '%s\n' "$line" > "$tmp/doc.md"
  out=$(CODE_REFS_ROOT="$tmp/code" "$here/check-code-refs.sh" "$tmp/doc.md" 2>"$tmp/stderr") || rc=$?
  out="$out$(cat "$tmp/stderr")"
  if perl_warned "$line"; then return 0; fi
  if [ "$want" = hit ] && [ "$rc" -ne 1 ]; then
    echo "FAIL: 当たるはずが rc=$rc: $line"; fail=1
  elif [ "$want" = miss ] && [ "$rc" -ne 0 ]; then
    echo "FAIL: 当たらないはずが rc=$rc: $line"; fail=1
  elif [ -n "$expect" ] && [[ "$out" != *"$expect"* ]]; then
    echo "FAIL: 出力に「$expect」が無い: $line"; fail=1
  else
    echo "ok: ${line//$'\n'/⏎}"; passed=$((passed + 1)); return 0
  fi
  failed=$((failed + 1))
}

# パス
check miss '`internal/store/jobs.go` を読む'
check miss '`internal/store/` と `cmd/app`'
check miss '`internal/store/jobs.go:12`'
check hit  '`internal/store/gone.go` を読む' 'internal/store/gone.go'
check hit  '`internal/store/jobs.go:12` と `cmd/gone/`' 'cmd/gone/'
check hit  '`cmd/app/other.go:3`'
# パターンや省略は対象外
check miss '`internal/...`'
check miss '`internal/…` と `cmd/…`'
check miss '`internal/*/x.go`'
check miss '`internal/{a,b}`'
# internal / cmd で始まらない名指しは対象外
check miss '`docs/gone.md`'
check miss '`web/src/gone.ts`'
# 型.メソッド
check miss '`Store.ListJobs`'
check miss '`*Store.ListJobs`'
check miss '`Store.ListJobs()`'
check miss '`Store.Close`'
check hit  '`Store.LatestAttempts` が消えた' 'Store.LatestAttempts'
check hit  '`Store.ListJobs` と `Store.Gone()`' 'Store.Gone()'
check hit  '`Gone.ListJobs`'
# 構造体のフィールド
check miss '`Airing.Title`'
check miss '`Airing.B`'
check hit  '`Airing.Missing`'
# 型の付かない名前、小文字で始まる名前は対象外
check miss '`ListJobs`'
check miss '`time.Now`'
check miss '`store.ListJobs`'
check miss '`Node.js`'
# バッククォートの外とコードフェンスの中は読まない
check miss 'Store.Gone と internal/gone.go'
check miss $'```\n`internal/gone.go`\n```'
# 免除の印: 書いた名指しだけを外す
check miss '`Store.Gone` <!-- code-ref-ok: Store.Gone -->'
check miss '`Store.Gone` と `internal/gone.go` <!-- code-ref-ok: Store.Gone、internal/gone.go -->'
check hit  '`Store.Gone` と `internal/gone.go` <!-- code-ref-ok: Store.Gone -->' 'internal/gone.go'
check hit  '`Store.ListJobs` <!-- code-ref-ok: Store.ListJobs -->' 'この行で当たらない'
check hit  '`Store.Gone` <!-- code-ref-ok -->' '名指しを書かない'
check hit  '`Store.Gone` <!-- code-ref-ok: -->' '名指しを書かない'

echo "通った: $passed 件、失敗: $failed 件"
if [ "$fail" -ne 0 ]; then exit 1; fi
