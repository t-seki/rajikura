package radiko

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gzLen は s を gzip にした大きさ（キャッシュが持つ大きさ）。
func gzLen(t *testing.T, s string) int64 {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return int64(buf.Len())
}

// キャッシュは本文を gzip のまま持ち、使うたびに展開する。応答が gzip でなければ、こちらで gzip にして持つ。
func TestProgramCacheKeepsGzip(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	for i := 0; i < 2; i++ {
		ps, err := c.Programs(context.Background(), "TBS", cacheDay)
		if err != nil || len(ps) != 1 || ps[0].Title != "t" {
			t.Fatalf("ps = %v, err = %v", ps, err)
		}
	}
	if n, b := c.progCache.stats(); n != 1 || b != gzLen(t, cacheXML) {
		t.Fatalf("件数 = %d, 大きさ = %d, gzip の大きさ = %d", n, b, gzLen(t, cacheXML))
	}
	for _, e := range c.progCache.entries {
		if !isGzip(e.gz) || e.station != "TBS" {
			t.Fatalf("持っているもの = %q（局 %q）", e.gz[:2], e.station)
		}
	}
}

// 応答が（Content-Encoding なしで）gzip のまま来たら、そのバイト列をそのまま持つ。
func TestProgramCacheKeepsResponseGzipAsIs(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(cacheXML))
	zw.Close()
	raw := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Write(raw)
	}))
	defer srv.Close()
	now := jst(2026, 10, 13, 12, 0, 0)
	c := NewClient()
	c.ProgramHost = srv.URL
	c.Now = func() time.Time { return now }
	c.EnableProgramCache(7, 1<<20, nil)
	ps, err := c.Programs(context.Background(), "TBS", cacheDay)
	if err != nil || len(ps) != 1 {
		t.Fatalf("ps = %v, err = %v", ps, err)
	}
	for _, e := range c.progCache.entries {
		if !bytes.Equal(e.gz, raw) {
			t.Fatal("応答の gzip をそのまま持っていない")
		}
	}
}

// 取り直しを緩めた読み手（ProgramsRelaxed）は、放送日が終わった後に取った分を尋ね直さず、ほかは 6 時間尋ねない。
// ふつうの読み手（Programs）は、同じ項目でも 60 秒で尋ね直す。
func TestProgramCacheRelaxedReads(t *testing.T) {
	ctx := context.Background()
	t.Run("今日の放送日は 6 時間尋ねない", func(t *testing.T) {
		cs := newCacheServer(t)
		now := jst(2026, 10, 13, 12, 0, 0)
		c := cacheClient(cs, &now, 7, 1<<20)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil {
			t.Fatal(err)
		}
		now = now.Add(6*time.Hour - time.Second)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil || cs.count() != 1 {
			t.Fatalf("6 時間以内: err = %v, hits = %d", err, cs.count())
		}
		// ふつうの読み手は 60 秒を過ぎたので尋ねる。
		if _, err := c.Programs(ctx, "TBS", cacheDay); err != nil || cs.count() != 2 {
			t.Fatalf("ふつうの読み手: err = %v, hits = %d", err, cs.count())
		}
		// 304 で取った時刻が新しくなる。そこから 6 時間を過ぎたら、緩めた読み手も ETag を付けて尋ねる。
		now = now.Add(6*time.Hour + time.Second)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil || cs.count() != 3 || cs.inm[2] != `"v1"` {
			t.Fatalf("6 時間の後: err = %v, hits = %d, inm = %q", err, cs.count(), cs.inm)
		}
	})
	t.Run("放送日が終わった後に取った分は尋ね直さない", func(t *testing.T) {
		cs := newCacheServer(t)
		// 放送日 10/13 は 10/14 の 5 時に終わる。その後に取る。
		now := jst(2026, 10, 14, 5, 0, 1)
		c := cacheClient(cs, &now, 7, 1<<20)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil {
			t.Fatal(err)
		}
		now = now.AddDate(0, 0, 5) // 6 時間をはるかに過ぎても、範囲の内なら尋ねない
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil || cs.count() != 1 {
			t.Fatalf("err = %v, hits = %d", err, cs.count())
		}
		// ふつうの読み手は尋ねる。
		if _, err := c.Programs(ctx, "TBS", cacheDay); err != nil || cs.count() != 2 {
			t.Fatalf("ふつうの読み手: err = %v, hits = %d", err, cs.count())
		}
	})
	t.Run("放送日が終わる前に取った過去の分は 6 時間で尋ね直す", func(t *testing.T) {
		cs := newCacheServer(t)
		now := jst(2026, 10, 14, 4, 59, 0) // 放送日 10/13 はまだ終わっていない
		c := cacheClient(cs, &now, 7, 1<<20)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil {
			t.Fatal(err)
		}
		now = now.Add(6*time.Hour + time.Second)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil || cs.count() != 2 {
			t.Fatalf("err = %v, hits = %d", err, cs.count())
		}
		// 尋ね直した（304）のは放送日が終わった後なので、そこからは尋ねない。
		now = now.AddDate(0, 0, 1)
		if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil || cs.count() != 2 {
			t.Fatalf("err = %v, hits = %d", err, cs.count())
		}
	})
}

// 同じ局・放送日を同時に 2 回は取りにいかない（取りにいっている途中なら、その結果を待って使う）。
func TestProgramCacheSingleFlight(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(cacheXML))
	}))
	defer srv.Close()
	now := jst(2026, 10, 13, 12, 0, 0)
	c := NewClient()
	c.ProgramHost = srv.URL
	c.Now = func() time.Time { return now }
	c.EnableProgramCache(7, 1<<20, nil)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = c.ProgramsRelaxed(context.Background(), "TBS", cacheDay)
			} else {
				_, err = c.Programs(context.Background(), "TBS", cacheDay)
			}
			errs <- err
		}(i)
	}
	// 全員が取りにいく（か、待つ）まで少し待ってから返す。
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("同時に %d 回取りにいった", n)
	}
	if len(c.progCache.flights) != 0 {
		t.Fatalf("取りにいっている途中の印が残った: %v", c.progCache.flights)
	}
}

// 局ごとの日別の番組表のうち、keep が false の局の分を捨てる。エリアの日別は捨てない。
func TestDropStationPrograms(t *testing.T) {
	cs := newCacheServer(t)
	now := jst(2026, 10, 13, 12, 0, 0)
	c := cacheClient(cs, &now, 7, 1<<20)
	ctx := context.Background()
	for _, st := range []string{"TBS", "MBS", "ABC"} {
		if _, err := c.ProgramsRelaxed(ctx, st, cacheDay); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.AreaPrograms(ctx, "JP27", cacheDay); err != nil {
		t.Fatal(err)
	}
	n := c.DropStationPrograms(func(id string) bool { return id == "TBS" })
	if n != 2 {
		t.Fatalf("捨てた件数 = %d", n)
	}
	if got, b := c.progCache.stats(); got != 2 || b != 2*gzLen(t, cacheXML) {
		t.Fatalf("残り = %d 件, %d バイト", got, b)
	}
	before := cs.count()
	if _, err := c.ProgramsRelaxed(ctx, "TBS", cacheDay); err != nil || cs.count() != before {
		t.Fatalf("TBS は残っているはず: err = %v, hits %d → %d", err, before, cs.count())
	}
	if _, err := c.ProgramsRelaxed(ctx, "MBS", cacheDay); err != nil || cs.count() != before+1 {
		t.Fatalf("MBS は捨てたはず: err = %v, hits %d → %d", err, before, cs.count())
	}
}
