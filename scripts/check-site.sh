#!/usr/bin/env bash
# 組み立てた案内のサイト（scripts/build-site.sh の出力）だけを調べる。
# - index.html が指す画像（img・source の src と srcset）・CSS（link href）・アイコン（link rel=icon）・script src のファイルが有る
# - CSS（link rel=stylesheet）とアイコン（link rel=icon）の link が有る
# - 入れ方のリンクが GitHub Release の最新（releases/latest）と docs/getting-started.md・docs/windows.md を指す
# - 外の URL の <script src> が無い。アクセス解析のタグ・広告を読み込まない。CSS が外の URL を読み込まない
# - ページを開くとブラウザが外へつなぎに行く link（stylesheet・preconnect・dns-prefetch・preload・prefetch・modulepreload・icon・apple-touch-icon・manifest）が、
#   外の URL を指さない。許すのは Google Fonts（https://fonts.googleapis.com の stylesheet と、そことhttps://fonts.gstatic.com への preconnect）だけ。
#   canonical・alternate の link と <a href> は外の URL でもよい
# - <style> の中に、外の @import・url() が無い（Google Fonts でも落とす）。src の無いインラインの <script> の中に、外の URL の import が無い
# - OGP の meta（og:title・og:description・og:image）が有り、content が空でない。og:image は案内のサイトの URL（https://t-seki.github.io/rajikura/）から始まる絶対の URL で、その後ろのパスのファイルが組み立てた中に有る
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

# OGP（X などのカード）
site_url='https://t-seki.github.io/rajikura/'
metas=$(tags 'meta')
og_content() { # og_content <property>: その meta の content を出す（無ければ何も出さない）
  local m
  m=$(grep -iE "[[:space:]]property[[:space:]]*=[[:space:]]*(\"$1\"|'$1'|$1([[:space:]>]|\$))" <<<"$metas" | head -n 1)
  [ -n "$m" ] && attr content <<<"$m"
}
for prop in og:title og:description og:image; do
  v=$(og_content "$prop")
  if [ -z "${v//[[:space:]]/}" ]; then bad "OGP の meta（property=\"$prop\"）が無いか、content が空"; fi
done
img=$(og_content og:image)
if [ -n "${img//[[:space:]]/}" ]; then
  case "$img" in
    "$site_url"?*)
      p=${img#"$site_url"}; p=${p%%[?#]*}
      [ -f "$dir/$p" ] || bad "og:image の指す画像が組み立てた中に無い: $img";;
    *) bad "og:image が案内のサイトの URL（$site_url）から始まる絶対の URL でない: $img";;
  esac
fi

# 外のスクリプト・アクセス解析
if tags 'script' | attr src | grep -qiE '^(https?:)?//'; then bad "外の URL の <script src> がある"; fi
if grep -qiE 'google-analytics|googletagmanager|gtag\(|plausible|matomo|piwik|clarity\.ms|hotjar|adsbygoogle|doubleclick|analytics' <<<"$flat"; then bad "アクセス解析か広告のタグがある"; fi

# 画像・埋め込み・動画・音声・style 属性からの外の URL
is_ext() { grep -qiE '^[[:space:]]*(https?:)?//' <<<"$1"; }
while IFS= read -r u; do
  [ -n "$u" ] && is_ext "$u" && bad "外の URL を読み込む <img>・<source> の src: $u"
done <<<"$(tags 'img|source' | attr src)"
while IFS= read -r u; do
  [ -n "$u" ] && is_ext "$u" && bad "外の URL を読み込む <img>・<source> の srcset: $u"
done <<<"$(tags 'img|source' | attr srcset | tr ',' '\n' | sed -E 's/^[[:space:]]+//; s/[[:space:]].*$//')"
while IFS= read -r u; do
  [ -n "$u" ] && is_ext "$u" && bad "外の URL を読み込む <iframe>・<video>・<audio> の src: $u"
done <<<"$(tags 'iframe|video|audio' | attr src)"
while IFS= read -r u; do
  [ -n "$u" ] && is_ext "$u" && bad "外の URL を読み込む <video> の poster: $u"
done <<<"$(tags 'video' | attr poster)"
while IFS= read -r m; do
  [ -n "$m" ] && bad "style 属性の url() が外の URL を読み込んでいる: $m"
done < <(tags '[a-zA-Z][a-zA-Z0-9-]*' | attr style | grep -oiE "url\([[:space:]]*[\"']?(https?:)?//[^)\"' ]*" || true)

# ブラウザが開いただけで外へつなぎに行く link
gf_css='https://fonts.googleapis.com'
gf_pre1='https://fonts.googleapis.com'
gf_pre2='https://fonts.gstatic.com'
origin_is() { # origin_is <url> <origin>: URL がその origin（直後は / ? # か終わり）か
  local u=${1,,} o=$2
  case "$u" in "$o"|"$o"/*|"$o"\?*|"$o"\#*) return 0;; esac
  return 1
}
while IFS= read -r tag; do
  [ -n "$tag" ] || continue
  href=$(attr href <<<"$tag" | head -n 1)
  href=${href#"${href%%[![:space:]]*}"}
  grep -qiE '^(https?:)?//' <<<"$href" || continue
  for rel in $(attr rel <<<"$tag" | tr 'A-Z' 'a-z'); do
    case "$rel" in
      stylesheet) origin_is "$href" "$gf_css" && continue;;
      preconnect) { origin_is "$href" "$gf_pre1" || origin_is "$href" "$gf_pre2"; } && continue;;
      dns-prefetch|preload|prefetch|modulepreload|icon|apple-touch-icon|manifest) ;;
      *) continue;;
    esac
    bad "外の URL を読み込む link（rel=$rel）がある: $href"
  done
done <<<"$(tags 'link')"

# <style> の中と、src の無いインラインの <script> の中
sep=$''
split_close() { sed -E "s#</$1[[:space:]]*>#&${sep}#gI" <<<"$flat" | tr "$sep" '
' | grep -iE "<$1([[:space:]>])" || true; }
while IFS= read -r piece; do
  [ -n "$piece" ] || continue
  body=$(sed -E 's/^.*<[sS][tT][yY][lL][eE]([[:space:]][^>]*)?>//' <<<"$piece")
  while IFS= read -r m; do
    [ -n "$m" ] && bad "<style> の中で外の URL を読み込んでいる（@import・url()）: $m"
  done < <(grep -oiE "@import[[:space:]]*(url\([[:space:]]*)?[\"']?(https?:)?//[^;)\"' ]*|url\([[:space:]]*[\"']?(https?:)?//[^)\"' ]*" <<<"$body" || true)
  while IFS= read -r m; do
    [ -n "$m" ] && bad "<style> の中で外の URL を文字列で書いている（image-set など）: $m"
  done < <(grep -oiE "[\"'][[:space:]]*(https?:)?//[^\"']*" <<<"$body" || true)
done <<<"$(split_close style)"
while IFS= read -r piece; do
  [ -n "$piece" ] || continue
  stag=$(grep -oiE '<script([[:space:]][^>]*)?>' <<<"$piece" | tail -n 1)
  [ -n "$stag" ] || continue
  [ -z "$(attr src <<<"$stag")" ] || continue
  body=${piece#*"$stag"}
  while IFS= read -r m; do
    [ -n "$m" ] && bad "インラインのスクリプトの中で外の URL を import している: $m"
  done < <(grep -oiE "(import|from)[[:space:]]*\(?[[:space:]]*[\"'\`](https?:)?//[^\"'\`]*" <<<"$body" || true)
done <<<"$(split_close script)"

for css in "$dir"/*.css; do
  [ -e "$css" ] || continue
  if grep -qE '@import|url\([[:space:]]*["'"'"']?(https?:)?//' "$css"; then echo "$css: 外の URL を読み込んでいる" >&2; fail=1; fi
done

[ "$fail" -eq 0 ] && echo "ok: $dir"
exit "$fail"
