#!/usr/bin/env bash
# Go のソース（*.go）のコメントが名指しするファイルの名前が、実在するかを検査する。
# 実在しなければファイルと行を出して失敗する。docs と README.md の名指しは scripts/check-code-refs.sh が見る（入力が md と Go で違うので別のスクリプトにした）。
#
# - 読むコメント: 行頭の `//`、またはコードの後ろに空白を挟んだ `//` から行末まで。`/* */` は読まない。
#   `http://` のように空白の無い `//` はコメントとみなさない。
# - 名指し（`.go` で終わる名前。バッククォートの有無は問わない）の照合:
#   - `internal/…`・`cmd/…` で始まるパス: repo root からのパスとして実在すること。
#   - スラッシュの無い名前（`main_windows.go` など）: そのコメントがあるファイルと同じディレクトリに実在すること。
#   - それ以外のパス（`a/b.go`）、`*` や `{` を含む名前（`*_test.go` などのパターン）、`_` や `-` で始まる断片は対象外。
# - 限界（見ない形）:
#   - `.` で始まるディレクトリと `node_modules` の配下は読まない（`vendor/` は読む）。
#   - 名前は ASCII の英数字・`_`・`-` だけ。日本語が直前・直後に空白なしで付いていても名前だけを拾う。
#   - 別のパッケージのファイルをスラッシュ無しの名前で引くコメントは、同じディレクトリに無いので落ちる。パスで書くか、免除する。
#   - 文字列リテラルの中の ` // …`（空白を挟んだ `//`）はコメントとして読む。
#   - 関数や型の名前の名指しは見ない。
# - 誤検知の免除は、同じ行のコメントの末尾の `code-ref-ok: 名指し、名指し`（全角読点「、」区切り）で、その行の書いた名指しだけを外す。
#   その行で当たらない名指しを書いた印（古い印・書き間違い）と、名指しの無い印はエラー（check-code-refs.sh の code-ref-ok と同じ）。
#
# 使い方: scripts/check-go-comment-refs.sh   （repo のどこから呼んでもよい）
#   環境変数 GO_COMMENT_REFS_ROOT で検査する置き場を差し替えられる（scripts/check-go-comment-refs_test.sh 用。既定は repo root）。
#   終了コード: 0 は通った、1 は実在しない名指しか印の誤り、または .go のファイルが 1 つも無い（対象が拾えていない）。
#   CRLF の行も LF と同じに読む（行末の CR を落とす）。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
code_root=${GO_COMMENT_REFS_ROOT:-$root}

perl -CSDA -Mutf8 -Mstrict -Mwarnings -e '
  use File::Find;
  use File::Basename;
  my ($code_root) = @ARGV;
  my @go;
  find({ no_chdir => 1, wanted => sub {
    (my $rel = $File::Find::name) =~ s{^\Q$code_root\E/?}{};
    return if $rel =~ m{(^|/)(\.[^/]+|node_modules)/};
    push @go, $rel if /\.go$/ && -f $File::Find::name;
  } }, $code_root);
  my $bad = 0;
  if (!@go) { print "$code_root に .go のファイルが 1 つも無い（検査の対象が拾えていない）\n"; exit 1; }
  for my $rel (sort @go) {
    open(my $h, "<:encoding(UTF-8)", "$code_root/$rel") or die "$rel: $!";
    my $dir = dirname($rel); $dir = "" if $dir eq ".";
    my $n = 0;
    while (my $line = <$h>) {
      $n++; chomp $line; $line =~ s/\r$//;
      next unless $line =~ m{(?:^|\s)(//.*)$};
      my $c = $1;
      my %ok; my $mark = 0;
      if ($c =~ s/\s*code-ref-ok:?\s*(.*?)\s*$//) { $mark = 1; $ok{$_} = 0 for grep { length } map { s/^\s+|\s+$//gr } split /、/, $1; }
      # 境界と名前の本体は ASCII だけ（-CSDA で \w が日本語に当たり、「のgone.go」を名前に取ったり「gone.goを」を見逃したりするため）
      while ($c =~ m{(?<![A-Za-z0-9_./*{}-])((?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_-]+\.go)(?![A-Za-z0-9_])}g) {
        my $name = $1;
        next if $name =~ m{^[_-]};
        my $missing;
        if ($name =~ m{^(?:internal|cmd)/}) { $missing = !-e "$code_root/$name"; }
        elsif ($name !~ m{/}) { $missing = !-e "$code_root/" . ($dir eq "" ? "" : "$dir/") . $name; }
        else { next; }
        next unless $missing;
        if (exists $ok{$name}) { $ok{$name} = 1; next; }
        print "$rel:$n: 実在しないファイルの名指し: $name\n"; $bad = 1;
      }
      for my $w (sort keys %ok) { next if $ok{$w}; print "$rel:$n: 印 code-ref-ok の「$w」はこの行で当たらない\n"; $bad = 1; }
      if ($mark && !%ok) { print "$rel:$n: 名指しを書かない code-ref-ok の印\n"; $bad = 1; }
    }
    close $h;
  }
  exit($bad ? 1 : 0);
' "$code_root"
