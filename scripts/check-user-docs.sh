#!/usr/bin/env bash
# 使う人向けの docs に、実装の仕様と検証の記録が書かれていないかを検査する。次の 3 つを見る。
# 当たればファイルと行を出して失敗する。API の口・欄は docs/api.md に置く（docs/development.md の「docs の書き方」）。
#   (1) 画面が内部で叩く API のパス（`/api/…`）
#   (2) API の欄の名前（docs/api.md がバッククォートで挙げる camelCase の識別子）
#   (3) 検証の記録の言い回し
#
# - 対象は下の user_docs の一覧。docs/development.md の「docs の書き方」の最初の項が挙げる使う人向けの docs と揃える。
#   どちらかを足したらもう一方を直す。揃っているかも、引数なしで流したときにこのスクリプトが検査する
#   （docs/development.md の「使う人向けの docs（」で始まる項の、「）」までのバッククォートのパスと比べる）。
# - 当たるもの (1): 行のどこかにある rajikura の API のパス（`/api/` の後ろにパスが続く文字列。コードフェンスやバッククォートの中も含む）。
#   `/api/` の直前が ASCII の英数字・`_`・`.`・`-` のもの（ホスト名に続く外の URL の `https://discord.com/api/webhooks/…` や、
#   シェル変数に続く `$URL/api/jobs`。変数はホスト名と同じに扱う）、`/api` だけ（中継先の説明など）、`docs/api.md` は当たらない。
#   直前が日本語（`その下の/api/jobs` など）や空白・バッククォート・引用符・`>` のものは当たる。
# - 当たるもの (2): 行のどこかにある、docs/api.md が単独でバッククォートに入れて挙げる camelCase の識別子（`timeFree30Excluded` など）。
#   単語の境界で照合する。バッククォートの中も地の文も含む。外の URL（`http://` か `https://` で始まる文字列）の中は見ない。
#   docs/api.md に出ない camelCase（`macOS` など）は当てない。API の欄を足したら docs/api.md に書けば、この検査も効く。
# - 当たるもの (3): 次の言い回し。
#   「確かめていない」「確かめたことはまだ無い」「未確認」「実機」「検証した」「動かして確かめ」。
#   日付（`2026-10-14`・`10月14日`）と「確かめ」「検証」「計測」「測定」が同じ行にある組。
#   日付だけの行（通知の本文の例など）と、「確かめる」「検証」だけの手順や動作の説明は当てない。
#   使う人の判断に効く制限は、事実の 1 文で書き直す（「〜することがある」「保証が無い」など）。
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
  my %fields;
  open(my $a, "<", "docs/api.md") or die "docs/api.md: $!";
  while (my $l = <$a>) { $fields{$1} = 1 while $l =~ /`([a-z][a-z0-9]*[A-Z][A-Za-z0-9]*)`/g; }
  close $a;
  my $date = qr/\d{4}-\d{2}-\d{2}|\d{1,2}月\d{1,2}日/;
  for my $file (@ARGV) {
    open(my $h, "<", $file) or die "$file: $!";
    my $n = 0;
    while (my $line = <$h>) {
      $n++; chomp $line; $line =~ s/\r$//;
      while ($line =~ m{(?<![A-Za-z0-9_.\-])(/api/[^\s`)>"\x27、,]*)}g) {
        print "$file:$n: 使う人向けの docs に API のパス: $1（docs/api.md に置く）\n"; $bad = 1;
      }
      (my $nourl = $line) =~ s{https?://[^\s`)>"\x27、,]*}{}g;
      while ($nourl =~ m{(?<![A-Za-z0-9_])([a-z][a-z0-9]*[A-Z][A-Za-z0-9]*)(?![A-Za-z0-9_])}g) {
        next unless $fields{$1};
        print "$file:$n: 使う人向けの docs に API の欄の名前: $1（docs/api.md に置く）\n"; $bad = 1;
      }
      if ($line =~ /(確かめていない|確かめたことはまだ無い|未確認|実機|検証した|動かして確かめ)/) {
        print "$file:$n: 使う人向けの docs に検証の記録: $1（使う人の判断に効く制限だけを事実の 1 文で書く）\n"; $bad = 1;
      } elsif ($line =~ $date && $line =~ /(確かめ|検証|計測|測定)/) {
        print "$file:$n: 使う人向けの docs に検証の記録: 日付と「$1」の組（使う人の判断に効く制限だけを事実の 1 文で書く）\n"; $bad = 1;
      }
    }
    close $h;
  }
  exit($bad ? 1 : 0);
' "${files[@]}" || rc=1
exit "$rc"
