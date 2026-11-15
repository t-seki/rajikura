#!/usr/bin/env bash
# 組み立てた案内のサイト（scripts/build-site.sh の出力）だけを調べる。
# - index.html が指す画像（img・source の src と srcset）・CSS（link href）・アイコン（link rel=icon）・script src のファイルが有る
# - CSS（link rel=stylesheet）とアイコン（link rel=icon）の link が有る
# - 入れ方のリンクが GitHub Release の最新（releases/latest）と docs/getting-started.md・docs/windows.md を指す
# - 外の URL の <script src> が無い。アクセス解析のタグ・広告を読み込まない。CSS が外の URL を読み込まない
# タグは改行をまたいでもよく、属性の値は二重引用符・一重引用符・引用符無しのどれでも拾う。
# 使い方: scripts/check-site.sh [組み立てた場所]   （既定は _site）
set -uo pipefail

dir=${1:-_site}
html="$dir/index.html"
fail=0
bad() { echo "$html: $1" >&2; fail=1; }

if [ ! -f "$html" ]; then echo "$html が無い" >&2; exit 1; fi

# 改行をまたぐタグも 1 つに拾えるよう、改行を空白にした 1 行で見る
flat=$(tr '\r\n\t' '   ' <"$html")
tags() { # tags <タグ名の正規表現>: そのタグを 1 行に 1 つ出す
  grep -oiE "<($1)([[:space:]][^>]*)?>" <<<"$flat" || true
}
# attr <属性名> : 標準入力のタグごとに、その属性の値を 1 行に 1 つ出す（引用符を外す）
attr() {
  grep -oiE "[[:space:]]$1[[:space:]]*=[[:space:]]*(\"[^\"]*\"|'[^']*'|[^[:space:]\"'>]+)" \
    | sed -E "s/^[[:space:]]*[A-Za-z-]+[[:space:]]*=[[:space:]]*//; s/^\"(.*)\"\$/\\1/; s/^'(.*)'\$/\\1/"
}

# 相対パスの参照先が有ること
refs=$(
  tags 'img|source|script' | attr src
  tags 'link' | attr href
  tags 'img|source' | attr srcset | tr ',' '\n' | sed -E 's/^[[:space:]]+//; s/[[:space:]].*$//'
)
while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  case "$ref" in
    http://*|https://*|//*|mailto:*|data:*|\#*) continue;;
  esac
  p=${ref%%[?#]*}
  p=${p#./}
  [ -e "$dir/$p" ] || bad "指すファイルが組み立てた中に無い: $ref"
done <<<"$refs"

rels=$(tags 'link' | attr rel)
grep -qiE '(^|[[:space:]])stylesheet([[:space:]]|$)' <<<"$rels" || bad "CSS（link rel=stylesheet）が無い"
grep -qiE '(^|[[:space:]])icon([[:space:]]|$)' <<<"$rels" || bad "アイコン（link rel=icon）が無い"

# 入れ方のリンクの向き
hrefs=$(tags 'a' | attr href)
grep -qxF 'https://github.com/t-seki/rajikura/releases/latest' <<<"$hrefs" || bad "GitHub Release の最新（https://github.com/t-seki/rajikura/releases/latest）へのリンクが無い"
grep -qE '^https://github\.com/t-seki/rajikura/blob/main/docs/getting-started\.md(#.*)?$' <<<"$hrefs" || bad "docs/getting-started.md へのリンクが無い"
grep -qE '^https://github\.com/t-seki/rajikura/blob/main/docs/windows\.md(#.*)?$' <<<"$hrefs" || bad "docs/windows.md へのリンクが無い"

# 外のスクリプト・アクセス解析
if tags 'script' | attr src | grep -qiE '^(https?:)?//'; then bad "外の URL の <script src> がある"; fi
if grep -qiE 'google-analytics|googletagmanager|gtag\(|plausible|matomo|piwik|clarity\.ms|hotjar|adsbygoogle|doubleclick|analytics' <<<"$flat"; then bad "アクセス解析か広告のタグがある"; fi

for css in "$dir"/*.css; do
  [ -e "$css" ] || continue
  if grep -qE '@import|url\([[:space:]]*["'"'"']?(https?:)?//' "$css"; then echo "$css: 外の URL を読み込んでいる" >&2; fail=1; fi
done

[ "$fail" -eq 0 ] && echo "ok: $dir"
exit "$fail"
