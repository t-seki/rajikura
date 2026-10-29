package domain

import "testing"

func TestKeywordMatcher(t *testing.T) {
	a := Airing{Title: "オールナイトニッポン", Performer: "ﾊﾟﾝｻｰ 向井", Description: "ゲスト Ｔｏｋｙｏ の話"}
	cases := []struct {
		name            string
		words, excludes string
		want            bool
	}{
		{"番組名", "オールナイト", "", true},
		{"出演者（半角カナ）", "パンサー", "", true},
		{"説明だけに当たる", "tokyo", "", true},
		{"大文字小文字", "TOKYO", "", true},
		{"全角半角", "Ｔｏｋｙｏ", "", true},
		{"語を空白で区切ると、順序に関係なく別々に当たる", "井 向", "", true},
		{"AND は別の欄にまたがってよい", "オールナイト 向井 ゲスト", "", true},
		{"全角空白で区切る", "オールナイト　向井", "", true},
		{"AND の片方が外れる", "オールナイト 存在しない", "", false},
		{"除外", "オールナイト", "向井", false},
		{"除外が説明に当たる", "オールナイト", "tokyo", false},
		{"除外が当たらない", "オールナイト", "存在しない", true},
		{"除外のどれか 1 つ", "オールナイト", "存在しない 向井", false},
		{"語が空白だけ", "  　", "", false},
		{"語が無い", "", "向井", false},
	}
	for _, c := range cases {
		if got := NewKeywordMatcher(c.words, c.excludes).Match(a); got != c.want {
			t.Errorf("%s: Match(%q, %q) = %v, want %v", c.name, c.words, c.excludes, got, c.want)
		}
	}
}
