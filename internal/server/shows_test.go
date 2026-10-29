package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// fakeGuide は radiko の代わりの番組表。実際の radiko にはつながない。
type fakeGuide struct {
	stations []app.Station
	// airings は "局/放送日" → 放送回。
	airings map[string][]domain.Airing
	fail    bool
	// asked は Airings に渡された "局/放送日"。
	asked []string
}

func (g *fakeGuide) Stations(ctx context.Context) ([]app.Station, error) {
	if g.fail {
		return nil, errors.New("落ちた")
	}
	return g.stations, nil
}

func (g *fakeGuide) Airings(ctx context.Context, stationID string, day domain.BroadcastDay) ([]domain.Airing, error) {
	if g.fail {
		return nil, errors.New("落ちた")
	}
	key := stationID + "/" + day.String()
	g.asked = append(g.asked, key)
	return g.airings[key], nil
}

func jst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, domain.JST)
}

type showsEnv struct {
	t      *testing.T
	h      http.Handler
	store  *store.Store
	guide  *fakeGuide
	ui     *UI
	cookie *http.Cookie
	// now は newExpandUI の今の時刻（UI と app.App が読む）。newShowsUI では nil。
	now *time.Time
}

// newShowsUI は番組の API を載せた UI を作り、ログインしておく。今は 2026-10-07（水曜）の 12 時。
func newShowsUI(t *testing.T) *showsEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGuide{airings: map[string][]domain.Airing{}}
	now := jst(2026, 10, 7, 12, 0)
	ui := &UI{
		Sessions: st, Shows: st, Guide: g, Passwords: passwords(string(b)),
		Files: fstest.MapFS{"index.html": {Data: []byte("<p>index</p>")}},
		Now:   func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	return &showsEnv{t: t, h: h, store: st, guide: g, ui: ui, cookie: login(t, h)}
}

// call は API を呼び、状態を確かめて、body を out に読む（out が nil なら読まない）。
func (e *showsEnv) call(method, path, body string, want int, out any) {
	e.t.Helper()
	rec := do(e.h, method, path, body, e.cookie)
	if rec.Code != want {
		e.t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body, want)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("%s %s: %v %s", method, path, err, rec.Body)
		}
	}
}

func showPath(name string) string { return "/api/shows/" + url.PathEscape(name) }

const flatJSON = `{"name":"パンサー向井のふらっと","enabled":true,"split":"join","check_title":true,"weekly":[
	{"station":"TBS","weekday":3,"start":"08:30"},
	{"station":"TBS","weekday":3,"start":"10:00"}]}`

func TestShowsCRUD(t *testing.T) {
	e := newShowsUI(t)
	var list []showJSON
	e.call("GET", "/api/shows", "", 200, &list)
	if len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}

	var created showJSON
	e.call("POST", "/api/shows", flatJSON, 201, &created)
	got, err := e.store.GetShow(context.Background(), "パンサー向井のふらっと")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Show{Name: "パンサー向井のふらっと", Split: domain.SplitJoin, CheckTitle: true, Weekly: []domain.Weekly{
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 8, Minute: 30}),
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 10}),
	}}
	if !showEqual(got, want) {
		t.Errorf("保存した番組 = %+v", got)
	}
	// 同じ名前でもう一度作ると 409。
	e.call("POST", "/api/shows", flatJSON, 409, nil)
	// 値が正しくなければ 400。
	e.call("POST", "/api/shows", `{"name":"","weekly":[]}`, 400, nil)
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":1,"start":"8:30"}]}`, 400, nil)
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"","weekday":1,"start":"08:30"}]}`, 400, nil)
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":7,"start":"08:30"}]}`, 400, nil)
	e.call("POST", "/api/shows", `{`, 400, nil)

	var one showJSON
	e.call("GET", showPath("パンサー向井のふらっと"), "", 200, &one)
	if one.Name != "パンサー向井のふらっと" || !*one.Enabled || len(one.Weekly) != 2 || one.Weekly[0].Start != "08:30" ||
		one.Weekly[1].Weekday != 3 || one.Weekly[1].Start != "10:00" {
		t.Errorf("GET = %+v", one)
	}
	// 毎週の予約の JSON は station・weekday・start だけで、kind・match・weekdays は無い。
	var raw struct {
		Weekly []map[string]any `json:"weekly"`
	}
	e.call("GET", showPath("パンサー向井のふらっと"), "", 200, &raw)
	for _, weekly := range raw.Weekly {
		if len(weekly) != 3 || weekly["station"] == nil || weekly["weekday"] == nil || weekly["start"] == nil {
			t.Errorf("毎週の予約の JSON = %v", weekly)
		}
	}
	e.call("GET", showPath("無い"), "", 404, nil)

	// 丸ごと置き換える。
	e.call("PUT", showPath("パンサー向井のふらっと"),
		`{"name":"パンサー向井のふらっと","enabled":true,"split":"none","check_title":false,"weekly":[{"station":"QRR","weekday":1,"start":"13:00"}]}`, 200, nil)
	got, _ = e.store.GetShow(context.Background(), "パンサー向井のふらっと")
	if got.Split != domain.SplitNone || got.CheckTitle || len(got.Weekly) != 1 || got.Weekly[0] != domain.NewWeekly("QRR", time.Monday, domain.Clock{Hour: 13}) {
		t.Errorf("置き換えた後 = %+v", got)
	}
	e.call("PUT", showPath("無い"), flatJSON, 404, nil)

	e.call("DELETE", showPath("パンサー向井のふらっと"), "", 204, nil)
	e.call("DELETE", showPath("パンサー向井のふらっと"), "", 404, nil)
	e.call("GET", "/api/shows", "", 200, &list)
	if len(list) != 0 {
		t.Errorf("消した後 = %+v", list)
	}
}

func showEqual(a, b domain.Show) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// enabled・check_title を省けば true にする。split を省けば "none"（分割番組でない）。
func TestShowsDefaults(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	var created showJSON
	e.call("POST", "/api/shows", `{"name":"省いた","weekly":[]}`, 201, &created)
	got, err := e.store.GetShow(ctx, "省いた")
	if err != nil || got.Disabled || !got.CheckTitle || got.Split != domain.SplitNone {
		t.Errorf("作成で省いた = %+v, %v", got, err)
	}
	if created.Split == nil || *created.Split != "none" {
		t.Errorf("作成の応答の split = %v", created.Split)
	}
	// 書いた値はそのとおりにする。
	e.call("PUT", showPath("省いた"), `{"name":"省いた","enabled":false,"check_title":false,"split":"separate","weekly":[]}`, 200, nil)
	got, _ = e.store.GetShow(ctx, "省いた")
	if !got.Disabled || got.CheckTitle || got.Split != domain.SplitSeparate {
		t.Errorf("false と書いた = %+v", got)
	}
	// 置き換えで省けば、また既定に戻る（丸ごと置き換える）。
	e.call("PUT", showPath("省いた"), `{"name":"省いた","weekly":[]}`, 200, nil)
	got, _ = e.store.GetShow(ctx, "省いた")
	if got.Disabled || !got.CheckTitle || got.Split != domain.SplitNone {
		t.Errorf("置き換えで省いた = %+v", got)
	}
}

// split は "none"・"join"・"separate" の 3 つの文字列を受け付け、返す。bool・ほかの値・使わなくなった join のキーは 400（ADR 0003）。
func TestShowsSplit(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	for _, tt := range []struct {
		value string
		want  domain.SplitMode
	}{
		{"none", domain.SplitNone},
		{"join", domain.SplitJoin},
		{"separate", domain.SplitSeparate},
	} {
		name := "番組-" + tt.value
		e.call("POST", "/api/shows", `{"name":"`+name+`","split":"`+tt.value+`","weekly":[]}`, 201, nil)
		got, err := e.store.GetShow(ctx, name)
		if err != nil || got.Split != tt.want {
			t.Errorf("split=%q: 保存した番組 = %+v, %v", tt.value, got, err)
		}
		var raw map[string]any
		e.call("GET", showPath(name), "", 200, &raw)
		if raw["split"] != tt.value {
			t.Errorf("split=%q: GET の split = %v", tt.value, raw["split"])
		}
		if _, ok := raw["join"]; ok {
			t.Errorf("split=%q: GET に join のキーがある: %v", tt.value, raw)
		}
		e.call("PUT", showPath(name), `{"name":"`+name+`","split":"`+tt.value+`","weekly":[]}`, 200, nil)
	}
	for _, body := range []string{
		`{"name":"x","split":true,"weekly":[]}`,
		`{"name":"x","split":false,"weekly":[]}`,
		`{"name":"x","split":"","weekly":[]}`,
		`{"name":"x","split":"parts","weekly":[]}`,
		`{"name":"x","split":"Join","weekly":[]}`,
		`{"name":"x","split":1,"weekly":[]}`,
		`{"name":"x","join":true,"weekly":[]}`,
		`{"name":"x","join":false,"split":"join","weekly":[]}`,
		`{"name":"x","join":null,"weekly":[]}`,
	} {
		e.call("POST", "/api/shows", body, 400, nil)
		e.call("PUT", showPath("番組-none"), body, 400, nil)
	}
	if _, err := e.store.GetShow(ctx, "x"); err == nil {
		t.Error("400 にした番組を作った")
	}
	if got, _ := e.store.GetShow(ctx, "番組-none"); got.Split != domain.SplitNone {
		t.Errorf("400 にした置き換えで番組が変わった: %+v", got)
	}
}

// 一括の置き換え（PUT /api/shows）は無い（ADR 0017）。呼んでも 404 か 405 で、番組は変わらない。
func TestShowsBulkReplaceIsGone(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	e.call("POST", "/api/shows", flatJSON, 201, nil)
	before, _ := e.store.ListShows(ctx)
	rec := do(e.h, "PUT", "/api/shows", `{"shows":[],"prune":true}`, e.cookie)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /api/shows = %d、want 404 か 405", rec.Code)
	}
	if after, _ := e.store.ListShows(ctx); !showsEqual(after, before) {
		t.Errorf("PUT /api/shows で番組が変わった: %+v", after)
	}
}

func showsEqual(a, b []domain.Show) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// 名前に / や空白があっても、URL エンコードすれば引ける。
func TestShowsNameEscaping(t *testing.T) {
	e := newShowsUI(t)
	name := "A/B ? #C%"
	e.call("POST", "/api/shows", `{"name":"A/B ? #C%","enabled":true,"check_title":true,"weekly":[]}`, 201, nil)
	var one showJSON
	e.call("GET", showPath(name), "", 200, &one)
	if one.Name != name {
		t.Errorf("GET = %+v", one)
	}
	e.call("DELETE", showPath(name), "", 204, nil)
}

// 名前の変更は、古い名前の番組を消して新しい名前の番組を作る。ジョブは古い名前のまま触らない。
func TestShowsRename(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	e.call("POST", "/api/shows", flatJSON, 201, nil)
	e.call("POST", "/api/shows", `{"name":"別の番組","enabled":true,"check_title":true,"weekly":[]}`, 201, nil)
	day := domain.NewBroadcastDay(2026, 10, 7)
	a := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 8, 30), End: jst(2026, 10, 7, 10, 0), Title: "パンサー向井のふらっと"}
	id, err := e.store.InsertJob(ctx, domain.Job{ShowName: "パンサー向井のふらっと", Day: day, State: domain.StateDone,
		Airings: []domain.Airing{a}, NextAttemptAt: a.End, Deadline: jst(2026, 10, 15, 5, 0), OutputPath: "/library/x.m4a"})
	if err != nil {
		t.Fatal(err)
	}

	renamed := `{"name":"ふらっと（新）","enabled":true,"split":"join","check_title":true,"weekly":[{"station":"TBS","weekday":3,"start":"08:30"}]}`
	// 新しい名前がほかの番組と重なれば 409 で、何も変えない。
	e.call("PUT", showPath("パンサー向井のふらっと"),
		`{"name":"別の番組","enabled":true,"check_title":true,"weekly":[]}`, 409, nil)
	e.call("PUT", showPath("パンサー向井のふらっと"), renamed, 200, nil)
	if _, err := e.store.GetShow(ctx, "パンサー向井のふらっと"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("古い名前の番組が残っている: %v", err)
	}
	if got, err := e.store.GetShow(ctx, "ふらっと（新）"); err != nil || len(got.Weekly) != 1 {
		t.Errorf("新しい名前の番組 = %+v, %v", got, err)
	}
	if j, err := e.store.GetJob(ctx, id); err != nil || j.ShowName != "パンサー向井のふらっと" || j.OutputPath != "/library/x.m4a" {
		t.Errorf("ジョブ = %+v, %v（古い名前のまま、保存先も変えない）", j, err)
	}
}

func TestShowsEnabledAndNextAiring(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	e.call("POST", "/api/shows", flatJSON, 201, nil)
	e.call("POST", "/api/shows", `{"name":"すっぴんしゃん","enabled":true,"check_title":true,"weekly":[]}`, 201, nil)

	e.call("PUT", showPath("すっぴんしゃん")+"/enabled", `{"enabled":false}`, 200, nil)
	if got, _ := e.store.GetShow(ctx, "すっぴんしゃん"); !got.Disabled {
		t.Errorf("無効にならない: %+v", got)
	}
	e.call("PUT", showPath("すっぴんしゃん")+"/enabled", `{}`, 400, nil)
	e.call("PUT", showPath("無い")+"/enabled", `{"enabled":true}`, 404, nil)

	// 次の放送予定は、waiting のジョブの放送回のうち、開始時刻が今より後のいちばん早いものの開始時刻。
	day := domain.NewBroadcastDay(2026, 10, 8)
	early := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 8, 8, 30), End: jst(2026, 10, 8, 10, 0)}
	late := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 8, 10, 30), End: jst(2026, 10, 8, 11, 0)}
	// 今（10-07 12:00）より前に始まった放送回（最初の試行を待っている）は、次の放送予定にしない。
	past := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 8, 30), End: jst(2026, 10, 7, 10, 0)}
	insert := func(show string, d domain.BroadcastDay, as ...domain.Airing) {
		t.Helper()
		if _, err := e.store.InsertJob(ctx, domain.Job{ShowName: show, Day: d, State: domain.StateWaiting,
			Airings: as, NextAttemptAt: as[0].End, Deadline: jst(2026, 10, 16, 5, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	insert("パンサー向井のふらっと", day, late)
	insert("パンサー向井のふらっと", day, early)
	insert("パンサー向井のふらっと", domain.NewBroadcastDay(2026, 10, 7), past)
	// 無効な番組には、waiting のジョブが残っていても出さない。
	insert("すっぴんしゃん", day, domain.Airing{StationID: "TBS", Start: jst(2026, 10, 8, 11, 0), End: jst(2026, 10, 8, 11, 30)})
	var list []showJSON
	e.call("GET", "/api/shows", "", 200, &list)
	if len(list) != 2 || list[0].Name != "すっぴんしゃん" || *list[0].Enabled || list[0].NextAiring != nil {
		t.Fatalf("list[0] = %+v", list[0])
	}
	if f := list[1]; !*f.Enabled || f.NextAiring == nil || !f.NextAiring.Equal(early.Start) {
		t.Errorf("list[1] = %+v", f)
	}
}

// 番組の一覧は、当たらない日（枠の開始時刻が今より後のもの）を枠の開始時刻の順に付ける。次の放送予定がある番組にも付け、
// 無効な番組には付けない。
func TestShowsUnmatchedDays(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	e.call("POST", "/api/shows", flatJSON, 201, nil)
	e.call("POST", "/api/shows", `{"name":"すっぴんしゃん","enabled":false,"weekly":[{"station":"TBS","weekday":3,"start":"11:00"}]}`, 201, nil)

	day := domain.NewBroadcastDay(2026, 10, 14)
	covering := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 14, 8, 0), End: jst(2026, 10, 14, 9, 0), Title: "特番"}
	titled := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 14, 9, 0), End: jst(2026, 10, 14, 9, 30), Title: "パンサー向井のふらっと (1)"}
	at := func(name string, d domain.BroadcastDay, start time.Time) domain.UnmatchedDay {
		return domain.UnmatchedDay{ShowName: name, StationID: "TBS", Day: d, Start: start}
	}
	late := at("パンサー向井のふらっと", day, jst(2026, 10, 14, 10, 0))
	early := at("パンサー向井のふらっと", day, jst(2026, 10, 14, 8, 30))
	early.Covering, early.Titled = &covering, []domain.Airing{titled}
	off := at("すっぴんしゃん", day, jst(2026, 10, 14, 11, 0))
	// 今（10-07 12:00）より前に始まった枠は出さない。
	today := domain.NewBroadcastDay(2026, 10, 7)
	past := at("パンサー向井のふらっと", today, jst(2026, 10, 7, 10, 0))
	for _, r := range []struct {
		day  domain.BroadcastDay
		days []domain.UnmatchedDay
	}{{day, []domain.UnmatchedDay{late, early, off}}, {today, []domain.UnmatchedDay{past}}} {
		if err := e.store.ReplaceUnmatchedDays(ctx, "TBS", r.day, today, domain.UnmatchedResult{Days: r.days}); err != nil {
			t.Fatal(err)
		}
	}
	// 次の放送予定もある番組。
	if _, err := e.store.InsertJob(ctx, domain.Job{ShowName: "パンサー向井のふらっと", Day: day, State: domain.StateWaiting,
		Airings: []domain.Airing{titled}, NextAttemptAt: titled.End, Deadline: jst(2026, 10, 22, 5, 0)}); err != nil {
		t.Fatal(err)
	}

	var list []showJSON
	e.call("GET", "/api/shows", "", 200, &list)
	if len(list) != 2 || list[0].Name != "すっぴんしゃん" || len(list[0].UnmatchedDays) != 0 {
		t.Fatalf("無効な番組 = %+v", list)
	}
	f := list[1]
	if f.NextAiring == nil || len(f.UnmatchedDays) != 2 {
		t.Fatalf("番組 = %+v", f)
	}
	u := f.UnmatchedDays[0]
	if u.Day != "2026-10-14" || u.Station != "TBS" || !u.Start.Equal(early.Start) || u.Covering == nil || u.Covering.Title != "特番" ||
		!u.Covering.End.Equal(covering.End) || len(u.Titled) != 1 || !u.Titled[0].Start.Equal(titled.Start) {
		t.Errorf("当たらない日[0] = %+v", u)
	}
	if u := f.UnmatchedDays[1]; !u.Start.Equal(late.Start) || u.Covering != nil || u.Titled == nil || len(u.Titled) != 0 {
		t.Errorf("当たらない日[1] = %+v（covering は null、titled は空の配列）", u)
	}
	// JSON では、その時刻を含む放送回が無ければ null、番組名の合う放送回が無ければ空の配列。
	rec := do(e.h, "GET", "/api/shows", "", e.cookie)
	if body := rec.Body.String(); !strings.Contains(body, `"covering":null,"titled":[]`) {
		t.Errorf("body = %s", body)
	}
}

func TestStations(t *testing.T) {
	e := newShowsUI(t)
	e.guide.stations = []app.Station{{ID: "TBS", Name: "TBSラジオ"}, {ID: "LFR", Name: "ニッポン放送"}}
	var got []struct{ ID, Name string }
	e.call("GET", "/api/stations", "", 200, &got)
	if len(got) != 2 || got[0].ID != "TBS" || got[0].Name != "TBSラジオ" {
		t.Errorf("stations = %+v", got)
	}
	e.guide.fail = true
	e.call("GET", "/api/stations", "", 502, nil)
}

// 局・曜日・開始時刻から、その放送日の番組表で開始時刻が一致する放送回を探し、番組名の末尾の印を落とした名前を返す。
func TestSuggestShowName(t *testing.T) {
	e := newShowsUI(t)
	// 今日は 2026-10-07（水曜）。月曜は 10-12、水曜は今日、火曜は 10-13。
	e.guide.airings["TBS/2026-10-12"] = []domain.Airing{
		{StationID: "TBS", Start: jst(2026, 10, 12, 8, 0), End: jst(2026, 10, 12, 8, 30), Title: "ニュース"},
		{StationID: "TBS", Start: jst(2026, 10, 12, 8, 30), End: jst(2026, 10, 12, 10, 0), Title: "パンサー向井のふらっと（１）"},
	}
	// 深夜 1 時の放送は前の放送日に載る。
	e.guide.airings["LFR/2026-10-13"] = []domain.Airing{
		{StationID: "LFR", Start: jst(2026, 10, 14, 1, 0), End: jst(2026, 10, 14, 3, 0), Title: "オールナイトニッポン 1時～3時"},
	}
	var got showNameJSON
	e.call("GET", "/api/show-name?station=TBS&weekday=1&start=08:30", "", 200, &got)
	if !got.Found || got.Name != "パンサー向井のふらっと" || got.Title != "パンサー向井のふらっと（１）" || got.Day != "2026-10-12" {
		t.Errorf("月曜 08:30 = %+v", got)
	}
	got = showNameJSON{}
	e.call("GET", "/api/show-name?station=LFR&weekday=2&start=01:00", "", 200, &got)
	if !got.Found || got.Name != "オールナイトニッポン" || got.Day != "2026-10-13" {
		t.Errorf("火曜 01:00（放送日は火曜、日付は水曜） = %+v", got)
	}
	got = showNameJSON{}
	e.call("GET", "/api/show-name?station=TBS&weekday=1&start=09:00", "", 200, &got)
	if got.Found || got.Name != "" || got.Day != "2026-10-12" {
		t.Errorf("見つからない = %+v", got)
	}
	e.call("GET", "/api/show-name?station=TBS&weekday=3&start=08:30", "", 200, &got)
	if last := e.guide.asked[len(e.guide.asked)-1]; last != "TBS/2026-10-07" {
		t.Errorf("今日と同じ曜日は今日の番組表を見る: %s", last)
	}

	e.call("GET", "/api/show-name?station=TBS&weekday=7&start=08:30", "", 400, nil)
	e.call("GET", "/api/show-name?weekday=1&start=08:30", "", 400, nil)
	e.call("GET", "/api/show-name?station=TBS&weekday=1&start=8:30", "", 400, nil)
	e.guide.fail = true
	e.call("GET", "/api/show-name?station=TBS&weekday=1&start=08:30", "", 502, nil)
}

// 番組の API もセッションが要る。
func TestShowsNeedSession(t *testing.T) {
	e := newShowsUI(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/shows"}, {"POST", "/api/shows"}, {"GET", "/api/shows/x"}, {"PUT", "/api/shows/x"},
		{"DELETE", "/api/shows/x"}, {"PUT", "/api/shows/x/enabled"}, {"GET", "/api/stations"}, {"GET", "/api/show-name"},
	} {
		if rec := do(e.h, c.method, c.path, "{}"); rec.Code != 401 {
			t.Errorf("%s %s = %d", c.method, c.path, rec.Code)
		}
	}
}
