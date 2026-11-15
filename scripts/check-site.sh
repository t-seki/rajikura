#!/usr/bin/env bash
# 組み立てた案内のサイト（scripts/build-site.sh の出力）だけを調べる。
# - index.html が指す画像（img src）・CSS（link href）・アイコン（link rel=icon）のファイルが有る
# - 入れ方のリンクが GitHub Release の最新（releases/latest）と docs/getting-started.md・docs/windows.md を指す
# - 外の URL の <script src> が無い。アクセス解析のタグ・広告を読み込まない
# 使い方: scripts/check-site.sh [組み立てた場所]   （既定は _site）
set -uo pipefail

dir=${1:-_site}
html="$dir/index.html"
fail=0
bad() { echo "$html: $1" >&2; fail=1; }

if [ ! -f "$html" ]; then echo "$html が無い" >&2; exit 1; fi

# 相対パスの参照先が有ること
refs=$(grep -oE '<(img|link|source|script)\b[^>]*>' "$html" | grep -oE '(src|href)="[^"]*"' | sed -E 's/^(src|href)="//; s/"$//')
while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  case "$ref" in
    http://*|https://*|//*|mailto:*|\#*) continue;;
  esac
  p=${ref%%[?#]*}
  [ -e "$dir/$p" ] || bad "指すファイルが組み立てた中に無い: $ref"
done <<<"$refs"
grep -qE '<link\b[^>]*rel="stylesheet"' "$html" || bad "CSS（link rel=stylesheet）が無い"
grep -qE '<link\b[^>]*rel="[^"]*icon[^"]*"' "$html" || bad "アイコン（link rel=icon）が無い"

# 入れ方のリンクの向き
grep -qF 'href="https://github.com/t-seki/rajikura/releases/latest"' "$html" || bad "GitHub Release の最新（https://github.com/t-seki/rajikura/releases/latest）へのリンクが無い"
grep -qE 'href="https://github.com/t-seki/rajikura/blob/main/docs/getting-started\.md[^"]*"' "$html" || bad "docs/getting-started.md へのリンクが無い"
grep -qE 'href="https://github.com/t-seki/rajikura/blob/main/docs/windows\.md[^"]*"' "$html" || bad "docs/windows.md へのリンクが無い"

# 外のスクリプト・アクセス解析
if grep -qiE '<script\b[^>]*src="(https?:)?//' "$html"; then bad "外の URL の <script src> がある"; fi
if grep -qiE 'google-analytics|googletagmanager|gtag\(|plausible|matomo|piwik|clarity\.ms|hotjar|adsbygoogle|doubleclick|analytics' "$html"; then bad "アクセス解析か広告のタグがある"; fi

for css in "$dir"/*.css; do
  [ -e "$css" ] || continue
  if grep -qE '@import|url\(\s*["'"'"']?(https?:)?//' "$css"; then echo "$css: 外の URL を読み込んでいる" >&2; fail=1; fi
done

[ "$fail" -eq 0 ] && echo "ok: $dir"
exit "$fail"
