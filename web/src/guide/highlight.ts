// 検索の結果の強調。語ごとに、サーバー（domain.KeywordMatcher。domain.NormalizeTitle）と同じ手順（NFKC・空白の除去・大文字化）で、当たった部分を探す。
// 細かい違いは残る（`ß` の大文字化、`\s` と Go の unicode.IsSpace が違う U+FEFF・U+0085）。強調が出ないだけで、検索の結果は変わらない。

const norm = (s: string) => s.normalize("NFKC").replace(/\s+/gu, "").toUpperCase();

/** 書記素のまとまり（半角カナ + 半角の濁点、「か」+ 結合濁点のように、NFKC で前後と合成される列は 1 つにまとまる）。 */
function pieces(text: string): string[] {
  if (typeof Intl !== "undefined" && "Segmenter" in Intl) {
    return Array.from(new Intl.Segmenter("ja", { granularity: "grapheme" }).segment(text), (s) => s.segment);
  }
  return Array.from(text);
}

/**
 * text のうち query の語（空白で区切った語。サーバーの domain.KeywordMatcher と同じ）に当たった部分を、
 * [start, end) の組（text の添字）で返す。語ごとに探し、重なる・隣り合う範囲は 1 つにまとめる。
 */
export function matchRanges(text: string, query: string): [number, number][] {
  const words = query.split(/\s+/u).filter((w) => norm(w) !== "");
  if (words.length === 0) return [];
  const all = words.flatMap((w) => wordRanges(text, w)).sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const out: [number, number][] = [];
  for (const [s, e] of all) {
    const last = out[out.length - 1];
    if (last && s <= last[1]) last[1] = Math.max(last[1], e);
    else out.push([s, e]);
  }
  return out;
}

/**
 * text のうち 1 つの語 query に当たった部分を [start, end) の組（text の添字）で返す。
 * 正規化は書記素のまとまりごとにかけ、正規化した文字が元のどのまとまりから来たかを覚えて、位置を戻す。
 */
function wordRanges(text: string, query: string): [number, number][] {
  const want = norm(query);
  if (!want) return [];
  let hay = "";
  const from: number[] = []; // hay の i 文字目が text のどこから来たか
  const to: number[] = []; // 同じく、どこまでか
  let pos = 0;
  for (const piece of pieces(text)) {
    const n = norm(piece);
    for (let k = 0; k < n.length; k++) {
      from.push(pos);
      to.push(pos + piece.length);
    }
    hay += n;
    pos += piece.length;
  }
  const out: [number, number][] = [];
  for (let i = hay.indexOf(want); i >= 0; i = hay.indexOf(want, i + want.length)) {
    out.push([from[i], to[i + want.length - 1]]);
  }
  return out;
}
