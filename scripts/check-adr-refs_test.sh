#!/usr/bin/env bash
# scripts/check-adr-refs.sh の例文テスト。仮の置き場を ADR_REFS_ROOT で渡し、仮の docs/adr/README.md の索引
# （0001 は通常、0002 は Obsoleted by 0003、0004 は Updated by 0005）を置いて、
# 1 つのファイルの中身ごとに「当たる（失敗する）」「当たらない（通る）」を確かめる。
# 当たる例では、出力にファイル・行番号・置き換え先の番号が入っていることも確かめる。
# 各例で、スクリプトの stderr に perl の警告が出たら落とす。
# 使い方: scripts/check-adr-refs_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
code="$tmp/code"

fail=0
passed=0
failed=0

reset() {
  rm -rf "$code"
  mkdir -p "$code/docs/adr" "$code/cmd/app" "$code/internal/x" "$code/web/src" "$code/scripts"
  cat > "$code/docs/adr/README.md" <<'IDX'
# 設計の決定

| 番号 | 題 | 要約 |
| --- | --- | --- |
| [0001](0001-a.md) | 通常 | a |
| [0002](0002-b.md) | 古い（Obsoleted by 0003） | b |
| [0003](0003-c.md) | 新しい | c |
| [0004](0004-d.md) | 一部更新された（Updated by 0005） | d |
| [0005](0005-e.md) | 更新した | e |
IDX
}

# check <hit|miss> <置き場からの相対パス> <中身> [hit のとき、出力に含まれるはずの文字列...]
check() {
  local want=$1 rel=$2 body=$3 rc=0 out
  shift 3
  reset
  mkdir -p "$(dirname "$code/$rel")"
  printf '%s\n' "$body" > "$code/$rel"
  out=$(ADR_REFS_ROOT="$code" "$here/check-adr-refs.sh" 2>"$tmp/stderr") || rc=$?
  out="$out$(cat "$tmp/stderr")"
  local label="$rel: ${body//$'\n'/⏎}"
  if grep -qE 'at -e line|Use of uninitialized' "$tmp/stderr"; then
    echo "FAIL: perl の警告が stderr に出た（$(head -n1 "$tmp/stderr")）: $label"; fail=1; failed=$((failed + 1)); return 0
  fi
  if [ "$want" = hit ] && [ "$rc" -ne 1 ]; then
    echo "FAIL: 当たるはずが rc=$rc: $label"; fail=1; failed=$((failed + 1)); return 0
  elif [ "$want" = miss ] && [ "$rc" -ne 0 ]; then
    echo "FAIL: 当たらないはずが rc=$rc: $label ($out)"; fail=1; failed=$((failed + 1)); return 0
  fi
  local e
  for e in "$@"; do
    if [[ "$out" != *"$e"* ]]; then
      echo "FAIL: 出力に「$e」が無い: $label ($out)"; fail=1; failed=$((failed + 1)); return 0
    fi
  done
  if [ "$want" = miss ] && [ -n "$out" ]; then
    echo "FAIL: 通るのに出力がある: $label ($out)"; fail=1; failed=$((failed + 1)); return 0
  fi
  echo "ok: $label"; passed=$((passed + 1))
}

# 参照の形 × 置き場（置き換え済み 0002 → 0003 で落ちる）
check hit internal/x/a.go '// ADR 0002 を見る。' 'internal/x/a.go:1:' '0003'
check hit cmd/app/main.go $'package main\n\n// docs/adr/0002-b.md を見る。' 'cmd/app/main.go:3:' '0003'
check hit internal/x/a.go '	x := 1 // adr/0002-b.md を見る' 'internal/x/a.go:1:' '0003'
check hit web/src/a.ts '// ADR 0002 を見る' 'web/src/a.ts:1:' '0003'
check hit web/src/a.tsx $'/**\n * ADR 0002 のとおり\n */' 'web/src/a.tsx:2:' '0003'
check hit scripts/x.sh $'#!/bin/sh\n# docs/adr/0002-b.md を見る' 'scripts/x.sh:2:' '0003'
check hit internal/x/m.sql '-- ADR 0002 のため' 'internal/x/m.sql:1:' '0003'
check hit docs/guide.md '詳しくは [ADR 0002](adr/0002-b.md)。' 'docs/guide.md:1:' '0003'
check hit docs/sub/guide.md '[b](../adr/0002-b.md)' 'docs/sub/guide.md:1:' '0003'
check hit docs/guide.md '[b](docs/adr/0002-b.md)' 'docs/guide.md:1:' '0003'
check hit README.md '作りは ADR 0002 にある' 'README.md:1:' '0003'
check hit README.md '[b](docs/adr/0002-b.md)' 'README.md:1:' '0003'
check hit GLOSSARY.md 'ADR 0002 を見る' 'GLOSSARY.md:1:' '0003'
check hit GLOSSARY.md '[b](docs/adr/0002-b.md)' 'GLOSSARY.md:1:' '0003'
check hit docs/guide.md $'一行目\nADR0002' 'docs/guide.md:2:' '0003'

# 並べた 2 つめ以降の番号
check hit internal/x/a.go '// ADR 0004・0002 を見る。' 'internal/x/a.go:1:' 'ADR 0002' '0003'
check hit internal/x/a.go '// ADR 0001・0004・0002 を見る。' 'ADR 0002' '0003'
check hit docs/guide.md 'ADR 0004、0002 のとおり' 'ADR 0002' '0003'
check hit docs/guide.md 'ADR 0001,0002' 'ADR 0002' '0003'
check miss internal/x/a.go '// ADR 0004・0001 を見る。'
check miss internal/x/a.go '// ADR 0004・0005 を見る。'

# Updated by の ADR を指す参照は通る
check miss internal/x/a.go '// ADR 0004 を見る。'
check miss docs/guide.md '[d](adr/0004-d.md) と ADR 0004'
check miss README.md '[d](docs/adr/0004-d.md)'
check miss internal/x/m.sql '-- docs/adr/0004-d.md のため'
# 通常と新しい ADR を指す参照
check miss internal/x/a.go '// ADR 0001 と ADR 0003 を見る。'
check miss docs/guide.md '[c](adr/0003-c.md)'

# docs/adr/ の中は読まない
check miss docs/adr/0001-a.md 'ADR 0002 と [b](0002-b.md)、docs/adr/0002-b.md'
check miss docs/adr/0006-f.md '決めた当時は ADR 0002 を指す。'
check miss docs/adr/sub/x.md 'adr/0002-b.md'
# 対象外の置き場（対象の一覧に無いディレクトリ、コメントでない行）
check miss other/a.go '// ADR 0002 を見る。'
check miss internal/x/a.go 'var s = "ADR 0002"'
check miss internal/x/a.go 'u := "http://example.com/ADR 0002"'
check miss scripts/x.sh 'echo "ADR 0002"'
check miss internal/x/a.txt 'ADR 0002'

# 免除の印: 行末の印で、その行の書いた番号だけを外す。当たらない印はエラー
check miss internal/x/m.sql '-- ADR 0002 のため adr-ref-ok: 0002'
check miss internal/x/m.sql '-- ADR 0002・0004 のため adr-ref-ok: 0002'
check miss internal/x/m.sql '-- adr/0002-b.md と ADR 0002 adr-ref-ok: 0002'
check hit internal/x/m.sql '-- ADR 0002 のため' 'ADR 0002'
check hit internal/x/m.sql '-- 0002 の外 adr-ref-ok: 0002' 'internal/x/m.sql:1:' '当たらない'
check hit internal/x/m.sql '-- ADR 0004 のため adr-ref-ok: 0004' 'internal/x/m.sql:1:' '当たらない'
check hit internal/x/m.sql '-- ADR 0001 のため adr-ref-ok: 0001' '当たらない'
check hit internal/x/m.sql '-- ADR 0002 のため adr-ref-ok: 0003' 'ADR 0002' '当たらない'
check hit internal/x/m.sql '-- ADR 0002 のため adr-ref-ok: 0002、0003' '当たらない'
check hit internal/x/m.sql '-- ADR 0002 のため adr-ref-ok: 0002、0003' '0003'

# 索引が無い・Obsoleted by が無いとき
reset
rm "$code/docs/adr/README.md"
printf '%s\n' '// ADR 0002' > "$code/internal/x/a.go"
rc=0; ADR_REFS_ROOT="$code" "$here/check-adr-refs.sh" >"$tmp/out" 2>&1 || rc=$?
if [ "$rc" -eq 1 ] && grep -q 'README.md' "$tmp/out"; then echo "ok: 索引が無ければ失敗する"; passed=$((passed + 1)); else echo "FAIL: 索引が無いのに rc=$rc"; fail=1; failed=$((failed + 1)); fi
reset
printf '%s\n' '| [0001](0001-a.md) | 通常 | a |' > "$code/docs/adr/README.md"
printf '%s\n' '// ADR 0001' > "$code/internal/x/a.go"
rc=0; ADR_REFS_ROOT="$code" "$here/check-adr-refs.sh" >"$tmp/out" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then echo "ok: Obsoleted by が無ければ通る"; passed=$((passed + 1)); else echo "FAIL: Obsoleted by が無いのに rc=$rc"; fail=1; failed=$((failed + 1)); fi

echo "通った: $passed 件、失敗: $failed 件"
exit $fail
