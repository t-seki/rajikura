package domain

import "strings"

// KeywordMatcher は、キーワードと番組表の検索が共に使う照合。
// 見る欄は放送回の番組名・出演者・説明。語は空白（全角も）で区切った語の AND で、
// どの語も 3 つの欄のどれかに含まれれば当たる。除外する語は、どれか 1 つでも 3 つの欄のどれかに含まれれば外す。
// 語も欄も NormalizeTitle で正規化してから部分一致を見る。
// 局の絞り込みは持たない（呼ぶ側で行う）。
type KeywordMatcher struct {
	words    []string // 正規化済み
	excludes []string // 正規化済み
}

// NewKeywordMatcher は、空白で区切った語 words と、任意の除外する語 excludes から照合を作る。
// 正規化して空になる語は捨てる。語が 1 つも無ければ、何にも当たらない。
func NewKeywordMatcher(words, excludes string) KeywordMatcher {
	return KeywordMatcher{words: splitNormalized(words), excludes: splitNormalized(excludes)}
}

func splitNormalized(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		if n := NormalizeTitle(f); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Empty は、当てる語が 1 つも無いとき true（何にも当たらない）。
func (m KeywordMatcher) Empty() bool { return len(m.words) == 0 }

// Match は、a が語のすべてに当たり、除外する語のどれにも当たらないとき true。
func (m KeywordMatcher) Match(a Airing) bool {
	if m.Empty() {
		return false
	}
	fields := [3]string{NormalizeTitle(a.Title), NormalizeTitle(a.Performer), NormalizeTitle(a.Description)}
	in := func(w string) bool {
		for _, f := range fields {
			if strings.Contains(f, w) {
				return true
			}
		}
		return false
	}
	for _, w := range m.words {
		if !in(w) {
			return false
		}
	}
	for _, x := range m.excludes {
		if in(x) {
			return false
		}
	}
	return true
}
