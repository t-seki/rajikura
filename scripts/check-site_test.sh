#!/usr/bin/env bash
# scripts/check-site.sh の例文テスト。一時フォルダに組み立てた中身を作り、通る・落ちるを確かめる。
# 使い方: scripts/check-site_test.sh   （repo のどこから呼んでもよい）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
passed=0
failed=0

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
# build <名前> [EXTRA の行に入れる中身（改行を含んでよい）] [sed の式]
build() {
  local d="$tmp/$1" extra=${2:-}
  mkdir -p "$d/images"
  touch "$d/favicon.svg" "$d/style.css" "$d/images/jobs.png"
  while IFS= read -r line; do
    if [ "$line" = EXTRA ]; then printf '%s\n' "$extra"; else printf '%s\n' "$line"; fi
  done < <(good_html) > "$d/index.html"
  if [ -n "${3:-}" ]; then sed -i.bak -E "$3" "$d/index.html"; rm -f "$d/index.html.bak"; fi
  echo "$d"
}
expect() { # expect <pass|fail> <説明> <フォルダ> [落ちたときの出力に含む文字列]
  local want=$1 name=$2 d=$3 needle=${4:-} rc=0 out
  out=$("$here/check-site.sh" "$d" 2>&1) || rc=$?
  if [ "$want" = pass ] && [ "$rc" -eq 0 ]; then echo "ok: 通る: $name"; passed=$((passed + 1))
  elif [ "$want" = fail ] && [ "$rc" -ne 0 ] && [ -n "$out" ] && { [ -z "$needle" ] || grep -qF -- "$needle" <<<"$out"; }; then
    echo "ok: 落ちる: $name"; passed=$((passed + 1))
  else echo "FAIL: $name (rc=$rc) $out"; fail=1; failed=$((failed + 1)); fi
}

nl=$'\n'

expect pass "正しい中身" "$(build good)"
d=$(build multi "<img${nl}  src=\"images/jobs.png\"${nl}  alt=\"x\">"); expect pass "改行をまたぐ img の指す画像が有る" "$d"
d=$(build sq "<img src='images/jobs.png' alt=''>"); expect pass "一重引用符の img の指す画像が有る" "$d"
d=$(build srcsetok '<img src="images/jobs.png" srcset="images/jobs.png 1x, images/jobs.png 2x" alt="">'); expect pass "srcset の指す画像が有る" "$d"
d=$(build relq "" "s|rel=\"stylesheet\"|rel='stylesheet'|; s|rel=\"icon\"|rel=icon|"); expect pass "rel が一重引用符・引用符無しでも CSS とアイコンが有る" "$d"
d=$(build relmulti "" 's|<link rel="stylesheet" href="style.css">|<link\n  rel="stylesheet"\n  href="style.css">|'); expect pass "改行をまたぐ link の CSS が有る" "$d"

d=$(build noimg); rm "$d/images/jobs.png"; expect fail "指す画像が無い" "$d" "images/jobs.png"
d=$(build multinoimg "<img${nl}  src=\"images/guide2.png\"${nl}  alt=\"番組表\">"); expect fail "改行をまたぐ img の指す画像が無い" "$d" "images/guide2.png"
d=$(build sqnoimg "<img src='images/guide2.png' alt=''>"); expect fail "一重引用符の img の指す画像が無い" "$d" "images/guide2.png"
d=$(build nqnoimg "<img src=images/guide2.png alt=''>"); expect fail "引用符無しの img の指す画像が無い" "$d" "images/guide2.png"
d=$(build srcsetno '<img src="images/jobs.png" srcset="images/jobs.png 1x, images/guide2@2x.png 2x" alt="">'); expect fail "srcset の指す画像が無い" "$d" "images/guide2@2x.png"
d=$(build nocss); rm "$d/style.css"; expect fail "指す CSS が無い" "$d" "style.css"
d=$(build nocsslink "" '/rel="stylesheet"/d'); expect fail "CSS の link が無い" "$d" "rel=stylesheet"
d=$(build noicon); rm "$d/favicon.svg"; expect fail "指すアイコンが無い" "$d" "favicon.svg"
d=$(build noiconlink "" '/rel="icon"/d'); expect fail "アイコンの link が無い" "$d" "rel=icon"
expect fail "Release のリンクが releases/latest でない" "$(build rel '' 's|releases/latest|releases|')" "releases/latest"
expect fail "Release のリンクが無い" "$(build norel '' '/releases\/latest/d')" "releases/latest"
expect fail "docs/getting-started.md へのリンクが無い" "$(build nogs '' '/getting-started/d')" "getting-started.md"
expect fail "docs/windows.md が別の所を指す" "$(build otherwin '' 's|docs/windows\.md|docs/other.md|')" "windows.md"
expect fail "外の <script src=\"https://...\">" "$(build ext '<script src="https://example.com/x.js"></script>')" "外の URL の <script src>"
expect fail "外の <script src='//...'>（改行をまたぐ）" "$(build ext2 "<script${nl}src='//example.com/x.js'></script>")" "外の URL の <script src>"
expect fail "アクセス解析のタグ" "$(build ga '<script>gtag(1)</script>')" "アクセス解析"
d=$(build cssext); echo '@import url("https://example.com/x.css");' > "$d/style.css"; expect fail "CSS が外の URL を読み込む" "$d" "外の URL を読み込んでいる"
expect fail "index.html が無い" "$tmp/empty" "index.html が無い"

echo "通った: $passed 件、失敗: $failed 件"
if [ "$fail" -ne 0 ]; then exit 1; fi
