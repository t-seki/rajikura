// Package domain は rajikura のドメインの規則（放送日、毎週の予約の一致、ジョブへの展開、
// 録音期限、試行の時刻、状態の遷移）を持つ。I/O を持たない純粋なロジックで、
// 時刻に依存する関数は now を引数で受け取る。
package domain

import (
	"fmt"
	"time"
	_ "time/tzdata" // Windows とコンテナでも Asia/Tokyo を読めるようにする
)

// JST は日本時間。時刻は日本時間で扱う。
var JST = mustLoadJST()

func mustLoadJST() *time.Location {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		panic(err)
	}
	return loc
}

// dayBoundary は放送日の区切り（日本時間の 05:00）。
const dayBoundary = 5 * time.Hour

// BroadcastDay は放送日。日本時間の 05:00 で日を区切る。
type BroadcastDay struct {
	Year  int
	Month time.Month
	Day   int
}

// BroadcastDayOf は時刻 t が属する放送日を返す。0:00〜4:59 は前日の放送日に属する。
func BroadcastDayOf(t time.Time) BroadcastDay {
	s := t.In(JST).Add(-dayBoundary)
	return BroadcastDay{Year: s.Year(), Month: s.Month(), Day: s.Day()}
}

// NewBroadcastDay は年月日から放送日を作る。日付は正規化される。
func NewBroadcastDay(year int, month time.Month, day int) BroadcastDay {
	d := time.Date(year, month, day, 0, 0, 0, 0, JST)
	return BroadcastDay{Year: d.Year(), Month: d.Month(), Day: d.Day()}
}

// ParseBroadcastDay は "2006-01-02" の形の放送日を読む。
func ParseBroadcastDay(s string) (BroadcastDay, error) {
	d, err := time.ParseInLocation("2006-01-02", s, JST)
	if err != nil {
		return BroadcastDay{}, fmt.Errorf("放送日 %q を読めない: %w", s, err)
	}
	return BroadcastDay{Year: d.Year(), Month: d.Month(), Day: d.Day()}, nil
}

// IsZero は放送日が決まっていない（零値）か。
func (d BroadcastDay) IsZero() bool { return d == BroadcastDay{} }

// Before は d が o より前の放送日か。
func (d BroadcastDay) Before(o BroadcastDay) bool {
	if d.Year != o.Year {
		return d.Year < o.Year
	}
	if d.Month != o.Month {
		return d.Month < o.Month
	}
	return d.Day < o.Day
}

// String は "2006-01-02" の形を返す。
func (d BroadcastDay) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

// Start は放送日の始まり（その日の 05:00 JST）を返す。
func (d BroadcastDay) Start() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 5, 0, 0, 0, JST)
}

// End は放送日の終わり（翌日の 05:00 JST）を返す。
func (d BroadcastDay) End() time.Time {
	return time.Date(d.Year, d.Month, d.Day+1, 5, 0, 0, 0, JST)
}

// Weekday は放送日の曜日。深夜の放送回の曜日も放送日で数える（月曜深夜 1 時は月曜）。
func (d BroadcastDay) Weekday() time.Weekday {
	return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, JST).Weekday()
}

// AddDays は n 日後の放送日を返す。
func (d BroadcastDay) AddDays(n int) BroadcastDay {
	return NewBroadcastDay(d.Year, d.Month, d.Day+n)
}

// Clock は日本時間の時:分。
type Clock struct {
	Hour   int
	Minute int
}

// ClockOf は時刻 t の日本時間の時:分を返す。
func ClockOf(t time.Time) Clock {
	j := t.In(JST)
	return Clock{Hour: j.Hour(), Minute: j.Minute()}
}

// ParseClock は "15:04" の形の時:分を読む。
func ParseClock(s string) (Clock, error) {
	digit := func(b byte) bool { return '0' <= b && b <= '9' }
	if len(s) != 5 || s[2] != ':' || !digit(s[0]) || !digit(s[1]) || !digit(s[3]) || !digit(s[4]) {
		return Clock{}, fmt.Errorf("時刻 %q は HH:MM の形ではない", s)
	}
	c := Clock{
		Hour:   int(s[0]-'0')*10 + int(s[1]-'0'),
		Minute: int(s[3]-'0')*10 + int(s[4]-'0'),
	}
	if err := c.Validate(); err != nil {
		return Clock{}, err
	}
	return c, nil
}

// Validate は時:分が 00:00〜23:59 の範囲にあるか確かめる。
func (c Clock) Validate() error {
	if c.Hour < 0 || c.Hour > 23 || c.Minute < 0 || c.Minute > 59 {
		return fmt.Errorf("時刻 %d:%d は 00:00〜23:59 の範囲にない", c.Hour, c.Minute)
	}
	return nil
}

// String は "15:04" の形を返す。
func (c Clock) String() string {
	return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
}
