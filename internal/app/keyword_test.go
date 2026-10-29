package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// fakeAreaGuide は、偽の radiko（fakeRadiko）の局ごとの番組表を、エリアの日別番組表として返す（展開の番組表と同じ放送回）。
// failDays の放送日はエラーにする。
type fakeAreaGuide struct {
	f *fakeRadiko

	mu       sync.Mutex
	failDays map[string]bool
}

var areaStationNames = map[string]string{"TBS": "TBSラジオ", "QRR": "文化放送"}

func (g *fakeAreaGuide) AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error) {
	g.mu.Lock()
	fail := g.failDays[day.String()]
	g.mu.Unlock()
	if fail {
		return nil, errors.New("落ちた")
	}
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	prefix := fmt.Sprintf("%04d%02d%02d/", day.Year, int(day.Month), day.Day)
	var out []AreaStation
	for _, st := range []string{"QRR", "TBS"} {
		ps, ok := g.f.programs[prefix+st]
		if !ok {
			continue
		}
		s := AreaStation{ID: st, Name: areaStationNames[st]}
		for _, p := range ps {
			info := p.info
			if info == "" {
				info = "説明"
			}
			s.Airings = append(s.Airings, domain.Airing{ProgramID: p.id, StationID: st, Start: p.start, End: p.end, Title: p.title,
				Performer: p.pfm, Description: info, ImageURL: p.img})
		}
		out = append(out, s)
	}
	return out, nil
}

func (g *fakeAreaGuide) setFail(day domain.BroadcastDay, fail bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failDays == nil {
		g.failDays = map[string]bool{}
	}
	g.failDays[day.String()] = fail
}

// newKeywordEnv は、今が 10-07 の 6 時（放送の前）で、TBS の番組表に flat1・news・flat2・suppin があり、番組が無い環境を作る。
func newKeywordEnv(t *testing.T) (*env, *fakeAreaGuide) {
	t.Helper()
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	g := &fakeAreaGuide{f: e.fake}
	e.app.AreaGuide = g
	return e, g
}

func (e *env) createKeyword(k domain.Keyword) int64 {
	e.t.Helper()
	id, err := e.store.CreateKeyword(e.ctx, k)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) keywordRecords(id int64) []store.KeywordAiring {
	e.t.Helper()
	rs, err := e.store.KeywordAirings(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return rs
}

// 出演者名のキーワードを作ると、番組表の同期で、当たったまだ始まっていない放送回が、番組名の番組の 1 回の予約になり
// （番組が無ければ作られ）、ジョブになって録音される。録音を終えると 1 回の予約は消え、番組は残る。
func TestKeywordSyncCreatesShowAndRecords(t *testing.T) {
	e, _ := newKeywordEnv(t)
	d := e.useDiscord()
	e.app.MuteSuccess = true // キーワードの出来事は notify.success = false では止めない
	id := e.createKeyword(domain.Keyword{Words: "イモトアヤコ"})
	e.sync()

	r, err := e.store.GetShow(e.ctx, suppin.title)
	if err != nil {
		t.Fatalf("番組が作られていない: %v", err)
	}
	if r.Disabled || r.Split != domain.SplitNone || !r.CheckTitle || len(r.Weekly) != 0 || len(r.Onces) != 1 ||
		r.Onces[0].KeywordID != id || !r.Onces[0].Start.Equal(suppin.start) {
		t.Fatalf("作った番組 = %+v", r)
	}
	j := e.job(suppin.title)
	if j.State != domain.StateWaiting || len(j.Airings) != 1 || !j.Airings[0].Start.Equal(suppin.start) {
		t.Fatalf("ジョブ = %+v", j)
	}
	if recs := e.keywordRecords(id); len(recs) != 1 || recs[0].State != store.KeywordAiringPlanned || recs[0].StationName != "TBSラジオ" {
		t.Fatalf("記録 = %+v", recs)
	}
	e.checkNotifications()
	d.expectOne(t, "【キーワードで録音予定】", "イモトアヤコ", suppin.title, "放送日 2026-10-07 11:00〜11:01 TBSラジオ",
		"番組「"+suppin.title+"」は新しく作った")
	// 通知は足した先の番組を指す。
	if ns := e.notifications(); len(ns) != 1 || ns[0].Kind != notify.KindKeywordAdded || ns[0].Show != suppin.title || ns[0].JobID != 0 {
		t.Fatalf("通知 = %+v", ns)
	}

	// 次の同期でも足し直さない（通知もしない）。
	e.sync()
	if r, _ := e.store.GetShow(e.ctx, suppin.title); len(r.Onces) != 1 {
		t.Fatalf("足し直した: %+v", r.Onces)
	}
	e.checkNotifications()
	d.expectNone(t)

	// 録音する。
	e.clock.Set(afterBroadcast)
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	if j := e.job(suppin.title); j.State != domain.StateDone {
		t.Fatalf("録音されない: %+v", j)
	}
	r, err = e.store.GetShow(e.ctx, suppin.title)
	if err != nil || len(r.Onces) != 0 {
		t.Fatalf("録り終えた後の番組 = %+v, %v（予約の無い番組として残る）", r, err)
	}
	if recs := e.keywordRecords(id); len(recs) != 1 || recs[0].State != store.KeywordAiringDone {
		t.Fatalf("録り終えた後の記録 = %+v", recs)
	}
	if p, _ := e.store.KeywordPending(e.ctx); p[id] != 0 {
		t.Errorf("録り終えた後の録音予定の数 = %d", p[id])
	}
}

// 番組名の末尾に印があれば、同じ局・同じ放送日の同じ分割番組のパートをまとめて足し、通知は 1 回にまとめる。
// 既にある同じ名前の番組（NFKC と前後の空白を揃えて比べる）に足す。
func TestKeywordSplitPartsToExistingShow(t *testing.T) {
	e, _ := newKeywordEnv(t)
	d := e.useDiscord()
	existing := domain.NewShow("パンサー向井のふらっと")
	existing.Split = domain.SplitJoin
	e.putShows(existing)
	id := e.createKeyword(domain.Keyword{Words: "パンサー"})
	e.sync()
	r, err := e.store.GetShow(e.ctx, existing.Name)
	if err != nil || len(r.Onces) != 2 || r.Onces[0].KeywordID != id || r.Onces[1].KeywordID != id {
		t.Fatalf("番組 = %+v, %v", r, err)
	}
	j := e.job(existing.Name)
	if len(j.Airings) != 2 {
		t.Fatalf("パートが 1 つのジョブにならない: %+v", j)
	}
	e.checkNotifications()
	msg := d.expectOne(t, "2 パートをまとめて", "番組「パンサー向井のふらっと」に足した")
	if strings.Contains(msg, "新しく作った") {
		t.Errorf("既存の番組なのに新しく作ったと書いた: %q", msg)
	}
	if strings.Contains(msg, "別のファイル") {
		t.Errorf("分割番組でつなぐ番組なのに別のファイルになると書いた: %q", msg)
	}
	if ps, _ := e.store.KeywordPending(e.ctx); ps[id] != 2 {
		t.Errorf("録音予定の数 = %d（パートの数だけ数える）", ps[id])
	}
}

// 分割番組でない既存の番組にパートを 2 つ以上まとめて足したら、通知の本文に、パートごとに別のファイルになることを添える。
// 番組の設定は変えず、ジョブはパートごとに分かれる（ADR 0003）。
func TestKeywordSplitPartsToNonSplitShow(t *testing.T) {
	e, _ := newKeywordEnv(t)
	d := e.useDiscord()
	existing := domain.NewShow("パンサー向井のふらっと")
	e.putShows(existing)
	e.createKeyword(domain.Keyword{Words: "パンサー"})
	e.sync()
	r, err := e.store.GetShow(e.ctx, existing.Name)
	if err != nil || len(r.Onces) != 2 || r.Split != domain.SplitNone {
		t.Fatalf("番組 = %+v, %v", r, err)
	}
	jobs, err := e.store.ListJobs(e.ctx)
	if err != nil || len(jobs) != 2 || jobs[0].Part+jobs[1].Part != 3 {
		t.Fatalf("パートごとのジョブにならない: %+v, %v", jobs, err)
	}
	e.checkNotifications()
	d.expectOne(t, "2 パートをまとめて", "番組「パンサー向井のふらっと」は分割番組でないので、パートごとに別のファイルになる")
}

// 分割番組でない既存の番組に 1 つだけ足しても、足した後にその番組が同じ放送日に 2 つ以上の放送回を当てるなら、通知の本文に、
// パートごとに別のファイルになることを添える。番組の設定は変えない。
func TestKeywordSeparateNoticeWithExistingOnce(t *testing.T) {
	e, _ := newKeywordEnv(t)
	d := e.useDiscord()
	existing := domain.NewShow("イモトアヤコのすっぴんしゃん")
	e.putShows(existing)
	if _, err := e.store.CreateOnce(e.ctx, existing.Name, domain.Once{StationID: "TBS", Start: news.start}); err != nil {
		t.Fatal(err)
	}
	e.createKeyword(domain.Keyword{Words: "イモト"})
	e.sync()
	r, err := e.store.GetShow(e.ctx, existing.Name)
	if err != nil || len(r.Onces) != 2 || r.Split != domain.SplitNone {
		t.Fatalf("番組 = %+v, %v", r, err)
	}
	e.checkNotifications()
	msg := d.expectOne(t, "番組「イモトアヤコのすっぴんしゃん」は分割番組でないので、パートごとに別のファイルになる")
	if strings.Contains(msg, "パートをまとめて") {
		t.Errorf("1 つだけ足したのにまとめたと書いた: %q", msg)
	}
}

// 始まった放送回、局の絞り込みの外、番組表を取れない放送日には当てない。人が消した 1 回の予約は足し直さない。
func TestKeywordSkipsAndDoesNotReAdd(t *testing.T) {
	e, g := newKeywordEnv(t)
	e.clock.Set(jst(2026, 10, 7, 9, 0)) // flat1 は始まっている
	qrr := e.createKeyword(domain.Keyword{Words: "ニュース", Stations: []string{"QRR"}})
	none := e.createKeyword(domain.Keyword{Words: "向井 イモト"})                      // 語の AND: どの放送回にも両方は含まれない
	excluded := e.createKeyword(domain.Keyword{Words: "すっぴんしゃん", Exclude: "イモト"}) // 除外する語が出演者に当たる
	flat := e.createKeyword(domain.Keyword{Words: "ふらっと"})
	g.setFail(day, true)
	e.sync()
	if rs, _ := e.store.ListShows(e.ctx); len(rs) != 0 {
		t.Fatalf("番組表を取れない放送日に当てた: %+v", rs)
	}
	g.setFail(day, false)
	e.sync()
	rs, _ := e.store.ListShows(e.ctx)
	if len(rs) != 1 || rs[0].Name != "パンサー向井のふらっと" || len(rs[0].Onces) != 2 {
		// flat2 が当たり、印があるので同じ日の flat1 もパートとしてまとめる（始まっていない回にだけ当てるのは、当たった放送回の判定）。
		t.Fatalf("番組 = %+v", rs)
	}
	if recs := e.keywordRecords(qrr); len(recs) != 0 {
		t.Errorf("局の絞り込みの外に当てた: %+v", recs)
	}
	if recs := e.keywordRecords(none); len(recs) != 0 {
		t.Errorf("語の AND に当たらない放送回に当てた: %+v", recs)
	}
	if recs := e.keywordRecords(excluded); len(recs) != 0 {
		t.Errorf("除外する語に当たる放送回に当てた: %+v", recs)
	}
	// 人が 1 回の予約を消しても、足し直さない（消した後の展開でジョブも外れる。Web UI で消したときと同じ）。
	for _, o := range rs[0].Onces {
		if err := e.store.DeleteOnce(e.ctx, o.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.app.expandShows(e.ctx, rs[0]); err != nil {
		t.Logf("展開（今日から先の番組表は 404）: %v", err)
	}
	e.sync()
	if r, _ := e.store.GetShow(e.ctx, "パンサー向井のふらっと"); len(r.Onces) != 0 {
		t.Fatalf("人が消した回を足し直した: %+v", r.Onces)
	}
	recs := e.keywordRecords(flat)
	if len(recs) != 2 || recs[0].State != store.KeywordAiringRemoved {
		t.Errorf("記録 = %+v", recs)
	}
	if p, _ := e.store.KeywordPending(e.ctx); p[flat] != 0 {
		t.Errorf("外された回を録音予定に数えた: %d", p[flat])
	}
}

// 上限に達したら足さずに 1 回だけ通知し、同期のたびには送らない。キーワードどうしが同じ放送回に当たったら、先に作った方が足す。
func TestKeywordLimitAndOverlap(t *testing.T) {
	e, _ := newKeywordEnv(t)
	d := e.useDiscord()
	e.app.KeywordMaxPending = 1
	first := e.createKeyword(domain.Keyword{Words: "説明"}) // 全部の放送回に当たる
	second := e.createKeyword(domain.Keyword{Words: "ニュース"})
	e.sync()
	recs := e.keywordRecords(first)
	if len(recs) != 2 { // 開始が早い flat1 と、パートの flat2
		t.Fatalf("先のキーワードの記録 = %+v", recs)
	}
	// ニュースは上限で 1 つ目が足さなかったので、2 つ目が足す。
	if recs := e.keywordRecords(second); len(recs) != 1 || !recs[0].Start.Equal(news.start) {
		t.Fatalf("後のキーワードの記録 = %+v", recs)
	}
	e.checkNotifications()
	got := d.take()
	limits := 0
	for _, m := range got {
		if strings.HasPrefix(m, "【キーワードの上限】") {
			limits++
			if !strings.Contains(m, "「説明」") || !strings.Contains(m, "上限（1 件）") {
				t.Errorf("上限の通知 = %q", m)
			}
		}
	}
	if limits != 1 || len(got) != 3 {
		t.Fatalf("通知 = %q", got)
	}
	e.sync()
	e.checkNotifications()
	d.expectNone(t)
}

// キーワードを作った・変えた直後の依頼（RequestKeywords）で、同期を待たずに当て、足した番組の局・放送日を展開する。
func TestRequestKeywordsExpands(t *testing.T) {
	e, _ := newKeywordEnv(t)
	e.createKeyword(domain.Keyword{Words: "イモトアヤコ"})
	e.app.RequestKeywords()
	e.expandPending()
	if !e.hasJob(suppin.title) {
		t.Fatalf("依頼の後にジョブができていない: %+v", e.jobs())
	}
}
