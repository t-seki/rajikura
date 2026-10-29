package radiko

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const cacheXML = `<radiko><stations><station id="TBS"><name>TBS</name><progs>` +
	`<prog id="1" ft="20261013050000" to="20261013060000"><title>t</title></prog></progs></station></stations></radiko>`

// cacheServer は番組表を返す偽のサーバー。要求の回数とヘッダーを記録する。
type cacheServer struct {
	srv *httptest.Server

	mu        sync.Mutex
	hits      int
	inm, ims  []string // 各要求の If-None-Match / If-Modified-Since
	etag      string
	lastMod   string
	failNext  bool
	always304 bool
	body      string
}

func newCacheServer(t *testing.T) *cacheServer {
	cs := &cacheServer{etag: `"v1"`, body: cacheXML}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		cs.hits++
		cs.inm = append(cs.inm, r.Header.Get("If-None-Match"))
		cs.ims = append(cs.ims, r.Header.Get("If-Modified-Since"))
		if cs.failNext {
			cs.failNext = false
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if cs.always304 || (cs.etag != "" && r.Header.Get("If-None-Match") == cs.etag) ||
			(cs.etag == "" && cs.lastMod != "" && r.Header.Get("If-Modified-Since") == cs.lastMod) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if cs.etag != "" {
			w.Header().Set("ETag", cs.etag)
		}
		if cs.lastMod != "" {
			w.Header().Set("Last-Modified", cs.lastMod)
		}
		fmt.Fprint(w, cs.body)
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *cacheServer) count() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.hits
}

// cacheClient は、時計を now で動かせるキャッシュ有効のクライアントを返す。
func cacheClient(cs *cacheServer, now *time.Time, pastDays int, maxBytes int64) *Client {
	c := NewClient()
	c.ProgramHost = cs.srv.URL
	c.Now = func() time.Time { return *now }
	c.EnableProgramCache(pastDays, maxBytes, nil)
	return c
}

var cacheDay = jst(2026, 10, 13, 0, 0, 0)

func TestProgramCacheFreshThenConditional(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	ctx := context.Background()

	if _, err := c.Programs(ctx, "TBS", cacheDay); err != nil {
		t.Fatal(err)
	}
	// 60 秒以内は尋ねない。
	now = now.Add(59 * time.Second)
	ps, err := c.Programs(ctx, "TBS", cacheDay)
	if err != nil || len(ps) != 1 {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	if cs.count() != 1 {
		t.Fatalf("60 秒以内の要求 = %d 回", cs.count())
	}
	// 60 秒の後は ETag 付きで尋ね、304 でキャッシュを返す。
	now = now.Add(2 * time.Second)
	ps, err = c.Programs(ctx, "TBS", cacheDay)
	if err != nil || len(ps) != 1 {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	if cs.count() != 2 || cs.inm[1] != `"v1"` {
		t.Fatalf("hits = %d, If-None-Match = %q", cs.count(), cs.inm)
	}
	// 304 で取った時刻が新しくなり、また 60 秒は尋ねない。
	now = now.Add(30 * time.Second)
	if _, err := c.Programs(ctx, "TBS", cacheDay); err != nil || cs.count() != 2 {
		t.Fatalf("err = %v, hits = %d", err, cs.count())
	}
	// 200 で差し替える。
	cs.mu.Lock()
	cs.etag = `"v2"`
	cs.body = strings.Replace(cacheXML, "<title>t</title>", "<title>new</title>", 1)
	cs.mu.Unlock()
	now = now.Add(61 * time.Second)
	ps, err = c.Programs(ctx, "TBS", cacheDay)
	if err != nil || len(ps) != 1 || ps[0].Title != "new" {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	if cs.inm[2] != `"v1"` {
		t.Fatalf("If-None-Match = %q", cs.inm)
	}
	now = now.Add(61 * time.Second)
	if _, err := c.Programs(ctx, "TBS", cacheDay); err != nil || cs.inm[3] != `"v2"` {
		t.Fatalf("err = %v, If-None-Match = %q", err, cs.inm)
	}
}

func TestProgramCacheLastModifiedWhenNoETag(t *testing.T) {
	cs := newCacheServer(t)
	cs.etag = ""
	cs.lastMod = "Tue, 13 Oct 2026 01:00:00 GMT"
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	if _, err := c.Programs(context.Background(), "TBS", cacheDay); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.Programs(context.Background(), "TBS", cacheDay); err != nil {
		t.Fatal(err)
	}
	if cs.count() != 2 || cs.ims[1] != cs.lastMod || cs.inm[1] != "" {
		t.Fatalf("hits = %d, ims = %q, inm = %q", cs.count(), cs.ims, cs.inm)
	}
}

func TestProgramCacheNoValidatorsRefetchesPlain(t *testing.T) {
	cs := newCacheServer(t)
	cs.etag = ""
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	for i := 0; i < 2; i++ {
		if _, err := c.Programs(context.Background(), "TBS", cacheDay); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Minute)
	}
	if cs.count() != 2 || cs.inm[1] != "" || cs.ims[1] != "" {
		t.Fatalf("hits = %d, inm = %q, ims = %q", cs.count(), cs.inm, cs.ims)
	}
}

func TestProgramCacheAreaPrograms(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	for i := 0; i < 2; i++ {
		got, err := c.AreaPrograms(context.Background(), "JP13", cacheDay)
		if err != nil || len(got) != 1 {
			t.Fatalf("got = %v, err = %v", got, err)
		}
	}
	if cs.count() != 1 {
		t.Fatalf("hits = %d", cs.count())
	}
}

func TestProgramCacheDisabledAlwaysAsks(t *testing.T) {
	cs := newCacheServer(t)
	c := NewClient()
	c.ProgramHost = cs.srv.URL
	for i := 0; i < 2; i++ {
		if _, err := c.Programs(context.Background(), "TBS", cacheDay); err != nil {
			t.Fatal(err)
		}
	}
	if cs.count() != 2 || cs.inm[1] != "" {
		t.Fatalf("hits = %d, inm = %q", cs.count(), cs.inm)
	}
}

func TestProgramCacheRangeByMembership(t *testing.T) {
	now := jst(2026, 10, 13, 12, 0, 0)
	for _, tc := range []struct {
		name     string
		pastDays int
		offset   int // 今日からの日数
		cached   bool
	}{
		{"free 7 日前は持つ", 7, -7, true},
		{"free 8 日前は持たない", 7, -8, false},
		{"free 7 日先は持つ", 7, 7, true},
		{"free 8 日先は持たない", 7, 8, false},
		{"timefree30 30 日前は持つ", 30, -30, true},
		{"timefree30 31 日前は持たない", 30, -31, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newCacheServer(t)
			c := cacheClient(cs, &now, tc.pastDays, 1<<20)
			day := cacheDay.AddDate(0, 0, tc.offset)
			for i := 0; i < 2; i++ {
				if _, err := c.Programs(context.Background(), "TBS", day); err != nil {
					t.Fatal(err)
				}
			}
			if tc.cached && cs.count() != 1 {
				t.Fatalf("持つはずが %d 回尋ねた", cs.count())
			}
			if !tc.cached && cs.count() != 2 {
				t.Fatalf("持たないはずが %d 回しか尋ねていない", cs.count())
			}
			n, _ := c.progCache.stats()
			if tc.cached != (n == 1) {
				t.Fatalf("件数 = %d", n)
			}
		})
	}
}

func TestProgramCacheDropsEntriesThatLeaveRange(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	old := cacheDay.AddDate(0, 0, -7)
	if _, err := c.Programs(context.Background(), "TBS", old); err != nil {
		t.Fatal(err)
	}
	// 2 日進むと、old は範囲の外になる。別の放送日を取ったときに捨てられる。
	now = now.AddDate(0, 0, 2)
	if _, err := c.Programs(context.Background(), "TBS", cacheDay); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.progCache.stats(); n != 1 {
		t.Fatalf("件数 = %d", n)
	}
}

func TestProgramCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	size := int64(len(cacheXML))
	c := cacheClient(cs, &now, 7, size*2) // 2 件まで入る
	ctx := context.Background()
	d1, d2, d3 := cacheDay, cacheDay.AddDate(0, 0, 1), cacheDay.AddDate(0, 0, 2)

	for _, d := range []time.Time{d1, d2} {
		if _, err := c.Programs(ctx, "TBS", d); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	// d1 を使って、d2 を最も長く使っていないものにする。
	if _, err := c.Programs(ctx, "TBS", d1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := c.Programs(ctx, "TBS", d3); err != nil { // d2 が捨てられる
		t.Fatal(err)
	}
	if n, b := c.progCache.stats(); n != 2 || b != size*2 {
		t.Fatalf("件数 = %d, 大きさ = %d", n, b)
	}
	before := cs.count()
	if _, err := c.Programs(ctx, "TBS", d1); err != nil || cs.count() != before {
		t.Fatalf("d1 は残っているはず: err = %v, hits %d → %d", err, before, cs.count())
	}
	if _, err := c.Programs(ctx, "TBS", d2); err != nil || cs.count() != before+1 {
		t.Fatalf("d2 は捨てられたはず: err = %v, hits %d → %d", err, before, cs.count())
	}
}

func TestProgramCacheBodyLargerThanLimitIsNotKept(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 10)
	ps, err := c.Programs(context.Background(), "TBS", cacheDay)
	if err != nil || len(ps) != 1 {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	if n, b := c.progCache.stats(); n != 0 || b != 0 {
		t.Fatalf("件数 = %d, 大きさ = %d", n, b)
	}
}

func TestProgramCache304WithoutCacheIsTemporary(t *testing.T) {
	cs := newCacheServer(t)
	cs.always304 = true
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	_, err := c.Programs(context.Background(), "TBS", cacheDay)
	if !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := c.progCache.stats(); n != 0 {
		t.Fatalf("件数 = %d", n)
	}
}

func TestProgramCacheConditionalGetFailure(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	ctx := context.Background()
	if _, err := c.Programs(ctx, "TBS", cacheDay); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	cs.mu.Lock()
	cs.failNext = true
	cs.mu.Unlock()
	if _, err := c.Programs(ctx, "TBS", cacheDay); !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v", err)
	}
	// 失敗してもキャッシュは残り、次の問い合わせで 304 になれば使える。
	ps, err := c.Programs(ctx, "TBS", cacheDay)
	if err != nil || len(ps) != 1 {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	if cs.inm[2] != `"v1"` {
		t.Fatalf("If-None-Match = %q", cs.inm)
	}
}

func TestProgramCacheConcurrent(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, int64(len(cacheXML))*3)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := cacheDay.AddDate(0, 0, i%5)
			if _, err := c.Programs(context.Background(), "TBS", d); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}
