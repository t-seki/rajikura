package app

import (
	"context"
	"sync"
	"testing"

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

	// 非会員・タイムフリー30 は、自分のエリアの局一覧を取らず、印を立てない（いつも in）。
	g.set("QRR")
	calls := g.calls
	for _, m := range []domain.Membership{domain.MembershipFree, domain.MembershipTimeFree30} {
		e.app.Membership = m
		e.sync()
		if fj := e.job(flatShow.Name); fj.Airings[0].OutOfArea || fj.Live() {
			t.Fatalf("%s: 印が立った: %+v", m, fj)
		}
	}
	if g.calls != calls {
		t.Errorf("エリアフリーでないのに自分のエリアの局一覧を取った: %d 回", g.calls-calls)
	}
}
