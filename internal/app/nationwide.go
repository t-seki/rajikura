package app

// 全国の検索（用語集）の番組表（docs/adr/0035-nationwide-search-from-prefetched-station-guides.md）。
//
// 全国の検索は、radiko の全局（full.xml の局の一覧）の、局ごとの日別の番組表（radiko.Client.ProgramsRelaxed）から探す。
// 会員種別がエリアフリー・ダブルプランのあいだ、全国の番組表の取り込み役（runNationwide。展開役とは別の goroutine）が、
// serve の起動の直後と 6 時間ごとに、表示する範囲の全部の放送日（今日の N 日前から 7 日先まで。N は domain.MaxAvailableDays）の
// 全国の局の番組表を radiko.Client の番組表のキャッシュに取り込む（並列で取りにいく数は nationwideParallel 本まで。DB には書かない）。
// 取り直しは緩めて読む（放送日が終わった後に取った分は取り直さず、ほかは 6 時間尋ねない。internal/radiko/cache.go）。
// 取り込みが済む前に全国の検索が来たら、足りない分をその場で取る（同じ局・放送日を同時に 2 回は取りにいかないのは
// radiko.Client のキャッシュが受け持つ）。
// 会員種別を変えて保存したら（SettingsApplied）、取り込み役を起こす。エリアフリー・ダブルプランに変えたら取り込みを始め、
// その間で変えたら取り込む放送日の範囲を合わせ、非会員・タイムフリー30 に変えたら取り込みを止めて、全国の分（自分のエリアの局の
// ほかの局の分）をキャッシュから捨てる。

import (
	"cmp"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/radiko"
)

const (
	// DefaultNationwideInterval は、全国の番組表を取り込み直す間。
	DefaultNationwideInterval = 6 * time.Hour
	// nationwideParallel は、全国の番組表を並列で取りにいく数の上限。
	nationwideParallel = 8
)

// NationwideStation は、全国の検索の放送回の局。
type NationwideStation struct {
	ID   string
	Name string
	// AreaID は局のエリア ID（full.xml の area_id）。
	AreaID string
	// Own は、局が自分のエリアの局一覧（その放送日の自分のエリアの番組表）にあるか。
	Own bool
}

// NationwideAiring は、全国の検索で見つけた放送回と、その局。
type NationwideAiring struct {
	Airing  domain.Airing
	Station NationwideStation
}

// Nationwide は全国の検索の番組表を引いた結果。
type Nationwide struct {
	// Airings は keep に当たった放送回（局・放送日の順は決めない）。
	Airings []NationwideAiring
	// Stations は探した局の数、FailedStations は、ある放送日を 1 つでも取れなかった局の数。
	Stations       int
	FailedStations int
}

// NationwideGuide は、全国の検索の番組表の取得元（*RadikoAreaGuide が満たす）。
type NationwideGuide interface {
	// NationwideAirings は、全国の局の、放送日 days の放送回のうち keep に当たるものを返す。取れなかった局・放送日があっても
	// 取れた分で答え、取れなかった局の数を返す。局の一覧か自分のエリアが分からないときはエラー。
	// 自分のエリアの局一覧に無い局の放送回には、エリアの外から録る印（domain.Airing.OutOfArea）を立てる。
	NationwideAirings(ctx context.Context, days []domain.BroadcastDay, keep func(domain.Airing) bool) (Nationwide, error)
	// PrefetchNationwide は、全国の局の、放送日 days の番組表を番組表のキャッシュに取り込む。取れた・取れなかった局・放送日の数を返す。
	// 局の一覧が分からないときはエラー。ctx が取り消されたら、残りを取りにいかずに戻る。
	PrefetchNationwide(ctx context.Context, days []domain.BroadcastDay) (fetched, failed int, err error)
	// DropNationwide は、全国の分（自分のエリアの局のほかの局の、局ごとの日別の番組表）をキャッシュから捨て、捨てた件数を返す。
	DropNationwide(ctx context.Context) int
}

// stationDay は、局と放送日の組。
type stationDay struct {
	station radiko.Station
	day     domain.BroadcastDay
}

// forStationDays は、局 stations と放送日 days のすべての組について、並列（nationwideParallel 本まで）に fn を呼ぶ。
// ctx が取り消されたら、まだ始めていない組は呼ばない。
func forStationDays(ctx context.Context, stations []radiko.Station, days []domain.BroadcastDay, fn func(stationDay)) {
	work := make(chan stationDay)
	var wg sync.WaitGroup
	for range nationwideParallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sd := range work {
				fn(sd)
			}
		}()
	}
feed:
	for _, d := range days {
		for _, st := range stations {
			select {
			case work <- stationDay{station: st, day: d}:
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(work)
	wg.Wait()
}

func dayTime(day domain.BroadcastDay) time.Time {
	return time.Date(day.Year, day.Month, day.Day, 0, 0, 0, 0, domain.JST)
}

// stationList は、全国の局の一覧（full.xml。エリアの一覧と同じ TTL の間覚える）と、局→エリア ID を返す。
func (g *RadikoAreaGuide) stationList(ctx context.Context) (AreaInfo, error) {
	info, err := g.Areas(ctx)
	if err != nil {
		return AreaInfo{}, err
	}
	if len(info.Stations) == 0 {
		return AreaInfo{}, errors.New("radiko の局の一覧が空")
	}
	return info, nil
}

// NationwideAirings は NationwideGuide を実装する。局ごとの日別の番組表を、取り直しを緩めて読む（radiko.Client.ProgramsRelaxed）。
// 自分のエリアの局一覧（放送日ごとの自分のエリアの番組表）を取れなかった放送日は、全局をその放送日を取れなかったものとして数える
// （印を決められないため）。
func (g *RadikoAreaGuide) NationwideAirings(ctx context.Context, days []domain.BroadcastDay, keep func(domain.Airing) bool) (Nationwide, error) {
	info, err := g.stationList(ctx)
	if err != nil {
		return Nationwide{}, err
	}
	// 放送日ごとの自分のエリアの局一覧。取れなければ nil。
	own := make([]map[string]bool, len(days))
	dayIndex := make(map[domain.BroadcastDay]int, len(days))
	var wg sync.WaitGroup
	for i, d := range days {
		dayIndex[d] = i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ss, err := g.AreaAiringsOf(ctx, "", d); err == nil && len(ss) > 0 {
				own[i] = stationSet(ss)
			}
		}()
	}
	wg.Wait()
	var mu sync.Mutex
	failed := map[string]bool{}
	var hits []NationwideAiring
	forStationDays(ctx, info.Stations, days, func(sd stationDay) {
		inOwn := own[dayIndex[sd.day]]
		var ps []radiko.Program
		err := ctx.Err()
		if inOwn != nil && err == nil {
			ps, err = g.Client.ProgramsRelaxed(ctx, sd.station.ID, dayTime(sd.day))
		}
		if inOwn == nil || err != nil {
			mu.Lock()
			failed[sd.station.ID] = true
			mu.Unlock()
			return
		}
		st := NationwideStation{ID: sd.station.ID, Name: sd.station.Name, AreaID: info.StationArea[sd.station.ID], Own: inOwn[sd.station.ID]}
		var found []NationwideAiring
		for _, a := range toAirings(ps) {
			a.OutOfArea = !st.Own
			if keep(a) {
				found = append(found, NationwideAiring{Airing: a, Station: st})
			}
		}
		if len(found) > 0 {
			mu.Lock()
			hits = append(hits, found...)
			mu.Unlock()
		}
	})
	if err := ctx.Err(); err != nil {
		return Nationwide{}, err
	}
	return Nationwide{Airings: hits, Stations: len(info.Stations), FailedStations: len(failed)}, nil
}

// PrefetchNationwide は NationwideGuide を実装する。
func (g *RadikoAreaGuide) PrefetchNationwide(ctx context.Context, days []domain.BroadcastDay) (fetched, failed int, err error) {
	info, err := g.stationList(ctx)
	if err != nil {
		return 0, 0, err
	}
	var mu sync.Mutex
	forStationDays(ctx, info.Stations, days, func(sd stationDay) {
		if ctx.Err() != nil {
			return
		}
		_, err := g.Client.ProgramsRelaxed(ctx, sd.station.ID, dayTime(sd.day))
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				failed++
			}
			return
		}
		fetched++
	})
	return fetched, failed, ctx.Err()
}

// DropNationwide は NationwideGuide を実装する。自分のエリアの今日の局一覧を取れなければ、局ごとの日別の番組表を全部捨てる
// （自分のエリアの局の分は、使うときに取り直す）。
func (g *RadikoAreaGuide) DropNationwide(ctx context.Context) int {
	keep := map[string]bool{}
	if ss, err := g.AreaAirings(ctx, domain.BroadcastDayOf(g.now())); err == nil {
		keep = stationSet(ss)
	}
	return g.Client.DropStationPrograms(func(id string) bool { return keep[id] })
}

// nationwideDays は、会員種別 m で全国の番組表を持つ放送日（今日の N 日前から 7 日先まで）。
func (a *App) nationwideDays(m domain.Membership) []domain.BroadcastDay {
	today := domain.BroadcastDayOf(a.now())
	var out []domain.BroadcastDay
	for d := -domain.MaxAvailableDays(m); d <= radiko.ProgramCacheFutureDays; d++ {
		out = append(out, today.AddDays(d))
	}
	return out
}

// wakeNationwide は、全国の番組表の取り込み役を起こす（会員種別を保存したとき）。塞がっていれば捨てる。
func (a *App) wakeNationwide() {
	a.initExpander()
	select {
	case a.nationwideWake <- struct{}{}:
	default:
	}
}

// runNationwide は全国の番組表の取り込み役（goroutine 1 本）の中身。Nationwide が nil なら何もしない。
// 起動の直後と NationwideInterval ごとと、起こされたとき（wakeNationwide）に、今の会員種別を見て、エリアフリー・ダブルプランなら
// 取り込み、そうでなく前に取り込んでいたなら全国の分を捨てる。取り込みの途中で起こされたら、会員種別を見直し、取り込む放送日の範囲が
// 変わった・エリアフリー・ダブルプランでなくなったなら、取り込みを止めてやり直す。展開役とは別なので、展開役を待たせない。
func (a *App) runNationwide(ctx context.Context) {
	if a.Nationwide == nil {
		return
	}
	a.initExpander()
	t := time.NewTicker(cmp.Or(a.NationwideInterval, DefaultNationwideInterval))
	defer t.Stop()
	held := false
	for ctx.Err() == nil {
		m := a.membership()
		if m.AreaFree() {
			held = true
			if !a.prefetchNationwide(ctx, m) {
				continue
			}
		} else if held {
			n := a.Nationwide.DropNationwide(ctx)
			held = false
			a.log().Info("会員種別がエリアフリー・ダブルプランでなくなったので、全国の番組表の取り込みを止め、全国の分をキャッシュから捨てた",
				"membership", m, "dropped", n)
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		case <-a.nationwideWake:
		}
	}
}

// prefetchNationwide は、会員種別 m の範囲で全国の番組表を 1 回取り込む。途中で起こされて、会員種別がエリアフリー・ダブルプランで
// なくなったか、取り込む放送日の範囲が変わったなら、取り込みを止めて false を返す（呼び出し側がすぐにやり直す）。
func (a *App) prefetchNationwide(ctx context.Context, m domain.Membership) bool {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		fetched, failed int
		err             error
	}
	done := make(chan result, 1)
	days := a.nationwideDays(m)
	started := a.now()
	go func() {
		f, fl, err := a.Nationwide.PrefetchNationwide(pctx, days)
		done <- result{f, fl, err}
	}()
	for {
		select {
		case r := <-done:
			switch {
			case r.err != nil && ctx.Err() == nil:
				a.log().Warn("全国の番組表を取り込めない（次の取り込みか全国の検索で取り直す）", "err", r.err)
			case r.err == nil:
				lvl := a.log().Info
				if r.failed > 0 {
					lvl = a.log().Warn
				}
				lvl("全国の番組表を取り込んだ", "days", len(days), "fetched", r.fetched, "failed", r.failed,
					"took", a.now().Sub(started).Round(time.Second))
			}
			return true
		case <-ctx.Done():
			cancel()
			<-done
			return true
		case <-a.nationwideWake:
			if nm := a.membership(); nm.AreaFree() && domain.MaxAvailableDays(nm) == domain.MaxAvailableDays(m) {
				continue
			}
			cancel()
			<-done
			a.log().Info("会員種別が変わったので、全国の番組表の取り込みをやり直す", "from", m, "to", a.membership())
			return false
		}
	}
}
