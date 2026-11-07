package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// ownAreaGuide は、自分のエリアの局一覧だけを返す AreaGuide（放送回は持たない）。stations を変えると自分のエリアが変わったことになる。
type ownAreaGuide struct {
	mu       sync.Mutex
	stations []string
	calls    int
}

func (g *ownAreaGuide) set(stations ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stations = stations
}

func (g *ownAreaGuide) AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	var out []AreaStation
	for _, s := range g.stations {
		out = append(out, AreaStation{ID: s, Name: s})
	}
	return out, nil
}

func (g *ownAreaGuide) AreaAiringsOf(ctx context.Context, areaID string, day domain.BroadcastDay) ([]AreaStation, error) {
	return g.AreaAirings(ctx, day)
}

func (g *ownAreaGuide) Areas(ctx context.Context) (AreaInfo, error) {
	return AreaInfo{Own: "JP13"}, nil
}

// 展開は、エリアフリーの会員では、局が自分のエリアの局一覧に無ければ放送回にエリアの外から録る印（domain.Airing.OutOfArea）を
// 立て、ts_out_ng で録音の方式を決める。展開のたびにその時点の自分のエリアで判定し、waiting のジョブの印を書き直す（#27）。
func TestSyncMarksOutOfArea(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	ng1 := flat1
	ng1.outNG = true
	e.fake.setPrograms("TBS", day, ng1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	g := &ownAreaGuide{}
	g.set("QRR") // TBS は自分のエリアの外
	e.app.AreaGuide = g
	e.app.Membership = domain.MembershipAreaFree

	e.sync()
	fj := e.job(flatShow.Name)
	if len(fj.Airings) != 2 || !fj.Airings[0].OutOfArea || !fj.Airings[1].OutOfArea || !fj.Live() {
		t.Fatalf("エリアの外の局で ts_out_ng の放送回を持つジョブは、ライブで録る: %+v", fj)
	}
	if sj := e.job(suppinShow.Name); !sj.Airings[0].OutOfArea || sj.Live() {
		t.Fatalf("ts_out_ng が 0 の放送回はタイムフリーから録る: %+v", sj)
	}

	// 自分のエリアが変わって TBS が入ったら、次の展開で印を書き直し、in のフラグで決める（ts_in_ng は 0 なのでタイムフリー）。
	g.set("QRR", "TBS")
	e.sync()
	fj = e.job(flatShow.Name)
	if fj.Airings[0].OutOfArea || fj.Airings[1].OutOfArea || fj.Live() || fj.State != domain.StateWaiting {
		t.Fatalf("自分のエリアの局になったら、印を外してタイムフリーから録る: %+v", fj)
	}

	// 非会員・タイムフリー30 は、自分のエリアの局に印を立てない（いつも in）。自分のエリアの外の局には予約を当てない
	// （TestSyncSkipsOutOfAreaUnlessAreaFree）。
	for _, m := range []domain.Membership{domain.MembershipFree, domain.MembershipTimeFree30} {
		e.app.Membership = m
		e.sync()
		if fj := e.job(flatShow.Name); fj.Airings[0].OutOfArea || fj.Live() {
			t.Fatalf("%s: 印が立った: %+v", m, fj)
		}
	}
}

// 非会員・タイムフリー30 では、自分のエリアの外の局の放送回に、毎週の予約も 1 回の予約も当てない（録れないため）。
// エリアフリー・ダブルプランでは今までどおり当てる。番組と予約は消さないので、自分のエリアが変わって局が自分のエリアの
// 局になると、次の展開から当たる（#32）。
func TestSyncSkipsOutOfAreaUnlessAreaFree(t *testing.T) {
	for _, m := range []domain.Membership{domain.MembershipFree, domain.MembershipTimeFree30, domain.MembershipAreaFree, domain.MembershipDouble} {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, beforeBroadcast)
			e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
			e.putShows(flatShow, suppinShow)
			once := e.newsOnce()
			g := &ownAreaGuide{}
			g.set("QRR") // TBS は自分のエリアの外
			e.app.AreaGuide = g
			e.app.Membership = m

			e.sync()
			names := []string{flatShow.Name, suppinShow.Name, once.Name}
			for _, name := range names {
				if got := e.hasJob(name); got != m.AreaFree() {
					t.Errorf("番組 %s のジョブがある = %v、エリアフリーを含む = %v: %+v", name, got, m.AreaFree(), e.jobs())
				}
			}
			if s, err := e.store.GetShow(e.ctx, once.Name); err != nil || len(s.Onces) != 1 {
				t.Errorf("1 回の予約は残る: %+v, %v", s, err)
			}

			// 自分のエリアが変わって TBS が入ったら、次の展開から当たる。
			g.set("QRR", "TBS")
			e.sync()
			for _, name := range names {
				if !e.hasJob(name) {
					t.Errorf("自分のエリアの局になったのに、番組 %s のジョブが無い: %+v", name, e.jobs())
				}
			}
		})
	}
}

// 非会員で、自分のエリアの外の局になった局の、展開が触るジョブ（waiting と人が決めたジョブ）は、その局の放送回を外され、
// 放送回が 0 になって消える。試行を始めた retrying のジョブは変えない（#32）。
func TestSyncDropsOutOfAreaJobsUnlessAreaFree(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	e.newsOnce()
	g := &ownAreaGuide{}
	g.set("QRR", "TBS")
	e.app.AreaGuide = g
	e.sync()
	if n := len(e.jobs()); n != 3 {
		t.Fatalf("自分のエリアの局ではジョブができる: %+v", e.jobs())
	}
	// 1 つを retrying にする（1 回目の試行が失敗した）。
	rj, ok, err := e.store.ClaimDue(e.ctx, afterBroadcast)
	if err != nil || !ok {
		t.Fatalf("ClaimDue = %+v, %v, %v", rj, ok, err)
	}
	if err := e.store.Retry(e.ctx, rj.ID, afterBroadcast.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// ほかの 1 つを人が録らないと決める（録らない予定。展開が触る）。
	var person domain.Job
	for _, j := range e.jobs() {
		if j.ID != rj.ID {
			person = j
			break
		}
	}
	if err := e.store.SkipByPerson(e.ctx, person.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}

	// 引っ越しで TBS が自分のエリアの外になった。
	g.set("QRR")
	e.sync()
	jobs := e.jobs()
	if len(jobs) != 1 || jobs[0].ID != rj.ID || jobs[0].State != domain.StateRetrying || len(jobs[0].Airings) != len(rj.Airings) {
		t.Fatalf("展開が触るジョブは消え（人が決めたジョブ %d も）、retrying のジョブ %d は残る: %+v", person.ID, rj.ID, jobs)
	}
}

// 非会員で予約を当てなかった局・放送日からは、当たらない日を出さない。今ある記録も、当てない間は出さない（#32）。
func TestSyncNoUnmatchedDaysOutOfArea(t *testing.T) {
	e := newUnmatchedEnv(t)
	g := &ownAreaGuide{}
	g.set("QRR", "TBS")
	e.app.AreaGuide = g
	e.sync()
	if got := unmatchedStarts(e.unmatched(unmatchedShow.Name)); len(got) != 2 {
		t.Fatalf("自分のエリアの局では当たらない日が出る: %v", got)
	}

	g.set("QRR") // TBS が自分のエリアの外になった
	e.sync()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 0 {
		t.Fatalf("当てない局の当たらない日が残った: %v", unmatchedStarts(got))
	}

	// 初めから自分のエリアの外の局なら、記録も通知もしない。
	f := newUnmatchedEnv(t)
	h := &ownAreaGuide{}
	h.set("QRR")
	f.app.AreaGuide = h
	f.sync()
	if got := f.unmatched(unmatchedShow.Name); len(got) != 0 {
		t.Fatalf("当てない局の当たらない日が出た: %v", unmatchedStarts(got))
	}
	if ns := f.notifications(); len(ns) != 0 {
		t.Fatalf("通知 = %+v", ns)
	}
}

// failingOwnAreaGuide は、自分のエリアの局一覧を取れない AreaGuide。
type failingOwnAreaGuide struct{ ownAreaGuide }

func (g *failingOwnAreaGuide) AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error) {
	return nil, errors.New("取れない")
}

// 非会員で自分のエリアの局一覧を取れないときは、その局・放送日の展開を失敗にし、ジョブを作らない（取れないまま当てない。#32）。
func TestSyncFailsWithoutOwnAreaUnlessAreaFree(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	e.app.AreaGuide = &failingOwnAreaGuide{}
	if err := e.app.Sync(e.ctx); err == nil {
		t.Fatal("どの局・放送日も展開できないのに、同期が成功した")
	}
	if jobs := e.jobs(); len(jobs) != 0 {
		t.Fatalf("局一覧を取れないのにジョブを作った: %+v", jobs)
	}
}
