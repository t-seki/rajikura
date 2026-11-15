#!/usr/bin/env bash
# scripts/check-site.sh の例文テスト。一時フォルダに組み立てた中身を作り、通る・落ちるを確かめる。
# 使い方: scripts/check-site_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

good_html() {
  cat <<'H'
<!DOCTYPE html>
<html lang="ja"><head>
<link rel="icon" href="favicon.svg">
<link rel="stylesheet" href="style.css">
</head><body>
<img src="images/jobs.png" alt="">
<a href="https://github.com/t-seki/rajikura/blob/main/docs/getting-started.md">a</a>
<a href="https://github.com/t-seki/rajikura/blob/main/docs/windows.md">b</a>
<a href="https://github.com/t-seki/rajikura/releases/latest">c</a>
EXTRA
</body></html>
H
}
# build <名前> [EXTRA の中身] [sed の式]
build() {
  local d="$tmp/$1"
  mkdir -p "$d/images"
  touch "$d/favicon.svg" "$d/style.css" "$d/images/jobs.png"
  good_html | sed "s|EXTRA|${2:-}|" > "$d/index.html"
  if [ -n "${3:-}" ]; then sed -i.bak -E "$3" "$d/index.html"; rm -f "$d/index.html.bak"; fi
  echo "$d"
}
expect() { # expect <pass|fail> <説明> <フォルダ>
  local want=$1 name=$2 d=$3 rc=0 out
  out=$("$here/check-site.sh" "$d" 2>&1) || rc=$?
  if [ "$want" = pass ] && [ "$rc" -eq 0 ]; then echo "ok: 通る: $name"
  elif [ "$want" = fail ] && [ "$rc" -ne 0 ] && [ -n "$out" ]; then echo "ok: 落ちる: $name"
  else echo "FAIL: $name (rc=$rc) $out"; fail=1; fi
}

expect pass "正しい中身" "$(build good)"

d=$(build noimg); rm "$d/images/jobs.png"; expect fail "指す画像が無い" "$d"
d=$(build nocss); rm "$d/style.css"; expect fail "指す CSS が無い" "$d"
d=$(build noicon); rm "$d/favicon.svg"; expect fail "指すアイコンが無い" "$d"
expect fail "Release のリンクが releases/latest でない" "$(build rel '' 's|releases/latest|releases|')"
expect fail "Release のリンクが無い" "$(build norel '' '/releases\/latest/d')"
expect fail "docs/getting-started.md へのリンクが無い" "$(build nogs '' '/getting-started/d')"
expect fail "docs/windows.md が別の所を指す" "$(build otherwin '' 's|docs/windows\.md|docs/other.md|')"
expect fail "外の <script src=\"https://...\">" "$(build ext '<script src="https://example.com/x.js"></script>')"
expect fail "アクセス解析のタグ" "$(build ga '<script>gtag(1)</script>')"
expect fail "index.html が無い" "$tmp/empty"

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "すべて通った"
