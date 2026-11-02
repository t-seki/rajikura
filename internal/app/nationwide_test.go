package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/radiko"
)

// nationwideServer は radiko の偽のサーバー（認証、full.xml、自分のエリア（JP13）の日別番組表、局ごとの日別番組表）。
// 局は TBS（JP13）・MBS（JP27）・ABC（JP27）の 3 つで、自分のエリアの番組表には TBS だけが出る。
type nationwideServer struct {
	srv *httptest.Server
	// stationHits は局ごとの日別番組表の要求の数（パス → 回数）。
	mu          sync.Mutex
	stationHits map[string]int
	// fail は落とす局 ID。
	fail map[string]bool
}

func newNationwideServer(t *testing.T) *nationwideServer {
	t.Helper()
	f := &nationwideServer{stationHits: map[string]int{}, fail: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/api/auth1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Radiko-AuthToken", "tok")
		w.Header().Set("X-Radiko-KeyOffset", "3")
		w.Header().Set("X-Radiko-KeyLength", "8")
	})
	mux.HandleFunc("/v2/api/auth2", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "JP13,東京都,tokyo Japan\n")
	})
	mux.HandleFunc("/v3/station/region/full.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<region><stations>`+
			`<station><id>TBS</id><name>TBSラジオ</name><area_id>JP13</area_id></station>`+
			`<station><id>MBS</id><name>MBSラジオ</name><area_id>JP27</area_id></station>`+
			`<station><id>ABC</id><name>ABCラジオ</name><area_id>JP27</area_id></station>`+
			`</stations></region>`)
	})
	mux.HandleFunc("/v3/program/date/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<radiko><stations><station id="TBS"><name>TBSラジオ</name><progs></progs></station></stations></radiko>`)
	})
	mux.HandleFunc("/v3/program/station/date/", func(w http.ResponseWriter, r *http.Request) {
		// /v3/program/station/date/<YYYYMMDD>/<局>.xml
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, ".xml"), "/")
		day, st := parts[len(parts)-2], parts[len(parts)-1]
		f.mu.Lock()
		f.stationHits[r.URL.Path]++
		fail := f.fail[st]
		f.mu.Unlock()
		if fail {
			http.Error(w, "x", http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprintf(w, `<radiko><stations><station id="%s"><name>%s</name><progs>`+
			`<prog id="1" ft="%s130000" to="%s140000"><title>%s の昼の番組</title><pfm>共通の出演者</pfm></prog>`+
			`</progs></station></stations></radiko>`, st, st, day, day, st)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *nationwideServer) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.stationHits {
		n += v
	}
	return n
}

func (f *nationwideServer) guide(now *time.Time) *RadikoAreaGuide {
	c := radiko.NewClient()
	c.AuthHost, c.ProgramHost = f.srv.URL, f.srv.URL
	c.Now = func() time.Time { return *now }
	c.EnableProgramCache(30, 1<<20, nil)
	g := NewRadikoAreaGuide(c)
	g.Now = func() time.Time { return *now }
	return g
}

// 全国の検索は、全国の局の局ごとの日別の番組表から探し、自分のエリアの局一覧に無い局の放送回にエリアの外から録る印が立つ。
func TestNationwideAiringsMarksOutOfArea(t *testing.T) {
	f := newNationwideServer(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, domain.JST)
	g := f.guide(&now)
	days := []domain.BroadcastDay{domain.NewBroadcastDay(2026, time.October, 13), domain.NewBroadcastDay(2026, time.October, 14)}
	m := domain.NewKeywordMatcher("共通の出演者", "")
	got, err := g.NationwideAirings(context.Background(), days, m.Match)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stations != 3 || got.FailedStations != 0 || len(got.Airings) != 6 {
		t.Fatalf("結果 = %+v", got)
	}
	for _, a := range got.Airings {
		wantOut := a.Station.ID != "TBS"
		if a.Airing.OutOfArea != wantOut || a.Station.Own == wantOut || a.Airing.StationID != a.Station.ID {
			t.Errorf("%s の放送回 = %+v", a.Station.ID, a)
		}
		wantArea := map[string]string{"TBS": "JP13", "MBS": "JP27", "ABC": "JP27"}[a.Station.ID]
		if a.Station.AreaID != wantArea || a.Station.Name == "" {
			t.Errorf("%s の局 = %+v", a.Station.ID, a.Station)
		}
	}
	// keep に当たらない放送回は返さない。
	none, err := g.NationwideAirings(context.Background(), days, func(domain.Airing) bool { return false })
	if err != nil || len(none.Airings) != 0 {
		t.Fatalf("当たらないはず: %+v, %v", none, err)
	}
	// 2 回目は取り直しを緩めて読むので、radiko に尋ねない（局 3 × 放送日 2 = 6 回のまま）。
	if f.hits() != 6 {
		t.Fatalf("局ごとの日別番組表の要求 = %d（want 6）", f.hits())
	}
}

// 取れなかった局があっても取れた分を返し、取れなかった局の数を返す。
func TestNationwideAiringsPartialFailure(t *testing.T) {
	f := newNationwideServer(t)
	f.fail["ABC"] = true
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, domain.JST)
	g := f.guide(&now)
	days := []domain.BroadcastDay{domain.NewBroadcastDay(2026, time.October, 13)}
	got, err := g.NationwideAirings(context.Background(), days, func(domain.Airing) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range got.Airings {
		ids = append(ids, a.Station.ID)
	}
	slices.Sort(ids)
	if got.Stations != 3 || got.FailedStations != 1 || !slices.Equal(ids, []string{"MBS", "TBS"}) {
		t.Fatalf("結果 = %+v（局 %v）", got, ids)
	}
	// 取れなかった分はキャッシュに入らないので、次で取り直す。
	f.mu.Lock()
	f.fail["ABC"] = false
	f.mu.Unlock()
	got, err = g.NationwideAirings(context.Background(), days, func(domain.Airing) bool { return true })
	if err != nil || got.FailedStations != 0 || len(got.Airings) != 3 {
		t.Fatalf("取り直した結果 = %+v, %v", got, err)
	}
}

// 取り込みは全国の局の全部の放送日を取り、取り込んだ後の全国の検索は radiko に尋ねない。
// 全国の分を捨てると、自分のエリアの局（TBS）の分だけが残る。
func TestPrefetchAndDropNationwide(t *testing.T) {
	f := newNationwideServer(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, domain.JST)
	g := f.guide(&now)
	var days []domain.BroadcastDay
	for i := range 5 {
		days = append(days, domain.NewBroadcastDay(2026, time.October, 10+i))
	}
	fetched, failed, err := g.PrefetchNationwide(context.Background(), days)
	if err != nil || fetched != 15 || failed != 0 || f.hits() != 15 {
		t.Fatalf("fetched = %d, failed = %d, err = %v, 要求 = %d", fetched, failed, err, f.hits())
	}
	if _, err := g.NationwideAirings(context.Background(), days, func(domain.Airing) bool { return true }); err != nil || f.hits() != 15 {
		t.Fatalf("取り込んだ後の検索で尋ねた: err = %v, 要求 = %d", err, f.hits())
	}
	if n := g.DropNationwide(context.Background()); n != 10 {
		t.Fatalf("捨てた件数 = %d（want MBS と ABC の 10）", n)
	}
	if _, err := g.NationwideAirings(context.Background(), days, func(domain.Airing) bool { return true }); err != nil || f.hits() != 25 {
		t.Fatalf("捨てた後の検索: err = %v, 要求 = %d（want 25）", err, f.hits())
	}
}

// fakeNationwide は NationwideGuide の代わり。取り込み・捨てたことを記録する。
type fakeNationwide struct {
	mu       sync.Mutex
	prefetch [][]domain.BroadcastDay
	drops    int
	// block があれば、取り込みは ctx が取り消されるまで戻らない。
	block    atomic.Bool
	canceled atomic.Int32
	calls    chan struct{}
}

func (f *fakeNationwide) NationwideAirings(context.Context, []domain.BroadcastDay, func(domain.Airing) bool) (Nationwide, error) {
	return Nationwide{}, nil
}

func (f *fakeNationwide) PrefetchNationwide(ctx context.Context, days []domain.BroadcastDay) (int, int, error) {
	f.mu.Lock()
	f.prefetch = append(f.prefetch, days)
	f.mu.Unlock()
	f.calls <- struct{}{}
	if f.block.Load() {
		<-ctx.Done()
		f.canceled.Add(1)
		return 0, 0, ctx.Err()
	}
	return len(days), 0, nil
}

func (f *fakeNationwide) DropNationwide(context.Context) int {
	f.mu.Lock()
	f.drops++
	f.mu.Unlock()
	f.calls <- struct{}{}
	return 1
}

func (f *fakeNationwide) snapshot() (prefetch [][]domain.BroadcastDay, drops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prefetch), f.drops
}

func waitCall(t *testing.T, f *fakeNationwide) {
	t.Helper()
	select {
	case <-f.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("取り込み役が呼ばない")
	}
}

func noCall(t *testing.T, f *fakeNationwide) {
	t.Helper()
	select {
	case <-f.calls:
		t.Fatal("呼ばないはずが呼んだ")
	case <-time.After(100 * time.Millisecond):
	}
}

func nationwideApp(f *fakeNationwide, m *atomic.Value, interval time.Duration) *App {
	return &App{
		Nationwide:         f,
		NationwideInterval: interval,
		Now:                func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, domain.JST) },
		Live: func() Runtime {
			return Runtime{Config: config.Config{Membership: m.Load().(domain.Membership)}}
		},
	}
}

// エリアフリー・ダブルプランでは、起動の直後と間ごとに全国の番組表を取り込む（エリアフリーは 15 日分、ダブルプランは 38 日分）。
// 非会員・タイムフリー30 では取り込まない。
func TestRunNationwideByMembership(t *testing.T) {
	for _, tc := range []struct {
		m    domain.Membership
		days int
	}{{domain.MembershipAreaFree, 15}, {domain.MembershipDouble, 38}, {domain.MembershipFree, 0}, {domain.MembershipTimeFree30, 0}} {
		t.Run(string(tc.m), func(t *testing.T) {
			f := &fakeNationwide{calls: make(chan struct{}, 16)}
			var m atomic.Value
			m.Store(tc.m)
			a := nationwideApp(f, &m, 50*time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); a.runNationwide(ctx) }()
			if tc.days == 0 {
				noCall(t, f)
				noCall(t, f)
			} else {
				waitCall(t, f) // 起動の直後
				waitCall(t, f) // 間の後
			}
			cancel()
			<-done
			pre, drops := f.snapshot()
			if drops != 0 {
				t.Errorf("捨てた回数 = %d", drops)
			}
			for _, days := range pre {
				if len(days) != tc.days || days[0] != domain.NewBroadcastDay(2026, time.October, 13).AddDays(-(tc.days-8)) ||
					days[len(days)-1] != domain.NewBroadcastDay(2026, time.October, 20) {
					t.Errorf("取り込んだ放送日 = %v .. %v（%d 日）", days[0], days[len(days)-1], len(days))
				}
			}
		})
	}
}

// 会員種別を変えて保存したら、取り込み役が起きる。エリアフリー・ダブルプランでなくしたら、取り込みを止めて全国の分を捨てる。
// エリアフリーとダブルプランの間で変えたら、取り込む放送日の範囲を合わせてやり直す。非会員からエリアフリーにしたら取り込みを始める。
func TestRunNationwideFollowsMembershipChange(t *testing.T) {
	f := &fakeNationwide{calls: make(chan struct{}, 16)}
	var m atomic.Value
	m.Store(domain.MembershipFree)
	a := nationwideApp(f, &m, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.runNationwide(ctx) }()
	save := func(from, to domain.Membership) {
		m.Store(to)
		mp := string(to)
		a.SettingsApplied(context.Background(), config.Patch{Membership: &mp}, config.Config{Membership: from}, config.Config{Membership: to}, nil)
	}
	noCall(t, f)

	// 非会員 → エリアフリー: 取り込みを始める（取り込みの途中で止める）。
	f.block.Store(true)
	save(domain.MembershipFree, domain.MembershipAreaFree)
	waitCall(t, f)
	// エリアフリー → ダブルプラン: 範囲が変わるので止めてやり直す。
	save(domain.MembershipAreaFree, domain.MembershipDouble)
	waitCall(t, f)
	if f.canceled.Load() != 1 {
		t.Fatalf("取り込みを止めた回数 = %d", f.canceled.Load())
	}
	// ダブルプラン → タイムフリー30: 止めて捨てる。
	save(domain.MembershipDouble, domain.MembershipTimeFree30)
	waitCall(t, f)
	pre, drops := f.snapshot()
	if f.canceled.Load() != 2 || drops != 1 || len(pre) != 2 || len(pre[0]) != 15 || len(pre[1]) != 38 {
		t.Fatalf("止めた = %d, 捨てた = %d, 取り込み = %d 回（%v）", f.canceled.Load(), drops, len(pre), pre)
	}
	// 取り込んでいないあいだに起こされても（タイムフリー30 のまま）、もう一度は捨てない。
	a.wakeNationwide()
	noCall(t, f)
	cancel()
	<-done
}
