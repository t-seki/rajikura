package domain

import (
	"errors"
	"testing"
	"time"
)

// jst は日本時間の時刻を作る。
func jst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, JST)
}

func TestBroadcastDayOf(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"04:59 は前日", jst(2026, 10, 6, 4, 59), "2026-10-05"},
		{"05:00 は当日", jst(2026, 10, 6, 5, 0), "2026-10-06"},
		{"00:00 は前日", jst(2026, 10, 6, 0, 0), "2026-10-05"},
		{"23:59 は当日", jst(2026, 10, 6, 23, 59), "2026-10-06"},
		{"月初の深夜は前月末", jst(2026, 11, 1, 2, 0), "2026-10-31"},
		{"元日の深夜は前年", jst(2027, 1, 1, 4, 0), "2026-12-31"},
		{"UTC で渡しても日本時間で数える", time.Date(2026, 10, 5, 19, 59, 0, 0, time.UTC), "2026-10-05"},
		{"UTC 20:00 は日本時間 05:00", time.Date(2026, 10, 5, 20, 0, 0, 0, time.UTC), "2026-10-06"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BroadcastDayOf(tt.t).String(); got != tt.want {
				t.Errorf("BroadcastDayOf(%v) = %s, want %s", tt.t, got, tt.want)
			}
		})
	}
}

func TestAiringEqualIgnoresDescriptionParts(t *testing.T) {
	a := Airing{StationID: "TBS", Start: jst(2026, 10, 17, 5, 0), Title: "t", Description: "こちら"}
	b := a
	b.DescriptionParts = []DescriptionPart{{Text: "こちら", Href: "https://example.com/"}}
	if !a.equal(b) || !b.equal(a) {
		t.Error("部品の並びだけが違う放送回は等しいはず")
	}
}

func TestBroadcastDayBounds(t *testing.T) {
	d := NewBroadcastDay(2026, 10, 31)
	if got, want := d.Start(), jst(2026, 10, 31, 5, 0); !got.Equal(want) {
		t.Errorf("Start = %v, want %v", got, want)
	}
	if got, want := d.End(), jst(2026, 11, 1, 5, 0); !got.Equal(want) {
		t.Errorf("End = %v, want %v", got, want)
	}
	if got := d.AddDays(1).String(); got != "2026-11-01" {
		t.Errorf("AddDays(1) = %s", got)
	}
	p, err := ParseBroadcastDay("2026-10-31")
	if err != nil || p != d {
		t.Errorf("ParseBroadcastDay = %v, %v", p, err)
	}
	if _, err := ParseBroadcastDay("2026/10/31"); err == nil {
		t.Error("ParseBroadcastDay は形の違う文字列をエラーにする")
	}
}

func TestBroadcastDayWeekday(t *testing.T) {
	// 2026-10-05 は月曜。
	tests := []struct {
		name string
		t    time.Time
		want time.Weekday
	}{
		{"月曜 05:00", jst(2026, 10, 5, 5, 0), time.Monday},
		{"月曜深夜 1 時（暦では火曜）は月曜", jst(2026, 10, 6, 1, 0), time.Monday},
		{"月曜深夜 4:59 は月曜", jst(2026, 10, 6, 4, 59), time.Monday},
		{"火曜 05:00 は火曜", jst(2026, 10, 6, 5, 0), time.Tuesday},
		{"月曜 00:30（暦では月曜）は日曜", jst(2026, 10, 5, 0, 30), time.Sunday},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BroadcastDayOf(tt.t).Weekday(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseClock(t *testing.T) {
	tests := []struct {
		in   string
		want Clock
		ok   bool
	}{
		{"01:00", Clock{1, 0}, true},
		{"23:59", Clock{23, 59}, true},
		{"24:00", Clock{}, false},
		{"1:00", Clock{}, false},
		{"12:60", Clock{}, false},
		{"ab:cd", Clock{}, false},
		{"12:3x", Clock{}, false},
		{"+1:00", Clock{}, false},
		{"-1:00", Clock{}, false},
		{"01:00 ", Clock{}, false},
		{"0100", Clock{}, false},
		{"00:00", Clock{0, 0}, true},
	}
	for _, tt := range tests {
		got, err := ParseClock(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseClock(%q) = %v, %v", tt.in, got, err)
		}
		if tt.ok && got.String() != tt.in {
			t.Errorf("String() = %s, want %s", got.String(), tt.in)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"オールナイトニッポン", "オールナイトニッポン"},
		{"ＡＢＣ　ラジオ", "ABCラジオ"},
		{"abc def", "ABCDEF"},
		{"ﾗｼﾞｵ", "ラジオ"},
		{"  ふらっと (1)\t", "ふらっと(1)"},
		{"No.１", "NO.1"},
	}
	for _, tt := range tests {
		if got := NormalizeTitle(tt.in); got != tt.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func airing(station string, start, end time.Time, title string) Airing {
	return Airing{StationID: station, Start: start, End: end, Title: title}
}

func TestWeeklyMatches(t *testing.T) {
	// 2026-10-05 は月曜。
	late := airing("LFR", jst(2026, 10, 6, 1, 0), jst(2026, 10, 6, 3, 0), "オールナイトニッポン")
	tests := []struct {
		name   string
		weekly Weekly
		a      Airing
		want   bool
	}{
		{"毎週の予約: 一致", NewWeekly("LFR", time.Monday, Clock{1, 0}), late, true},
		{"毎週の予約: 深夜を暦の曜日で書くと外れる", NewWeekly("LFR", time.Tuesday, Clock{1, 0}), late, false},
		{"毎週の予約: 局が違う", NewWeekly("TBS", time.Monday, Clock{1, 0}), late, false},
		{"毎週の予約: 分が違う（放送時間の変更）", NewWeekly("LFR", time.Monday, Clock{1, 30}), late, false},
		// 開始日は放送日で比べる。2026-10-06 の 1 時は放送日 2026-10-05（月曜）。
		{"開始日: 放送日が開始日と同じなら当たる（深夜は前日の放送日）", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 10, 5)), late, true},
		{"開始日: 深夜の放送回は暦の日が開始日でも放送日が前なら当たらない", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 10, 6)), late, false},
		{"開始日: 開始日より前の放送日は当たらない", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 10, 12)), late, false},
		{"開始日: 開始日より後の放送日は当たる", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 9, 28)), late, true},
		{"開始日: 年をまたいでも比べられる", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2027, 1, 4)), late, false},
		// 終了日も放送日で比べ、終了日の放送日を含む。
		{"終了日: 放送日が終了日と同じなら当たる（深夜は前日の放送日）", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithUntil(NewBroadcastDay(2026, 10, 5)), late, true},
		{"終了日: 深夜の放送回は暦の日が終了日より後でも放送日が終了日なら当たる", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithUntil(NewBroadcastDay(2026, 10, 5)), late, true},
		{"終了日: 終了日より後の放送日は当たらない", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithUntil(NewBroadcastDay(2026, 9, 28)), late, false},
		{"終了日: 終了日より前の放送日は当たる", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithUntil(NewBroadcastDay(2026, 10, 12)), late, true},
		{"開始日と終了日: 同じ日なら、その放送日だけ当たる", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 10, 5)).WithUntil(NewBroadcastDay(2026, 10, 5)), late, true},
		{"開始日と終了日: 内側の放送日は当たる", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 9, 28)).WithUntil(NewBroadcastDay(2026, 10, 12)), late, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.weekly.Matches(tt.a); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAiringDeadline(t *testing.T) {
	a := airing("LFR", jst(2026, 10, 5, 13, 0), jst(2026, 10, 5, 15, 0), "x")
	lateA := airing("LFR", jst(2026, 10, 6, 3, 0), jst(2026, 10, 6, 5, 0), "x")
	cross := airing("LFR", jst(2026, 10, 6, 4, 30), jst(2026, 10, 6, 5, 30), "x")
	tests := []struct {
		name string
		a    Airing
		m    Membership
		want time.Time
	}{
		{"非会員は 7 日", a, MembershipFree, jst(2026, 10, 13, 5, 0)},
		{"エリアフリーは 7 日", a, MembershipAreaFree, jst(2026, 10, 13, 5, 0)},
		{"タイムフリー30は 30 日", a, MembershipTimeFree30, jst(2026, 11, 5, 5, 0)},
		{"ダブルプランは 30 日", a, MembershipDouble, jst(2026, 11, 5, 5, 0)},
		{"05:00 ちょうどに終わる回は前の放送日", lateA, MembershipFree, jst(2026, 10, 13, 5, 0)},
		{"05:00 をまたぐ回は終了時刻の放送日", cross, MembershipFree, jst(2026, 10, 14, 5, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AiringDeadline(tt.a, tt.m); !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got.In(JST), tt.want)
			}
		})
	}
}

func TestJobDeadline(t *testing.T) {
	early := airing("LFR", jst(2026, 10, 6, 4, 0), jst(2026, 10, 6, 4, 30), "x")
	late := airing("LFR", jst(2026, 10, 6, 4, 30), jst(2026, 10, 6, 5, 30), "x")
	got := JobDeadline([]Airing{late, early}, MembershipFree)
	if want := jst(2026, 10, 13, 5, 0); !got.Equal(want) {
		t.Errorf("JobDeadline = %v, want %v（最も早いもの）", got, want)
	}
	if !JobDeadline(nil, MembershipFree).IsZero() {
		t.Error("放送回が無ければゼロ値")
	}
}

func TestExcludedFromTimeFree30(t *testing.T) {
	if ExcludedFromTimeFree30(Airing{TimeFree: TimeFreeFlags{InNG: 1, OutNG: 1}}) {
		t.Error("仮の実装は常に対象外ではない")
	}
}

func TestJudgeAtDeadline(t *testing.T) {
	a1 := airing("LFR", jst(2026, 10, 5, 8, 30), jst(2026, 10, 5, 10, 0), "(1)")
	a2 := airing("LFR", jst(2026, 10, 5, 10, 0), jst(2026, 10, 5, 11, 0), "(2)")
	as := []Airing{a1, a2}
	tests := []struct {
		name        string
		fetched     []bool
		want        Verdict
		wantMissing int
	}{
		{"何も取れていない", []bool{false, false}, VerdictExpire, 2},
		{"一部だけ取れた", []bool{true, false}, VerdictSavePartial, 1},
		{"すべて取れた", []bool{true, true}, VerdictSaveAll, 0},
		{"fetched が短ければ取れていない扱い", []bool{true}, VerdictSavePartial, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, missing := JudgeAtDeadline(as, tt.fetched)
			if got != tt.want || len(missing) != tt.wantMissing {
				t.Errorf("got %v, %d missing; want %v, %d", got, len(missing), tt.want, tt.wantMissing)
			}
		})
	}
	_, missing := JudgeAtDeadline(as, []bool{true, false})
	if missing[0].Title != "(2)" {
		t.Errorf("欠落は (2) のはず: %v", missing)
	}
}

func TestFirstAttemptAt(t *testing.T) {
	end := jst(2026, 10, 5, 15, 0)
	tests := []struct {
		name  string
		delay time.Duration
		want  time.Time
	}{
		{"tf_max_delay が 150 秒より短い", 60 * time.Second, end.Add(150*time.Second + 2*time.Minute)},
		{"tf_max_delay が 0", 0, end.Add(150*time.Second + 2*time.Minute)},
		{"tf_max_delay が 150 秒より長い", 10 * time.Minute, end.Add(12 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstAttemptAt(end, tt.delay); !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNextAttemptAtSequence(t *testing.T) {
	// 失敗し続けたときの試行の列。
	first := jst(2026, 10, 5, 15, 5)
	deadline := jst(2026, 10, 13, 5, 0)
	p := RetryPolicy{Membership: MembershipAreaFree}
	want := []time.Duration{15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	last := first
	for i, d := range want {
		next, ok := p.NextAttemptAt(i+1, last, deadline, last.Add(time.Minute))
		if !ok || !next.Equal(last.Add(d)) {
			t.Fatalf("%d 回目の失敗の後: got %v, %v; want %v", i+1, next, ok, last.Add(d))
		}
		last = next
	}
}

func TestNextAttemptAt(t *testing.T) {
	deadline := jst(2026, 10, 13, 5, 0)
	tests := []struct {
		name      string
		policy    RetryPolicy
		attempts  int
		lastStart time.Time
		now       time.Time
		want      time.Time
		ok        bool
	}{
		{
			name:     "期限を過ぎるなら期限の 10 分前に最後の試行",
			policy:   RetryPolicy{Membership: MembershipAreaFree},
			attempts: 8, lastStart: jst(2026, 10, 13, 0, 0), now: jst(2026, 10, 13, 0, 5),
			want: jst(2026, 10, 13, 4, 50), ok: true,
		},
		{
			name:     "期限の 10 分前の試行の後は無い",
			policy:   RetryPolicy{Membership: MembershipAreaFree},
			attempts: 9, lastStart: jst(2026, 10, 13, 4, 50), now: jst(2026, 10, 13, 4, 55),
			ok: false,
		},
		{
			name:     "期限の内側に収まるなら間隔どおり",
			policy:   RetryPolicy{Membership: MembershipAreaFree},
			attempts: 8, lastStart: jst(2026, 10, 12, 22, 0), now: jst(2026, 10, 12, 22, 5),
			want: jst(2026, 10, 13, 4, 0), ok: true,
		},
		{
			name:     "期限を過ぎていたら無い",
			policy:   RetryPolicy{Membership: MembershipAreaFree},
			attempts: 3, lastStart: jst(2026, 10, 13, 4, 0), now: jst(2026, 10, 13, 5, 0),
			ok: false,
		},
		{
			name:     "非会員は既定の上限 10 回で止まる",
			policy:   RetryPolicy{Membership: MembershipFree},
			attempts: 10, lastStart: jst(2026, 10, 7, 0, 0), now: jst(2026, 10, 7, 0, 5),
			ok: false,
		},
		{
			name:     "非会員は上限の手前なら続く",
			policy:   RetryPolicy{Membership: MembershipFree},
			attempts: 9, lastStart: jst(2026, 10, 7, 0, 0), now: jst(2026, 10, 7, 0, 5),
			want: jst(2026, 10, 7, 6, 0), ok: true,
		},
		{
			name:     "非会員の上限は設定で変えられる",
			policy:   RetryPolicy{Membership: MembershipFree, MaxAttemptsFree: 3},
			attempts: 3, lastStart: jst(2026, 10, 7, 0, 0), now: jst(2026, 10, 7, 0, 5),
			ok: false,
		},
		{
			name:     "非会員以外は上限が無い",
			policy:   RetryPolicy{Membership: MembershipTimeFree30, MaxAttemptsFree: 3},
			attempts: 30, lastStart: jst(2026, 10, 7, 0, 0), now: jst(2026, 10, 7, 0, 5),
			want: jst(2026, 10, 7, 6, 0), ok: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.policy.NextAttemptAt(tt.attempts, tt.lastStart, deadline, tt.now)
			if ok != tt.ok || (ok && !got.Equal(tt.want)) {
				t.Errorf("got %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestTransition(t *testing.T) {
	allowed := map[[2]State]bool{
		{StateWaiting, StateRunning}:  true,
		{StateRunning, StateDone}:     true,
		{StateRunning, StateRetrying}: true,
		{StateRunning, StateExpired}:  true,
		{StateRetrying, StateRunning}: true,
		{StateDone, StateRetrying}:    true,
		{StateExpired, StateRetrying}: true,
		{StateSkipped, StateRetrying}: true,
		{StateWaiting, StateSkipped}:  true,
		{StateSkipped, StateWaiting}:  true,
	}
	for _, from := range States {
		for _, to := range States {
			want := allowed[[2]State{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
			err := Transition(from, to)
			if (err == nil) != want || (err != nil && !errors.Is(err, ErrInvalidTransition)) {
				t.Errorf("Transition(%s, %s) = %v", from, to, err)
			}
		}
	}
}

func TestParse(t *testing.T) {
	for _, s := range States {
		if got, err := ParseState(string(s)); err != nil || got != s {
			t.Errorf("ParseState(%s) = %v, %v", s, got, err)
		}
	}
	if _, err := ParseState("nope"); err == nil {
		t.Error("ParseState は不明な状態をエラーにする")
	}
	for _, m := range []Membership{MembershipFree, MembershipAreaFree, MembershipTimeFree30, MembershipDouble} {
		if got, err := ParseMembership(string(m)); err != nil || got != m {
			t.Errorf("ParseMembership(%s) = %v, %v", m, got, err)
		}
	}
	if _, err := ParseMembership("premium"); err == nil {
		t.Error("ParseMembership は不明な会員種別をエラーにする")
	}
}

func TestSplitMode(t *testing.T) {
	for _, m := range []SplitMode{SplitNone, SplitJoin, SplitSeparate} {
		if got, err := ParseSplitMode(m.String()); err != nil || got != m {
			t.Errorf("ParseSplitMode(%q) = %v, %v", m.String(), got, err)
		}
	}
	for _, s := range []string{"", "true", "Join", "parts"} {
		if _, err := ParseSplitMode(s); err == nil {
			t.Errorf("ParseSplitMode(%q) にエラーが無い", s)
		}
	}
	for _, tt := range []struct {
		m            SplitMode
		joins, split bool
	}{{SplitNone, false, false}, {SplitJoin, true, true}, {SplitSeparate, false, true}} {
		r := Show{Name: "ANN", Split: tt.m}
		if r.Joins() != tt.joins || r.IsSplit() != tt.split {
			t.Errorf("%v: Joins = %v, IsSplit = %v", tt.m, r.Joins(), r.IsSplit())
		}
	}
	if NewShow("ANN").Split != SplitNone {
		t.Error("NewShow の既定が分割番組でない、になっていない")
	}
}

func TestShowValidate(t *testing.T) {
	tests := []struct {
		name string
		show Show
		ok   bool
	}{
		{"正しい", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}), NewWeekly("TBS", time.Friday, Clock{22, 0})), true},
		{"毎週の予約が無くてもよい", NewShow("ANN"), true},
		{"番組名が空", NewShow(""), false},
		{"局が空", NewShow("ANN", NewWeekly("", time.Monday, Clock{1, 0})), false},
		{"毎週の予約の曜日が範囲外", NewShow("ANN", NewWeekly("LFR", time.Weekday(7), Clock{1, 0})), false},
		{"毎週の予約の曜日が負", NewShow("ANN", NewWeekly("LFR", time.Weekday(-1), Clock{1, 0})), false},
		{"毎週の予約の時刻が範囲外", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{25, 0})), false},
		{"毎週の予約の分が範囲外", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 60})), false},
		{"終了日だけ", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithUntil(NewBroadcastDay(2026, 12, 26))), true},
		{"終了日が開始日と同じ", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 12, 26)).WithUntil(NewBroadcastDay(2026, 12, 26))), true},
		{"終了日が開始日より後", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 10, 5)).WithUntil(NewBroadcastDay(2026, 12, 26))), true},
		{"終了日が開始日より前", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithFrom(NewBroadcastDay(2026, 12, 26)).WithUntil(NewBroadcastDay(2026, 12, 25))), false},
		{"終了日が正しくない", NewShow("ANN", NewWeekly("LFR", time.Monday, Clock{1, 0}).WithUntil(BroadcastDay{Year: 2026, Month: 2, Day: 30})), false},
		{"分割番組でつなぐ", Show{Name: "ANN", Split: SplitJoin}, true},
		{"分割番組でつながない", Show{Name: "ANN", Split: SplitSeparate}, true},
		{"分割番組の設定が範囲外", Show{Name: "ANN", Split: SplitMode(3)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.show.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate = %v", err)
			}
		})
	}
}
