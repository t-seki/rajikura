package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
	"github.com/t-seki/rajikura/internal/radiko"
)

// Station は局の情報。
type Station struct {
	ID   string
	Name string
	// TFMaxDelay はタイムフリーが公開されるまでの最大の遅れ。
	TFMaxDelay time.Duration
}

// Guide は番組表の取得元。MVP の実装は radiko の XML（RadikoGuide）だけで、
// 後で JSON の API などに差し替えられるように、app はこのインターフェースを介して呼ぶ。
type Guide interface {
	// Stations は局の一覧を返す。
	Stations(ctx context.Context) ([]Station, error)
	// Airings は局 stationID の放送日 day の放送回を返す。
	Airings(ctx context.Context, stationID string, day domain.BroadcastDay) ([]domain.Airing, error)
}

// Fetcher は音声の入手元（タイムフリーとライブ）。*radiko.Client が満たす。
type Fetcher interface {
	Authenticate(ctx context.Context) (radiko.Token, error)
	// FetchTimefree はタイムフリーからの録音で、放送回 1 つを入手する。onChunk はチャンクを書くたびに呼ぶ（nil なら呼ばない）。
	FetchTimefree(ctx context.Context, token radiko.Token, stationID string, start, end time.Time, w io.Writer, onChunk func(seconds float64)) (radiko.FetchResult, error)
	// FetchLive はライブ録音で、放送中のストリームから req の範囲を入手する（live.go）。
	FetchLive(ctx context.Context, token radiko.Token, req radiko.LiveRequest, w io.Writer) (radiko.LiveResult, error)
}

// Media は m4a への格納と長さの測定。*media.Tools（ffmpeg / ffprobe）と media.Builtin が満たし、
// 設定 muxer で選ぶ（ADR 0013）。
type Media interface {
	Remux(ctx context.Context, inputs []string, out string, tags media.Tags, cover string) error
	Duration(ctx context.Context, file string) (time.Duration, error)
}

// RadikoGuide は radiko の XML の番組表を Guide にする。
type RadikoGuide struct {
	Client *radiko.Client
}

// Stations は Guide を実装する。full.xml から局 ID・局名・tf_max_delay を読む。
func (g RadikoGuide) Stations(ctx context.Context) ([]Station, error) {
	ss, err := g.Client.Stations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Station, len(ss))
	for i, s := range ss {
		out[i] = Station{ID: s.ID, Name: s.Name, TFMaxDelay: time.Duration(s.TFMaxDelay) * time.Second}
	}
	return out, nil
}

// Airings は Guide を実装する。
func (g RadikoGuide) Airings(ctx context.Context, stationID string, day domain.BroadcastDay) ([]domain.Airing, error) {
	ps, err := g.Client.Programs(ctx, stationID, time.Date(day.Year, day.Month, day.Day, 0, 0, 0, 0, domain.JST))
	if err != nil {
		return nil, err
	}
	return toAirings(ps), nil
}

func toAirings(ps []radiko.Program) []domain.Airing {
	out := make([]domain.Airing, len(ps))
	for i, p := range ps {
		var parts []domain.DescriptionPart
		for _, dp := range p.DescriptionParts {
			parts = append(parts, domain.DescriptionPart{Text: dp.Text, Href: dp.Href})
		}
		out[i] = domain.Airing{
			ProgramID:        p.ID,
			StationID:        p.StationID,
			Start:            p.Start,
			End:              p.End,
			Title:            p.Title,
			Performer:        p.Performer,
			Description:      p.Description,
			DescriptionParts: parts,
			ImageURL:         p.ImageURL,
			// 4 つのフラグはそのまま写す。in と out のどちらを見るかは、エリアの外から録るかの印（OutOfArea）で domain が決める。
			// 印はここでは立てない（呼び出し側が自分のエリアで決める。App.markOutOfArea と RadikoAreaGuide.AreaAiringsOf）。
			TimeFree: domain.TimeFreeFlags{InNG: atoi(p.TSInNG), OutNG: atoi(p.TSOutNG),
				PlusInNG: atoi(p.TSPlusInNG), PlusOutNG: atoi(p.TSPlusOutNG)},
		}
	}
	return out
}

// stationSet はエリアの番組表 ss に出てくる局 ID の集合（エリアの局一覧）。
func stationSet(ss []AreaStation) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s.ID] = true
	}
	return out
}

// markOutOfArea は、展開する局 station の放送日 day の放送回 airings に、エリアの外から録るかの印（domain.Airing.OutOfArea）を
// 付けたものを返す（airings は書き換えない）。印は、局が自分のエリア（radiko の認証で得たエリア）の同じ放送日の局一覧
// （AreaGuide.AreaAirings）に無ければ立てる。判定は展開のたびに、その時点の自分のエリアで行う（展開が触るジョブの印は、
// 展開で書き直る。展開が触らないジョブの付け直しは、ここではしない）。
// 会員種別がエリアフリー・ダブルプランでなければ、自分のエリアの局しか録らないので、印を立てない（自分のエリアの外の局に
// 予約を当てないかは unrecordableOutOfArea が決める）。
// AreaGuide が無いとき（テストの組み立て）も立てない。自分のエリアの局一覧を取れない・空なら、印を決められないのでエラー。
func (a *App) markOutOfArea(ctx context.Context, station string, day domain.BroadcastDay, airings []domain.Airing) ([]domain.Airing, error) {
	if !a.membership().AreaFree() || a.AreaGuide == nil {
		return airings, nil
	}
	in, err := a.inOwnArea(ctx, station, day)
	if err != nil {
		return nil, err
	}
	if in {
		return airings, nil
	}
	out := slices.Clone(airings)
	for i := range out {
		out[i].OutOfArea = true
	}
	return out, nil
}

// unrecordableOutOfArea は、展開する局 station の放送日 day の放送回に、番組の予約（毎週と 1 回）を当てないかを返す（#32）。
// 会員種別（設定で選んだもの。App.membership）がエリアフリー・ダブルプランでなく、局が自分のエリアの外の局（自分のエリアの
// 同じ放送日の局一覧に無い。markOutOfArea と同じ判定）なら当てない（録れないため）。radiko がログインを拒んで非会員として
// 認証していても、選んだ会員種別がエリアフリー・ダブルプランなら当てる。AreaGuide が無いとき（テストの組み立て）は判定しない
// （当てる）。自分のエリアの局一覧を取れない・空なら、決められないのでエラー（その局・放送日の展開を失敗にする）。
func (a *App) unrecordableOutOfArea(ctx context.Context, station string, day domain.BroadcastDay) (bool, error) {
	if a.membership().AreaFree() || a.AreaGuide == nil {
		return false, nil
	}
	in, err := a.inOwnArea(ctx, station, day)
	if err != nil {
		return false, err
	}
	return !in, nil
}

// inOwnArea は、局 station が自分のエリアの放送日 day の局一覧（AreaGuide.AreaAirings）に入っているか。
// 局一覧を取れない・空ならエラー。
func (a *App) inOwnArea(ctx context.Context, station string, day domain.BroadcastDay) (bool, error) {
	ss, err := a.AreaGuide.AreaAirings(ctx, day)
	if err != nil {
		return false, fmt.Errorf("自分のエリアの局一覧を取れない: %w", err)
	}
	if len(ss) == 0 {
		return false, errors.New("自分のエリアの局一覧が空")
	}
	return stationSet(ss)[station], nil
}

// atoi は数字でなければ 0 を返す（読めない値は 0（聴ける）とみなす）。
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// AreaStation は、エリアの日別番組表に出てくる 1 局とその放送回。
type AreaStation struct {
	ID      string
	Name    string
	Airings []domain.Airing
}

// AreaGuide は、radiko が判定したエリアの全局の日別番組表の取得元（Web UI の番組表の画面用）。
// 番組表の同期（Guide）とは別の口だが、番組表の取得は同じ radiko.Client を通るので、
// radiko.Client の番組表のキャッシュ（60 秒と条件付きの GET）を共有する。
type AreaGuide interface {
	// AreaAirings は放送日 day の、自分のエリアの全局の放送回を返す。エリアが分からない・取れないときはエラー。
	AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error)
	// AreaAiringsOf は放送日 day の、エリア areaID の全局の放送回を返す。areaID が空なら AreaAirings と同じ（自分のエリア）。
	// 自分のエリア以外を返してよいかは呼び出し側が決める（エリアフリーの会員だけ。internal/server/guide.go）。
	// 自分のエリアの局一覧に無い局の放送回には、エリアの外から録るかの印（domain.Airing.OutOfArea）を立てる。
	AreaAiringsOf(ctx context.Context, areaID string, day domain.BroadcastDay) ([]AreaStation, error)
	// Areas は、自分のエリアと、radiko の局の一覧（full.xml）から作ったエリアの一覧を返す。
	Areas(ctx context.Context) (AreaInfo, error)
}

// Area は番組表で選べるエリア。
type Area struct {
	ID   string
	Name string
}

// AreaInfo は、自分のエリアと、選べるエリアの一覧。
type AreaInfo struct {
	// Own は自分のエリア ID（認証で radiko が判定したもの）。
	Own string
	// Areas は選べるエリアの一覧（エリア ID の番号の順。自分のエリアを含む）。
	Areas []Area
	// StationArea は局 ID → その局のエリア ID。
	StationArea map[string]string
	// Stations は radiko の全局（full.xml の局の一覧の順。全国の検索が探す局。nationwide.go）。
	Stations []radiko.Station
}

// DefaultAreaTTL は RadikoAreaGuide がエリア ID とエリアの一覧を覚えておく時間。
const DefaultAreaTTL = time.Hour

// RadikoAreaGuide は radiko のエリアの日別番組表（/v3/program/date/<YYYYMMDD>/<エリア ID>.xml）を、
// AreaGuide にする。番組表は覚えず（radiko.Client の番組表のキャッシュに任せる）、エリア ID とエリアの一覧だけを TTL の間覚える。
// ゼロ値ではなく NewRadikoAreaGuide で作る。複数の goroutine から呼んでよい。
type RadikoAreaGuide struct {
	Client *radiko.Client
	// TTL はエリア ID とエリアの一覧を覚えておく時間。0 なら DefaultAreaTTL。
	TTL time.Duration
	// Now は現在時刻。nil なら time.Now。
	Now func() time.Time
	// OnArea があれば、認証（auth2）が通って自分のエリアが分かるたびに、そのエリア ID で呼ぶ（cmd が App.SeeOwnArea につなぐ。
	// 自分のエリアの変化に気付くため。internal/app/own_area.go）。エリア外と判定されたときは呼ばない。
	OnArea func(areaID string)

	mu     sync.Mutex
	area   string
	areaAt time.Time
	// list はエリアの一覧（full.xml から作る）。TTL の間覚える。
	list   radiko.AreaList
	listAt time.Time
	// flights は取得中のもの（キー: "auth" か "<エリア ID>/<放送日>"）。同じものを重ねて取りにいかず、結果を待って使う。
	flights map[string]*areaFlight
}

type areaFlight struct {
	done chan struct{}
	val  any
	err  error
}

// do は同じ key の取得が進行中ならその結果を待ち、無ければ fn を行う。結果を覚えるのは fn の中で行う
// （do 自身は覚えない。取得中の印を外すのと覚えるのの間に、同じものをもう一度取りにいく呼び出しが入らないように）。
// fn がパニックしても取得中の印は外し、待つ側にはエラーを返す（パニックは先頭の呼び出しにそのまま伝わる）。
// 取得は呼び出し元の ctx の取り消しに巻き込まれないようにして、待つ側に影響させない。
func (g *RadikoAreaGuide) do(ctx context.Context, key string, fn func(ctx context.Context) (any, error)) (any, error) {
	g.mu.Lock()
	if f, ok := g.flights[key]; ok {
		g.mu.Unlock()
		select {
		case <-f.done:
			return f.val, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if g.flights == nil {
		g.flights = map[string]*areaFlight{}
	}
	f := &areaFlight{done: make(chan struct{})}
	g.flights[key] = f
	g.mu.Unlock()
	f.err = errors.New("エリアの番組表を取る処理が異常終了した")
	defer func() {
		g.mu.Lock()
		delete(g.flights, key)
		g.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = fn(context.WithoutCancel(ctx))
	return f.val, f.err
}

// NewRadikoAreaGuide は RadikoAreaGuide を作る。
func NewRadikoAreaGuide(c *radiko.Client) *RadikoAreaGuide {
	return &RadikoAreaGuide{Client: c}
}

func (g *RadikoAreaGuide) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *RadikoAreaGuide) ttl() time.Duration {
	if g.TTL > 0 {
		return g.TTL
	}
	return DefaultAreaTTL
}

// areaID は覚えているエリア ID を返す。無い・古ければ認証（auth2）で取り直す。
func (g *RadikoAreaGuide) areaID(ctx context.Context) (string, error) {
	g.mu.Lock()
	if g.area != "" && g.now().Sub(g.areaAt) < g.ttl() {
		a := g.area
		g.mu.Unlock()
		return a, nil
	}
	g.mu.Unlock()
	v, err := g.do(ctx, "auth", func(ctx context.Context) (any, error) {
		tok, err := g.Client.Authenticate(ctx)
		if err != nil {
			return "", err
		}
		if tok.AreaID == "" {
			return "", radiko.ErrOutOfArea
		}
		g.mu.Lock()
		g.area, g.areaAt = tok.AreaID, g.now()
		g.mu.Unlock()
		if g.OnArea != nil {
			g.OnArea(tok.AreaID)
		}
		return tok.AreaID, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// setOwnArea は、覚えている自分のエリアを area に差し替える（どの認証で気付いたとき（この RadikoAreaGuide の認証も含む）でも、
// 自分のエリアが変わったと分かって展開役が付け直すとき。TTL を待たずに新しいエリアで番組表を引くため。
// internal/app/own_area.go の ownAreaSetter）。付け直しが後で失敗しても戻さない（radiko が判定した今のエリアなので）。
// 覚えた時刻も今にする。OnArea は呼ばない。
func (g *RadikoAreaGuide) setOwnArea(area string) {
	g.mu.Lock()
	g.area, g.areaAt = area, g.now()
	g.mu.Unlock()
}

// AreaAirings は AreaGuide を実装する。番組表は覚えない。同じ放送日を重ねて取りにいくときは、結果を共有する。
// 返したスライスは重ねた呼び出しと共有するので、呼び出し側は書き換えない。
func (g *RadikoAreaGuide) AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error) {
	return g.AreaAiringsOf(ctx, "", day)
}

// Areas は AreaGuide を実装する。エリアの一覧は full.xml から作り、エリア ID と同じ TTL の間覚える（失敗は覚えない）。
func (g *RadikoAreaGuide) Areas(ctx context.Context) (AreaInfo, error) {
	own, err := g.areaID(ctx)
	if err != nil {
		return AreaInfo{}, err
	}
	g.mu.Lock()
	if g.list.Areas != nil && g.now().Sub(g.listAt) < g.ttl() {
		l := g.list
		g.mu.Unlock()
		return areaInfo(own, l), nil
	}
	g.mu.Unlock()
	v, err := g.do(ctx, "areas", func(ctx context.Context) (any, error) {
		l, err := g.Client.Areas(ctx)
		if err != nil {
			return radiko.AreaList{}, err
		}
		g.mu.Lock()
		g.list, g.listAt = l, g.now()
		g.mu.Unlock()
		return l, nil
	})
	if err != nil {
		return AreaInfo{}, err
	}
	return areaInfo(own, v.(radiko.AreaList)), nil
}

func areaInfo(own string, l radiko.AreaList) AreaInfo {
	out := AreaInfo{Own: own, StationArea: l.StationArea, Stations: l.Stations}
	for _, a := range l.Areas {
		out.Areas = append(out.Areas, Area{ID: a.ID, Name: a.Name})
	}
	return out
}

// AreaAiringsOf は AreaGuide を実装する。自分のエリア以外のエリアでは、自分のエリアの同じ放送日の局一覧に無い局の放送回に、
// エリアの外から録るかの印（domain.Airing.OutOfArea）を立てる（自分のエリアの番組表も取る。取れなければエラー）。
// 自分のエリアの局一覧にもある局（局は複数のエリアにまたがる）の放送回と、自分のエリアの番組表の放送回には立てない。
func (g *RadikoAreaGuide) AreaAiringsOf(ctx context.Context, area string, day domain.BroadcastDay) ([]AreaStation, error) {
	own, err := g.areaID(ctx)
	if err != nil {
		return nil, err
	}
	if area == "" {
		area = own
	}
	v, err := g.do(ctx, area+"/"+day.String(), func(ctx context.Context) (any, error) {
		ss, err := g.Client.AreaPrograms(ctx, area, time.Date(day.Year, day.Month, day.Day, 0, 0, 0, 0, domain.JST))
		if err != nil {
			return nil, err
		}
		var inOwn map[string]bool
		if area != own {
			ownSS, err := g.AreaAiringsOf(ctx, own, day)
			if err != nil {
				return nil, err
			}
			inOwn = stationSet(ownSS)
		}
		out := make([]AreaStation, len(ss))
		for i, s := range ss {
			airings := toAirings(s.Programs)
			if inOwn != nil && !inOwn[s.ID] {
				for j := range airings {
					airings[j].OutOfArea = true
				}
			}
			out[i] = AreaStation{ID: s.ID, Name: s.Name, Airings: airings}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	out := v.([]AreaStation)
	return out, nil
}
