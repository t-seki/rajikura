#!/usr/bin/env bash
# 置き換え済み（Obsoleted by）の ADR を指す参照が、コードのコメントと docs に残っていないかを検査する。
# 残っていればファイルと行と置き換え先を出して失敗する。置き換えたら、参照を置き換え先へ向け直す（docs/development.md の「ADR の決まり」）。
#
# - 置き換え済みの ADR: docs/adr/README.md の表の行（`| [番号](…) |` で始まる行）に `Obsoleted by N` を持つもの。
#   番号と置き換え先はその索引から集める（スクリプトは番号を持たない）。`Updated by N`（一部だけの更新）の ADR を指す参照は通す。
# - 対象:
#   - cmd/・internal/・web/src/・scripts/ のソースのコメント。.go・.ts・.tsx・.js・.mjs は `//`（直前が行頭か空白）から行末までと、
#     `/*` か `*` で始まる行。.sql は `--` から行末まで。.sh は行頭（空白の後）の `#` の行。文字列リテラルの中は読まない。
#   - docs/（docs/adr/ を除く）の *.md、README.md、GLOSSARY.md の全行。
# - 対象外: docs/adr/ の中（古い ADR の本文と、決めた当時の文脈で古い ADR を指す ADR の本文は直さない）。
#   `.` で始まるディレクトリと node_modules の配下。
# - 参照の形:
#   - パス: `docs/adr/<番号>-….md`、`adr/<番号>-….md`（相対リンクの `../adr/…` や `(adr/…)` も当たる）。
#   - 「ADR <番号>」。「ADR 甲・乙」のように「・」「、」「,」で並べた 2 つめ以降の番号も当てる。
# - 免除は、決めた当時の文脈で古い ADR を指す行（internal/store/migrations/ の SQL のコメント）だけに使う。行末の `adr-ref-ok: 0030` のように、
#   印の名前の後ろに「:」と 4 桁の番号（複数は全角読点「、」区切り。「,」も読む）を書くと、その行のその番号だけを外す。
#   その行で当たらない番号を書いた印（古い印・書き間違い）、番号を書かない印、4 桁の番号でないものを書いた印はエラー
#   （check-code-refs.sh の code-ref-ok と同じ）。
# - 案内する置き換え先は、`Obsoleted by` の連鎖をたどった最後の ADR（0025 → 0031 → 0036 なら 0036）。
# - 索引の表の行が 1 つも読めないとき、`Obsoleted by` を持つ表の行から番号か置き換え先が読めないときは、
#   索引の書き方が変わったとみて失敗する（黙って通さない）。
#   `Updated by` の ADR を指す行には印を付けない（当たらないのでエラーになる）。
#
# 使い方: scripts/check-adr-refs.sh   （repo のどこから呼んでもよい）
#   環境変数 ADR_REFS_ROOT で検査する置き場を差し替えられる（scripts/check-adr-refs_test.sh 用。既定は repo root）。
#   終了コード: 0 は通った、1 は置き換え済みの ADR を指す参照がある、または索引が読めない。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
code_root=${ADR_REFS_ROOT:-$root}

perl -CSDA -Mutf8 -Mstrict -Mwarnings -e '
  use File::Find;
  my ($root) = @ARGV;
  my $index = "$root/docs/adr/README.md";
  open(my $ih, "<:encoding(UTF-8)", $index) or do { print "docs/adr/README.md を読めない（索引が無い）\n"; exit 1; };
  my %next;
  my ($rows, $broken) = (0, 0);
  while (<$ih>) {
    s/\r$//;
    if (/^\|\s*\[(\d{4})\]/) {
      my $n = $1;
      $rows++;
      if (/Obsoleted by (\d{4})\b/) { $next{$n} = $1; }
      elsif (/Obsoleted by/) { print "docs/adr/README.md:$.: Obsoleted by の置き換え先の番号を読めない\n"; $broken = 1; }
    } elsif (/^\|/ && /Obsoleted by/) {
      print "docs/adr/README.md:$.: Obsoleted by を持つ表の行の番号を読めない\n"; $broken = 1;
    }
  }
  close $ih;
  if (!$rows) { print "docs/adr/README.md の索引の表の行（| [番号](…) |）を 1 つも読めない\n"; exit 1; }
  exit 1 if $broken;
  exit 0 unless %next;
  my %last;
  for my $n (keys %next) {
    my ($m, %been) = ($next{$n});
    while (exists $next{$m} && !$been{$m}++) { $m = $next{$m}; }
    $last{$n} = $m;
  }

  my @files;
  my $skip = sub { $_[0] =~ m{(^|/)(\.[^/]+|node_modules)(/|$)} };
  for my $top (qw(cmd internal web/src scripts docs)) {
    next unless -d "$root/$top";
    find({ no_chdir => 1, wanted => sub {
      (my $rel = $File::Find::name) =~ s{^\Q$root\E/}{};
      return if $skip->($rel) || !-f $File::Find::name;
      return if $rel =~ m{^docs/adr/};
      if ($top eq "docs") { push @files, $rel if $rel =~ /\.md$/; }
      else { push @files, $rel if $rel =~ /\.(go|ts|tsx|js|mjs|sql|sh)$/; }
    } }, "$root/$top");
  }
  for my $f (qw(README.md GLOSSARY.md)) { push @files, $f if -f "$root/$f"; }

  my $bad = 0;
  for my $rel (sort @files) {
    open(my $h, "<:encoding(UTF-8)", "$root/$rel") or die "$rel: $!";
    my $n = 0;
    while (my $line = <$h>) {
      $n++; chomp $line; $line =~ s/\r$//;
      my $text = $line;
      if ($rel =~ /\.md$/) { }
      elsif ($rel =~ /\.sql$/) { next unless $text =~ /(?:^|\s)(--.*)$/; $text = $1; }
      elsif ($rel =~ /\.sh$/) { next unless $text =~ /^\s*#/; }
      else {
        if ($text =~ m{^\s*(?:/\*|\*)}) { }
        elsif ($text =~ m{(?:^|\s)(//.*)$}) { $text = $1; }
        else { next; }
      }
      my %ok; my $mark = 0;
      if ($text =~ s/(?:^|\s+)adr-ref-ok:?\s*(.*?)\s*$//) {
        $mark = 1;
        for my $w (grep { length } split /\s*[、,]\s*/, $1) {
          $mark = 2;
          if ($w =~ /^\d{4}$/) { $ok{$w} = 0; }
          else { print "$rel:$n: 印 adr-ref-ok の「$w」は 4 桁の番号でない\n"; $bad = 1; }
        }
      }
      my %seen;
      while ($text =~ m{adr/(\d{4})-}g) { $seen{$1} = 1; }
      while ($text =~ /ADR\s*(\d{4}(?:\s*[・、,]\s*\d{4})*)/g) { $seen{$_} = 1 for $1 =~ /(\d{4})/g; }
      for my $num (sort keys %seen) {
        next unless exists $next{$num};
        if (exists $ok{$num}) { $ok{$num} = 1; next; }
        print "$rel:$n: ADR $num は置き換え済み。ADR $last{$num} へ向ける（Obsoleted by $next{$num}）\n"; $bad = 1;
      }
      for my $w (sort keys %ok) { next if $ok{$w}; print "$rel:$n: 印 adr-ref-ok の「$w」はこの行で当たらない\n"; $bad = 1; }
      if ($mark == 1) { print "$rel:$n: 番号を書かない adr-ref-ok の印\n"; $bad = 1; }
    }
    close $h;
  }
  exit($bad ? 1 : 0);
' "$code_root"
