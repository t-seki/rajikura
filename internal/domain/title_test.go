package domain

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// trailingMarkCases は、番組名の末尾の印の例。web/src/guide/GuidePage.test.tsx の同じ名前の表と同じ例にする。
// want は TrimTrailingMark の結果（印でなければ、前後の空白を落としただけ）。
var trailingMarkCases = []struct {
	title, want string
	mark        bool
}{
	// 括弧付きの N時台（全角・半角）
	{"ナイツ ザ・ラジオショー（13時台）", "ナイツ ザ・ラジオショー", true},
	{"ナイツ ザ・ラジオショー(１４時台)", "ナイツ ザ・ラジオショー", true},
	// 括弧付きの PARTN（大文字・小文字・全角、数字との間の空白）
	{"STEP ONE(PART1)", "STEP ONE", true},
	{"STEP ONE （ｐａｒｔ　２）", "STEP ONE", true},
	{"STEP ONE(Part 3)", "STEP ONE", true},
	// 括弧の無い PartN（前に空白があるときだけ）
	{"垣花正 あなたとハッピー！ Part2", "垣花正 あなたとハッピー！", true},
	{"Good Luck！Morning！ Part1", "Good Luck！Morning！", true},
	{"番組 PART 2", "番組", true},
	{"番組　ｐａｒｔ２", "番組", true},
	// Hour.N
	{"AWAKE Hour.1", "AWAKE", true},
	{"AWAKE　Ｈｏｕｒ．２", "AWAKE", true},
	// 丸数字
	{"HAPPYパンチ！　①", "HAPPYパンチ！", true},
	{"HAPPYパンチ！ ⑳", "HAPPYパンチ！", true},
	// 括弧の無い N時台。前に曜日、後ろに日付が付いてもよい
	{"ふんわり　火曜ＭＣは木村祐一さん　８時台（１０／２０）", "ふんわり　火曜ＭＣは木村祐一さん", true},
	{"ふんわり「おひる、こんなんどう？スペシャル」　９時台", "ふんわり「おひる、こんなんどう？スペシャル」", true},
	{"ふんわり「おひる、こんなんどう？スペシャル」　１０時台", "ふんわり「おひる、こんなんどう？スペシャル」", true},
	{"ふんわり　まいにちリクエスト♪　火曜１１時台（１０／２０）", "ふんわり　まいにちリクエスト♪", true},
	{"番組 8時台 (10/20)", "番組", true},
	// 前からある形
	{"パンサー向井の#ふらっと (1)", "パンサー向井の#ふらっと", true},
	{"ふらっと １２時３０分〜１２時５５分", "ふらっと", true},
	// 印にしないもの
	{"番組(10/20)", "番組(10/20)", false},
	{"番組 （１０／２０）", "番組 （１０／２０）", false},
	{"XPart2", "XPart2", false},
	{"番組Part2", "番組Part2", false},
	{"Part2の番組", "Part2の番組", false},
	{"番組 Part2 スペシャル", "番組 Part2 スペシャル", false},
	{"13時台の番組", "13時台の番組", false},
	{"番組（13時台）スペシャル", "番組（13時台）スペシャル", false},
	{"HAPPYパンチ！①", "HAPPYパンチ！①", false},
	{"AWAKEHour.1", "AWAKEHour.1", false},
	{"番組 2", "番組 2", false},
	{"SCHOOL OF LOCK!", "SCHOOL OF LOCK!", false},
	{"垣花正 あなたとハッピー！", "垣花正 あなたとハッピー！", false},
}

func TestTrailingMarkCases(t *testing.T) {
	for _, c := range trailingMarkCases {
		if got := TrimTrailingMark(c.title); got != c.want {
			t.Errorf("TrimTrailingMark(%q) = %q, want %q", c.title, got, c.want)
		}
		if got := HasTrailingMark(c.title); got != c.mark {
			t.Errorf("HasTrailingMark(%q) = %v, want %v", c.title, got, c.mark)
		}
		// NFKC をかけてから見る StripTrailingMark も、同じ印を落とす（印でなければ NFKC をかけただけ）。
		if got, want := StripTrailingMark(c.title), norm.NFKC.String(c.want); got != want {
			t.Errorf("StripTrailingMark(%q) = %q, want %q", c.title, got, want)
		}
	}
}

func TestTitleMatchesName(t *testing.T) {
	tests := []struct {
		title, name string
		want        bool
	}{
		{"パンサー向井の#ふらっと (1)", "パンサー向井の#ふらっと", true},
		{"VIVANT悪役会議室", "VIVANT 悪役会議室", true},
		{"オールナイトニッポン0(ZERO) feat. YAMANO BIG BAND JAZZ CONTEST", "オールナイトニッポン0(ZERO)", true},
		{"オールナイトニッポン0(ZERO)　決戦！お笑い有楽城～ラジオトークの陣～", "オールナイトニッポン0(ZERO)", true},
		{"イースト駅前クリニックpresents川島明のねごと", "スカルプDpresents川島明のねごと", false},
		{"紅しょうが稲田とレインボー池田のチャリで30分", "パンサー向井のチャリで30分", false},
		{"プロ野球中継 巨人対阪神", "水曜日のダウンタウン", false},
		// 末尾の印: 全角の括弧と数字、時間の形、波ダッシュ。
		{"番組（２）", "番組", true},
		// name にも末尾の印があるときは、name 側の印も落とす。
		{"番組(1)", "番組(1)", true},
		{"番組 (2)", "番組(1)", true},
		{"別の番組(1)", "番組(1)", false},
		{"番組 10時～11時", "番組", true},
		{"番組 10時〜11時", "番組", true},
		{"", "番組", false},
		{"番組", "", false},
	}
	for _, tt := range tests {
		if got := TitleMatchesName(tt.title, tt.name); got != tt.want {
			t.Errorf("TitleMatchesName(%q, %q) = %v, want %v", tt.title, tt.name, got, tt.want)
		}
	}
}

func TestStripTrailingMark(t *testing.T) {
	for in, want := range map[string]string{
		"ふらっと (1)":           "ふらっと",
		"ふらっと(12)":           "ふらっと",
		"ふらっと 10時～11時":       "ふらっと",
		"ふらっと 12時～12時55分":    "ふらっと",
		"ふらっと 12時30分～13時":    "ふらっと",
		"ふらっと １２時３０分〜１２時５５分": "ふらっと",
		"ふらっと(ZERO)":         "ふらっと(ZERO)",
		"(1)ふらっと":            "(1)ふらっと",
		"ふらっと":               "ふらっと",
	} {
		if got := StripTrailingMark(in); got != want {
			t.Errorf("StripTrailingMark(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimTrailingMark(t *testing.T) {
	for in, want := range map[string]string{
		"ふらっと (1)":           "ふらっと",
		"ふらっと（１２）":           "ふらっと",
		"ふらっと　(1)":           "ふらっと",
		"ふらっと 10時～11時":       "ふらっと",
		"ふらっと １０時〜１１時":       "ふらっと",
		"ふらっと 10時~11時":       "ふらっと",
		"ふらっと 12時～12時55分":    "ふらっと",
		"ふらっと 12時30分～13時":    "ふらっと",
		"ふらっと １２時３０分〜１２時５５分": "ふらっと",
		"ふらっと(ZERO)":         "ふらっと(ZERO)",
		"(1)ふらっと":            "(1)ふらっと",
		" ふらっと　":             "ふらっと",
		// 原文どおり（全角空白・全角記号を残す）。
		"オールナイトニッポン0(ZERO)　決戦！お笑い有楽城～ラジオトークの陣～": "オールナイトニッポン0(ZERO)　決戦！お笑い有楽城～ラジオトークの陣～",
		"(1)": "",
	} {
		if got := TrimTrailingMark(in); got != want {
			t.Errorf("TrimTrailingMark(%q) = %q, want %q", in, got, want)
		}
		// 比べるときの StripTrailingMark と、同じ印を落とす。
		if a, b := NormalizeTitle(TrimTrailingMark(in)), NormalizeTitle(StripTrailingMark(in)); a != b {
			t.Errorf("%q: TrimTrailingMark と StripTrailingMark の結果が食い違う: %q, %q", in, a, b)
		}
	}
}

func TestSameTitle(t *testing.T) {
	tests := []struct {
		title, name string
		want        bool
	}{
		{"パンサー向井の#ふらっと (1)", "パンサー向井の#ふらっと", true},
		{"パンサー向井の＃ふらっと（２）", "パンサー向井の#ふらっと", true},
		{"VIVANT悪役会議室", "VIVANT 悪役会議室", true},
		{"オールナイトニッポン0(ZERO)", "オールナイトニッポン0(ZERO)", true},
		// 前方一致では同じとしない。
		{"オールナイトニッポン0(ZERO) feat. YAMANO BIG BAND JAZZ CONTEST", "オールナイトニッポン0(ZERO)", false},
		{"オールナイトニッポン0(ZERO)　決戦！お笑い有楽城～ラジオトークの陣～", "オールナイトニッポン0(ZERO)", false},
		{"○○のオールナイトニッポン0(ZERO)", "オールナイトニッポン0(ZERO)", false},
		{"番組 (2)", "番組(1)", true},
	}
	for _, tt := range tests {
		if got := SameTitle(tt.title, tt.name); got != tt.want {
			t.Errorf("SameTitle(%q, %q) = %v, want %v", tt.title, tt.name, got, tt.want)
		}
	}
}
