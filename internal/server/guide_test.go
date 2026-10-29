package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
)

// fakeAreaGuide は radiko のエリアの日別番組表の代わり。
type fakeAreaGuide struct {
	mu       sync.Mutex
	stations map[string][]app.AreaStation // 放送日 → 局
	fail     bool
	asked    []string
}

func (g *fakeAreaGuide) AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]app.AreaStation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, day.String())
	if g.fail {
		return nil, errors.New("落ちた")
	}
	return g.stations[day.String()], nil
}

type guideEnv struct {
	*showsEnv
	area *fakeAreaGuide
}

// newGuideUI は番組表の API を載せた UI を作る。今は 2026-10-07（水曜）の 12 時。
// 放送日 2026-10-07 の TBS には、状態の違う放送回が 1 時間おきに並ぶ。
func newGuideUI(t *testing.T) *guideEnv {
	t.Helper()
	e := newShowsUI(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	air := func(h int, title, performer string, inNG int) domain.Airing {
		return domain.Airing{
			StationID: "TBS", Start: jst(2026, 10, 7, h, 0), End: jst(2026, 10, 7, h+1, 0), Title: title, Performer: performer,
			TimeFree: domain.TimeFreeFlags{InNG: inNG},
		}
	}
	area := &fakeAreaGuide{stations: map[string][]app.AreaStation{
		"2026-10-07": {
			{ID: "TBS", Name: "TBSラジオ", Airings: []domain.Airing{
				air(5, "朝の番組", "", 0),           // ジョブ waiting
				air(6, "取得済み", "", 0),           // ジョブ done
				air(7, "取れなかった", "", 0),         // ジョブ expired
				air(8, "毎週の予約", "", 0),          // ジョブ無し、毎週の予約に当たる
				air(9, "単発", "", 0),             // ジョブ無し、1 回の予約に当たる
				air(10, "無効な番組", "", 0),         // ジョブ無し、無効な番組の毎週の予約に当たる
				air(11, "対象外", "", 1),           // 予約なし、ts_in_ng
				air(12, "Ａｎｄ　Ｍｏｒｅ", "田中 太郎", 0), // 検索用
			}},
			{ID: "QRR", Name: "文化放送", Airings: []domain.Airing{
				{StationID: "QRR", Start: jst(2026, 10, 7, 8, 0), End: jst(2026, 10, 7, 9, 0), Title: "別の局"},
			}},
		},
		"2026-10-08": {
			{ID: "TBS", Name: "TBSラジオ", Airings: []domain.Airing{
				{StationID: "TBS", Start: jst(2026, 10, 8, 13, 0), End: jst(2026, 10, 8, 14, 0), Title: "あした", Performer: "山田 花子",
					Description: "詳細はこちら", DescriptionParts: []domain.DescriptionPart{{Text: "詳細は"}, {Text: "こちら", Href: "https://example.com/"}}},
			}},
		},
	}}
	ui := &UI{
		Sessions: e.store, Shows: e.store, Jobs: e.store, Guide: e.guide, AreaGuide: area, Passwords: passwords(string(hash)),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   e.ui.Now,
	}
	e.ui = ui
	e.h = Handler(Health{LastPoll: time.Now}, ui)
	e.cookie = login(t, e.h)

	ctx := context.Background()
	newWeekly := func(h int) domain.Weekly {
		return domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: h})
	}
	mustShow := func(s domain.Show) {
		if err := e.store.CreateShow(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	mustShow(domain.NewShow("ジョブの番組", newWeekly(5)))
	// 番組表の番組名（毎週の予約）が番組の名前と食い違うので、番組名を確かめない（録らない予定にしない）番組にする。
	timed := domain.NewShow("時刻の番組", newWeekly(8))
	timed.CheckTitle = false
	mustShow(timed)
	mustShow(domain.NewShow("単発の番組"))
	if _, err := e.store.CreateOnce(ctx, "単発の番組", domain.Once{StationID: "TBS", Start: jst(2026, 10, 7, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	off := domain.NewShow("無効の番組", newWeekly(10))
	off.Disabled = true
	mustShow(off)

	now := e.ui.now()
	for _, j := range []struct {
		name  string
		hour  int
		state domain.State
	}{{"ジョブの番組", 5, domain.StateWaiting}, {"完了の番組", 6, domain.StateDone}, {"期限切れの番組", 7, domain.StateExpired}} {
		a := air(j.hour, "", "", 0)
		addJob(t, e.store, j.name, "2026-10-07", 0, j.state, now, a)
	}
	return &guideEnv{showsEnv: e, area: area}
}

type guideDayResp struct {
	Day      string `json:"day"`
	Stations []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Airings []struct {
			StationID    string `json:"stationId"`
			StationName  string `json:"stationName"`
			Title        string `json:"title"`
			State        string `json:"state"`
			ShowName     string `json:"showName"`
			JobID        int64  `json:"jobId"`
			NotAvailable bool   `json:"notAvailable"`
		} `json:"airings"`
	} `json:"stations"`
}

func TestGuideDayStates(t *testing.T) {
	e := newGuideUI(t)
	var got guideDayResp
	e.call("GET", "/api/guide?day=2026-10-07", "", 200, &got)
	if got.Day != "2026-10-07" || len(got.Stations) != 2 || got.Stations[0].ID != "TBS" || got.Stations[0].Name != "TBSラジオ" {
		t.Fatalf("応答 = %+v", got)
	}
	type want struct {
		state, show string
		na          bool
	}
	wants := map[string]want{
		"朝の番組":     {"planned", "ジョブの番組", false},
		"取得済み":     {"done", "完了の番組", false},
		"取れなかった":   {"missed", "期限切れの番組", false},
		"毎週の予約":    {"planned", "時刻の番組", false},
		"単発":       {"planned", "単発の番組", false},
		"無効な番組":    {"none", "", false},
		"対象外":      {"none", "", true},
		"Ａｎｄ　Ｍｏｒｅ": {"none", "", false},
	}
	// ジョブの放送回のタイトルは空で作ったので、番組表のタイトルで引く（Key で照合する）。
	n := 0
	for _, a := range got.Stations[0].Airings {
		w, ok := wants[a.Title]
		if !ok {
			t.Fatalf("想定外の放送回 %+v", a)
		}
		n++
		if a.State != w.state || a.ShowName != w.show || a.NotAvailable != w.na || a.StationName != "TBSラジオ" {
			t.Errorf("%s = %+v, want %+v", a.Title, a, w)
		}
	}
	if n != len(wants) {
		t.Fatalf("放送回の数 = %d", n)
	}
	// ほかの局の放送回は、同じ時刻でも TBS の毎週の予約に当たらない。
	if q := got.Stations[1].Airings[0]; q.State != "none" {
		t.Errorf("別の局 = %+v", q)
	}
}

func TestGuideDayRange(t *testing.T) {
	e := newGuideUI(t)
	// 今日は 2026-10-07。範囲は 09-30 から 10-14。
	for _, d := range []string{"2026-09-30", "2026-10-14"} {
		e.call("GET", "/api/guide?day="+d, "", 200, nil)
	}
	for _, d := range []string{"2026-09-29", "2026-10-15", "", "あした", "2026-13-01"} {
		e.call("GET", "/api/guide?day="+d, "", 400, nil)
	}
	// 範囲の外は radiko に取りにいかない。
	for _, asked := range e.area.asked {
		if asked != "2026-09-30" && asked != "2026-10-14" {
			t.Errorf("範囲の外の放送日 %s を取りにいった", asked)
		}
	}
}

func TestGuideBadGateway(t *testing.T) {
	e := newGuideUI(t)
	e.area.fail = true
	e.call("GET", "/api/guide?day=2026-10-07", "", 502, nil)
	e.call("GET", "/api/guide/search?q=a", "", 502, nil)
}

type guideSearchResp struct {
	Q         string `json:"q"`
	Truncated bool   `json:"truncated"`
	Airings   []struct {
		Title       string `json:"title"`
		StationName string `json:"stationName"`
		State       string `json:"state"`
		Start       string `json:"start"`
	} `json:"airings"`
}

func TestGuideSearch(t *testing.T) {
	e := newGuideUI(t)
	titles := func(q string) []string {
		var got guideSearchResp
		e.call("GET", "/api/guide/search?q="+urlQuery(q), "", 200, &got)
		var out []string
		for _, a := range got.Airings {
			out = append(out, a.Title)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	// 正規化（全角 → 半角、空白の除去、大文字化）してから番組名の部分一致を見る。
	eq(titles("and more"), "Ａｎｄ　Ｍｏｒｅ")
	eq(titles("ANDM"), "Ａｎｄ　Ｍｏｒｅ")
	// 出演者の部分一致。放送日をまたいで探し、開始時刻の順に返す。
	eq(titles("田中太郎"), "Ａｎｄ　Ｍｏｒｅ")
	eq(titles("花子"), "あした")
	eq(titles("ない言葉"))
	// 説明だけに当たる放送回も返す。
	eq(titles("こちら"), "あした")
	// 空白区切りの語は AND で、別の欄にまたがってよい。
	eq(titles("あした 花子 詳細"), "あした")
	eq(titles("and　田中"), "Ａｎｄ　Ｍｏｒｅ")
	eq(titles("花子 田中"))
	// 単発の番組名「単発」は、番組表の番組名の「単発」に当たり、状態も付く。
	var got guideSearchResp
	e.call("GET", "/api/guide/search?q="+urlQuery("単発"), "", 200, &got)
	if len(got.Airings) != 1 || got.Airings[0].State != "planned" || got.Airings[0].StationName != "TBSラジオ" {
		t.Fatalf("単発 = %+v", got)
	}
	// 範囲の 15 日分を引く。
	if len(e.area.asked) < 15 {
		t.Fatalf("取りにいった放送日 = %v", e.area.asked)
	}
	// 空の q は 400（空白だけも）。
	e.call("GET", "/api/guide/search", "", 400, nil)
	e.call("GET", "/api/guide/search?q="+urlQuery(" 　"), "", 400, nil)
}

func TestGuideSearchOrderedByStart(t *testing.T) {
	e := newGuideUI(t)
	var got guideSearchResp
	e.call("GET", "/api/guide/search?q="+urlQuery("の"), "", 200, &got)
	for i := 1; i < len(got.Airings); i++ {
		if got.Airings[i-1].Start > got.Airings[i].Start {
			t.Fatalf("開始時刻の順でない: %+v", got.Airings)
		}
	}
	if len(got.Airings) < 2 {
		t.Fatalf("%+v", got.Airings)
	}
}

func TestGuideNeedsSession(t *testing.T) {
	e := newGuideUI(t)
	for _, p := range []string{"/api/guide?day=2026-10-07", "/api/guide/search?q=a"} {
		if rec := do(e.h, "GET", p, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
}

func urlQuery(s string) string { return url.QueryEscape(s) }

// 番組表の API は、説明を部品の並び（descriptionParts）でも返す。説明が空なら空の配列。
func TestGuideDescriptionParts(t *testing.T) {
	e := newGuideUI(t)
	var got struct {
		Stations []struct {
			Airings []struct {
				Title            string `json:"title"`
				Description      string `json:"description"`
				DescriptionParts []struct {
					Text string `json:"text"`
					Href string `json:"href"`
				} `json:"descriptionParts"`
			} `json:"airings"`
		} `json:"stations"`
	}
	e.call("GET", "/api/guide?day=2026-10-08", "", 200, &got)
	a := got.Stations[0].Airings[0]
	if a.Title != "あした" || a.Description != "詳細はこちら" || len(a.DescriptionParts) != 2 ||
		a.DescriptionParts[0].Text != "詳細は" || a.DescriptionParts[0].Href != "" ||
		a.DescriptionParts[1].Text != "こちら" || a.DescriptionParts[1].Href != "https://example.com/" {
		t.Errorf("あした = %+v", a)
	}
	// 説明が空の放送回は、null ではなく空の配列で返す。
	got.Stations = nil
	e.call("GET", "/api/guide?day=2026-10-07", "", 200, &got)
	if p := got.Stations[0].Airings[0].DescriptionParts; p == nil || len(p) != 0 {
		t.Errorf("説明が空の放送回の descriptionParts = %#v", p)
	}
}

// 今日より前の放送日で、ジョブの無い放送回は、1 回の予約に当たるときだけ録音予定。毎週の予約だけなら予約なし。
func TestGuideDayPastWithoutJob(t *testing.T) {
	e := newGuideUI(t)
	ctx := context.Background()
	// 今日は 2026-10-07（水）。2026-10-05（月）は過去、2026-10-12（月）は未来。
	mon := func(h int) domain.Weekly {
		return domain.NewWeekly("TBS", time.Monday, domain.Clock{Hour: h})
	}
	monday := domain.NewShow("月曜の番組", mon(8), mon(9))
	monday.CheckTitle = false // 番組表の番組名が番組の名前と食い違っても録らない予定にしない
	if err := e.store.CreateShow(ctx, monday); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateOnce(ctx, "単発の番組", domain.Once{StationID: "TBS", Start: jst(2026, 10, 5, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateOnce(ctx, "単発の番組", domain.Once{StationID: "TBS", Start: jst(2026, 10, 12, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []int{5, 12} {
		e.area.stations[fmt.Sprintf("2026-10-%02d", d)] = []app.AreaStation{{ID: "TBS", Name: "TBSラジオ", Airings: []domain.Airing{
			{StationID: "TBS", Start: jst(2026, 10, d, 8, 0), End: jst(2026, 10, d, 9, 0), Title: "毎週の予約だけ"},
			{StationID: "TBS", Start: jst(2026, 10, d, 9, 0), End: jst(2026, 10, d, 10, 0), Title: "単発も"},
		}}}
	}
	states := func(day string) map[string]string {
		var got guideDayResp
		e.call("GET", "/api/guide?day="+day, "", 200, &got)
		out := map[string]string{}
		for _, a := range got.Stations[0].Airings {
			out[a.Title] = a.State
		}
		return out
	}
	if got := states("2026-10-05"); got["毎週の予約だけ"] != "none" || got["単発も"] != "planned" {
		t.Errorf("過去: %v", got)
	}
	if got := states("2026-10-12"); got["毎週の予約だけ"] != "planned" || got["単発も"] != "planned" {
		t.Errorf("未来: %v", got)
	}
}

// 今日より前の放送日で、開始日を持つ毎週の予約に当たる放送回は、展開と同じ当たり方で状態を決める（番組名が合えば
// 録音予定、食い違えば録らなかった）。録らなかった（skipped）のジョブの放送回も録らなかった。
func TestGuideDayPastWeeklyFrom(t *testing.T) {
	e := newGuideUI(t)
	ctx := context.Background()
	// 今日は 2026-10-07（水）。2026-10-06（火）は過去。
	tue := func(h int) domain.Weekly {
		return domain.NewWeekly("TBS", time.Tuesday, domain.Clock{Hour: h}).WithFrom(domain.NewBroadcastDay(2026, 10, 6))
	}
	if err := e.store.CreateShow(ctx, domain.NewShow("火曜の番組", tue(8), tue(9))); err != nil {
		t.Fatal(err)
	}
	air := func(h int, title string) domain.Airing {
		return domain.Airing{StationID: "TBS", Start: jst(2026, 10, 6, h, 0), End: jst(2026, 10, 6, h+1, 0), Title: title}
	}
	e.area.stations["2026-10-06"] = []app.AreaStation{{ID: "TBS", Name: "TBSラジオ", Airings: []domain.Airing{
		air(8, "火曜の番組"), air(9, "特番"), air(10, "録らなかったのジョブ"),
	}}}
	jobID := addJob(t, e.store, "別の番組", "2026-10-06", 0, domain.StateSkipped, e.ui.now(), air(10, ""))
	var got guideDayResp
	e.call("GET", "/api/guide?day=2026-10-06", "", 200, &got)
	want := map[string][2]string{
		"火曜の番組":      {"planned", "火曜の番組"},
		"特番":         {"skipped", "火曜の番組"},
		"録らなかったのジョブ": {"skipped", "別の番組"},
	}
	for _, a := range got.Stations[0].Airings {
		if w := want[a.Title]; a.State != w[0] || a.ShowName != w[1] {
			t.Errorf("%s = %s %s, want %v", a.Title, a.State, a.ShowName, w)
		}
		// jobId はジョブのある放送回にだけ付く（番組表の詳細が手動再試行に使う）。
		if wantID := map[bool]int64{true: jobID}[a.Title == "録らなかったのジョブ"]; a.JobID != wantID {
			t.Errorf("%s の jobId = %d, want %d", a.Title, a.JobID, wantID)
		}
	}
}

// chaptersSettings は設定 chapters だけを返す Settings。
type chaptersSettings struct {
	Settings
	chapters bool
}

func (s chaptersSettings) Current() config.Config {
	return config.Config{Chapters: s.chapters, Membership: domain.MembershipFree}
}

// ジョブの無いタイムフリーの放送回の、放送の前か後かの時刻（最初の試行の時刻）は、設定 chapters に依らず終了の数分後で、
// チャプターを待って遅らせない（展開と同じ。ADR 0027）。
func TestGuideDaySkipWithoutJobChapters(t *testing.T) {
	for _, tt := range []struct {
		settings Settings
		want     string
	}{
		// 最初の試行は 11:04:30 で、今（12:00）はその後。
		{nil, "skipped"},
		{chaptersSettings{chapters: true}, "skipped"},
		{chaptersSettings{chapters: false}, "skipped"},
	} {
		e := newGuideUI(t)
		e.ui.Settings = tt.settings
		// 今は 2026-10-07（水）の 12:00。
		if err := e.store.CreateShow(context.Background(), domain.NewShow("QRRの番組", domain.NewWeekly("QRR", time.Wednesday, domain.Clock{Hour: 10}))); err != nil {
			t.Fatal(err)
		}
		st := &e.area.stations["2026-10-07"][1]
		st.Airings = append(st.Airings, domain.Airing{StationID: "QRR", Start: jst(2026, 10, 7, 10, 0), End: jst(2026, 10, 7, 11, 0), Title: "特番"})
		var got guideDayResp
		e.call("GET", "/api/guide?day=2026-10-07", "", 200, &got)
		found := false
		for _, a := range got.Stations[1].Airings {
			if a.Title == "特番" {
				found = true
				if a.State != tt.want {
					t.Errorf("settings %+v: state = %s, want %s", tt.settings, a.State, tt.want)
				}
			}
		}
		if !found {
			t.Fatal("特番が無い")
		}
	}
}

// ジョブの無い放送回の録らない予定・録らなかったは、その放送回だけのジョブの最初の試行の時刻（展開が確定する時刻）で分ける。
// タイムフリーの対象外の放送回（ライブで録る）は放送の開始の 1 分前、それ以外は放送の終了の後。
func TestGuideDaySkipWithoutJobLive(t *testing.T) {
	e := newGuideUI(t)
	// 今は 2026-10-07（水）の 12:00。
	qrr := func(h int) domain.Weekly { return domain.NewWeekly("QRR", time.Wednesday, domain.Clock{Hour: h}) }
	if err := e.store.CreateShow(context.Background(), domain.NewShow("QRRの番組", qrr(11), qrr(12), qrr(13))); err != nil {
		t.Fatal(err)
	}
	air := func(h int, title string, inNG int) domain.Airing {
		return domain.Airing{StationID: "QRR", Start: jst(2026, 10, 7, h, 0), End: jst(2026, 10, 7, h+1, 0), Title: title,
			TimeFree: domain.TimeFreeFlags{InNG: inNG}}
	}
	st := &e.area.stations["2026-10-07"][1]
	st.Airings = append(st.Airings, air(11, "特番A", 0), air(12, "特番B", 2), air(13, "特番C", 2))
	var got guideDayResp
	e.call("GET", "/api/guide?day=2026-10-07", "", 200, &got)
	want := map[string]string{
		"特番A": "skip_planned", // タイムフリー: 最初の試行（終了の後の 12:04:30）の前
		"特番B": "skipped",      // ライブ: 最初の試行（開始の 1 分前の 11:59）を過ぎた
		"特番C": "skip_planned", // ライブ: 最初の試行（12:59）の前
	}
	n := 0
	for _, a := range got.Stations[1].Airings {
		w, ok := want[a.Title]
		if !ok {
			continue
		}
		n++
		if a.State != w || a.ShowName != "QRRの番組" {
			t.Errorf("%s = %s %s, want %s", a.Title, a.State, a.ShowName, w)
		}
	}
	if n != len(want) {
		t.Fatalf("放送回の数 = %d", n)
	}
}
