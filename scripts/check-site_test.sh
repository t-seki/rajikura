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
<meta property="og:title" content="題">
<meta property="og:description" content="説明">
<meta property="og:image" content="https://t-seki.github.io/rajikura/images/jobs.png">
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
d=$(build ogtitle "" '/og:title/d'); expect fail "og:title が無い" "$d" "og:title"
d=$(build ogdesc "" '/og:description/d'); expect fail "og:description が無い" "$d" "og:description"
d=$(build ogimg "" '/og:image/d'); expect fail "og:image が無い" "$d" "og:image"
d=$(build ogempty "" 's|(og:title" content=")[^"]*|\1|'); expect fail "og:title の content が空" "$d" "og:title"
d=$(build ogrel "" 's|https://t-seki.github.io/rajikura/images/jobs.png|images/jobs.png|'); expect fail "og:image が相対パス" "$d" "og:image"
d=$(build ognoimg "" 's|images/jobs.png" *>|images/none.png">|; /og:image/s|jobs.png|none.png|'); expect fail "og:image の指す画像が組み立てた中に無い" "$d" "none.png"
d=$(build ogsq "" 's|<meta property="og:image" content="([^"]*)">|<meta\n  content=\x27\1\x27\n  property=og:image>|'); expect pass "og:image が改行をまたぎ、一重引用符・引用符無しでも拾う" "$d"
L='<link rel="stylesheet" href="https://example.com/x.css">'
d=$(build gfpre1 '<link rel="preconnect" href="https://fonts.googleapis.com">'); expect pass "Google Fonts の preconnect" "$d"
d=$(build gfpre2 '<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>'); expect pass "fonts.gstatic.com の preconnect" "$d"
d=$(build gfcss '<link href="https://fonts.googleapis.com/css2?family=X" rel="stylesheet">'); expect pass "Google Fonts の stylesheet（rel が href の後ろ）" "$d"
expect fail "外の URL の stylesheet の link" "$(build extcss "$L")" "外の URL を読み込む link（rel=stylesheet）"
expect fail "外の stylesheet の link（改行をまたぎ、引用符無し）" "$(build extcss2 "<link${nl} rel='stylesheet'${nl} href=https://example.com/x.css>")" "https://example.com/x.css"
expect fail "origin が Google Fonts に似た stylesheet（サブドメイン風）" "$(build gfsub '<link rel="stylesheet" href="https://fonts.googleapis.com.example.com/css2?family=X">')" "rel=stylesheet"
expect fail "パスに Google Fonts を含む stylesheet" "$(build gfpath '<link rel="stylesheet" href="https://example.com/fonts.googleapis.com/x.css">')" "rel=stylesheet"
expect fail "fonts.gstatic.com の stylesheet" "$(build gstcss '<link rel="stylesheet" href="https://fonts.gstatic.com/x.css">')" "rel=stylesheet"
for r in preconnect dns-prefetch preload prefetch modulepreload icon apple-touch-icon manifest; do
  expect fail "外の URL の link rel=$r" "$(build "ext$r" "<link rel=\"$r\" href=\"https://example.com/x\">")" "rel=$r）がある: https://example.com/x"
  expect fail "外の URL（//）の link rel=$r" "$(build "extp$r" "<link rel=\"$r\" href=\"//example.com/x\">")" "rel=$r）がある: //example.com/x"
done
expect fail "Google Fonts でない外の URL の dns-prefetch と Google Fonts の preconnect" "$(build mixpre "<link rel=\"preconnect\" href=\"https://fonts.gstatic.com\">${nl}<link rel=\"dns-prefetch\" href=\"https://example.com\">")" "rel=dns-prefetch"
expect pass "外の URL の canonical・alternate の link と <a href>" "$(build extok "<link rel=\"canonical\" href=\"https://example.com/\">${nl}<link rel=\"alternate\" href=\"https://example.com/feed.xml\">${nl}<a href=\"https://example.com/\">x</a>")"
expect fail "<style> の中の外の @import url()" "$(build styimp1 '<style>@import url("https://example.com/x.css");</style>')" "<style> の中"
expect fail "<style> の中の Google Fonts の @import" "$(build styimp2 "<style>@import \"https://fonts.googleapis.com/css2?family=X\";</style>")" "<style> の中"
expect fail "<style> の中の外の url()" "$(build styurl "<style>body{background:url('//example.com/a.png')}</style>")" "//example.com/a.png"
expect pass "<style> の中の相対の url()" "$(build styok '<style>body{background:url(images/jobs.png)}</style>')"
expect fail "インラインの module の import \"https://...\"" "$(build imp1 '<script type="module">import "https://example.com/m.js";</script>')" "インラインのスクリプトの中"
expect fail "インラインの module の import x from '...'" "$(build imp2 "<script type=\"module\">import x from 'https://example.com/m.js';</script>")" "インラインのスクリプトの中"
expect fail "インラインの動的な import(...)" "$(build imp3 '<script>import("https://example.com/m.js")</script>')" "インラインのスクリプトの中"
expect pass "インラインのスクリプトの相対の import" "$(build impok '<script type="module">import "./a.js";</script>')"
expect fail "大文字の閉じタグの後ろに小文字の script が続く" "$(build impup "<SCRIPT type=\"module\">import \"https://example.com/m.js\";</SCRIPT><script>1</script>")" "インラインのスクリプトの中"
expect fail "大文字の閉じタグの後ろに小文字の style が続く" "$(build styup "<STYLE>@import url(\"https://example.com/x.css\");</STYLE><style>p{}</style>")" "<style> の中"
# 落ちたときの出力に含む文字列は、その検査の出す文ごと渡す（「指すファイルが有るか」の検査など、別の検査の文で当たらないように）
m_src='外の URL を読み込む <img>・<source> の src: '
m_srcset='外の URL を読み込む <img>・<source> の srcset: '
m_embed='外の URL を読み込む <iframe>・<video>・<audio> の src: '
m_poster='外の URL を読み込む <video> の poster: '
m_attr='style 属性の url() が外の URL を読み込んでいる: '
m_cssurl='<style> の中で外の URL を読み込んでいる（@import・url()）: '
m_str='<style> の中で外の URL を文字列で書いている（image-set など）: '
expect fail "<img src> の外の URL" "$(build extimg '<img src="https://example.com/a.png" alt="">')" "${m_src}https://example.com/a.png"
expect fail "<img src> の外の URL（//・大文字）" "$(build extimg2 '<img src="//EXAMPLE.com/a.png" alt="">')" "${m_src}//EXAMPLE.com/a.png"
expect fail "<img srcset> の外の URL" "$(build extsrcset '<img src="images/jobs.png" srcset="images/jobs.png 1x, //example.com/a.png 2x" alt="">')" "${m_srcset}//example.com/a.png"
expect fail "<source srcset> の外の URL" "$(build extsrcset2 '<picture><source srcset="http://example.com/a.png 2x"></picture>')" "${m_srcset}http://example.com/a.png"
expect fail "<source src> の外の URL" "$(build extsource '<video><source src="https://example.com/a.mp4"></video>')" "${m_src}https://example.com/a.mp4"
expect fail "<iframe src> の外の URL" "$(build extiframe '<iframe src="https://example.com/f"></iframe>')" "${m_embed}https://example.com/f"
expect fail "<video src> の外の URL" "$(build extvideo '<video src="https://example.com/v.mp4"></video>')" "${m_embed}https://example.com/v.mp4"
expect fail "<video poster> の外の URL" "$(build extposter '<video poster="https://example.com/p.png"></video>')" "${m_poster}https://example.com/p.png"
expect fail "<audio src> の外の URL" "$(build extaudio '<audio src="//example.com/a.mp3"></audio>')" "${m_embed}//example.com/a.mp3"
expect fail "style 属性の url() の外の URL" "$(build extstyleattr '<div style="background:url(https://example.com/a.png)"></div>')" "${m_attr}url(https://example.com/a.png"
expect fail "<style> の image-set の文字列の外の URL" "$(build extimageset '<style>body{background-image:image-set("https://example.com/a.png" 1x)}</style>')" "${m_str}\"https://example.com/a.png"
expect fail "<style> の -webkit-image-set の文字列の外の URL" "$(build extwebkit "<style>body{background-image:-webkit-image-set('//example.com/a.png' 1x)}</style>")" "${m_str}'//example.com/a.png"
expect fail "<style> の引用符無しの url() の外の URL" "$(build extcssurl '<style>body{background:url(https://example.com/a.png)}</style>')" "${m_cssurl}url(https://example.com/a.png"
expect fail "大文字の HTTPS:// の <img src>" "$(build extupimg '<img src="HTTPS://example.com/a.png" alt="">')" "${m_src}HTTPS://example.com/a.png"
expect fail "大文字の URL( の style 属性" "$(build extupstyle '<div style="background:URL(HTTPS://example.com/a.png)"></div>')" "${m_attr}URL(HTTPS://example.com/a.png"
expect fail "大文字の IMAGE-SET の文字列" "$(build extupset '<style>body{background-image:IMAGE-SET("HTTPS://example.com/a.png" 1x)}</style>')" "${m_str}\"HTTPS://example.com/a.png"
expect pass "自分の相対パスの画像（img・srcset・poster・style 属性・image-set）" "$(build relimgs '<img src="images/jobs.png" srcset="images/jobs.png 2x" alt=""><video poster="images/jobs.png"></video><div style="background:url(images/jobs.png);left:0"></div><style>body{background-image:image-set("images/jobs.png" 1x)}</style>')"
expect fail "index.html が無い" "$tmp/empty" "index.html が無い"

echo "通った: $passed 件、失敗: $failed 件"
if [ "$fail" -ne 0 ]; then exit 1; fi
