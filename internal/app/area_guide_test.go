package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/radiko"
)

// areaServer は radiko の偽のサーバー（認証とエリアの日別番組表だけ）。
type areaServer struct {
	srv       *httptest.Server
	auths     atomic.Int32
	programs  atomic.Int32
	failAuth  atomic.Bool
	failGuide atomic.Bool
	// lastProgram は最後の番組表の要求のパス。fullXML は full.xml の要求の数。
	lastProgram atomic.Value
	fullXML     atomic.Int32
	// delay は認証と番組表の応答を遅らせる（並列の呼び出しを重ねるため）。
	delay time.Duration
}

func newAreaServer(t *testing.T) *areaServer {
	t.Helper()
	f := &areaServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/api/auth1", func(w http.ResponseWriter, r *http.Request) {
		if f.failAuth.Load() {
			http.Error(w, "x", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Radiko-AuthToken", "tok")
		w.Header().Set("X-Radiko-KeyOffset", "3")
		w.Header().Set("X-Radiko-KeyLength", "8")
	})
	mux.HandleFunc("/v2/api/auth2", func(w http.ResponseWriter, r *http.Request) {
		f.auths.Add(1)
		time.Sleep(f.delay)
		fmt.Fprint(w, "JP13,東京都,tokyo Japan\n")
	})
	mux.HandleFunc("/v3/program/date/", func(w http.ResponseWriter, r *http.Request) {
		f.programs.Add(1)
		f.lastProgram.Store(r.URL.Path)
		time.Sleep(f.delay)
		if f.failGuide.Load() {
			http.Error(w, "x", http.StatusInternalServerError)
			return
		}
		// JP27 の番組表には、TBS（自分のエリアの JP13 にもある局）に加えて ABC（JP27 にだけある局）が出る。
		abc := ""
		if strings.HasSuffix(r.URL.Path, "/JP27.xml") {
			abc = `<station id="ABC"><name>ABCラジオ</name><progs><date>20261013</date>
<prog id="2" ft="20261013050000" to="20261013060000"><title>大阪の朝</title><ts_out_ng>2</ts_out_ng><tsplus_out_ng>1</tsplus_out_ng></prog>
</progs></station>`
		}
		fmt.Fprint(w, `<radiko><stations><station id="TBS"><name>TBSラジオ</name><progs><date>20261013</date>
<prog id="1" ft="20261013050000" to="20261013060000"><title>朝</title><pfm>出演者</pfm><ts_in_ng>1</ts_in_ng></prog>
</progs></station>`+abc+`</stations></radiko>`)
	})
	mux.HandleFunc("/v3/station/region/full.xml", func(w http.ResponseWriter, r *http.Request) {
		f.fullXML.Add(1)
		fmt.Fprint(w, `<region><stations><station><id>ABC</id><area_id>JP27</area_id></station><station><id>TBS</id><area_id>JP13</area_id></station></stations></region>`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *areaServer) guide() (*RadikoAreaGuide, *time.Time) {
	c := radiko.NewClient()
	c.AuthHost, c.ProgramHost = f.srv.URL, f.srv.URL
	g := NewRadikoAreaGuide(c)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, domain.JST)
	g.Now = func() time.Time { return now }
	return g, &now
}

func TestRadikoAreaGuideCachesAreaIDOnly(t *testing.T) {
	f := newAreaServer(t)
	g, now := f.guide()
	ctx := context.Background()
	d1 := domain.NewBroadcastDay(2026, time.October, 13)
	d2 := domain.NewBroadcastDay(2026, time.October, 14)

	ss, err := g.AreaAirings(ctx, d1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != "TBS" || ss[0].Name != "TBSラジオ" || len(ss[0].Airings) != 1 {
		t.Fatalf("局 = %+v", ss)
	}
	if a := ss[0].Airings[0]; a.StationID != "TBS" || a.Title != "朝" || a.Performer != "出演者" || a.TimeFree.InNG != 1 {
		t.Fatalf("放送回 = %+v", a)
	}

	// 番組表は覚えない（radiko.Client のキャッシュに任せる。このテストの Client はキャッシュを有効にしていない）。
	// エリア ID は 1 時間の内側なら、別の放送日でも認証し直さずに使う。
	*now = now.Add(59 * time.Minute)
	if _, err := g.AreaAirings(ctx, d1); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AreaAirings(ctx, d2); err != nil {
		t.Fatal(err)
	}
	if f.programs.Load() != 3 || f.auths.Load() != 1 {
		t.Fatalf("番組表の要求 = %d（want 3）、認証 = %d（want 1）", f.programs.Load(), f.auths.Load())
	}

	// 1 時間を過ぎたらエリア ID も取り直す。
	*now = now.Add(2 * time.Minute)
	if _, err := g.AreaAirings(ctx, d1); err != nil {
		t.Fatal(err)
	}
	if f.programs.Load() != 4 || f.auths.Load() != 2 {
		t.Fatalf("番組表の要求 = %d（want 4）、認証 = %d（want 2）", f.programs.Load(), f.auths.Load())
	}
}

func TestRadikoAreaGuideFailuresAreNotCached(t *testing.T) {
	f := newAreaServer(t)
	g, _ := f.guide()
	ctx := context.Background()
	d := domain.NewBroadcastDay(2026, time.October, 13)

	f.failAuth.Store(true)
	if _, err := g.AreaAirings(ctx, d); err == nil {
		t.Fatal("認証に失敗したのにエラーにならない")
	}
	f.failAuth.Store(false)
	f.failGuide.Store(true)
	if _, err := g.AreaAirings(ctx, d); err == nil {
		t.Fatal("番組表を取れないのにエラーにならない")
	}
	f.failGuide.Store(false)
	if _, err := g.AreaAirings(ctx, d); err != nil {
		t.Fatalf("直った後も失敗している: %v", err)
	}
}

func TestRadikoAreaGuideConcurrentCallsShareFetches(t *testing.T) {
	f := newAreaServer(t)
	f.delay = 100 * time.Millisecond
	g, _ := f.guide()
	ctx := context.Background()
	d1 := domain.NewBroadcastDay(2026, time.October, 13)
	d2 := domain.NewBroadcastDay(2026, time.October, 14)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := d1
			if i%2 == 1 {
				d = d2
			}
			if ss, err := g.AreaAirings(ctx, d); err != nil || len(ss) != 1 {
				t.Errorf("AreaAirings = %v, %v", ss, err)
			}
		}()
	}
	wg.Wait()
	// 認証は 1 回、放送日ごとの番組表も 1 回ずつ。
	if f.auths.Load() != 1 || f.programs.Load() != 2 {
		t.Fatalf("認証 = %d（want 1）、番組表の要求 = %d（want 2）", f.auths.Load(), f.programs.Load())
	}
}

// fn がパニックしても、その key の取得は詰まらない。待っていた側にはエラーが返り、次の呼び出しは fn を行える。
func TestRadikoAreaGuideDoSurvivesPanic(t *testing.T) {
	g := NewRadikoAreaGuide(radiko.NewClient())
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	leaderDone := make(chan any, 1)
	go func() {
		defer func() { leaderDone <- recover() }()
		g.do(ctx, "k", func(context.Context) (any, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started
	waiterErr := make(chan error, 1)
	go func() {
		_, err := g.do(ctx, "k", func(context.Context) (any, error) { return "waiter ran fn", nil })
		waiterErr <- err
	}()
	// 待つ側が取得中のものに相乗りするのを待ってから、パニックさせる。
	time.Sleep(100 * time.Millisecond)
	close(release)
	if r := <-leaderDone; r != "boom" {
		t.Fatalf("パニックが先頭の呼び出しに伝わらない: %v", r)
	}
	if err := <-waiterErr; err == nil {
		t.Fatal("待っていた側がエラーを受け取らない")
	}
	v, err := g.do(ctx, "k", func(context.Context) (any, error) { return "ok", nil })
	if err != nil || v != "ok" {
		t.Fatalf("パニックの後の取得 = %v, %v", v, err)
	}
}

// エリアを指した番組表は、そのエリアの番組表を引く。自分のエリア以外では、自分のエリア（認証で判定する）の番組表も引き、その局一覧に
// 無い局の放送回にエリアの外から録る印（domain.Airing.OutOfArea）を立てる（#27）。エリアの一覧は full.xml から作り、1 時間覚える。
func TestRadikoAreaGuideOtherArea(t *testing.T) {
	f := newAreaServer(t)
	g, now := f.guide()
	ctx := context.Background()
	d := domain.NewBroadcastDay(2026, time.October, 13)

	ss, err := g.AreaAiringsOf(ctx, "JP27", d)
	if err != nil {
		t.Fatal(err)
	}
	if f.programs.Load() != 2 || f.auths.Load() != 1 {
		t.Fatalf("番組表の要求 = %d、認証 = %d（want JP27 と自分のエリアの 2 つ、認証 1 回）", f.programs.Load(), f.auths.Load())
	}
	if len(ss) != 2 || ss[0].ID != "TBS" || ss[1].ID != "ABC" {
		t.Fatalf("局 = %+v", ss)
	}
	// TBS は自分のエリア（JP13）にもあるので in、ABC は自分のエリアに無いので out のフラグで決める。
	if a := ss[0].Airings[0]; a.OutOfArea || !a.TimeFreeExcluded() {
		t.Errorf("TBS の放送回 = %+v", a)
	}
	if a := ss[1].Airings[0]; !a.OutOfArea || !a.TimeFreeExcluded() || !domain.ExcludedFromTimeFree30(a) ||
		a.TimeFree != (domain.TimeFreeFlags{OutNG: 2, PlusOutNG: 1}) {
		t.Errorf("ABC の放送回 = %+v", a)
	}
	// 空のエリア ID は自分のエリア。自分のエリアの番組表には印を立てない。
	own, err := g.AreaAiringsOf(ctx, "", d)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastProgram.Load(); got != "/v3/program/date/20261013/JP13.xml" || f.auths.Load() != 1 {
		t.Fatalf("要求のパス = %v、認証 = %d", got, f.auths.Load())
	}
	if len(own) != 1 || own[0].Airings[0].OutOfArea {
		t.Fatalf("自分のエリアの局 = %+v", own)
	}

	info, err := g.Areas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Own != "JP13" || len(info.Areas) != 2 || info.Areas[0] != (Area{"JP13", "東京都"}) || info.Areas[1] != (Area{"JP27", "大阪府"}) ||
		info.StationArea["ABC"] != "JP27" {
		t.Fatalf("Areas = %+v", info)
	}
	*now = now.Add(59 * time.Minute)
	if _, err := g.Areas(ctx); err != nil {
		t.Fatal(err)
	}
	if f.fullXML.Load() != 1 {
		t.Fatalf("full.xml の要求 = %d（want 1）", f.fullXML.Load())
	}
	*now = now.Add(2 * time.Minute)
	if _, err := g.Areas(ctx); err != nil {
		t.Fatal(err)
	}
	if f.fullXML.Load() != 2 {
		t.Fatalf("1 時間後の full.xml の要求 = %d（want 2）", f.fullXML.Load())
	}
}
