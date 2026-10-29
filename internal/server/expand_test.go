package server

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
)

// newExpandUI は、番組の API に番組を変えた直後の展開（app.App）をつないだ UI を作り、展開役を起こしておく。
// 番組表は今日（2026-10-07 水曜）の TBS だけ: 13:00 の「すっぴん」と 14:00 の「ニュース」。
func newExpandUI(t *testing.T) *showsEnv {
	t.Helper()
	e := newExpandUIWithoutExpander(t)
	startExpander(t, e.ui.Expander.(*app.App))
	return e
}

// startExpander は展開役を起こし、テストの終わりに止める。
func startExpander(t *testing.T, a *app.App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.RunExpander(ctx); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// newExpandUIWithoutExpander は newExpandUI と同じ UI を、展開役を起こさずに作る。
func newExpandUIWithoutExpander(t *testing.T) *showsEnv {
	t.Helper()
	e := newShowsUI(t)
	today := domain.NewBroadcastDay(2026, time.October, 7)
	e.guide.airings["TBS/"+today.String()] = []domain.Airing{
		{StationID: "TBS", Start: jst(2026, 10, 7, 13, 0), End: jst(2026, 10, 7, 14, 0), Title: "すっぴん"},
		{StationID: "TBS", Start: jst(2026, 10, 7, 14, 0), End: jst(2026, 10, 7, 14, 10), Title: "ニュース"},
	}
	now := jst(2026, 10, 7, 12, 0)
	e.now = &now
	nowFn := func() time.Time { return now }
	a := &app.App{
		Store: e.store, Guide: e.guide, Membership: domain.MembershipFree,
		Now: nowFn,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ui := &UI{
		Sessions: e.store, Shows: e.store, Guide: e.guide, Expander: a, Passwords: e.ui.Passwords,
		Files: e.ui.Files, Now: nowFn, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.ui = ui
	e.h = Handler(Health{LastPoll: time.Now}, ui)
	e.cookie = login(t, e.h)
	return e
}

// waitingShows は、積まれている展開の依頼を展開し終えてから、waiting のジョブの番組の名前と、その放送回の数を返す。
func (e *showsEnv) waitingShows() map[string]int {
	e.t.Helper()
	if a, ok := e.ui.Expander.(*app.App); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.ExpandPending(ctx); err != nil {
			e.t.Fatal(err)
		}
	}
	return e.waitingNow()
}

// waitingNow は、展開の依頼を待たずに、今の waiting のジョブの番組の名前と、その放送回の数を返す。
func (e *showsEnv) waitingNow() map[string]int {
	e.t.Helper()
	jobs, err := e.store.ListJobs(context.Background(), domain.StateWaiting)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int{}
	for _, j := range jobs {
		out[j.ShowName] += len(j.Airings)
	}
	return out
}

const suppinTimeJSON = `{"station":"TBS","weekday":3,"start":"13:00"}`

// 番組の API は展開を待たずに応答を返す（展開役が動いていなくても返る）。積まれた依頼は、展開役が動き出してから展開される。
func TestShowsRespondWithoutWaitingForExpand(t *testing.T) {
	e := newExpandUIWithoutExpander(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.call("POST", "/api/shows", `{"name":"すっぴん","weekly":[`+suppinTimeJSON+`]}`, 201, nil)
		e.call("POST", "/api/shows", `{"name":"ニュース","weekly":[{"station":"TBS","weekday":3,"start":"14:00"}]}`, 201, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("展開役が動いていないのに、応答が返らない")
	}
	if got := e.waitingNow(); len(got) != 0 {
		t.Fatalf("展開役が動く前にジョブができた: %v", got)
	}
	startExpander(t, e.ui.Expander.(*app.App))
	if got := e.waitingShows(); len(got) != 2 || got["すっぴん"] != 1 || got["ニュース"] != 1 {
		t.Fatalf("展開役が動いた後: %v", got)
	}
}

// 番組の作成・置き換え（名前の変更）・有効／無効・削除の後に、同期を待たずに展開する。
func TestShowsExpandAfterChange(t *testing.T) {
	e := newExpandUI(t)
	e.call("POST", "/api/shows", `{"name":"すっぴん","weekly":[`+suppinTimeJSON+`]}`, 201, nil)
	if got := e.waitingShows(); len(got) != 1 || got["すっぴん"] != 1 {
		t.Fatalf("作成の後: %v", got)
	}

	// 名前の変更で、古い名前の waiting のジョブは新しい名前に付け替わる。
	// 番組表の番組名（すっぴん）は新しい名前で始まらないので、番組名を確かめない番組にする（録らない予定にしない）。
	e.call("PUT", showPath("すっぴん"), `{"name":"すっぴん2","check_title":false,"weekly":[`+suppinTimeJSON+`]}`, 200, nil)
	if got := e.waitingShows(); len(got) != 1 || got["すっぴん2"] != 1 {
		t.Fatalf("名前の変更の後: %v", got)
	}
	// 毎週の予約を変えると、変える前の局・放送日も展開する（毎週の予約を別の局に移しても古いジョブが消える）。
	e.call("PUT", showPath("すっぴん2"), `{"name":"すっぴん2","check_title":false,"weekly":[{"station":"QRR","weekday":3,"start":"13:00"}]}`, 200, nil)
	if got := e.waitingShows(); len(got) != 0 {
		t.Fatalf("局を移した後: %v", got)
	}
	e.call("PUT", showPath("すっぴん2"), `{"name":"すっぴん2","check_title":false,"weekly":[`+suppinTimeJSON+`]}`, 200, nil)

	// 無効にすると waiting のジョブがすぐ消え、有効に戻すとすぐできる。
	e.call("PUT", showPath("すっぴん2")+"/enabled", `{"enabled":false}`, 200, nil)
	if got := e.waitingShows(); len(got) != 0 {
		t.Fatalf("無効にした後: %v", got)
	}
	e.call("PUT", showPath("すっぴん2")+"/enabled", `{"enabled":true}`, 200, nil)
	if got := e.waitingShows(); got["すっぴん2"] != 1 {
		t.Fatalf("有効に戻した後: %v", got)
	}

	// 削除の後もすぐ消える。
	e.call("DELETE", showPath("すっぴん2"), "", 204, nil)
	if got := e.waitingShows(); len(got) != 0 {
		t.Fatalf("削除の後: %v", got)
	}
	// 無い番組は今までどおり 404（展開しない）。
	e.call("DELETE", showPath("すっぴん2"), "", 404, nil)
	e.call("PUT", showPath("すっぴん2")+"/enabled", `{"enabled":true}`, 404, nil)
	e.call("PUT", showPath("すっぴん2"), `{"name":"すっぴん2","weekly":[]}`, 404, nil)
}

// 無効化・削除・名前の変更の直後に、放送済みで最初の試行を待つ waiting のジョブ（過去の放送日）も消える。
// 名前の変更では、古い名前の done でないジョブが新しい名前に付け替わるので、その過去の放送日の waiting のジョブは、
// 展開の後も新しい名前で残る（取りこぼさない。毎週の予約から作り直すのではない）。
func TestShowsExpandRemovesPastWaitingJobs(t *testing.T) {
	for _, tt := range []struct {
		name   string
		method string
		path   string
		body   string
		want   int
		// after は操作の後の waiting のジョブ（番組の名前 → 放送回の数）。
		after map[string]int
	}{
		{"無効化", "PUT", showPath("すっぴん") + "/enabled", `{"enabled":false}`, 200, map[string]int{}},
		{"削除", "DELETE", showPath("すっぴん"), "", 204, map[string]int{}},
		{"名前の変更", "PUT", showPath("すっぴん"), `{"name":"すっぴん（新）","weekly":[` + suppinTimeJSON + `]}`, 200,
			map[string]int{"すっぴん（新）": 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newExpandUI(t)
			e.call("POST", "/api/shows", `{"name":"すっぴん","weekly":[`+suppinTimeJSON+`]}`, 201, nil)
			if got := e.waitingShows(); got["すっぴん"] != 1 {
				t.Fatalf("作成の後: %v", got)
			}
			// 翌日。10-07 のジョブは waiting のまま（最初の試行の前）。
			*e.now = jst(2026, 10, 8, 12, 0)
			e.call(tt.method, tt.path, tt.body, tt.want, nil)
			if got := e.waitingShows(); !maps.Equal(got, tt.after) {
				t.Errorf("waiting のジョブ = %v, want %v", got, tt.after)
			}
			if tt.name == "名前の変更" {
				jobs, _ := e.store.ListJobs(context.Background(), domain.StateWaiting)
				if len(jobs) != 1 || jobs[0].Day != domain.NewBroadcastDay(2026, time.October, 7) {
					t.Errorf("新しい名前のジョブ = %+v", jobs)
				}
			}
		})
	}
}

// 番組を作った直後の展開でも、今日より前の放送日では、その日にほかの番組の waiting のジョブがあっても、開始日を持たない毎週の予約だけに
// 当たる放送回からジョブを作らない。すでにある waiting のジョブは残る。
func TestShowsExpandPastDayCreatesNoWeeklyJobs(t *testing.T) {
	e := newExpandUI(t)
	e.call("POST", "/api/shows", `{"name":"すっぴん","weekly":[`+suppinTimeJSON+`]}`, 201, nil)
	if got := e.waitingShows(); got["すっぴん"] != 1 {
		t.Fatalf("作成の後: %v", got)
	}
	// 翌日。10-07 のすっぴんのジョブは waiting のまま（最初の試行の前）。10-07 のニュースに当たる番組を作る。
	*e.now = jst(2026, 10, 8, 12, 0)
	e.call("POST", "/api/shows", `{"name":"ニュース","weekly":[{"station":"TBS","weekday":3,"start":"14:00"}]}`, 201, nil)
	if got := e.waitingShows(); !maps.Equal(got, map[string]int{"すっぴん": 1}) {
		t.Errorf("waiting のジョブ = %v, want map[すっぴん:1]", got)
	}
}

// 毎週の予約の無い番組を作れ、名前を変えると 1 回の予約が引き継がれて、新しい名前ですぐ展開される。
func TestShowsWithoutWeekly(t *testing.T) {
	e := newExpandUI(t)
	e.call("POST", "/api/shows", `{"name":"ニュース","weekly":[]}`, 201, nil)
	e.call("POST", "/api/shows", `{"name":"毎週の予約を書かない"}`, 201, nil)
	var got showJSON
	e.call("GET", showPath("毎週の予約を書かない"), "", 200, &got)
	if got.Weekly == nil || len(got.Weekly) != 0 {
		t.Errorf("毎週の予約の無い番組 = %+v", got)
	}
	if _, err := e.store.CreateOnce(context.Background(), "ニュース",
		domain.Once{StationID: "TBS", Start: jst(2026, 10, 7, 14, 0)}); err != nil {
		t.Fatal(err)
	}
	e.call("PUT", showPath("ニュース"), `{"name":"ニュース（単発）","weekly":[]}`, 200, nil)
	if got := e.waitingShows(); len(got) != 1 || got["ニュース（単発）"] != 1 {
		t.Fatalf("名前の変更の後: %v", got)
	}
	show, err := e.store.GetShow(context.Background(), "ニュース（単発）")
	if err != nil || len(show.Onces) != 1 {
		t.Errorf("1 回の予約が引き継がれていない: %+v, %v", show, err)
	}
	// 番組を消すと 1 回の予約も消え、ジョブもすぐ消える。
	e.call("DELETE", showPath("ニュース（単発）"), "", 204, nil)
	if got := e.waitingShows(); len(got) != 0 {
		t.Fatalf("削除の後: %v", got)
	}
}
