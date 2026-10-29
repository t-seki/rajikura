package radiko

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// 番組表のキャッシュの定数。
const (
	// ProgramCacheFreshFor は、取ってから radiko に尋ねずにキャッシュを返す時間。
	// radiko の応答の Cache-Control の max-age（60 秒）に合わせた固定の値で、応答のヘッダーは読まない。
	ProgramCacheFreshFor = 60 * time.Second
	// ProgramCacheFutureDays は、キャッシュに持つ放送日の先の限り（今日から何日先まで）。
	ProgramCacheFutureDays = 7
	// DefaultProgramCacheBytes は、キャッシュの合計の大きさの既定の上限。
	DefaultProgramCacheBytes int64 = 64 << 20
	// programCacheLogEvery は、使用量を Info で出す間隔の下限。
	programCacheLogEvery = time.Hour
)

type cacheEntry struct {
	body         []byte
	etag         string
	lastModified string
	fetchedAt    time.Time
	lastUsed     time.Time
	day          time.Time // 放送日（日本時間の 0 時）
}

// programCache は番組表の XML（展開後）をメモリにだけ持つキャッシュ。
// 持つ放送日は「今日の pastDays 日前から 7 日先まで」で、合計が maxBytes を超えたら最も長く使っていないものから捨てる。
type programCache struct {
	pastDays int
	maxBytes int64
	log      *slog.Logger

	mu      sync.Mutex
	entries map[string]*cacheEntry
	bytes   int64
	lastLog time.Time
}

// EnableProgramCache は、番組表（Programs と AreaPrograms）のキャッシュを有効にする。
// 放送日が今日の pastDays 日前から 7 日先までの分だけを持ち、合計が maxBytes（本文の大きさ）を超えたら、
// 最も長く使っていないものから捨てる。メモリにだけ持つ。Client を使い始める前に呼ぶ。
func (c *Client) EnableProgramCache(pastDays int, maxBytes int64, log *slog.Logger) {
	if maxBytes <= 0 {
		maxBytes = DefaultProgramCacheBytes
	}
	c.progCache = &programCache{pastDays: pastDays, maxBytes: maxBytes, log: log, entries: map[string]*cacheEntry{}}
}

// getProgramXML は番組表の XML を取る。キャッシュが無効、または放送日が範囲の外なら、普通の getXML と同じ。
//
//   - 取ってから 60 秒以内: radiko に尋ねずにキャッシュを返す。
//   - 60 秒の後: ETag（無ければ Last-Modified）を付けた GET で尋ね、304 ならキャッシュを返し、200 なら差し替える。
//   - ETag も Last-Modified も無い応答は、60 秒の後は普通の GET で取り直す。
func (c *Client) getProgramXML(ctx context.Context, rawURL string, day time.Time) ([]byte, error) {
	pc := c.progCache
	now := c.now()
	if pc == nil || !pc.inRange(now, day) {
		return c.getXML(ctx, rawURL)
	}
	if body, ok := pc.fresh(rawURL, now); ok {
		return body, nil
	}
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
		pc.touch(rawURL, now)
		return prev.body, nil
	case http.StatusOK:
		body, err := maybeGunzip(resp.body)
		if err != nil {
			return nil, fmt.Errorf("%w: gunzip %s: %v", ErrTemporary, rawURL, err)
		}
		pc.store(rawURL, day, body, resp.header.Get("ETag"), resp.header.Get("Last-Modified"), now)
		return body, nil
	default:
		return nil, statusError("GET "+rawURL, resp.status)
	}
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

func (pc *programCache) fresh(key string, now time.Time) ([]byte, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	e := pc.entries[key]
	if e == nil || now.Before(e.fetchedAt) || now.Sub(e.fetchedAt) >= ProgramCacheFreshFor {
		return nil, false
	}
	e.lastUsed = now
	return e.body, true
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

func (pc *programCache) store(key string, day time.Time, body []byte, etag, lastModified string, now time.Time) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if old := pc.entries[key]; old != nil {
		pc.bytes -= int64(len(old.body))
		delete(pc.entries, key)
	}
	pc.entries[key] = &cacheEntry{body: body, etag: etag, lastModified: lastModified,
		fetchedAt: now, lastUsed: now, day: dayStart(day)}
	pc.bytes += int64(len(body))

	// 範囲の外を捨てる。
	for k, e := range pc.entries {
		if !pc.inRange(now, e.day) {
			pc.bytes -= int64(len(e.body))
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
		pc.bytes -= int64(len(oldest.body))
		delete(pc.entries, oldestKey)
		evicted++
	}
	if pc.log != nil && (evicted > 0 || now.Sub(pc.lastLog) >= programCacheLogEvery) {
		pc.lastLog = now
		pc.log.Info("番組表のキャッシュ", "entries", len(pc.entries), "bytes", pc.bytes,
			"max_bytes", pc.maxBytes, "evicted", evicted)
	}
}

// stats はキャッシュの件数と合計の大きさを返す（テスト用）。
func (pc *programCache) stats() (entries int, bytes int64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return len(pc.entries), pc.bytes
}
