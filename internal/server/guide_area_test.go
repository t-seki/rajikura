package server

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
)

// osakaStations は、自分のエリア（JP13）の外のエリア JP27 の 2026-10-07 の番組表。
func osakaStations() map[string]map[string][]app.AreaStation {
	return map[string]map[string][]app.AreaStation{"JP27": {"2026-10-07": {
		{ID: "ABC", Name: "ABCラジオ", Airings: []domain.Airing{
			{StationID: "ABC", Start: jst(2026, 10, 7, 13, 0), End: jst(2026, 10, 7, 14, 0), Title: "大阪の番組"},
		}},
	}}}
}

// area を渡すと、そのエリアの全局の放送回を返す。エリアフリーでなければ自分のエリア以外を断る。
func TestGuideDayArea(t *testing.T) {
	e := newGuideUI(t)
	e.area.others = osakaStations()
	m := domain.MembershipFree
	e.ui.Settings = membershipSettings{m: &m}

	// 非会員・タイムフリー30 は、自分のエリアなら（省いても、自分のエリア ID を渡しても）通り、ほかのエリアは 400。
	for _, mm := range []domain.Membership{domain.MembershipFree, domain.MembershipTimeFree30} {
		m = mm
		e.call("GET", "/api/guide?day=2026-10-07", "", 200, nil)
		e.call("GET", "/api/guide?day=2026-10-07&area=JP13", "", 200, nil)
		var bad map[string]string
		e.call("GET", "/api/guide?day=2026-10-07&area=JP27", "", 400, &bad)
		if !strings.Contains(bad["error"], "エリアフリー") {
			t.Errorf("%s: 断る理由 = %v", mm, bad)
		}
		e.call("GET", "/api/guide/search?q=a&area=JP27", "", 400, nil)
	}

	for _, mm := range []domain.Membership{domain.MembershipAreaFree, domain.MembershipDouble} {
		m = mm
		var got guideDayResp
		e.call("GET", "/api/guide?day=2026-10-07&area=JP27", "", 200, &got)
		if len(got.Stations) != 1 || got.Stations[0].ID != "ABC" || got.Stations[0].Airings[0].Title != "大阪の番組" ||
			got.Stations[0].Airings[0].StationName != "ABCラジオ" {
			t.Fatalf("%s: 応答 = %+v", mm, got)
		}
		// 省けば自分のエリア。
		e.call("GET", "/api/guide?day=2026-10-07", "", 200, &got)
		if got.Stations[0].ID != "TBS" {
			t.Errorf("%s: area を省いたときの先頭の局 = %s", mm, got.Stations[0].ID)
		}
		// 一覧にないエリアは 400。
		e.call("GET", "/api/guide?day=2026-10-07&area=JP99", "", 400, nil)
		// 検索も area のエリアを見る。
		var sr struct {
			Airings []struct {
				Title string `json:"title"`
			} `json:"airings"`
		}
		e.call("GET", "/api/guide/search?q=%E5%A4%A7%E9%98%AA&area=JP27", "", 200, &sr)
		if len(sr.Airings) != 1 || sr.Airings[0].Title != "大阪の番組" {
			t.Errorf("%s: 検索 = %+v", mm, sr)
		}
	}
}

// 番組表で選べるエリアの一覧。切り替えられるのはエリアフリー・ダブルプランだけ。
func TestGuideAreas(t *testing.T) {
	e := newGuideUI(t)
	m := domain.MembershipFree
	e.ui.Settings = membershipSettings{m: &m}
	type resp struct {
		Own        string `json:"own"`
		Switchable bool   `json:"switchable"`
		Areas      []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"areas"`
	}
	for mm, want := range map[domain.Membership]bool{
		domain.MembershipFree: false, domain.MembershipTimeFree30: false, domain.MembershipAreaFree: true, domain.MembershipDouble: true,
	} {
		m = mm
		var got resp
		e.call("GET", "/api/guide/areas", "", 200, &got)
		if got.Own != "JP13" || got.Switchable != want || len(got.Areas) != 2 || got.Areas[1].ID != "JP27" || got.Areas[1].Name != "大阪府" {
			t.Errorf("%s: 応答 = %+v, switchable want %v", mm, got, want)
		}
	}
	e.area.fail = true
	e.call("GET", "/api/guide/areas", "", 502, nil)
}

// 他のエリアの番組表から、エリアフリーの会員はその局の放送回に 1 回の予約を足せる。ほかの会員種別は番組表に無い放送回として断る。
func TestOnceInOtherArea(t *testing.T) {
	e, area := newOnceUI(t)
	area.others = osakaStations()
	m := domain.MembershipAreaFree
	e.ui.Settings = membershipSettings{m: &m}
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	body := `{"airings":[{"station":"ABC","start":"` + jst(2026, 10, 7, 13, 0).Format(time.RFC3339) + `"}]}`

	m = domain.MembershipFree
	var bad map[string]string
	e.call("POST", osPath("単発の番組"), body, 400, &bad)
	if !strings.Contains(bad["error"], "番組表に無い放送回") {
		t.Fatalf("断る理由 = %v", bad)
	}

	m = domain.MembershipAreaFree
	var got createdResp
	e.call("POST", osPath("単発の番組"), body, 201, &got)
	if len(got.Onces) != 1 || got.Onces[0].StationID != "ABC" || got.Onces[0].Title != "大阪の番組" || got.Onces[0].Missing {
		t.Fatalf("足した 1 回の予約 = %+v", got.Onces)
	}
	if !slices.Contains(area.askedAreas, "JP27") {
		t.Errorf("JP27 の番組表を見ていない: %v", area.askedAreas)
	}
}

// 他のエリアの番組表を取れないときは、その局の 1 回の予約だけ突き合わせを飛ばし、同じ放送日の自分のエリアの 1 回の予約は突き合わせる。
func TestOnceViewsOtherAreaFailure(t *testing.T) {
	e, area := newOnceUI(t)
	area.others = osakaStations()
	m := domain.MembershipAreaFree
	e.ui.Settings = membershipSettings{m: &m}
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	e.call("POST", osPath("単発の番組"), `{"airings":[{"station":"ABC","start":"`+jst(2026, 10, 7, 13, 0).Format(time.RFC3339)+`"}]}`, 201, nil)
	e.call("POST", osPath("単発の番組"), airingsBody(13), 201, nil)

	area.mu.Lock()
	area.failOthers = true
	area.mu.Unlock()
	var got showJSON
	e.call("GET", showPath("単発の番組"), "", 200, &got)
	if len(got.Onces) != 2 {
		t.Fatalf("onces = %+v", got.Onces)
	}
	for _, o := range got.Onces {
		switch o.StationID {
		case "TBS":
			if o.Missing || o.Title != "すっぴん" {
				t.Errorf("自分のエリアの 1 回の予約 = %+v", o)
			}
		case "ABC":
			if o.Missing || o.Title != "" {
				t.Errorf("他のエリアの 1 回の予約 = %+v（分からないので missing は false、題名は空）", o)
			}
		}
	}
}
