package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		time.Sleep(f.delay)
		if f.failGuide.Load() {
			http.Error(w, "x", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `<radiko><stations><station id="TBS"><name>TBSラジオ</name><progs><date>20261013</date>
<prog id="1" ft="20261013050000" to="20261013060000"><title>朝</title><pfm>出演者</pfm><ts_in_ng>1</ts_in_ng></prog>
</progs></station></stations></radiko>`)
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
