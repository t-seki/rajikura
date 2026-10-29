package domain

import (
	"slices"
	"testing"
	"time"
)

func TestKeywordKey(t *testing.T) {
	cases := []struct{ a, b string }{
		{"パンサー 向井", "向井　パンサー"},
		{"ﾊﾟﾝｻｰ", "パンサー"},
		{"abc", "ABC"},
		{"向井 向井", "向井"},
	}
	for _, c := range cases {
		if KeywordKey(c.a) != KeywordKey(c.b) {
			t.Errorf("KeywordKey(%q) = %q, KeywordKey(%q) = %q（同じ形になってほしい）", c.a, KeywordKey(c.a), c.b, KeywordKey(c.b))
		}
	}
	if KeywordKey("  　") != "" {
		t.Errorf("空白だけの語の形 = %q", KeywordKey("  　"))
	}
	if err := (Keyword{Words: "　"}).Validate(); err == nil {
		t.Error("語が空のキーワードを通した")
	}
	if err := (Keyword{Words: "向井", Stations: []string{"TBS", " "}}).Validate(); err == nil {
		t.Error("空の局を通した")
	}
	if !(Keyword{Words: "x"}).InStations("TBS") || !(Keyword{Words: "x", Stations: []string{"TBS"}}).InStations("TBS") ||
		(Keyword{Words: "x", Stations: []string{"QRR"}}).InStations("TBS") {
		t.Error("局の絞り込みが違う")
	}
}

func TestDefaultShowName(t *testing.T) {
	cases := map[string]string{
		"パンサー向井のふらっと(1)": "パンサー向井のふらっと",
		"ＡＢＣ ラジオ　８時～９時":  "ABC ラジオ",
		"  番組名  ":        "番組名",
		"ニュース":           "ニュース",
	}
	for in, want := range cases {
		if got := DefaultShowName(in); got != want {
			t.Errorf("DefaultShowName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindShow(t *testing.T) {
	rs := []Show{{Name: "ABC ラジオ"}, {Name: "ＡＢＣ ラジオ"}, {Name: " 番組 "}}
	if i := FindShow(rs, "ＡＢＣ ラジオ"); i != 1 {
		t.Errorf("完全に同じ名前を先に選ばない: %d", i)
	}
	if i := FindShow(rs, "番組"); i != 2 {
		t.Errorf("NFKC と前後の空白を揃えて比べない: %d", i)
	}
	if i := FindShow(rs, "無い"); i != -1 {
		t.Errorf("無い名前: %d", i)
	}
}

func TestKeywordParts(t *testing.T) {
	day := func(h, mi int) time.Time { return jst(2026, 10, 8, h, mi) }
	a := func(st string, h int, title string) Airing {
		return Airing{StationID: st, Start: day(h, 0), End: day(h+1, 0), Title: title}
	}
	part1 := a("TBS", 8, "パンサー向井のふらっと(1)")
	part2 := a("TBS", 10, "パンサー向井のふらっと(2)")
	ng := a("TBS", 11, "パンサー向井のふらっと(3)")
	ng.TimeFree.InNG = 1
	other := a("QRR", 9, "パンサー向井のふらっと(1)")
	news1 := a("TBS", 12, "ニュース")
	news2 := a("TBS", 13, "ニュース")
	dayAirings := []Airing{part2, news1, part1, ng, other, news2}

	now := jst(2026, 10, 7, 12, 0)
	// タイムフリーの対象外のパート（ライブで録る）もまとめる。
	got := KeywordParts(part2, dayAirings, now, MembershipFree)
	if len(got) != 3 || got[0].Key() != part1.Key() || got[1].Key() != part2.Key() || got[2].Key() != ng.Key() {
		t.Errorf("印のある番組名のパート = %+v", got)
	}
	// 印が無ければ、当たった放送回だけ（同じ日の同じ番組名を巻き込まない）。
	if got := KeywordParts(news1, dayAirings, now, MembershipFree); len(got) != 1 || got[0].Key() != news1.Key() {
		t.Errorf("印の無い番組名 = %+v", got)
	}
	// 番組表にまだ載っていなくても、当たった放送回は含める。
	if got := KeywordParts(part1, nil, now, MembershipFree); len(got) != 1 || got[0].Key() != part1.Key() {
		t.Errorf("番組表が無いとき = %+v", got)
	}
	// 録音期限（放送の終了の 5 分後）を過ぎたタイムフリーの対象外のパートは除く。対象内のパートと、まだ過ぎていない
	// 対象外のパートは残す。
	ngPast := a("TBS", 7, "パンサー向井のふらっと(0)")
	ngPast.TimeFree.InNG = 1
	at := day(8, 6) // ngPast（7:00〜8:00）の録音期限 8:05 を過ぎた
	got = KeywordParts(part2, append(dayAirings, ngPast), at, MembershipFree)
	if len(got) != 3 || got[0].Key() != part1.Key() || got[1].Key() != part2.Key() || got[2].Key() != ng.Key() {
		t.Errorf("録音期限を過ぎた対象外のパートを除かない = %+v", got)
	}
	if got := KeywordParts(part2, append(dayAirings, ngPast), day(8, 4), MembershipFree); len(got) != 4 {
		t.Errorf("録音期限の前の対象外のパートを除いた = %+v", got)
	}
}

// 自動の組の規則。番組表の autoGroup（web/src/guide/GuidePage.test.tsx の「自動の組」）と同じ例。
// 同じ局・同じ放送日で印を落とした番組名が同じ放送回を集め、その中に印のある放送回が 1 つでもあれば組にする。
func TestKeywordPartsGroupShapes(t *testing.T) {
	now := jst(2026, 10, 19, 12, 0)
	at := func(st string, h, mi, h2, mi2 int, title string) Airing {
		return Airing{StationID: st, Start: jst(2026, 10, 20, h, mi), End: jst(2026, 10, 20, h2, mi2), Title: title}
	}
	lfr := []Airing{
		at("LFR", 8, 0, 10, 16, "垣花正 あなたとハッピー！"),
		at("LFR", 10, 16, 10, 21, "ニュース"),
		at("LFR", 10, 21, 11, 0, "垣花正 あなたとハッピー！ Part2"),
		at("LFR", 13, 0, 14, 0, "ナイツ ザ・ラジオショー（13時台）"),
		at("LFR", 14, 0, 15, 0, "ナイツ ザ・ラジオショー（14時台）"),
		at("LFR", 15, 0, 15, 30, "ナイツ ザ・ラジオショー（15時台）"),
	}
	tfm := []Airing{
		at("FMT", 21, 55, 22, 0, "TOKYO FM NEWS"),
		at("FMT", 22, 0, 23, 0, "SCHOOL OF LOCK!"),
		at("FMT", 23, 0, 23, 55, "SCHOOL OF LOCK!"),
		at("FMT", 23, 55, 24, 0, "TOKYO FM NEWS"),
	}
	tbs := []Airing{
		at("TBS", 8, 30, 9, 0, "パンサー向井のふらっと (1)"),
		at("TBS", 9, 0, 9, 5, "ニュース"),
		at("TBS", 9, 5, 11, 0, "パンサー向井のふらっと (2)"),
	}
	qrr := []Airing{
		at("QRR", 8, 0, 9, 0, "ふんわり　火曜ＭＣは木村祐一さん　８時台（１０／２０）"),
		at("QRR", 9, 0, 10, 0, "ふんわり「おひる、こんなんどう？スペシャル」　９時台"),
		at("QRR", 10, 0, 11, 0, "ふんわり「おひる、こんなんどう？スペシャル」　１０時台"),
		at("QRR", 11, 0, 12, 0, "ふんわり　まいにちリクエスト♪　火曜１１時台（１０／２０）"),
	}
	starts := func(as []Airing) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.Start.Format("15:04"))
		}
		return out
	}
	for _, c := range []struct {
		name string
		a    Airing
		day  []Airing
		want []string
	}{
		{"ナイツ（どのパートにも括弧付きの N時台）", lfr[4], lfr, []string{"13:00", "14:00", "15:00"}},
		{"垣花（印の無い 1 つ目から見ても Part2 と組になる。間のニュースは入らない）", lfr[0], lfr, []string{"08:00", "10:21"}},
		{"垣花（Part2 から）", lfr[2], lfr, []string{"08:00", "10:21"}},
		{"ふらっと (1) と (2)（間のニュースは入らない）", tbs[0], tbs, []string{"08:30", "09:05"}},
		{"SCHOOL OF LOCK!（印が無い放送回だけなので 1 件）", tfm[1], tfm, []string{"22:00"}},
		{"TOKYO FM NEWS（1 日に何度もある。1 件）", tfm[0], tfm, []string{"21:55"}},
		{"ふんわり（副題が時間帯ごとに違うので 1 件）", qrr[0], qrr, []string{"08:00"}},
		{"ふんわり（副題が同じ 9時台と 10時台は組になる）", qrr[1], qrr, []string{"09:00", "10:00"}},
	} {
		if got := starts(KeywordParts(c.a, c.day, now, MembershipFree)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckOnce(t *testing.T) {
	now := jst(2026, 10, 7, 12, 0)
	future := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 13, 0), End: jst(2026, 10, 8, 14, 0), Title: "番組"}
	past := Airing{StationID: "TBS", Start: jst(2026, 10, 6, 13, 0), End: jst(2026, 10, 6, 14, 0), Title: "番組"}
	weekly := func(a Airing) Weekly { return NewWeekly(a.StationID, a.Day().Weekday(), ClockOf(a.Start)) }
	self := joinedShow("自分")
	other := joinedShow("他", weekly(future))
	disabledOther := joinedShow("無効", weekly(future))
	disabledOther.Disabled = true
	selfWeekly := joinedShow("自分", weekly(future), weekly(past))
	selfWeekly.CheckTitle = false
	// 番組名の食い違う毎週の予約は、今日から先の放送日でも録らない予定にする（足せる）。
	selfWeeklyMismatch := joinedShow("自分", weekly(future))
	selfOnce := joinedShow("自分")
	selfOnce.Onces = []Once{{StationID: past.StationID, Start: past.Start}}
	ng := future
	ng.TimeFree.InNG = 1
	// 開始日を持つ毎週の予約は、今日より前の放送日でも当たる。番組名が合えば録る（already）、食い違えば録らなかったにする（足せる）。
	fromPast := weekly(past).WithFrom(past.Day())
	selfFrom := joinedShow("番組", fromPast)
	selfFromMismatch := joinedShow("自分", fromPast)

	cases := []struct {
		name       string
		a          Airing
		self       *Show
		rs         []Show
		jobs       map[string]Job
		reason, by string
	}{
		{"足せる", future, &self, []Show{self}, nil, "", ""},
		{"まだ無い番組に足せる", future, nil, nil, nil, "", ""},
		{"タイムフリーの対象外も足せる", ng, &self, []Show{self}, nil, "", ""},
		{"タイムフリーの対象外も、まだ無い番組に足せる", ng, nil, nil, nil, "", ""},
		{"ほかの番組のジョブ", future, &self, []Show{self}, map[string]Job{future.Key(): {ShowName: "他"}}, RefuseOtherShow, "他"},
		{"自分のジョブ", future, &self, []Show{self}, map[string]Job{future.Key(): {ShowName: "自分"}}, RefuseAlready, "自分"},
		{"まだ無い番組では、ジョブはほかの番組", future, nil, nil, map[string]Job{future.Key(): {ShowName: "自分"}}, RefuseOtherShow, "自分"},
		{"ほかの番組の毎週", future, &self, []Show{self, other}, nil, RefuseOtherShow, "他"},
		{"無効なほかの番組は当てていない", future, &self, []Show{self, disabledOther}, nil, "", ""},
		{"自分の毎週（今日から先）", future, &selfWeekly, []Show{selfWeekly}, nil, RefuseAlready, "自分"},
		{"自分の毎週で番組名が食い違う（今日から先）は足せる", future, &selfWeeklyMismatch, []Show{selfWeeklyMismatch}, nil, "", ""},
		{"自分の毎週だけ（過去の放送日）は足せる", past, &selfWeekly, []Show{selfWeekly}, nil, "", ""},
		{"自分の 1 回（過去の放送日）", past, &selfOnce, []Show{selfOnce}, nil, RefuseAlready, "自分"},
		{"自分の開始日のある毎週（過去の放送日）", past, &selfFrom, []Show{selfFrom}, nil, RefuseAlready, "番組"},
		{"自分の開始日のある毎週で録らなかったにする（過去の放送日）は足せる", past, &selfFromMismatch, []Show{selfFromMismatch}, nil, "", ""},
	}
	for _, c := range cases {
		reason, by := CheckOnce(c.a, c.self, c.rs, c.jobs, now, MembershipFree)
		if reason != c.reason || by != c.by {
			t.Errorf("%s: CheckOnce = %q %q, want %q %q", c.name, reason, by, c.reason, c.by)
		}
	}
}

func TestJobsByAiringPriority(t *testing.T) {
	a := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 13, 0)}
	jobs := JobsByAiring([]Job{
		{ID: 1, State: StateExpired, Airings: []Airing{a}},
		{ID: 2, State: StateWaiting, Airings: []Airing{a}},
		{ID: 3, State: StateDone, Airings: []Airing{a}},
		{ID: 4, State: StateRetrying, Airings: []Airing{a}},
	})
	if jobs[a.Key()].ID != 3 {
		t.Errorf("録音済みを先に選ばない: %+v", jobs[a.Key()])
	}
}

// keywordFixture は PlanKeywords のテストの材料。今日は 2026-10-07。
type keywordFixture struct {
	now time.Time
	hit func(h int, title string) KeywordHit
}

func newKeywordFixture() keywordFixture {
	return keywordFixture{
		now: jst(2026, 10, 7, 12, 0),
		hit: func(h int, title string) KeywordHit {
			a := Airing{StationID: "TBS", Start: jst(2026, 10, 8, h, 0), End: jst(2026, 10, 8, h+1, 0), Title: title}
			return KeywordHit{Airing: a, StationName: "TBSラジオ", DayAirings: []Airing{a}}
		},
	}
}

func TestPlanKeywordsCreatesShow(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "向井"}
	h := f.hit(13, "ゲストの番組(1)")
	part2 := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 14, 0), End: jst(2026, 10, 8, 15, 0), Title: "ゲストの番組(2)"}
	h.DayAirings = append(h.DayAirings, part2)
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {h}}, MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 1 {
		t.Fatalf("additions = %+v", plan.Additions)
	}
	add := plan.Additions[0]
	want := Show{Name: "ゲストの番組", Split: SplitJoin, CheckTitle: true}
	if !add.Created || add.ShowName != "ゲストの番組" || add.Show.Name != want.Name ||
		add.Show.Split != want.Split || add.Separate || add.Show.CheckTitle != want.CheckTitle || add.Show.Disabled ||
		len(add.Show.Weekly) != 0 {
		t.Errorf("作る番組 = %+v", add)
	}
	if len(add.Airings) != 2 || add.Airings[1].Key() != part2.Key() {
		t.Errorf("パートをまとめていない: %+v", add.Airings)
	}
}

func TestPlanKeywordsExistingShow(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "向井"}
	h := f.hit(13, "ＡＢＣ ラジオ")
	existing := joinedShow("ABC ラジオ")
	existing.Split = SplitSeparate
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {h}}, Shows: []Show{existing},
		MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 1 || plan.Additions[0].Created || plan.Additions[0].ShowName != "ABC ラジオ" {
		t.Fatalf("既存の番組に足さない: %+v", plan.Additions)
	}
}

// パートが 1 つなら、作る番組は分割番組でない（SplitNone）。
func TestPlanKeywordsCreatesNonSplitShow(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "向井"}
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {f.hit(13, "ゲストの番組")}}, MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 1 || !plan.Additions[0].Created || plan.Additions[0].Show.Split != SplitNone || plan.Additions[0].Separate {
		t.Errorf("additions = %+v", plan.Additions)
	}
}

// 印の無いパートが別々にキーワードに当たっても、新しく作る番組が同じ放送日に 2 つ以上を当てるなら、分割番組でつなぐ番組で作る。
func TestPlanKeywordsCreatesJoinedForSeparateHits(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "LOCK"}
	h1, h2 := f.hit(22, "SCHOOL OF LOCK!"), f.hit(23, "SCHOOL OF LOCK!")
	day := []Airing{h1.Airing, h2.Airing}
	h1.DayAirings, h2.DayAirings = day, day
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {h1, h2}}, MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 2 {
		t.Fatalf("additions = %+v", plan.Additions)
	}
	first, second := plan.Additions[0], plan.Additions[1]
	if !first.Created || first.Show.Split != SplitJoin || second.Created || second.ShowName != "SCHOOL OF LOCK!" {
		t.Errorf("作る番組 = %+v / %+v", first, second)
	}
	if first.Separate || second.Separate {
		t.Errorf("つなぐ番組なのに別のファイルと知らせる: %v %v", first.Separate, second.Separate)
	}
	if len(first.Airings) != 1 || len(second.Airings) != 1 {
		t.Errorf("印の無い放送回をまとめた: %+v / %+v", first.Airings, second.Airings)
	}

	// 別の放送日に 1 つずつなら、分割番組でない番組のまま
	h3 := f.hit(22, "SCHOOL OF LOCK!")
	h3.Airing.Start, h3.Airing.End = h3.Airing.Start.AddDate(0, 0, 1), h3.Airing.End.AddDate(0, 0, 1)
	h3.DayAirings = []Airing{h3.Airing}
	h1.DayAirings = []Airing{h1.Airing}
	plan = PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {h1, h3}}, MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 2 || plan.Additions[0].Show.Split != SplitNone || plan.Additions[0].Separate || plan.Additions[1].Separate {
		t.Errorf("別の放送日 = %+v", plan.Additions)
	}
}

// 分割番組でない既存の番組が、足した後に同じ放送日に 2 つ以上を当てるなら Separate（パートごとに別のファイルになる）。
// 1 回で足した数によらない（もう当てている 1 回・毎週の予約も数える）。既存の番組の設定は変えない。
func TestPlanKeywordsSeparateWithExisting(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "LOCK"}
	h1, h2 := f.hit(22, "SCHOOL OF LOCK!"), f.hit(23, "SCHOOL OF LOCK!")
	h2.DayAirings = []Airing{h1.Airing, h2.Airing}
	withOnce := NewShow("SCHOOL OF LOCK!")
	withOnce.Onces = []Once{{StationID: "TBS", Start: h1.Airing.Start}}
	withWeekly := NewShow("SCHOOL OF LOCK!", NewWeekly("TBS", h1.Airing.Day().Weekday(), ClockOf(h1.Airing.Start)))
	alone := NewShow("SCHOOL OF LOCK!")
	joined := joinedShow("SCHOOL OF LOCK!")
	joined.Onces = withOnce.Onces
	for _, c := range []struct {
		name string
		r    Show
		want bool
	}{
		{"同じ放送日の 1 回の予約がある", withOnce, true},
		{"同じ放送日に毎週の予約で当たる", withWeekly, true},
		{"ほかに当てていない", alone, false},
		{"分割番組でつなぐ番組", joined, false},
	} {
		plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {h2}}, Shows: []Show{c.r},
			MaxPending: 10, Now: f.now})
		if len(plan.Additions) != 1 || plan.Additions[0].Created || plan.Additions[0].Separate != c.want {
			t.Errorf("%s: additions = %+v", c.name, plan.Additions)
		}
	}
}

// 分割番組でない既存の番組にパートを 2 つ以上まとめて足すときも Separate（パートごとに別のファイルになる）。番組の設定は変えない。
func TestPlanKeywordsSeparateParts(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "向井"}
	part2 := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 14, 0), End: jst(2026, 10, 8, 15, 0), Title: "ゲストの番組(2)"}
	for _, tt := range []struct {
		split SplitMode
		parts int
		want  bool
	}{
		{SplitNone, 2, true},
		{SplitNone, 1, false},
		{SplitJoin, 2, false},
		{SplitSeparate, 2, false},
	} {
		h := f.hit(13, "ゲストの番組(1)")
		if tt.parts == 2 {
			h.DayAirings = append(h.DayAirings, part2)
		}
		existing := NewShow("ゲストの番組")
		existing.Split = tt.split
		plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: {h}}, Shows: []Show{existing},
			MaxPending: 10, Now: f.now})
		if len(plan.Additions) != 1 || plan.Additions[0].Created || len(plan.Additions[0].Airings) != tt.parts || plan.Additions[0].Separate != tt.want {
			t.Errorf("split=%v parts=%d: additions = %+v", tt.split, tt.parts, plan.Additions)
		}
	}
}

func TestPlanKeywordsSkips(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "向井"}
	h := f.hit(13, "番組")
	disabled := joinedShow("番組")
	disabled.Disabled = true
	other := joinedShow("他", NewWeekly("TBS", h.Airing.Day().Weekday(), ClockOf(h.Airing.Start)))
	selfOnce := joinedShow("番組")
	selfOnce.Onces = []Once{{StationID: "TBS", Start: h.Airing.Start}}

	cases := []struct {
		name string
		in   KeywordInput
	}{
		{"足す先が無効な番組", KeywordInput{Shows: []Show{disabled}}},
		{"ほかの番組が当てている", KeywordInput{Shows: []Show{other}}},
		{"ほかの番組のジョブ", KeywordInput{Jobs: map[string]Job{h.Airing.Key(): {ShowName: "他"}}}},
		{"足す先の番組がもう当てている", KeywordInput{Shows: []Show{selfOnce}}},
		{"記録にある（人が消した回）", KeywordInput{Recorded: map[string]bool{h.Airing.Key(): true}}},
		{"無効なキーワード", KeywordInput{Keywords: []Keyword{{ID: 1, Words: "向井", Disabled: true}}}},
	}
	for _, c := range cases {
		in := c.in
		if in.Keywords == nil {
			in.Keywords = []Keyword{k}
		}
		if in.Hits == nil {
			in.Hits = map[int64][]KeywordHit{1: {h}}
		}
		in.MaxPending, in.Now = 10, f.now
		plan := PlanKeywords(in)
		if len(plan.Additions) != 0 || len(plan.Limited) != 0 {
			t.Errorf("%s: 飛ばさない: %+v", c.name, plan)
		}
	}
}

func TestPlanKeywordsTimeFreeExcluded(t *testing.T) {
	f := newKeywordFixture()
	h := f.hit(13, "番組(1)")
	h.Airing.TimeFree.InNG = 2
	part2 := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 14, 0), End: jst(2026, 10, 8, 15, 0), Title: "番組(2)"}
	part2.TimeFree.InNG = 2
	h.DayAirings = []Airing{h.Airing, part2}
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{{ID: 1, Words: "x"}}, Hits: map[int64][]KeywordHit{1: {h}}, MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 1 {
		t.Fatalf("タイムフリーの対象外の放送回（ライブで録る）を足さない: %+v", plan.Additions)
	}
	if ps := plan.Additions[0].Airings; len(ps) != 2 || ps[0].Key() != h.Airing.Key() || ps[1].Key() != part2.Key() {
		t.Errorf("タイムフリーの対象外のパートをまとめない: %+v", ps)
	}
}

// 録音期限（放送の終了の 5 分後）を過ぎたタイムフリーの対象外のパートは足さず、残りのパートは足す。
func TestPlanKeywordsSkipsExpiredLivePart(t *testing.T) {
	f := newKeywordFixture()
	h := f.hit(13, "番組(2)")
	part1 := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 10, 0), End: jst(2026, 10, 8, 11, 0), Title: "番組(1)"}
	part1.TimeFree.InNG = 2
	h.DayAirings = []Airing{part1, h.Airing}
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{{ID: 1, Words: "x"}}, Hits: map[int64][]KeywordHit{1: {h}}, MaxPending: 10,
		Now: jst(2026, 10, 8, 12, 0)})
	if len(plan.Additions) != 1 {
		t.Fatalf("additions = %+v", plan.Additions)
	}
	if ps := plan.Additions[0].Airings; len(ps) != 1 || ps[0].Key() != h.Airing.Key() {
		t.Errorf("録音期限を過ぎた対象外のパートを足した: %+v", ps)
	}
}

func TestPlanKeywordsSplitPartRefusedSkipsAll(t *testing.T) {
	f := newKeywordFixture()
	h := f.hit(13, "番組(1)")
	part2 := Airing{StationID: "TBS", Start: jst(2026, 10, 8, 14, 0), End: jst(2026, 10, 8, 15, 0), Title: "番組(2)"}
	h.DayAirings = append(h.DayAirings, part2)
	other := joinedShow("他", NewWeekly("TBS", part2.Day().Weekday(), ClockOf(part2.Start)))
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{{ID: 1, Words: "x"}}, Hits: map[int64][]KeywordHit{1: {h}},
		Shows: []Show{other}, MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 0 {
		t.Errorf("パートの 1 つをほかの番組が当てているのに足した: %+v", plan.Additions)
	}
}

func TestPlanKeywordsOverlapLowerIDAdds(t *testing.T) {
	f := newKeywordFixture()
	h := f.hit(13, "番組")
	first := Keyword{ID: 1, Words: "向井"}
	second := Keyword{ID: 2, Words: "パンサー"}
	plan := PlanKeywords(KeywordInput{
		Keywords: []Keyword{second, first}, Hits: map[int64][]KeywordHit{1: {h}, 2: {h}},
		Pending: map[int64]int{2: 1}, LimitNotified: map[int64]bool{2: true}, MaxPending: 10, Now: f.now,
	})
	if len(plan.Additions) != 1 || plan.Additions[0].Keyword.ID != 1 {
		t.Fatalf("先に作ったキーワード（ID の小さい方）が足さない: %+v", plan.Additions)
	}
	// もう一方は飛ばし、上限の数にも入れない（上限を下回っているので、印は下ろす）。
	if v, ok := plan.LimitNotified[2]; !ok || v {
		t.Errorf("ID 2 の上限の印 = %v %v", v, ok)
	}
}

func TestPlanKeywordsLimit(t *testing.T) {
	f := newKeywordFixture()
	k := Keyword{ID: 1, Words: "向井"}
	hits := []KeywordHit{f.hit(10, "一"), f.hit(12, "二"), f.hit(14, "三")}

	// 上限 2、録音予定 1: 開始の早い 1 つだけ足し、残りは上限で止めて 1 回だけ通知する。
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: hits},
		Pending: map[int64]int{1: 1}, MaxPending: 2, Now: f.now})
	if len(plan.Additions) != 1 || plan.Additions[0].Hit.Airing.Title != "一" {
		t.Fatalf("additions = %+v", plan.Additions)
	}
	if len(plan.Limited) != 1 || !plan.LimitNotified[1] {
		t.Fatalf("上限の通知 = %+v", plan)
	}

	// もう通知していれば、同期のたびには送らない。
	plan = PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: hits[1:]},
		Pending: map[int64]int{1: 2}, LimitNotified: map[int64]bool{1: true}, MaxPending: 2, Now: f.now})
	if len(plan.Additions) != 0 || len(plan.Limited) != 0 {
		t.Fatalf("通知済みなのにまた送る: %+v", plan)
	}
	if _, ok := plan.LimitNotified[1]; ok {
		t.Fatalf("上限に達したままなのに印を変えた: %+v", plan.LimitNotified)
	}

	// 録音を終えて空きができたら次の回を足し、上限を下回ったら印を下ろす。
	plan = PlanKeywords(KeywordInput{Keywords: []Keyword{k}, Hits: map[int64][]KeywordHit{1: hits[1:2]},
		Pending: map[int64]int{1: 0}, LimitNotified: map[int64]bool{1: true}, MaxPending: 2, Now: f.now})
	if len(plan.Additions) != 1 || plan.LimitNotified[1] {
		t.Fatalf("空きができた後 = %+v", plan)
	}
	if v, ok := plan.LimitNotified[1]; !ok || v {
		t.Fatalf("印を下ろさない: %v %v", v, ok)
	}
}

func TestPlanKeywordsAddsOnceAcrossHits(t *testing.T) {
	// 同じキーワードに、同じ分割番組のパートが別々に当たっても、1 回だけ足す。
	f := newKeywordFixture()
	h1 := f.hit(13, "番組(1)")
	h2 := f.hit(14, "番組(2)")
	h1.DayAirings = []Airing{h1.Airing, h2.Airing}
	h2.DayAirings = h1.DayAirings
	plan := PlanKeywords(KeywordInput{Keywords: []Keyword{{ID: 1, Words: "x"}}, Hits: map[int64][]KeywordHit{1: {h1, h2}},
		MaxPending: 10, Now: f.now})
	if len(plan.Additions) != 1 || len(plan.Additions[0].Airings) != 2 {
		t.Fatalf("additions = %+v", plan.Additions)
	}
}
