#!/usr/bin/env bash
# GLOSSARY.md の各項目の `_Avoid_:` の語が、docs/（docs/adr/ を除く）と README.md、
# および GLOSSARY.md の定義文に出てこないかを検査する。出たらファイルと行を出して失敗する。
#
# - GLOSSARY.md の定義文は、用語の行（`**用語**:`）の次の行から `_Avoid_:` の行・空行・見出しの前まで。
#   用語の行と `_Avoid_:` の行そのものは検査しない。どの項の _Avoid_ の語も対象（自分の項に限らない）。
#   引数でファイルを渡したときも、定義文の検査は常に行う。
# - 単純な部分一致。活用形や表記ゆれは拾わない。
# - _Avoid_ の語を含む、より長い用語集の用語（例: 「回」に対する「放送回」「休止回」）は、
#   照合の前に行から取り除く。
# - 1 文字の語（例: 「回」）は、直前が漢字・数字・「数字＋半角空白」のとき、または直後が漢字のときは当てない（回数・回収・6 回など）。
# - GLOSSARY.md の書式が前提: 用語の行は `**用語**:`、`_Avoid_:` の行は全角読点「、」区切り。
#   _Avoid_ の語に括弧（`（`・`(`）が入っていたら、その行を名指しして落とす（括弧の説明まで含めて 1 語になり、検査されないため）。
# - 誤検知の免除は 2 つ。
#   - 許す言い回し: scripts/glossary-allow.txt の言い回し（製品名など決まったもの）は、用語集の用語と同じく
#     照合の前に行から取り除く。1 行に 1 つ。`#` で始まる行はコメント、空行は無視。使われていなくてもエラーにしない。
#   - 語の単位の印: 行末の `<!-- glossary-ok: 語、語 -->`（全角読点「、」区切り）で、その行の書いた語だけを外す
#     （文脈で決まるもの、過去の運用の話など）。同じ行のほかの _Avoid_ の語は拾う。
#     語を書かない `<!-- glossary-ok -->` と、その行で当たらない語を書いた印（古い印・書き間違い）はエラー。
#
# 使い方: scripts/check-glossary.sh [検査するファイル...]   （repo のどこから呼んでもよい）
#   ファイルを渡すと docs/ と README.md の代わりにそのファイルを検査する（GLOSSARY.md の定義文は常に検査する）。環境変数 GLOSSARY_FILE で用語集を、GLOSSARY_ALLOW_FILE で
#   許す言い回しのファイルを差し替えられる（scripts/check-glossary_test.sh 用。既定は GLOSSARY.md、
#   scripts/glossary-allow.txt、docs/ と README.md）。
#   引数と環境変数の相対パスは、呼んだ場所ではなく repo root 基準で解決する。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

glossary=${GLOSSARY_FILE:-GLOSSARY.md}
allow=${GLOSSARY_ALLOW_FILE:-scripts/glossary-allow.txt}
files=()
if [ "$#" -gt 0 ]; then
  files=("$@")
else
  while IFS= read -r f; do files+=("$f"); done < <(
    { ls README.md 2>/dev/null || true; find docs -name '*.md' -not -path 'docs/adr/*'; } | LC_ALL=C sort
  )
fi

perl -CSDA -Mutf8 -Mstrict -Mwarnings -e '
  my ($glossary, $allow, @files) = @ARGV;
  open(my $g, "<", $glossary) or die "$glossary: $!";
  my (@terms, %avoid, @defs);
  my $in_def = 0;
  my $bad = 0;
  while (<$g>) {
    chomp; s/\r$//;
    if (/^\*\*(.+?)\*\*:/) { push @terms, $1; $in_def = 1; }
    elsif (/^_Avoid_:\s*(.+)$/) {
      my $list = $1;
      if ($list =~ /[（(]/) {
        print "$glossary:$.: _Avoid_ の語に括弧の説明は書けない（語だけを書く。説明は定義文へ）（$_）\n";
        $bad++;
      }
      $avoid{$_} = 1 for grep { length } map { s/^\s+|\s+$//gr } split /、/, $list;
      $in_def = 0;
    }
    elsif (/^\s*$/ || /^#/) { $in_def = 0; }
    elsif ($in_def) { push @defs, [$., $_]; }
  }
  close $g;
  my @words = sort keys %avoid;
  die "$glossary に _Avoid_ の語が 1 つも無い（形式が変わった？）\n" unless @words;
  my @phrases;
  open(my $a, "<", $allow) or die "$allow: $!";
  while (<$a>) {
    chomp; s/\r$//;
    next if /^\s*(#|$)/;
    push @phrases, s/^\s+|\s+$//gr;
  }
  close $a;
  my $check = sub {
    my ($f, $no, $line) = @_;
    my %ok;
    if ($line =~ s/\s*<!--\s*glossary-ok\s*(?::([^>]*?))?\s*-->\s*$//) {
      my @ok = grep { length } map { s/^\s+|\s+$//gr } split /、/, ($1 // "");
      if (!@ok) {
        print "$f:$no: 語を書かない glossary-ok は使えない（<!-- glossary-ok: 語、語 --> と書く）（$line）\n";
        $bad++;
      }
      $ok{$_} = 0 for @ok;
    }
    my $base = $line;
    $base =~ s/\Q$_\E//g for @phrases;
    for my $w (@words) {
      my $l = $base;
      for my $t (@terms) { next if $t eq $w || index($t, $w) < 0; $l =~ s/\Q$t\E//g; }
      my $re = length($w) == 1 ? qr/(?<![\p{sc=Han}\d])(?<!\d )\Q$w\E(?!\p{sc=Han})/ : qr/\Q$w\E/;
      next unless $l =~ $re;
      if (exists $ok{$w}) { $ok{$w} = 1; next; }
      print "$f:$no: 「$w」は用語集の _Avoid_ （$line）\n";
      $bad++;
    }
    for my $w (sort keys %ok) {
      next if $ok{$w};
      print "$f:$no: glossary-ok の「$w」はこの行で当たらない（古い印か書き間違い）（$line）\n";
      $bad++;
    }
  };
  # 用語集そのものの定義文（用語の行の次から _Avoid_ の行の前まで）も、同じきまりで検査する。
  $check->($glossary, @$_) for @defs;
  for my $f (@files) {
    open(my $in, "<", $f) or die "$f: $!";
    while (my $line = <$in>) {
      chomp $line; $line =~ s/\r$//;
      $check->($f, $., $line);
    }
    close $in;
  }
  if ($bad) {
    print STDERR "$bad 件。正しい使い方なら行末に <!-- glossary-ok: 語 --> を付ける（決まった言い回しは scripts/glossary-allow.txt）。\n";
    exit 1;
  }
' "$glossary" "$allow" "${files[@]}"
