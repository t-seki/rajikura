#!/usr/bin/env bash
# 使う人向けの docs に、画面が内部で叩く API のパス（`/api/…`）が書かれていないかを検査する。
# 書かれていればファイルと行を出して失敗する。API の口・欄は docs/api.md に置く（docs/development.md の「docs の書き方」）。
#
# - 対象は下の user_docs の一覧。docs/development.md の「docs の書き方」の最初の項が挙げる使う人向けの docs と揃える。
#   どちらかを足したらもう一方を直す。揃っているかも、引数なしで流したときにこのスクリプトが検査する
#   （docs/development.md の「使う人向けの docs（」で始まる項の、「）」までのバッククォートのパスと比べる）。
# - 当たるもの: 行のどこかにある rajikura の API のパス（`/api/` の後ろにパスが続く文字列。コードフェンスやバッククォートの中も含む）。
#   `/api/` の直前が ASCII の英数字・`_`・`.`・`-` のもの（ホスト名に続く外の URL の `https://discord.com/api/webhooks/…` や、
#   シェル変数に続く `$URL/api/jobs`。変数はホスト名と同じに扱う）、`/api` だけ（中継先の説明など）、`docs/api.md` は当たらない。
#   直前が日本語（`その下の/api/jobs` など）や空白・バッククォート・引用符・`>` のものは当たる。
# - 免除の印は作らない。当たる行は docs/api.md に移し、使う人向けの docs ではパスを書かずに docs/api.md の節へのリンクで指す。
#
# 使い方: scripts/check-user-docs.sh [検査するファイル...]   （repo のどこから呼んでもよい）
#   ファイルを渡すとそれだけを検査する（一覧の突き合わせはしない。scripts/check-user-docs_test.sh 用）。
#   引数の相対パスは、呼んだ場所ではなく repo root 基準で解決する。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

user_docs=(
  README.md
  docs/getting-started.md
  docs/cli.md
  docs/settings.md
  docs/notifications.md
  docs/web-ui.md
  docs/shows.md
  docs/recording.md
  docs/backup.md
  docs/windows.md
  docs/kikidame.md
)

rc=0
if [ "$#" -gt 0 ]; then
  files=("$@")
else
  files=("${user_docs[@]}")
  # docs/development.md の一覧と揃っているか
  line=$(grep -m1 '^- 使う人向けの docs（' docs/development.md || true)
  if [ -z "$line" ]; then
    echo "docs/development.md に「- 使う人向けの docs（」で始まる項が無い" >&2
    exit 1
  fi
  listed=$(printf '%s' "${line%%）*}" | grep -o '`[^`]*`' | tr -d '`' | LC_ALL=C sort)
  mine=$(printf '%s\n' "${user_docs[@]}" | LC_ALL=C sort)
  if [ "$listed" != "$mine" ]; then
    echo "scripts/check-user-docs.sh の一覧と docs/development.md の「使う人向けの docs」の一覧が違う:" >&2
    diff <(printf '%s\n' "$mine") <(printf '%s\n' "$listed") >&2 || true
    rc=1
  fi
fi

perl -CSDA -Mutf8 -Mstrict -Mwarnings -e '
  my $bad = 0;
  for my $file (@ARGV) {
    open(my $h, "<", $file) or die "$file: $!";
    my $n = 0;
    while (my $line = <$h>) {
      $n++; chomp $line; $line =~ s/\r$//;
      while ($line =~ m{(?<![A-Za-z0-9_.\-])(/api/[^\s`)>"\x27、,]*)}g) {
        print "$file:$n: 使う人向けの docs に API のパス: $1（docs/api.md に置く）\n"; $bad = 1;
      }
    }
    close $h;
  }
  exit($bad ? 1 : 0);
' "${files[@]}" || rc=1
exit "$rc"
