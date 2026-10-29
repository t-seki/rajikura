package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// newOnceUI は、1 回の予約の API と番組を変えた直後の展開（app.App）をつないだ UI を作る。今は 2026-10-07（水曜）の 12 時。
// 今日の TBS: 13:00 すっぴん、14:00 ニュース、15:00 対象外（ts_in_ng）。展開用の番組表（fakeGuide）と
// 画面用のエリアの番組表（fakeAreaGuide）は同じ放送回を返す。
func newOnceUI(t *testing.T) (*showsEnv, *fakeAreaGuide) {
	t.Helper()
	e := newExpandUI(t)
	airings := []domain.Airing{
		{StationID: "TBS", Start: jst(2026, 10, 7, 13, 0), End: jst(2026, 10, 7, 14, 0), Title: "すっぴん"},
		{StationID: "TBS", Start: jst(2026, 10, 7, 14, 0), End: jst(2026, 10, 7, 14, 10), Title: "ニュース"},
		{StationID: "TBS", Start: jst(2026, 10, 7, 15, 0), End: jst(2026, 10, 7, 16, 0), Title: "対象外", TimeFree: domain.TimeFreeFlags{InNG: 1}},
	}
	e.guide.airings["TBS/2026-10-07"] = airings
	// 昨日（火曜）の 13:00 の放送回。
	past := []domain.Airing{
		{StationID: "TBS", Start: jst(2026, 10, 6, 13, 0), End: jst(2026, 10, 6, 14, 0), Title: "昨日のすっぴん"},
	}
	e.guide.airings["TBS/2026-10-06"] = past
	area := &fakeAreaGuide{stations: map[string][]app.AreaStation{
		"2026-10-07": {{ID: "TBS", Name: "TBSラジオ", Airings: airings}},
		"2026-10-06": {{ID: "TBS", Name: "TBSラジオ", Airings: past}},
	}}
	ui := *e.ui
	ui.Onces = e.store
	ui.Jobs = e.store
	ui.AreaGuide = area
	e.ui = &ui
	e.h = Handler(Health{LastPoll: time.Now}, &ui)
	e.cookie = login(t, e.h)
	return e, area
}

func osPath(name string) string { return showPath(name) + "/onces" }

func airingsBody(starts ...int) string {
	var parts []string
	for _, h := range starts {
		parts = append(parts, `{"station":"TBS","start":"`+jst(2026, 10, 7, h, 0).Format(time.RFC3339)+`"}`)
	}
	return `{"airings":[` + strings.Join(parts, ",") + `]}`
}

type conflictResp struct {
	Reason   string `json:"reason"`
	ShowName string `json:"showName"`
	Airing   struct {
		Station string    `json:"station"`
		Start   time.Time `json:"start"`
	} `json:"airing"`
}

type createdResp struct {
	Onces []onceJSON `json:"onces"`
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func (e *showsEnv) onceCount(name string) int {
	e.t.Helper()
	s, err := e.store.GetShow(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(s.Onces)
}

func TestOnceCreateDeleteExpandsNow(t *testing.T) {
	e, _ := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	if got := e.waitingShows(); len(got) != 0 {
		t.Fatalf("足す前: %v", got)
	}

	var created createdResp
	e.call("POST", osPath("単発の番組"), airingsBody(13), 201, &created)
	if len(created.Onces) != 1 || created.Onces[0].Title != "すっぴん" || created.Onces[0].StationName != "TBSラジオ" ||
		created.Onces[0].End == nil || created.Onces[0].Missing {
		t.Fatalf("created = %+v", created)
	}
	if got := e.waitingShows(); len(got) != 1 || got["単発の番組"] != 1 {
		t.Fatalf("足した後にジョブができていない: %v", got)
	}

	e.call("DELETE", osPath("単発の番組")+"/"+idStr(created.Onces[0].ID), "", 204, nil)
	if got := e.waitingShows(); len(got) != 0 {
		t.Fatalf("消した後にジョブが残っている: %v", got)
	}
	if n := e.onceCount("単発の番組"); n != 0 {
		t.Fatalf("1 回の予約が残っている: %d", n)
	}
}

func TestOnceBatchIsAllOrNothing(t *testing.T) {
	e, _ := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"他の番組","weekly":[{"station":"TBS","weekday":3,"start":"14:00"}]}`, 201, nil)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	before := e.waitingShows()

	// 2 つ目が他の番組に当たっている: 1 つも足さない。
	var c conflictResp
	e.call("POST", osPath("単発の番組"), airingsBody(13, 14), 409, &c)
	if c.Reason != "other_show" || c.ShowName != "他の番組" || !c.Airing.Start.Equal(jst(2026, 10, 7, 14, 0)) {
		t.Fatalf("conflict = %+v", c)
	}
	// 2 つ目が番組表に無い: 1 つも足さない。
	e.call("POST", osPath("単発の番組"), airingsBody(13, 9), 400, nil)
	if n := e.onceCount("単発の番組"); n != 0 {
		t.Fatalf("断ったのに足されている: %d", n)
	}
	if got := e.waitingShows(); got["単発の番組"] != 0 || len(got) != len(before) {
		t.Fatalf("断ったのにジョブが変わった: %v → %v", before, got)
	}
}

func TestOnceBatchAddsAll(t *testing.T) {
	e, _ := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	var created createdResp
	e.call("POST", osPath("単発の番組"), airingsBody(13, 14), 201, &created)
	if len(created.Onces) != 2 || e.onceCount("単発の番組") != 2 {
		t.Fatalf("created = %+v", created)
	}
	// 連結する番組なので、同じ放送日の 2 つの放送回は 1 つのジョブに入る。
	if got := e.waitingShows(); got["単発の番組"] != 2 {
		t.Fatalf("waiting = %v", got)
	}
}

func TestOnceRejections(t *testing.T) {
	e, area := newOnceUI(t)
	// 番組表の番組名（すっぴん）が番組の名前と食い違うので、番組名を確かめない番組にする（録らない予定の放送回は足せるため）。
	e.call("POST", "/api/shows", `{"name":"毎週の予約の番組","check_title":false,"weekly":[{"station":"TBS","weekday":3,"start":"13:00"}]}`, 201, nil)
	e.call("POST", "/api/shows", `{"name":"無効な番組","enabled":false,"weekly":[{"station":"TBS","weekday":3,"start":"14:00"}]}`, 201, nil)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)

	var c conflictResp
	// 同じ番組が毎週の予約で当てている。
	e.call("POST", osPath("毎週の予約の番組"), airingsBody(13), 409, &c)
	if c.Reason != "already" || c.ShowName != "毎週の予約の番組" {
		t.Fatalf("同じ番組の毎週の予約: %+v", c)
	}
	// ほかの番組の毎週の予約が当てている。
	e.call("POST", osPath("単発の番組"), airingsBody(13), 409, &c)
	if c.Reason != "other_show" || c.ShowName != "毎週の予約の番組" {
		t.Fatalf("ほかの番組の毎週の予約: %+v", c)
	}
	// 番組表に無い放送回。
	e.call("POST", osPath("単発の番組"), airingsBody(9), 400, nil)
	// 範囲の外の放送日。
	e.call("POST", osPath("単発の番組"), `{"airings":[{"station":"TBS","start":"2026-09-01T10:00:00+09:00"}]}`, 400, nil)
	// 空・壊れた JSON・同じ放送回が重なる。
	e.call("POST", osPath("単発の番組"), `{"airings":[]}`, 400, nil)
	e.call("POST", osPath("単発の番組"), `{`, 400, nil)
	e.call("POST", osPath("単発の番組"), airingsBody(14, 14), 400, nil)
	// 無い番組。
	e.call("POST", osPath("無い番組"), airingsBody(14), 404, nil)

	// 無効なほかの番組の毎週の予約は当たっていないので、足せる。足した後に同じ番組へもう一度は「すでに録音予定」。
	e.call("POST", osPath("単発の番組"), airingsBody(14), 201, nil)
	e.call("POST", osPath("単発の番組"), airingsBody(14), 409, &c)
	if c.Reason != "already" || c.ShowName != "単発の番組" {
		t.Fatalf("同じ番組の 1 回の予約: %+v", c)
	}
	// ほかの番組の 1 回の予約が当てている。
	e.call("POST", osPath("無効な番組"), airingsBody(14), 409, &c)
	if c.Reason != "other_show" || c.ShowName != "単発の番組" {
		t.Fatalf("ほかの番組の 1 回の予約: %+v", c)
	}

	// radiko から番組表を取れなければ 502。
	area.fail = true
	e.call("POST", osPath("単発の番組"), airingsBody(13), 502, nil)
}

// タイムフリーの対象外の放送回にも 1 回の予約を足せ、展開でライブで録るジョブができる。録音期限（放送の終了の 5 分後）を
// 過ぎた対象外の放送回は、録音期限を過ぎた放送回として 400 で断る。
func TestOnceTimeFreeExcluded(t *testing.T) {
	e, area := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	var created createdResp
	e.call("POST", osPath("単発の番組"), airingsBody(15), 201, &created)
	if len(created.Onces) != 1 || created.Onces[0].Title != "対象外" {
		t.Fatalf("created = %+v", created)
	}
	if got := e.waitingShows(); got["単発の番組"] != 1 {
		t.Fatalf("足した後にジョブができていない: %v", got)
	}
	jobs, err := e.store.ListJobs(context.Background(), domain.StateWaiting)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !jobs[0].Live() {
		t.Fatalf("ライブで録るジョブになっていない: %+v", jobs)
	}

	// 今は 12:00。11:00〜11:30 の対象外の放送回は録音期限（11:35）を過ぎ、11:00〜11:56 の放送回は過ぎていない（12:01）。
	ended := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 11, 0), End: jst(2026, 10, 7, 11, 30), Title: "終わった対象外",
		TimeFree: domain.TimeFreeFlags{InNG: 2}}
	area.stations["2026-10-07"][0].Airings = append([]domain.Airing{ended}, area.stations["2026-10-07"][0].Airings...)
	var bad map[string]string
	e.call("POST", osPath("単発の番組"), airingsBody(11), 400, &bad)
	if !strings.Contains(bad["error"], "録音期限を過ぎた放送回") {
		t.Fatalf("録音期限を過ぎた放送回として断っていない: %v", bad)
	}
	area.stations["2026-10-07"][0].Airings[0].End = jst(2026, 10, 7, 11, 56)
	area.stations["2026-10-07"][0].Airings[0].Title = "終わったばかりの対象外"
	e.call("POST", osPath("単発の番組"), airingsBody(11), 201, nil)
}

func TestOnceListInShow(t *testing.T) {
	e, area := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"単発の番組","enabled":false,"weekly":[]}`, 201, nil)
	e.call("POST", osPath("単発の番組"), airingsBody(13), 201, nil) // 無効な番組にも足せる

	// 番組表の更新で 13:00 の放送回が消えた。
	full := area.stations["2026-10-07"][0].Airings
	area.stations["2026-10-07"][0].Airings = full[1:]
	var got showJSON
	e.call("GET", showPath("単発の番組"), "", 200, &got)
	if len(got.Onces) != 1 || !got.Onces[0].Missing || got.Onces[0].Title != "" ||
		!got.Onces[0].Start.Equal(jst(2026, 10, 7, 13, 0)) || got.Onces[0].StationID != "TBS" {
		t.Fatalf("onces = %+v", got.Onces)
	}
	// 番組表にあれば、番組名と終了時刻が付く。
	area.stations["2026-10-07"][0].Airings = full
	e.call("GET", showPath("単発の番組"), "", 200, &got)
	if got.Onces[0].Missing || got.Onces[0].Title != "すっぴん" || got.Onces[0].End == nil {
		t.Fatalf("onces = %+v", got.Onces)
	}
	// 番組表を取れなければ、番組の読み込みは成功し、missing は false のまま（分からない）。
	area.fail = true
	got = showJSON{}
	e.call("GET", showPath("単発の番組"), "", 200, &got)
	if len(got.Onces) != 1 || got.Onces[0].Missing || got.Onces[0].Title != "" {
		t.Fatalf("onces = %+v", got.Onces)
	}
	// 一覧には付けない。
	var list []showJSON
	e.call("GET", "/api/shows", "", 200, &list)
	if len(list) != 1 || list[0].Onces != nil {
		t.Fatalf("list = %+v", list)
	}
}

func TestOnceDeleteOfOtherShow(t *testing.T) {
	e, _ := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"A","weekly":[]}`, 201, nil)
	e.call("POST", "/api/shows", `{"name":"B","weekly":[]}`, 201, nil)
	var created createdResp
	e.call("POST", osPath("A"), airingsBody(13), 201, &created)
	id := idStr(created.Onces[0].ID)
	e.call("DELETE", osPath("B")+"/"+id, "", 404, nil)
	e.call("DELETE", osPath("無い")+"/"+id, "", 404, nil)
	e.call("DELETE", osPath("A")+"/x", "", 400, nil)
	if n := e.onceCount("A"); n != 1 {
		t.Fatalf("消えてしまった: %d", n)
	}
	e.call("DELETE", osPath("A")+"/"+id, "", 204, nil)
	e.call("DELETE", osPath("A")+"/"+id, "", 404, nil)
}

func pastBody() string {
	return `{"airings":[{"station":"TBS","start":"` + jst(2026, 10, 6, 13, 0).Format(time.RFC3339) + `"}]}`
}

// 今日より前の放送日では、同じ番組の毎週の予約だけに当たる放送回（ジョブなし）に 1 回の予約を足せ、展開でジョブができる。
// ほかの番組の毎週の予約に当たるときは、過去の放送日でも other_show。
func TestOncePastDay(t *testing.T) {
	e, _ := newOnceUI(t)
	weekly := `{"station":"TBS","weekday":2,"start":"13:00"}`
	e.call("POST", "/api/shows", `{"name":"毎週の予約の番組","weekly":[`+weekly+`]}`, 201, nil)
	if got := e.waitingShows(); got["毎週の予約の番組"] != 0 {
		t.Fatalf("過去の放送日の毎週の予約からジョブができた: %v", got)
	}
	e.call("POST", "/api/shows", `{"name":"ほかの番組","weekly":[]}`, 201, nil)
	var c conflictResp
	e.call("POST", osPath("ほかの番組"), pastBody(), 409, &c)
	if c.Reason != "other_show" || c.ShowName != "毎週の予約の番組" {
		t.Fatalf("過去の放送日のほかの番組の毎週の予約: %+v", c)
	}

	e.call("POST", osPath("毎週の予約の番組"), pastBody(), 201, nil)
	if got := e.waitingShows(); got["毎週の予約の番組"] != 1 {
		t.Fatalf("1 回の予約を足した後にジョブができていない: %v", got)
	}
	// 1 回の予約の Key が一致するので、もう一度は already。
	e.call("POST", osPath("毎週の予約の番組"), pastBody(), 409, &c)
	if c.Reason != "already" {
		t.Fatalf("%+v", c)
	}
}

// 放送回にジョブがあれば、そのジョブの番組で決める。
func TestOnceJobDecides(t *testing.T) {
	e, _ := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"ほかの番組","weekly":[]}`, 201, nil)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	a := domain.Airing{StationID: "TBS", Start: jst(2026, 10, 7, 13, 0), End: jst(2026, 10, 7, 14, 0), Title: "すっぴん"}
	addJob(t, e.store, "ほかの番組", "2026-10-07", 0, domain.StateDone, jst(2026, 10, 7, 12, 0), a)
	var c conflictResp
	e.call("POST", osPath("単発の番組"), airingsBody(13), 409, &c)
	if c.Reason != "other_show" || c.ShowName != "ほかの番組" {
		t.Fatalf("ほかの番組のジョブ: %+v", c)
	}
	e.call("POST", osPath("ほかの番組"), airingsBody(13), 409, &c)
	if c.Reason != "already" || c.ShowName != "ほかの番組" {
		t.Fatalf("同じ番組のジョブ: %+v", c)
	}
}

// racingOnces は CreateOnces が、並行するリクエストに先を越されたように ErrExists を返す保存先。
type racingOnces struct{ Onces }

func (racingOnces) CreateOnces(ctx context.Context, showName string, os []domain.Once) ([]int64, error) {
	return nil, fmt.Errorf("競合: %w", store.ErrExists)
}

// 競合で保存先に断られたときは 409 の already。どの放送回か分からないので airing は付かない。
func TestOnceRaceHasNoAiring(t *testing.T) {
	e, _ := newOnceUI(t)
	e.call("POST", "/api/shows", `{"name":"単発の番組","weekly":[]}`, 201, nil)
	e.ui.Onces = racingOnces{e.store}
	rec := do(e.h, "POST", osPath("単発の番組"), airingsBody(13), e.cookie)
	if rec.Code != 409 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["reason"] != "already" {
		t.Errorf("reason = %v", raw["reason"])
	}
	if _, ok := raw["airing"]; ok {
		t.Errorf("airing が付いている: %v", raw["airing"])
	}
}

func TestOnceNeedSession(t *testing.T) {
	e, _ := newOnceUI(t)
	for _, c := range [][2]string{{"POST", osPath("x")}, {"DELETE", osPath("x") + "/1"}} {
		if rec := do(e.h, c[0], c[1], airingsBody(13)); rec.Code != 401 {
			t.Errorf("%s %s = %d, want 401", c[0], c[1], rec.Code)
		}
	}
}
