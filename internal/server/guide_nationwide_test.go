package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
)

// fakeNationwide は全国の検索の番組表の代わり。
type fakeNationwide struct {
	mu       sync.Mutex
	airings  []app.NationwideAiring
	stations int
	failed   int
	err      error
	days     []domain.BroadcastDay
}

func (f *fakeNationwide) NationwideAirings(ctx context.Context, days []domain.BroadcastDay, keep func(domain.Airing) bool) (app.Nationwide, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.days = days
	if f.err != nil {
		return app.Nationwide{}, f.err
	}
	out := app.Nationwide{Stations: f.stations, FailedStations: f.failed}
	for _, a := range f.airings {
		if keep(a.Airing) {
			out.Airings = append(out.Airings, a)
		}
	}
	return out, nil
}

func (f *fakeNationwide) PrefetchNationwide(context.Context, []domain.BroadcastDay) (int, int, error) {
	return 0, 0, nil
}

func (f *fakeNationwide) DropNationwide(context.Context) int { return 0 }

type nationwideSearchResp struct {
	Scope          string `json:"scope"`
	FailedStations *int   `json:"failedStations"`
	Airings        []struct {
		StationID       string `json:"stationId"`
		StationName     string `json:"stationName"`
		StationAreaName string `json:"stationAreaName"`
		StationAreaID   string `json:"stationAreaId"`
		Title           string `json:"title"`
		State           string `json:"state"`
	} `json:"airings"`
}

// newNationwideUI は、全国の検索の番組表を持たせた番組表の UI。自分のエリアの TBS と、エリアの外の MBS・ABC が、同じ時刻に
// 同じ番組名（系列のネット番組）を流す。
func newNationwideUI(t *testing.T) (*guideEnv, *fakeNationwide, *domain.Membership) {
	t.Helper()
	e := newGuideUI(t)
	m := domain.MembershipAreaFree
	e.ui.Settings = membershipSettings{m: &m}
	air := func(st string, out bool) domain.Airing {
		return domain.Airing{StationID: st, Start: jst(2026, 10, 7, 20, 0), End: jst(2026, 10, 7, 21, 0), Title: "ネットの番組", OutOfArea: out}
	}
	nw := &fakeNationwide{stations: 3, airings: []app.NationwideAiring{
		{Airing: air("TBS", false), Station: app.NationwideStation{ID: "TBS", Name: "TBSラジオ", AreaID: "JP13", Own: true}},
		{Airing: air("MBS", true), Station: app.NationwideStation{ID: "MBS", Name: "MBSラジオ", AreaID: "JP27"}},
		{Airing: air("ABC", true), Station: app.NationwideStation{ID: "ABC", Name: "ABCラジオ", AreaID: "JP27"}},
	}}
	e.ui.Nationwide = nw
	return e, nw, &m
}

// 全国の検索は、全国の局の放送回から探し、局ごとにまとめずに返す。自分のエリアの外の局には局のエリアの名前が付く。
func TestGuideSearchNationwide(t *testing.T) {
	e, nw, m := newNationwideUI(t)
	for _, mm := range []domain.Membership{domain.MembershipAreaFree, domain.MembershipDouble} {
		*m = mm
		var got nationwideSearchResp
		e.call("GET", "/api/guide/search?q="+urlQuery("ネット")+"&scope=all", "", 200, &got)
		if got.Scope != "all" || got.FailedStations == nil || *got.FailedStations != 0 || len(got.Airings) != 3 {
			t.Fatalf("%s: 応答 = %+v", mm, got)
		}
		areas := map[string]string{}
		areaIDs := map[string]string{}
		for _, a := range got.Airings {
			areas[a.StationID] = a.StationAreaName
			areaIDs[a.StationID] = a.StationAreaID
			if a.Title != "ネットの番組" || a.State != "none" || a.StationName == "" {
				t.Errorf("%s: 放送回 = %+v", mm, a)
			}
		}
		if areas["TBS"] != "" || areas["MBS"] != "大阪府" || areas["ABC"] != "大阪府" {
			t.Errorf("%s: 局のエリアの名前 = %v", mm, areas)
		}
		if areaIDs["TBS"] != "" || areaIDs["MBS"] != "JP27" || areaIDs["ABC"] != "JP27" {
			t.Errorf("%s: 局のエリアの ID = %v", mm, areaIDs)
		}
		// 表示する範囲の全部の放送日を探す（エリアフリーは 15 日、ダブルプランは 38 日）。
		want := map[domain.Membership]int{domain.MembershipAreaFree: 15, domain.MembershipDouble: 38}[mm]
		if len(nw.days) != want {
			t.Errorf("%s: 探した放送日 = %d 日", mm, len(nw.days))
		}
	}
	// scope を省けば、自分のエリアの検索（全国の局は探さず、scope・failedStations・局のエリアの名前は付かない）。
	var own nationwideSearchResp
	e.call("GET", "/api/guide/search?q="+urlQuery("ネット"), "", 200, &own)
	if own.Scope != "" || own.FailedStations != nil || len(own.Airings) != 0 {
		t.Fatalf("自分のエリアの検索 = %+v", own)
	}
}

// 取れなかった局があっても取れた分を返し、取れなかった局の数を返す。全部取れなかったとき・局の一覧が分からないときは 502。
func TestGuideSearchNationwideFailures(t *testing.T) {
	e, nw, _ := newNationwideUI(t)
	nw.failed = 1
	var got nationwideSearchResp
	e.call("GET", "/api/guide/search?q="+urlQuery("ネット")+"&scope=all", "", 200, &got)
	if got.FailedStations == nil || *got.FailedStations != 1 || len(got.Airings) != 3 {
		t.Fatalf("応答 = %+v", got)
	}
	nw.failed, nw.airings = 3, nil
	e.call("GET", "/api/guide/search?q="+urlQuery("ネット")+"&scope=all", "", 502, nil)
	nw.failed, nw.err = 0, errors.New("落ちた")
	e.call("GET", "/api/guide/search?q="+urlQuery("ネット")+"&scope=all", "", 502, nil)
}

// scope=all は、選べない会員種別・area との併用・all のほかの値で 400。
func TestGuideSearchNationwideRejects(t *testing.T) {
	e, _, m := newNationwideUI(t)
	q := "/api/guide/search?q=" + urlQuery("ネット")
	for _, mm := range []domain.Membership{domain.MembershipFree, domain.MembershipTimeFree30} {
		*m = mm
		var bad map[string]string
		e.call("GET", q+"&scope=all", "", 400, &bad)
		if !strings.Contains(bad["error"], "エリアフリー") {
			t.Errorf("%s: 断る理由 = %v", mm, bad)
		}
	}
	*m = domain.MembershipDouble
	e.call("GET", q+"&scope=all&area=JP27", "", 400, nil)
	e.call("GET", q+"&scope=all&area=JP13", "", 400, nil)
	e.call("GET", q+"&scope=own", "", 400, nil)
	e.call("GET", q+"&scope=ALL", "", 400, nil)
	// 全国の検索の番組表が無ければ（組み立て）400。
	e.ui.Nationwide = nil
	e.call("GET", q+"&scope=all", "", 400, nil)
}
