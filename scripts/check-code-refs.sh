#!/usr/bin/env bash
# docs/（docs/adr/ を含む）と README.md がバッククォートで名指しするコードのパスと名前が、実在するかを検査する。
# 実在しなければファイルと行を出して失敗する。
#
# - パス: `internal/…` か `cmd/…` で始まるバッククォートの中身。ファイルかディレクトリが実在すること。
#   末尾の `/` と `:行番号` は外して照合する。`...` `…` `*` `{` を含むもの（パターン・省略）は対象外。
# - 型.メソッド: `Store.ListJobs` `*Store.ListJobs` `Store.ListJobs()` の形の Go の名前。
#   Go のソース（*.go）に `func (… *Store) ListJobs(` か、`type Store struct` の中の `ListJobs` のフィールドが実在すること。
#   小文字で始まる名前（`time.Now` など）と、型の付かない関数名だけの名指しは対象外（誤検知が多いため）。
# - 行をまたぐバッククォートと、コードフェンス（```）の中は読まない。
# - 誤検知の免除は、行末の `<!-- code-ref-ok: 名指し、名指し -->`（全角読点「、」区切り）で、その行の書いた名指しだけを外す。
#   その行で当たらない名指しを書いた印（古い印・書き間違い）はエラー。
#
# 使い方: scripts/check-code-refs.sh [検査するファイル...]   （repo のどこから呼んでもよい）
#   ファイルを渡すとそれだけを検査する（既定は docs/ 全体と README.md）。環境変数 CODE_REFS_ROOT でコードを探す
#   ルートを差し替えられる（scripts/check-code-refs_test.sh 用。既定は repo root）。
#   引数の相対パスは、呼んだ場所ではなく repo root 基準で解決する。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
code_root=${CODE_REFS_ROOT:-$root}

files=()
if [ "$#" -gt 0 ]; then
  files=("$@")
else
  while IFS= read -r f; do files+=("$f"); done < <(
    { ls README.md 2>/dev/null || true; find docs -name '*.md'; } | LC_ALL=C sort
  )
fi

perl -CSDA -Mutf8 -Mstrict -Mwarnings -e '
  use File::Find;
  my ($code_root, @files) = @ARGV;
  # Go のソースから、型ごとのメソッドとフィールドを集める
  my %member;
  my @go;
  find({ no_chdir => 1, wanted => sub {
    (my $rel = $File::Find::name) =~ s{^\Q$code_root\E/?}{};
    return if $rel =~ m{(^|/)(\.[a-z]+|node_modules)(/|$)};
    push @go, $File::Find::name if /\.go$/;
  } }, $code_root);
  for my $f (@go) {
    open(my $h, "<:encoding(UTF-8)", $f) or next;
    my $in;
    while (<$h>) {
      if (/^func\s*\(\s*(?:\w+\s+)?\*?\s*(\w+)(?:\[[^\]]*\])?\s*\)\s*(\w+)\s*[(\[]/) { $member{$1}{$2} = 1; next; }
      if (/^type\s+(\w+)\s+struct\s*\{\s*$/) { $in = $1; next; }
      if (defined $in) {
        if (/^\}/) { undef $in; next; }
        if (/^\s+(\w+(?:\s*,\s*\w+)*)\s+\S/) { $member{$in}{$_} = 1 for split /\s*,\s*/, $1; }
      }
    }
    close $h;
  }
  my $bad = 0;
  for my $file (@files) {
    open(my $h, "<", $file) or die "$file: $!";
    my ($n, $fence) = (0, 0);
    while (my $line = <$h>) {
      $n++; chomp $line; $line =~ s/\r$//;
      if ($line =~ /^\s*```/) { $fence = !$fence; next; }
      next if $fence;
      my %ok; my $mark = 0;
      if ($line =~ s/\s*<!--\s*code-ref-ok:?\s*(.*?)\s*-->\s*$//) { $mark = 1; $ok{$_} = 0 for grep { length } map { s/^\s+|\s+$//gr } split /、/, $1; }
      my $text = $line;
      while ($text =~ /`([^`]+)`/g) {
        my $span = $1; my $missing = 0;
        if ($span =~ m{^((?:internal|cmd)/[^\s`]*?)(?::\d+)?$} && $span !~ /\.\.\.|…|[*{]/) {
          (my $p = $1) =~ s{/$}{};
          $missing = !-e "$code_root/$p";
        } elsif ($span =~ /^\*?([A-Z]\w*)\.([A-Z]\w*)(?:\(.*\))?$/) {
          $missing = !$member{$1}{$2};
        } else { next; }
        next unless $missing;
        if (exists $ok{$span}) { $ok{$span} = 1; next; }
        print "$file:$n: 実在しない名指し: $span\n"; $bad = 1;
      }
      for my $w (sort keys %ok) { next if $ok{$w}; print "$file:$n: 印 code-ref-ok の「$w」はこの行で当たらない\n"; $bad = 1; }
      if ($mark && !%ok) { print "$file:$n: 名指しを書かない code-ref-ok の印\n"; $bad = 1; }
    }
    close $h;
  }
  exit($bad ? 1 : 0);
' "$code_root" "${files[@]}"
