package radiko

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// 番組表のキャッシュの定数。
const (
	// ProgramCacheFreshFor は、取ってから radiko に尋ねずにキャッシュを返す時間（ふつうの読み手。番組表の同期・番組表の画面・
	// 録音の前の取り直し・キーワード）。radiko の応答の Cache-Control の max-age（60 秒）に合わせた固定の値で、応答のヘッダーは読まない。
	ProgramCacheFreshFor = 60 * time.Second
	// ProgramCacheRelaxedFreshFor は、取り直しを緩めた読み手（全国の検索と取り込み。ProgramsRelaxed）が、取ってから radiko に
	// 尋ねずにキャッシュを返す時間（ADR 0035）。放送日が終わった後に取った分は、この時間を過ぎても尋ねない。
	ProgramCacheRelaxedFreshFor = 6 * time.Hour
	// ProgramCacheFutureDays は、キャッシュに持つ放送日の先の限り（今日から何日先まで）。
	ProgramCacheFutureDays = 7
	// DefaultProgramCacheBytes は、キャッシュの合計の大きさ（gzip の大きさ）の既定の上限。
	DefaultProgramCacheBytes int64 = 64 << 20
	// programCacheLogEvery は、使用量を Info で出す間隔の下限。
	programCacheLogEvery = time.Hour
)

// readMode は、キャッシュを読むときの取り直しの決まり。
type readMode int

const (
	// readNormal は、取ってから ProgramCacheFreshFor（60 秒）の間だけ尋ねずに使う。
	readNormal readMode = iota
	// readRelaxed は、放送日が終わった後に取った分を尋ねずに使い、ほかは ProgramCacheRelaxedFreshFor（6 時間）の間尋ねずに使う。
	readRelaxed
)

type cacheEntry struct {
	// gz は radiko の応答の本文の gzip（応答が gzip でなければ、こちらで gzip にしたもの）。使うたびに展開する。
	gz           []byte
	etag         string
	lastModified string
	fetchedAt    time.Time
	lastUsed     time.Time
	day          time.Time // 放送日（日本時間の 0 時）
	// station は局ごとの日別の番組表（Programs）の局 ID。エリアの日別（AreaPrograms）なら空。
	station string
}

// programFlight は、取りにいっている途中の番組表（キーは URL）。同じものを同時に 2 回は取りにいかず、結果を待って使う。
type programFlight struct {
	done chan struct{}
	body []byte
	err  error
}

// programCache は番組表の XML を、radiko の応答の gzip のままメモリにだけ持つキャッシュ（ADR 0035）。
// 持つ放送日は「今日の pastDays 日前から 7 日先まで」で、合計（gzip の大きさ）が maxBytes を超えたら最も長く使っていないものから捨てる。
type programCache struct {
	pastDays int
	maxBytes int64
	log      *slog.Logger

	mu      sync.Mutex
	entries map[string]*cacheEntry
	flights map[string]*programFlight
	bytes   int64
	lastLog time.Time
}

// EnableProgramCache は、番組表（Programs・ProgramsRelaxed と AreaPrograms）のキャッシュを有効にする。
// 放送日が今日の pastDays 日前から 7 日先までの分だけを、radiko の応答の gzip のまま持ち、合計（gzip の大きさ）が maxBytes を
// 超えたら、最も長く使っていないものから捨てる。メモリにだけ持つ。Client を使い始める前に呼ぶ。
func (c *Client) EnableProgramCache(pastDays int, maxBytes int64, log *slog.Logger) {
	if maxBytes <= 0 {
		maxBytes = DefaultProgramCacheBytes
	}
	c.progCache = &programCache{pastDays: pastDays, maxBytes: maxBytes, log: log,
		entries: map[string]*cacheEntry{}, flights: map[string]*programFlight{}}
}

// DropStationPrograms は、局ごとの日別の番組表（Programs・ProgramsRelaxed で取った分）のうち、keep が false を返す局の分を
// キャッシュから捨て、捨てた件数を返す（会員種別がエリアフリー・ダブルプランでなくなったときに、全国の分を捨てる。ADR 0035）。
// エリアの日別の番組表（AreaPrograms）は捨てない。キャッシュが無効なら何もしない。
func (c *Client) DropStationPrograms(keep func(stationID string) bool) int {
	pc := c.progCache
	if pc == nil {
		return 0
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	n := 0
	for k, e := range pc.entries {
		if e.station != "" && !keep(e.station) {
			pc.bytes -= int64(len(e.gz))
			delete(pc.entries, k)
			n++
		}
	}
	return n
}

// getProgramXML は番組表の XML（展開後）を取る。キャッシュが無効、または放送日が範囲の外なら、普通の getXML と同じ。
// station は局ごとの日別の番組表の局 ID（エリアの日別なら空。DropStationPrograms が使う）。
//
//   - 尋ねずに使える間（mode で決まる。readNormal は取ってから 60 秒。readRelaxed は放送日が終わった後に取った分はいつまでも、
//     ほかは取ってから 6 時間）: radiko に尋ねずにキャッシュを返す。
//   - 過ぎた後: ETag（無ければ Last-Modified）を付けた GET で尋ね、304 ならキャッシュを返し、200 なら差し替える。
//   - ETag も Last-Modified も無い応答は、過ぎた後は普通の GET で取り直す。
//
// 同じ URL を同時に 2 回は取りにいかない（取りにいっている途中なら、その結果を待って使う）。
func (c *Client) getProgramXML(ctx context.Context, rawURL string, day time.Time, station string, mode readMode) ([]byte, error) {
	pc := c.progCache
	now := c.now()
	if pc == nil || !pc.inRange(now, day) {
		return c.getXML(ctx, rawURL)
	}
	if gz, ok := pc.fresh(rawURL, now, mode); ok {
		body, err := gunzip(gz)
		if err != nil {
			return nil, fmt.Errorf("%w: gunzip cached %s: %v", ErrTemporary, rawURL, err)
		}
		return body, nil
	}
	f, leader := pc.join(rawURL)
	if !leader {
		select {
		case <-f.done:
			return f.body, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.err = errors.New("番組表を取る処理が異常終了した")
	defer pc.leave(rawURL, f)
	// 取りにいくのは待つ側と共有するので、呼び出し元の ctx の取り消しに巻き込まない（要求 1 回ごとのタイムアウトはかかる）。
	f.body, f.err = c.fetchProgramXML(context.WithoutCancel(ctx), pc, rawURL, day, station, now)
	return f.body, f.err
}

// fetchProgramXML は radiko に尋ね（覚えていれば条件付きの GET）、キャッシュを差し替えて、展開した XML を返す。
func (c *Client) fetchProgramXML(ctx context.Context, pc *programCache, rawURL string, day time.Time, station string, now time.Time) ([]byte, error) {
	prev := pc.peek(rawURL)
	headers := map[string]string{}
	if prev != nil {
		if prev.etag != "" {
			headers["If-None-Match"] = prev.etag
		} else if prev.lastModified != "" {
			headers["If-Modified-Since"] = prev.lastModified
		}
	}
	resp, err := c.get(ctx, rawURL, headers)
	if err != nil {
		return nil, err
	}
	switch resp.status {
	case http.StatusNotModified:
		if prev == nil || len(headers) == 0 {
			return nil, fmt.Errorf("%w: GET %s: 304 but no cache to reuse", ErrTemporary, rawURL)
		}
		body, err := gunzip(prev.gz)
		if err != nil {
			return nil, fmt.Errorf("%w: gunzip cached %s: %v", ErrTemporary, rawURL, err)
		}
		pc.touch(rawURL, now)
		return body, nil
	case http.StatusOK:
		body, gz, err := bodyAndGzip(resp.body)
		if err != nil {
			return nil, fmt.Errorf("%w: gunzip %s: %v", ErrTemporary, rawURL, err)
		}
		pc.store(rawURL, day, station, gz, resp.header.Get("ETag"), resp.header.Get("Last-Modified"), now)
		return body, nil
	default:
		return nil, statusError("GET "+rawURL, resp.status)
	}
}

// bodyAndGzip は、応答の本文 raw から、展開した本文と、持つための gzip を返す。raw が gzip ならそのまま持ち、そうでなければ gzip にする。
func bodyAndGzip(raw []byte) (body, gz []byte, err error) {
	if isGzip(raw) {
		body, err := gunzip(raw)
		if err != nil {
			return nil, nil, err
		}
		return body, raw, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, nil, err
	}
	return raw, buf.Bytes(), nil
}

func isGzip(b []byte) bool { return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b }

// gunzip は gzip を展開する。
func gunzip(gz []byte) ([]byte, error) {
	if !isGzip(gz) {
		return nil, errors.New("not gzip")
	}
	return maybeGunzip(gz)
}

func (pc *programCache) inRange(now, day time.Time) bool {
	today := dayStart(now)
	d := dayStart(day)
	return !d.Before(today.AddDate(0, 0, -pc.pastDays)) && !d.After(today.AddDate(0, 0, ProgramCacheFutureDays))
}

func dayStart(t time.Time) time.Time {
	t = t.In(JST)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, JST)
}

// settled は、e が放送日の終わった後（その放送日の翌朝 5 時より後）に取ったものか。
func (e *cacheEntry) settled() bool {
	return e.fetchedAt.After(e.day.AddDate(0, 0, 1).Add(5 * time.Hour))
}

func (pc *programCache) fresh(key string, now time.Time, mode readMode) ([]byte, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	e := pc.entries[key]
	if e == nil || now.Before(e.fetchedAt) {
		return nil, false
	}
	switch {
	case mode == readRelaxed && e.settled():
	case mode == readRelaxed && now.Sub(e.fetchedAt) < ProgramCacheRelaxedFreshFor:
	case now.Sub(e.fetchedAt) < ProgramCacheFreshFor:
	default:
		return nil, false
	}
	e.lastUsed = now
	return e.gz, true
}

// join は、key を取りにいっている途中ならその印を返し（leader は false）、無ければ印を立てて返す（leader は true。
// 取り終えたら leave を呼ぶ）。
func (pc *programCache) join(key string) (*programFlight, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if f, ok := pc.flights[key]; ok {
		return f, false
	}
	f := &programFlight{done: make(chan struct{})}
	pc.flights[key] = f
	return f, true
}

func (pc *programCache) leave(key string, f *programFlight) {
	pc.mu.Lock()
	delete(pc.flights, key)
	pc.mu.Unlock()
	close(f.done)
}

// peek は、キャッシュの写しを返す（無ければ nil）。
func (pc *programCache) peek(key string) *cacheEntry {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if e := pc.entries[key]; e != nil {
		cp := *e
		return &cp
	}
	return nil
}

// touch は 304 のとき、取った時刻を新しくする。
func (pc *programCache) touch(key string, now time.Time) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if e := pc.entries[key]; e != nil {
		e.fetchedAt = now
		e.lastUsed = now
	}
}

func (pc *programCache) store(key string, day time.Time, station string, gz []byte, etag, lastModified string, now time.Time) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if old := pc.entries[key]; old != nil {
		pc.bytes -= int64(len(old.gz))
		delete(pc.entries, key)
	}
	pc.entries[key] = &cacheEntry{gz: gz, etag: etag, lastModified: lastModified,
		fetchedAt: now, lastUsed: now, day: dayStart(day), station: station}
	pc.bytes += int64(len(gz))

	// 範囲の外を捨てる。
	for k, e := range pc.entries {
		if !pc.inRange(now, e.day) {
			pc.bytes -= int64(len(e.gz))
			delete(pc.entries, k)
		}
	}
	// 上限を超えたら、最も長く使っていないものから捨てる。
	evicted := 0
	for pc.bytes > pc.maxBytes && len(pc.entries) > 0 {
		var oldestKey string
		var oldest *cacheEntry
		for k, e := range pc.entries {
			if oldest == nil || e.lastUsed.Before(oldest.lastUsed) {
				oldestKey, oldest = k, e
			}
		}
		pc.bytes -= int64(len(oldest.gz))
		delete(pc.entries, oldestKey)
		evicted++
	}
	if pc.log != nil && (evicted > 0 || now.Sub(pc.lastLog) >= programCacheLogEvery) {
		pc.lastLog = now
		pc.log.Info("番組表のキャッシュ", "entries", len(pc.entries), "bytes", pc.bytes,
			"max_bytes", pc.maxBytes, "evicted", evicted)
	}
}

// stats はキャッシュの件数と合計の大きさ（gzip の大きさ）を返す（テスト用）。
func (pc *programCache) stats() (entries int, bytes int64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return len(pc.entries), pc.bytes
}
