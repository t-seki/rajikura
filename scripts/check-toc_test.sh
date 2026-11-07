#!/usr/bin/env bash
# scripts/check-toc.sh の例文テスト。docs の目次と本文の ## の見出しが、合うとき（通る）と食い違うとき（失敗する）を、
# 1 つずつ小さい md を作って確かめる。失敗の例は、出力にファイルと食い違った項が出ることも確かめる。
# 使い方: scripts/check-toc_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

passed=0
failed=0
check() { # check <pass|fail> <名前> <出力に含まれるはずの文字列（空なら見ない）>   本文は標準入力
  local want=$1 name=$2 expect=$3 rc=0 out
  cat > "$tmp/doc.md"
  out=$("$here/check-toc.sh" "$tmp/doc.md" 2>&1) || rc=$?
  if [ "$want" = pass ] && [ "$rc" -ne 0 ]; then
    echo "FAIL: 通るはずが rc=$rc: $name"; echo "$out"
  elif [ "$want" = fail ] && [ "$rc" -ne 1 ]; then
    echo "FAIL: 失敗するはずが rc=$rc: $name"
  elif [ "$want" = fail ] && [[ "$out" != *"$tmp/doc.md"* ]]; then
    echo "FAIL: 出力にファイル名が無い: $name"
  elif [ -n "$expect" ] && [[ "$out" != *"$expect"* ]]; then
    echo "FAIL: 出力に「$expect」が無い: $name"; echo "$out"
  else
    echo "ok: $name"; passed=$((passed + 1)); return 0
  fi
  failed=$((failed + 1))
}

check pass '目次と見出しが合う' '' <<'M'
# 題

- [一](#一)
- [二 `x`](#二-x)
- [Web UI の JSON API（`/api/`）](#web-ui-の-json-apiapi)

## 一

## 二 `x`

## Web UI の JSON API（`/api/`）
M

check pass '目次を持たないファイルは見ない' '' <<'M'
# 題

- 箇条書き

## 一
M

check pass '入れ子の項とコードブロックの中の ## は数えない' '' <<'M'
# 題

- [一](#一)
  - [子](#子)
- [二](#二)

## 一

### 子

```sh
## コメント
```

## 二
M

check pass '同じ見出しが続けば -1 が付く' '' <<'M'
# 題

- [同じ](#同じ)
- [同じ](#同じ-1)

## 同じ

## 同じ
M

check pass '項の行末の comment は除く' '' <<'M'
# 題

- [1 回の予約](#1-回の予約) <!-- glossary-ok: 回 -->

## 1 回の予約
M

check fail '順が食い違う' 'アンカー #二' <<'M'
# 題

- [二](#二)
- [一](#一)

## 一

## 二
M

check fail '項が足りない（本文の見出しが目次に無い）' '目次に無い見出し: 「二」' <<'M'
# 題

- [一](#一)

## 一

## 二
M

check fail '項が余る' '目次の余り: 「二」' <<'M'
# 題

- [一](#一)
- [二](#二)

## 一
M

check fail 'アンカーが見出しに合わない' 'アンカー #いち' <<'M'
# 題

- [一](#いち)

## 一
M

check fail '表示が見出しと違う' '表示「いち」' <<'M'
# 題

- [いち](#一)

## 一
M

echo "通った: $passed 件、失敗: $failed 件"
[ "$failed" -eq 0 ]
