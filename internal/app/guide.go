package app

import (
	"context"
	"errors"
	"io"
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
			// radiko のクライアントは ts_out_ng を読まないので、OutNG は 0 のまま。
			TimeFree: domain.TimeFreeFlags{InNG: atoi(p.TSInNG)},
		}
	}
	return out
}

// atoi は数字でなければ 0 を返す（フラグの解釈はまだ分かっていないので、読めない値は 0 とみなす）。
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
	// AreaAirings は放送日 day の、エリアの全局の放送回を返す。エリアが分からない・取れないときはエラー。
	AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error)
}

// DefaultAreaTTL は RadikoAreaGuide がエリア ID を覚えておく時間。
const DefaultAreaTTL = time.Hour

// RadikoAreaGuide は radiko のエリアの日別番組表（/v3/program/date/<YYYYMMDD>/<エリア ID>.xml）を、
// AreaGuide にする。番組表は覚えず（radiko.Client の番組表のキャッシュに任せる）、エリア ID だけを TTL の間覚える。
// ゼロ値ではなく NewRadikoAreaGuide で作る。複数の goroutine から呼んでよい。
type RadikoAreaGuide struct {
	Client *radiko.Client
	// TTL はエリア ID を覚えておく時間。0 なら DefaultAreaTTL。
	TTL time.Duration
	// Now は現在時刻。nil なら time.Now。
	Now func() time.Time

	mu     sync.Mutex
	area   string
	areaAt time.Time
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
		return tok.AreaID, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// AreaAirings は AreaGuide を実装する。番組表は覚えない。同じ放送日を重ねて取りにいくときは、結果を共有する。
// 返したスライスは重ねた呼び出しと共有するので、呼び出し側は書き換えない。
func (g *RadikoAreaGuide) AreaAirings(ctx context.Context, day domain.BroadcastDay) ([]AreaStation, error) {
	area, err := g.areaID(ctx)
	if err != nil {
		return nil, err
	}
	v, err := g.do(ctx, area+"/"+day.String(), func(ctx context.Context) (any, error) {
		ss, err := g.Client.AreaPrograms(ctx, area, time.Date(day.Year, day.Month, day.Day, 0, 0, 0, 0, domain.JST))
		if err != nil {
			return nil, err
		}
		out := make([]AreaStation, len(ss))
		for i, s := range ss {
			out[i] = AreaStation{ID: s.ID, Name: s.Name, Airings: toAirings(s.Programs)}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	out := v.([]AreaStation)
	return out, nil
}
