#!/usr/bin/env bash
# scripts/check-user-docs.sh の例文テスト。1 行ずつ「当たる（失敗する）」「当たらない（通る）」を確かめる。
# 各例で、スクリプトの stderr に perl の警告（at -e line を含む行など）が出たら落とす。
# 使い方: scripts/check-user-docs_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail=0
passed=0
failed=0
check() { # check <hit|miss> <行> [hit のとき、出力に含まれるはずの文字列]
  local want=$1 line=$2 expect=${3:-} rc=0 out
  printf '%s\n' "$line" > "$tmp/doc.md"
  out=$("$here/check-user-docs.sh" "$tmp/doc.md" 2>"$tmp/stderr") || rc=$?
  out="$out$(cat "$tmp/stderr")"
  if grep -qE 'at -e line|Use of uninitialized' "$tmp/stderr"; then
    echo "FAIL: perl の警告が stderr に出た（$(head -n1 "$tmp/stderr")）: $line"; fail=1; failed=$((failed + 1)); return 0
  fi
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

# 当たる
check hit  '画面は `GET /api/guide/areas` を叩く' '/api/guide/areas'
check hit  'POST /api/shows/{name}/onces を呼ぶ' '/api/shows/{name}/onces'
check hit  $'```\ncurl "$URL"/api/jobs\n```' '/api/jobs'
check hit  '[API](/api/keywords) を見る' '/api/keywords'
check hit  '`/api/a` と `/api/b`' '/api/b'
# 当たらない
check miss '`/api` の中継先を Vite に向ける'
check miss '[API の一覧](api.md) と docs/api.md'
check miss '名前に api を含む /apis や /graphapi/x'
check miss 'API を呼ぶ人は別の docs を読む'
# 外の URL の中の /api/（前にホスト名が付く）は当てない
check miss '`https://discord.com/api/webhooks/123/abc` を貼る'
check miss 'http://example.com/api/x'
check hit  "'<serve の URL>/api/backup'" '/api/backup'
# 日本語に空白なしで続く形は当てる（ホスト名ではない）
check hit  'その下の/api/jobs を呼ぶ' '/api/jobs'
check hit  'APIは/api/settingsです' '/api/settings'
# シェル変数に続く形は、ホスト名と同じに扱って当てない
check miss 'curl $URL/api/jobs'
# 免除の印は無い（書いても当たる行は当たる）
check hit  '`/api/jobs` <!-- api-path-ok: /api/jobs -->' '/api/jobs'

echo "通った: $passed 件、失敗: $failed 件"
if [ "$fail" -ne 0 ]; then exit 1; fi
