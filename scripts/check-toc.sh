#!/usr/bin/env bash
# docs の目次（ファイルの頭の、本文の `## ` の見出しへのリンクの箇条書き）が、本文の `## ` の見出しと合うかを検査する。
#
# - 対象は docs/*.md（docs/adr/ は目次を持たないので見ない）。引数でファイルを渡せば、それだけを見る（scripts/check-toc_test.sh 用）。
# - 目次とみなすのは、最初の `## ` の見出しより前にある、字下げの無い `- [表示](#アンカー)` の行。
#   1 行も無いファイル（目次を持たない docs）は見ない。字下げした項（`###` 以下を並べた入れ子）は突き合わせない。
# - 本文の見出しは、コードブロック（``` で囲んだ範囲）の外の `## ` の行。
# - 失敗にするのは次の 3 つ。ファイルと食い違った項（目次の何番目か、見出しは何か）を出す。
#   - 目次の項の数が `## ` の見出しの数と違う（項の抜け・余り）。
#   - N 番目の項のアンカーが、N 番目の見出しのアンカーと違う（順の食い違い・アンカーが見出しに合わない）。
#     アンカーは GitHub の作り方（小文字にし、文字・数字・_・-・空白以外を除き、空白を - にする。同じ見出しが続けば -1, -2 を付ける）で見出しから作る。
#   - N 番目の項の表示が、N 番目の見出しの文字と違う（行末の <!-- … --> は除いて比べる）。
#
# 使い方: scripts/check-toc.sh [ファイル…]   （repo のどこから呼んでもよい）
#   終了コード: 0 は通った、1 は目次の食い違い。
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
if [ "$#" -gt 0 ]; then
  files=("$@")
else
  files=("$root"/docs/*.md)
fi

perl -CSDA -e '
use strict; use warnings; use utf8;
my $bad = 0;
sub slug {
  my ($t) = @_;
  $t =~ s/`//g;
  $t = lc $t;
  $t =~ s/[^\p{L}\p{N}\p{M}_\- ]//g;
  $t =~ s/ /-/g;
  return $t;
}
sub strip { my ($t) = @_; $t =~ s/\s*<!--.*?-->\s*$//; $t =~ s/\s+$//; return $t; }
for my $f (@ARGV) {
  open(my $fh, "<", $f) or do { print STDERR "$f を開けない\n"; $bad = 1; next };
  my (@toc, @head, %seen);
  my ($infence, $seenhead) = (0, 0);
  while (my $l = <$fh>) {
    chomp $l; $l =~ s/\r$//;
    if ($l =~ /^\s*(```|~~~)/) { $infence = !$infence; next; }
    next if $infence;
    if ($l =~ /^## (.*)$/) {
      $seenhead = 1;
      my $text = strip($1);
      my $s = slug($text);
      my $n = $seen{$s}++;
      push @head, [$text, $n ? "$s-$n" : $s];
      next;
    }
    if (!$seenhead && $l =~ /^- \[(.*?)\]\(#([^)]*)\)\s*(?:<!--.*?-->)?\s*$/) {
      push @toc, [$1, $2];
    }
  }
  close $fh;
  next unless @toc;
  if (@toc != @head) {
    printf STDERR "%s: 目次の項が %d 個、本文の ## の見出しが %d 個で数が合わない\n", $f, scalar(@toc), scalar(@head);
    $bad = 1;
  }
  my $n = @toc < @head ? @toc : @head;
  for my $i (0 .. $n - 1) {
    my ($tt, $ta) = @{$toc[$i]};
    my ($ht, $ha) = @{$head[$i]};
    if ($ta ne $ha) {
      printf STDERR "%s: 目次の %d 番目「%s」のアンカー #%s が、本文の %d 番目の見出し「%s」（#%s）と合わない\n", $f, $i + 1, $tt, $ta, $i + 1, $ht, $ha;
      $bad = 1;
    } elsif ($tt ne $ht) {
      printf STDERR "%s: 目次の %d 番目の表示「%s」が、本文の見出し「%s」と違う\n", $f, $i + 1, $tt, $ht;
      $bad = 1;
    }
  }
  if (@toc > @head) {
    printf STDERR "%s: 目次の余り: %s\n", $f, join("、", map { "「$_->[0]」" } @toc[$n .. $#toc]);
  } elsif (@head > @toc) {
    printf STDERR "%s: 目次に無い見出し: %s\n", $f, join("、", map { "「$_->[0]」" } @head[$n .. $#head]);
  }
}
exit($bad ? 1 : 0);
' -- "${files[@]}"
rc=$?
if [ "$rc" -eq 0 ]; then
  echo "目次と本文の ## の見出しが合っている（${#files[@]} ファイル）"
fi
exit "$rc"
