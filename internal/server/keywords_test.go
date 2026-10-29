package server

import (
	"net/url"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
)

// newKeywordUI は、キーワードの API と、番組を変えた直後の展開（app.App。キーワードも当てる）をつないだ UI を作る。
// 今は 2026-10-07（水曜）の 12 時。TBS の今日: 11:00 済んだ回（向井）、13:00 すっぴん（出演 向井）、14:00 ニュース。
// あした: 13:00 ゲスト回(1)・14:00 ゲスト回(2)（出演 向井）。展開用の番組表とエリアの番組表は同じ放送回を返す。
func newKeywordUI(t *testing.T) *showsEnv {
	t.Helper()
	e := newExpandUIWithoutExpander(t)
	today := []domain.Airing{
		{StationID: "TBS", Start: jst(2026, 10, 7, 11, 0), End: jst(2026, 10, 7, 12, 0), Title: "済んだ回", Performer: "向井"},
		{StationID: "TBS", Start: jst(2026, 10, 7, 13, 0), End: jst(2026, 10, 7, 14, 0), Title: "すっぴん", Performer: "向井"},
		{StationID: "TBS", Start: jst(2026, 10, 7, 14, 0), End: jst(2026, 10, 7, 14, 10), Title: "ニュース"},
	}
	tomorrow := []domain.Airing{
		{StationID: "TBS", Start: jst(2026, 10, 8, 13, 0), End: jst(2026, 10, 8, 14, 0), Title: "ゲスト回(1)", Performer: "向井"},
		{StationID: "TBS", Start: jst(2026, 10, 8, 14, 0), End: jst(2026, 10, 8, 15, 0), Title: "ゲスト回(2)", Performer: "向井"},
	}
	e.guide.airings["TBS/2026-10-07"] = today
	e.guide.airings["TBS/2026-10-08"] = tomorrow
	area := &fakeAreaGuide{stations: map[string][]app.AreaStation{
		"2026-10-07": {{ID: "TBS", Name: "TBSラジオ", Airings: today}},
		"2026-10-08": {{ID: "TBS", Name: "TBSラジオ", Airings: tomorrow}},
	}}
	a := e.ui.Expander.(*app.App)
	a.AreaGuide = area
	startExpander(t, a)
	ui := *e.ui
	ui.Onces = e.store
	ui.Jobs = e.store
	ui.Keywords = e.store
	ui.AreaGuide = area
	e.ui = &ui
	e.h = Handler(Health{LastPoll: time.Now}, &ui)
	e.cookie = login(t, e.h)
	return e
}

type keywordResp struct {
	ID       int64    `json:"id"`
	Words    string   `json:"words"`
	Exclude  string   `json:"exclude"`
	Stations []string `json:"stations"`
	Enabled  bool     `json:"enabled"`
	Pending  int      `json:"pending"`
}

type keywordListResp struct {
	Keywords []keywordResp `json:"keywords"`
}

func keywordPath(id int64) string { return "/api/keywords/" + idStr(id) }

func TestKeywordsAPI(t *testing.T) {
	e := newKeywordUI(t)
	var k keywordResp
	e.call("POST", "/api/keywords", `{"words":"向井"}`, 201, &k)
	if k.ID == 0 || k.Words != "向井" || k.Exclude != "" || k.Stations == nil || len(k.Stations) != 0 || !k.Enabled || k.Pending != 0 {
		t.Fatalf("作ったキーワード = %+v", k)
	}
	e.call("POST", "/api/keywords", `{"words":"　向井 "}`, 409, nil)
	e.call("POST", "/api/keywords", `{"words":"  "}`, 400, nil)
	e.call("POST", "/api/keywords", `{"words":"x","stations":[""]}`, 400, nil)
	e.call("POST", "/api/keywords", `{`, 400, nil)
	var k2 keywordResp
	e.call("POST", "/api/keywords", `{"words":"ニュース","exclude":"天気","stations":["QRR"],"enabled":false}`, 201, &k2)
	if k2.Enabled || k2.Exclude != "天気" || len(k2.Stations) != 1 || k2.Stations[0] != "QRR" {
		t.Fatalf("2 つ目 = %+v", k2)
	}

	// 作った直後に当て、足した番組の局・放送日を展開する。
	if got := e.waitingShows(); got["すっぴん"] != 1 || got["ゲスト回"] != 2 || len(got) != 2 {
		t.Fatalf("waiting のジョブ = %v", got)
	}
	var list keywordListResp
	e.call("GET", "/api/keywords", "", 200, &list)
	if len(list.Keywords) != 2 || list.Keywords[0].ID != k.ID || list.Keywords[0].Pending != 3 || list.Keywords[1].Pending != 0 {
		t.Fatalf("一覧 = %+v", list)
	}

	// 足した 1 回の予約には、足したキーワードの ID と語が付く。
	var r showJSON
	e.call("GET", showPath("ゲスト回"), "", 200, &r)
	if len(r.Onces) != 2 || r.Onces[0].KeywordID != k.ID || r.Onces[0].KeywordWords != "向井" {
		t.Fatalf("番組の 1 回の予約 = %+v", r.Onces)
	}

	var airings struct {
		Airings []keywordAiringJSON `json:"airings"`
	}
	e.call("GET", keywordPath(k.ID)+"/airings", "", 200, &airings)
	if len(airings.Airings) != 3 || airings.Airings[0].Title != "ゲスト回(2)" || airings.Airings[0].ShowName != "ゲスト回" ||
		airings.Airings[0].StationName != "TBSラジオ" || airings.Airings[0].State != "planned" || airings.Airings[2].Title != "すっぴん" ||
		!airings.Airings[2].End.Equal(jst(2026, 10, 7, 14, 0)) {
		t.Fatalf("足した放送回 = %+v", airings)
	}
	e.call("GET", keywordPath(999)+"/airings", "", 404, nil)
	e.call("GET", "/api/keywords/x/airings", "", 400, nil)

	// 変える。足した 1 回の予約は残る。
	var k3 keywordResp
	e.call("PUT", keywordPath(k.ID), `{"words":"向井 ゲスト","stations":["TBS"]}`, 200, &k3)
	if k3.ID != k.ID || k3.Words != "向井 ゲスト" || len(k3.Stations) != 1 || k3.Pending != 3 {
		t.Fatalf("変えたキーワード = %+v", k3)
	}
	e.call("PUT", keywordPath(k.ID), `{"words":"ニュース"}`, 409, nil)
	e.call("PUT", keywordPath(999), `{"words":"y"}`, 404, nil)
	e.call("PUT", keywordPath(k.ID), `{"words":""}`, 400, nil)

	// 消しても、足した 1 回の予約と作った番組は残る（キーワードの印は付かなくなる）。
	e.call("DELETE", keywordPath(k.ID), "", 204, nil)
	e.call("DELETE", keywordPath(k.ID), "", 404, nil)
	var after showJSON
	e.call("GET", showPath("ゲスト回"), "", 200, &after)
	if len(after.Onces) != 2 || after.Onces[0].KeywordID != 0 || after.Onces[0].KeywordWords != "" {
		t.Fatalf("消した後の 1 回の予約 = %+v", after.Onces)
	}
}

func TestKeywordPreview(t *testing.T) {
	e := newKeywordUI(t)
	var res struct {
		Airings   []guideAiringJSON `json:"airings"`
		Truncated bool              `json:"truncated"`
	}
	e.call("GET", "/api/keywords/preview?words="+url.QueryEscape("向井"), "", 200, &res)
	// 始まった回（済んだ回）は入らない。放送回の形は番組表の検索と同じ。
	if len(res.Airings) != 3 || res.Airings[0].Title != "すっぴん" || res.Airings[0].StationName != "TBSラジオ" ||
		res.Airings[0].State != "none" || res.Truncated {
		t.Fatalf("preview = %+v", res)
	}
	e.call("GET", "/api/keywords/preview?words="+url.QueryEscape("向井")+"&exclude="+url.QueryEscape("ゲスト"), "", 200, &res)
	if len(res.Airings) != 1 {
		t.Fatalf("除外する語 = %+v", res)
	}
	e.call("GET", "/api/keywords/preview?words="+url.QueryEscape("向井")+"&stations=QRR,LFR", "", 200, &res)
	if len(res.Airings) != 0 || res.Airings == nil {
		t.Fatalf("局の絞り込み = %+v", res)
	}
	e.call("GET", "/api/keywords/preview?words=", "", 400, nil)
	if rec := do(e.h, "GET", "/api/keywords/preview?words=x", ""); rec.Code != 401 {
		t.Errorf("セッションなし = %d", rec.Code)
	}
	if rec := do(e.h, "GET", "/api/keywords", ""); rec.Code != 401 {
		t.Errorf("セッションなし = %d", rec.Code)
	}
}
