#!/usr/bin/env bash
# scripts/check-glossary.sh の例文テスト。テスト用の用語集を GLOSSARY_FILE で渡し、
# 1 行ずつ「当たる（失敗する）」「当たらない（通る）」を確かめる。
# 用語集の定義文の検査も、check_def で本文ごと確かめる。
# 各例で、スクリプトの stderr に perl の警告（at -e line を含む行など）が出たら落とす。
# 使い方: scripts/check-glossary_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/GLOSSARY.md" <<'G'
# test

**放送回**:
1 回分の放送。
_Avoid_: エピソード、回

**休止回**:
消えた放送回。

**配信**:
サービス。
_Avoid_: プラン
G

cat > "$tmp/allow.txt" <<'A'
# test

ダブルプラン
A

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
  out=$(GLOSSARY_FILE="$tmp/GLOSSARY.md" GLOSSARY_ALLOW_FILE="$tmp/allow.txt" "$here/check-glossary.sh" "$tmp/doc.md" 2>"$tmp/stderr") || rc=$?
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

check hit  'この回、'
check hit  '「回」'
check hit  '回。'
check hit  '無料プラン'
check hit  '会員プラン'
check hit  'エピソードを見る'
check miss '放送回'
check miss '休止回'
check miss '第一回'
check miss '6 回'
check miss '6回'
check miss '回数'
check miss 'この回、 <!-- glossary-ok: 回 -->'
check miss '無料プラン <!-- glossary-ok: プラン -->'
check miss 'この回、プラン <!-- glossary-ok: 回、プラン -->'
# 許す言い回し（scripts/glossary-allow.txt の形）
check miss 'ダブルプラン'
check hit  'ダブルプランと無料プラン' '「プラン」は用語集の _Avoid_'
# 語の単位の印: 書いた語は外し、書いていない別の語は拾う（出力で、どの語で当たったかを見る）
check hit  'プランと回 <!-- glossary-ok: プラン -->' '「回」は用語集の _Avoid_'
check hit  'プランとエピソード <!-- glossary-ok: プラン -->' '「エピソード」は用語集の _Avoid_'
# 語を書かない印、当たらない語を書いた印はエラー
check hit  '無料プラン <!-- glossary-ok -->' '語を書かない'
check hit  '無料プラン <!-- glossary-ok: -->' '語を書かない'
check hit  '無料 <!-- glossary-ok: プラン -->' '「プラン」はこの行で当たらない'
check hit  '無料プラン <!-- glossary-ok: エピソード -->' '「エピソード」はこの行で当たらない'

# 用語集の定義文の検査: 定義文（用語の行の次から _Avoid_ の行の前まで）が _Avoid_ の語を使うと落ちる。
# check_def <hit|miss> <用語集の本文> [hit のとき、出力に含まれるはずの文字列]
check_def() {
  local want=$1 body=$2 expect=${3:-} rc=0 out
  printf '%s\n' "$body" > "$tmp/def-glossary.md"
  : > "$tmp/empty.md"
  out=$(GLOSSARY_FILE="$tmp/def-glossary.md" GLOSSARY_ALLOW_FILE="$tmp/allow.txt" "$here/check-glossary.sh" "$tmp/empty.md" 2>"$tmp/stderr") || rc=$?
  out="$out$(cat "$tmp/stderr")"
  if perl_warned "$body"; then return 0; fi
  if [ "$want" = hit ] && [ "$rc" -ne 1 ]; then
    echo "FAIL: 定義文が当たるはずが rc=$rc: $body"; fail=1
  elif [ "$want" = miss ] && [ "$rc" -ne 0 ]; then
    echo "FAIL: 定義文が当たらないはずが rc=$rc: $body ($out)"; fail=1
  elif [ -n "$expect" ] && [[ "$out" != *"$expect"* ]]; then
    echo "FAIL: 出力に「$expect」が無い: $body"; fail=1
  else
    echo "ok: ${body//$'\n'/⏎}"; passed=$((passed + 1)); return 0
  fi
  failed=$((failed + 1))
}

nl=$'\n'
g_head=$'**放送回**:\n1 回分の放送。\n_Avoid_: エピソード、回\n\n**配信**:\n'
check_def miss "${g_head}サービス。${nl}_Avoid_: プラン"
# 自分の項の _Avoid_ の語
check_def hit  "${g_head}無料のプラン。${nl}_Avoid_: プラン" 'def-glossary.md:'
# 別の項の _Avoid_ の語
check_def hit  "${g_head}サービス。エピソードではない。${nl}_Avoid_: プラン" '「エピソード」は用語集の _Avoid_'
# 定義が複数行
check_def hit  "${g_head}サービス。${nl}2 行目はプラン。${nl}_Avoid_: プラン"
# 用語の行と _Avoid_ の行は検査しない（_Avoid_ の行は語が並ぶだけ）
check_def miss $'**プラン相談**:\n相談。\n\n**配信**:\nサービス。\n_Avoid_: プラン'
# より長い用語集の用語は除く（放送回の「回」）
check_def miss "${g_head}放送回の話。${nl}_Avoid_: プラン"
# 免除の印
check_def miss "${g_head}この回のこと。 <!-- glossary-ok: 回 -->${nl}_Avoid_: プラン"
check_def hit  "${g_head}この回のこと。${nl}_Avoid_: プラン" '「回」は用語集の _Avoid_'
# 定義文の終わり: 空行・見出しの後は定義文として当てない
check_def miss "${g_head}サービス。${nl}${nl}プランの話（定義文ではない）。${nl}_Avoid_: プラン"
check_def miss "${g_head}サービス。${nl}## プランの見出し${nl}_Avoid_: プラン"

# _Avoid_ の語に括弧の説明が混ざったら、その行を名指しして落とす
check_def hit  "${g_head}サービス。${nl}_Avoid_: プラン（説明）" 'def-glossary.md:7: _Avoid_ の語に括弧'
check_def hit  "${g_head}サービス。${nl}_Avoid_: プラン(説明)" '括弧の説明は書けない'
check_def hit  "${g_head}サービス。${nl}_Avoid_: 課金、プラン（説明）" '括弧の説明は書けない'
check_def miss "${g_head}サービス。${nl}_Avoid_: プラン、課金"
# 括弧のある _Avoid_ の行があっても、その行の括弧の無い語と、ほかの行の語は検査される
check_def hit  "${g_head}サービス。課金の話。${nl}_Avoid_: 課金、プラン（説明）" '「課金」は用語集の _Avoid_'
check_def hit  "${g_head}サービス。エピソードの話。${nl}_Avoid_: プラン（説明）" '「エピソード」は用語集の _Avoid_'
# 定義文に括弧があっても落ちない
check_def miss "${g_head}サービス（説明）。${nl}_Avoid_: プラン"

echo "通った: $passed 件、失敗: $failed 件"
if [ "$fail" -ne 0 ]; then exit 1; fi
