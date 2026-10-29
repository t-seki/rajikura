#!/usr/bin/env bash
# 「足したら直す列挙」（docs/development.md の「## 足したら直す列挙」の節）の印を検査する。
# 印は、列挙の項が直す場所のコメントに置く「足したら直す: <項の名前>」（Go・TS は //、Dockerfile は #、md は行末の <!-- -->。
# コメントの書き方は問わないが、Go・TS の行だけのコメントの印はブロックの途中に置かない。下の失敗の条件）。印の名前の打ち間違いと、改名で印が切れたものを止める。
#
# - 項の名前は、節の中の箇条（`- ` で始まる行。字下げした箇条も）のバッククォートの中の「足したら直す: <名前>」から読む。
#   箇条でない段落の書き方の説明は読まない。1 つも読めなければ失敗する（節の形が変わった）。
# - 数えるのは、git が追うファイル（git grep。作業ツリーの中身）の中の印。次は数えない。
#   - docs/adr/（過去の ADR は直さないので、印も置かない）
#   - docs/development.md の「## 足したら直す列挙」の節の中（次の `## ` の見出しの前まで）。ほかの節は数える
#   - このスクリプトと例文テスト（scripts/check-fix-markers_test.sh）
#   - バイナリのファイル
# - 1 行に印が複数あってよい。名前は「足したら直す:」の後から、`-->` か行の終わりまで（前後の空白は除く）。
# - 失敗にするのは次の 3 つ。
#   - 列挙のどれかの項の印が 1 つも無い。
#   - 列挙に無い名前の印がある（名前が空の印も）。ファイルと行を出す。
#   - Go・TS の行だけのコメント（`// 足したら直す: …`）の印が、コメントのブロックの途中にある。印の次の行（印だけの行は飛ばす）が、
#     文字のある `//` の行なら失敗にする。印をブロックの最後の行にするか、`//` だけの行で区切った段落に置けば通る。
#     限界: 次の行が `//go:generate`・`//nolint` などのディレクティブ（`//` の直後に空白が無い行）でも、文字のある `//` の行として落ちる。
#     印をディレクティブの直前に置かない。
#     見ない形: `/* */` のコメント、コードと同じ行の行末のコメント、Dockerfile の `#`、md の `<!-- -->`、印の前の行との関係（印の前に文字のある行が続いてよい）。
# - 項ごとの印の数の下限は持たない。印の付け忘れ（印の無い新しい場所）は見つけない。
#
# 使い方: scripts/check-fix-markers.sh   （repo のどこから呼んでもよい）
#   環境変数 FIX_MARKERS_ROOT で検査する repo を差し替えられる（scripts/check-fix-markers_test.sh 用。既定はこの repo）。
#   通ったら、項ごとの印の数を「<項の名前>: <数>」で 1 行ずつ出す。
#   終了コード: 0 は通った、1 は印か列挙の誤り、2 は git grep が失敗した。
set -euo pipefail

root=${FIX_MARKERS_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}
cd "$root"
doc=docs/development.md
if [ ! -f "$doc" ]; then
  echo "$doc が無い" >&2
  exit 1
fi

set +e
hits=$(git -c core.quotepath=off grep -n -I -e '足したら直す:' -- . \
  ':(exclude)docs/adr/' ':(exclude)scripts/check-fix-markers.sh' ':(exclude)scripts/check-fix-markers_test.sh')
rc=$?
set -e
# git grep は、当たりが無ければ 1、誤りなら 2 以上を返す
if [ "$rc" -gt 1 ]; then
  echo "git grep が失敗した（終了コード $rc）" >&2
  exit 2
fi

printf '%s\n' "$hits" | perl -CSDA -Mutf8 -Mstrict -Mwarnings -e '
  my ($doc) = @ARGV;
  open(my $h, "<", $doc) or die "$doc: $!";
  my ($in, $seen) = (0, 0);
  my (%skip, %count, @names);
  while (my $line = <$h>) {
    chomp $line; $line =~ s/\r$//;
    if ($line =~ /^## /) {
      $in = $line =~ /^## 足したら直す列挙\s*$/ ? 1 : 0;
      $seen ||= $in;
    }
    next unless $in;
    $skip{$.} = 1;
    next unless $line =~ /^\s*- /;
    while ($line =~ /`足したら直す:\s*([^`]*?)\s*`/g) {
      my $n = $1;
      next if !length($n) || exists $count{$n};
      $count{$n} = 0;
      push @names, $n;
    }
  }
  close $h;
  if (!$seen) { print "$doc に「## 足したら直す列挙」の節が無い\n"; exit 1; }
  if (!@names) { print "$doc の「## 足したら直す列挙」の箇条から、項の名前が 1 つも読めない（形が変わった？）\n"; exit 1; }
  my $bad = 0;
  my %lines;
  # Go・TS の行だけのコメントの印が、コメントのブロックの途中にあるか（次の行以降、印だけの行を飛ばした先が、文字のある // の行）
  my $mid = sub {
    my ($f, $no) = @_;
    my $ls = $lines{$f};
    if (!$ls) {
      open(my $fh, "<", $f) or return 0;
      my @a = map { s/\r?\n$//r } <$fh>;
      close $fh;
      $ls = $lines{$f} = \@a;
    }
    for (my $i = $no; $i < @$ls; $i++) {
      my $t = $ls->[$i];
      next if $t =~ m{^\s*//\s*足したら直す:};
      return $t =~ m{^\s*//\s*\S} ? 1 : 0;
    }
    return 0;
  };
  while (my $l = <STDIN>) {
    chomp $l; $l =~ s/\r$//;
    next unless length $l;
    my ($f, $no, $text) = $l =~ /^(.*?):(\d+):(.*)$/ or next;
    next if $f eq $doc && $skip{$no};
    if ($f =~ /\.(?:go|ts|tsx)$/ && $text =~ m{^\s*//\s*足したら直す:} && $mid->($f, $no)) {
      print "$f:$no: 印がコメントのブロックの途中にある（印は、ブロックの最後の行か、// だけの行で区切った段落に置く）\n";
      $bad++;
    }
    while ($text =~ /足したら直す:\s*(.*?)\s*(?:-->|$)/g) {
      my $n = $1;
      if (exists $count{$n}) { $count{$n}++; next; }
      print "$f:$no: 列挙に無い項の名前の印: 「$n」\n";
      $bad++;
    }
  }
  for my $n (@names) {
    next if $count{$n};
    print "$doc: 項「$n」の印が 1 つも無い\n";
    $bad++;
  }
  if ($bad) {
    print STDERR "$bad 件。印の名前は $doc の「足したら直す列挙」の項の名前に合わせる。\n";
    exit 1;
  }
  print "$_: $count{$_}\n" for @names;
' "$doc"
