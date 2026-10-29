package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeTitle は番組名を照合のために正規化する。
// NFKC（全角 → 半角）→ 空白の除去 → 大文字化。全角と半角・空白・大文字と小文字の表記の差を、番組名の食い違いに数えないため。
func NormalizeTitle(s string) string {
	s = norm.NFKC.String(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	return strings.ToUpper(s)
}

// 番組名の末尾の印（分割番組の番号や時間帯の印。用語集）の形。NFKC をかけた後の文字列で見る trailingMarkRe と、かける前の
// 文字列で見る rawTrailingMarkRe（と、web/src/guide/format.ts の trimTrailingMark）に、同じ形を入れる。
//
//   - 括弧付き（前の空白の有無を問わない）: `(N)`、`(N時台)`、`(PARTN)`（PART は大文字・小文字を問わず、数字との間の空白も許す）
//   - 時間の範囲（前の空白の有無を問わない）: `N時～N時`、分が付く `N時M分～N時M分`
//   - 括弧の無い形（前に空白があるときだけ。番組名の途中の語を落とさないため）: `PartN`・`Part N`（大文字・小文字を問わない）、
//     `Hour.N`、丸数字 `①`〜`⑳`、`N時台`（前に `火曜` などの曜日が付いてもよく、後ろに日付 `(MM/DD)` が続いてもよい）
//
// 丸数字は NFKC をかけると数字になり、空白の後の数字と見分けられないので、trailingMarkRe には入れず、StripTrailingMark が
// NFKC をかける前に circledMarkRe で落とす。
var trailingMarkRe = regexp.MustCompile(`(?:\s*(?:\((?:\d+|\d+時台|(?i:part)\s*\d+)\)|\d+時(?:\d+分)?[~〜]\d+時(?:\d+分)?)|\s+(?:(?i:part)\s*\d+|Hour\.\d+|(?:[月火水木金土日]曜)?\d+時台(?:\s*\(\d+/\d+\))?))\s*$`)

// circledMarkRe は、括弧の無い丸数字の印（`①`〜`⑳`。前に空白（全角も）があるときだけ）。
var circledMarkRe = regexp.MustCompile(`[\s\p{Zs}]+[①-⑳][\s\p{Zs}]*$`)

// StripTrailingMark は番組表の番組名の末尾の印（trailingMarkRe の形。`(N)`、`(N時台)`、` Part2`、` ①` など）を落とす。
// 全角・半角の違いは NFKC で揃えてから見るので、返す文字列も NFKC をかけたものになる。
// 印が無ければ、NFKC をかけただけの文字列を返す。
func StripTrailingMark(title string) string {
	title = circledMarkRe.ReplaceAllString(strings.TrimSpace(title), "")
	return trailingMarkRe.ReplaceAllString(norm.NFKC.String(strings.TrimSpace(title)), "")
}

// rawTrailingMarkRe は trailingMarkRe と同じ印（と丸数字）を、NFKC をかける前の文字列で見る。
// 全角の括弧・数字・英字・記号・波ダッシュ・空白も当てる。
var rawTrailingMarkRe = regexp.MustCompile(`(?:` +
	`[\s\p{Zs}]*(?:[(（](?:[0-9０-９]+|[0-9０-９]+時台|` + rawPart + `[\s\p{Zs}]*[0-9０-９]+)[)）]` +
	`|[0-9０-９]+時(?:[0-9０-９]+分)?[~〜～][0-9０-９]+時(?:[0-9０-９]+分)?)` +
	`|[\s\p{Zs}]+(?:` + rawPart + `[\s\p{Zs}]*[0-9０-９]+|[HＨ][oｏ][uｕ][rｒ][.．][0-9０-９]+|[①-⑳]` +
	`|(?:[月火水木金土日]曜)?[0-9０-９]+時台(?:[\s\p{Zs}]*[(（][0-9０-９]+[/／][0-9０-９]+[)）])?)` +
	`)[\s\p{Zs}]*$`)

// rawPart は、大文字・小文字・全角を問わない `PART`。
const rawPart = `[PpＰｐ][AaＡａ][RrＲｒ][TtＴｔ]`

// TrimTrailingMark は、番組表の番組名の末尾の印（StripTrailingMark と同じ形。全角でもよい）と前後の空白を落とす。
// StripTrailingMark と違い NFKC をかけないので、表示に使える（全角空白・全角記号は残る）。
func TrimTrailingMark(title string) string {
	title = strings.TrimFunc(title, unicode.IsSpace)
	return strings.TrimFunc(rawTrailingMarkRe.ReplaceAllString(title, ""), unicode.IsSpace)
}

// SameTitle は、番組表の番組名 airingTitle と番組の name が同じか。両方の末尾の印を落とし、
// NormalizeTitle をかけて完全一致を見る（TitleMatchesName の前方一致とは違う。ADR 0007）。
func SameTitle(airingTitle, name string) bool {
	return NormalizeTitle(StripTrailingMark(airingTitle)) == NormalizeTitle(StripTrailingMark(name))
}

// TitleMatchesName は、番組表の番組名 airingTitle が、番組の name と食い違っていないか。
// airingTitle と name の末尾の印を落とし、両方に NormalizeTitle をかけて、
// airingTitle が name で始まっていれば true。name が空なら false。
func TitleMatchesName(airingTitle, name string) bool {
	want := NormalizeTitle(StripTrailingMark(name))
	if want == "" {
		return false
	}
	return strings.HasPrefix(NormalizeTitle(StripTrailingMark(airingTitle)), want)
}

// Matches は毎週の予約が放送回に当たるか。局・曜日・開始時刻（時:分）が完全に一致すること。曜日は放送日で数える。
// 開始日（From）があれば、放送日がそれより前の放送回には当たらない（開始日の放送日からは当たる。0〜4 時台は前日の放送日）。
// 終了日（Until）があれば、放送日がそれより後の放送回には当たらない（終了日の放送日の放送回までは当たる）。
func (r Weekly) Matches(a Airing) bool {
	if r.StationID != a.StationID || r.Weekday != a.Day().Weekday() || r.Start != ClockOf(a.Start) {
		return false
	}
	return r.InDays(a.Day())
}

// Validate は毎週の予約の値が正しいか確かめる。
func (r Weekly) Validate() error {
	if r.StationID == "" {
		return errors.New("毎週の予約の局が空")
	}
	if r.Weekday < time.Sunday || r.Weekday > time.Saturday {
		return fmt.Errorf("毎週の予約の曜日 %d が範囲にない", int(r.Weekday))
	}
	if !r.From.IsZero() {
		if _, err := ParseBroadcastDay(r.From.String()); err != nil || r.From.Year < 1 || r.From.Year > 9999 {
			return fmt.Errorf("毎週の予約の開始日 %s が正しくない", r.From)
		}
	}
	if !r.Until.IsZero() {
		if _, err := ParseBroadcastDay(r.Until.String()); err != nil || r.Until.Year < 1 || r.Until.Year > 9999 {
			return fmt.Errorf("毎週の予約の終了日 %s が正しくない", r.Until)
		}
		if !r.From.IsZero() && r.Until.Before(r.From) {
			return fmt.Errorf("毎週の予約の終了日 %s が開始日 %s より前", r.Until, r.From)
		}
	}
	return r.Start.Validate()
}

// Validate は 1 回の予約の値が正しいか確かめる。
func (o Once) Validate() error {
	if o.StationID == "" {
		return errors.New("1 回の予約の局が空")
	}
	if o.Start.IsZero() {
		return errors.New("1 回の予約の開始時刻が無い")
	}
	return nil
}

// Validate は番組と、その毎週・1 回の予約の値が正しいか確かめる。毎週の予約が無い番組も正しい
// （1 回の予約だけを持つ番組）。
func (s Show) Validate() error {
	if s.Name == "" {
		return errors.New("番組の名前が空")
	}
	if s.Split != SplitNone && !s.IsSplit() {
		return fmt.Errorf("番組 %s の分割番組の設定 %d が正しくない", s.Name, int(s.Split))
	}
	for i, r := range s.Weekly {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("番組 %s の毎週の予約 %d: %w", s.Name, i+1, err)
		}
	}
	for i, o := range s.Onces {
		if err := o.Validate(); err != nil {
			return fmt.Errorf("番組 %s の 1 回の予約 %d: %w", s.Name, i+1, err)
		}
	}
	return nil
}

// Matches は番組の毎週か 1 回の予約のどれかが放送回に当たるか。1 回の予約は放送回の Key（局 + 開始時刻）の
// 一致で当たる。無効な番組は、予約を持っていても当たらない。
func (s Show) Matches(a Airing) bool {
	if s.Disabled {
		return false
	}
	for _, r := range s.Weekly {
		if r.Matches(a) {
			return true
		}
	}
	key := a.Key()
	for _, o := range s.Onces {
		if o.Key() == key {
			return true
		}
	}
	return false
}
