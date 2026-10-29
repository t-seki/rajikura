package radiko

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func jst(y int, m time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, m, d, h, mi, s, 0, JST)
}

func TestPartialKey(t *testing.T) {
	got, err := partialKey(3, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte(authKey[3:11]))
	if got != want {
		t.Fatalf("partialKey(3,8) = %q, want %q", got, want)
	}
	if want != "MTUxMDczYzA=" {
		t.Fatalf("unexpected fixed value %q", want)
	}
	for _, c := range [][2]int{{-1, 4}, {0, 0}, {30, 20}} {
		if _, err := partialKey(c[0], c[1]); err == nil {
			t.Errorf("partialKey(%d,%d) should fail", c[0], c[1])
		}
	}
}

func TestParseArea(t *testing.T) {
	for _, tc := range []struct {
		body, want string
		err        error
	}{
		{"JP13,東京都,tokyo Japan\n", "JP13", nil},
		{"JP27,大阪府,osaka Japan", "JP27", nil},
		{"OUT", "", ErrOutOfArea},
		{"out\n", "", ErrOutOfArea},
		{"", "", ErrOutOfArea},
		{" \n", "", ErrOutOfArea},
	} {
		got, err := parseArea(tc.body)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("parseArea(%q) = %q, %v; want %q, %v", tc.body, got, err, tc.want, tc.err)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	f := newFake(t)
	f.keyOffset, f.keyLength = 5, 6 // 毎回ヘッダから読むことの確認（auth2 側が同じ値で検証する）
	tok, err := f.client().Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "tok1" || tok.AreaID != "JP13" {
		t.Fatalf("token = %+v", tok)
	}
}

func TestAuthenticateOutOfArea(t *testing.T) {
	f := newFake(t)
	f.areaBody = "OUT"
	_, err := f.client().Authenticate(context.Background())
	if !errors.Is(err, ErrOutOfArea) {
		t.Fatalf("err = %v, want ErrOutOfArea", err)
	}
}

func TestPlanChunks(t *testing.T) {
	start := jst(2026, 10, 7, 10, 0, 0)
	type want struct {
		seekOff, endOff time.Duration
		l               int
	}
	for _, tc := range []struct {
		name string
		dur  time.Duration
		want []want
	}{
		{"exact multiple", 600 * time.Second, []want{{0, 300 * time.Second, 300}, {300 * time.Second, 600 * time.Second, 300}}},
		{"short last chunk rounds up", 623 * time.Second, []want{
			{0, 300 * time.Second, 300}, {300 * time.Second, 600 * time.Second, 300}, {600 * time.Second, 623 * time.Second, 25}}},
		{"under 5 sec", 600*time.Second + 2*time.Second, []want{
			{0, 300 * time.Second, 300}, {300 * time.Second, 600 * time.Second, 300}, {600 * time.Second, 602 * time.Second, 5}}},
		{"single short", 90 * time.Second, []want{{0, 90 * time.Second, 90}}},
		{"single odd", 91 * time.Second, []want{{0, 91 * time.Second, 95}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planChunks(start, start.Add(tc.dur))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d chunks %+v, want %d", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if !g.Seek.Equal(start.Add(w.seekOff)) || !g.End.Equal(start.Add(w.endOff)) || g.L != w.l {
					t.Errorf("chunk %d = %+v, want seek+%v end+%v l=%d", i, g, w.seekOff, w.endOff, w.l)
				}
				if g.End.After(start.Add(tc.dur)) {
					t.Errorf("chunk %d goes past the end", i)
				}
			}
		})
	}
}

func TestMaybeGunzip(t *testing.T) {
	plain := []byte("<radiko/>")
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(plain)
	zw.Close()
	for name, in := range map[string][]byte{"gzip": b.Bytes(), "plain": plain} {
		got, err := maybeGunzip(in)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	if got, err := maybeGunzip(nil); err != nil || len(got) != 0 {
		t.Errorf("empty: got %q, %v", got, err)
	}
	if _, err := maybeGunzip([]byte{0x1f, 0x8b, 0x00}); err == nil {
		t.Error("broken gzip should fail")
	}
}

func TestBroadcastDay(t *testing.T) {
	for _, tc := range []struct {
		in, want time.Time
	}{
		{jst(2026, 10, 8, 0, 0, 0), jst(2026, 10, 7, 0, 0, 0)},
		{jst(2026, 10, 8, 4, 59, 59), jst(2026, 10, 7, 0, 0, 0)},
		{jst(2026, 10, 8, 5, 0, 0), jst(2026, 10, 8, 0, 0, 0)},
		{jst(2026, 10, 8, 23, 30, 0), jst(2026, 10, 8, 0, 0, 0)},
		{time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), jst(2026, 10, 7, 0, 0, 0)}, // JST 01:00
	} {
		if got := BroadcastDay(tc.in); !got.Equal(tc.want) {
			t.Errorf("BroadcastDay(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPrograms(t *testing.T) {
	for _, gz := range []bool{false, true} {
		f := newFake(t)
		f.gzipPrograms = gz
		progs, err := f.client().Programs(context.Background(), "TBS", jst(2026, 10, 7, 0, 0, 0))
		if err != nil {
			t.Fatalf("gzip=%v: %v", gz, err)
		}
		if len(progs) != 3 {
			t.Fatalf("gzip=%v: got %d programs", gz, len(progs))
		}
		p0, p1, p2 := progs[0], progs[1], progs[2]
		if p0.StationID != "TBS" || p0.Title != "深夜の番組" || p0.Performer != "出演者A" ||
			p0.ImageURL != "https://example.invalid/img1.jpg" {
			t.Errorf("p0 = %+v", p0)
		}
		if !p0.Start.Equal(jst(2026, 10, 8, 1, 30, 0)) || !p0.End.Equal(jst(2026, 10, 8, 3, 0, 0)) {
			t.Errorf("p0 time = %v - %v", p0.Start, p0.End)
		}
		if got := BroadcastDay(p0.Start); !got.Equal(jst(2026, 10, 7, 0, 0, 0)) {
			t.Errorf("p0 broadcast day = %v", got)
		}
		if p0.Description != "深夜の案内 & 説明" {
			t.Errorf("p0 description = %q", p0.Description)
		}
		if p1.Description != "info が空なので desc" || p1.TSInNG != "2" || p1.TSPlusInNG != "1" {
			t.Errorf("p1 = %+v", p1)
		}
		if p2.Description != "リンク <3" {
			t.Errorf("p2 description = %q", p2.Description)
		}
		// 分割番組: 同じ id が並ぶが、開始時刻で区別できる。
		if p1.ID != p2.ID || p1.Start.Equal(p2.Start) || !p1.End.Equal(p2.Start) {
			t.Errorf("split program: %+v / %+v", p1, p2)
		}
	}
}

func TestStations(t *testing.T) {
	f := newFake(t)
	st, err := f.client().Stations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 || st[0] != (Station{ID: "TBS", Name: "TBSラジオ", TFMaxDelay: 90}) || st[1].ID != "QRR" {
		t.Fatalf("stations = %+v", st)
	}
}

func TestParseCandidatesOrder(t *testing.T) {
	got, err := parseCandidates([]byte(`<urls>
<url areafree="0" timefree="0"><playlist_create_url>live</playlist_create_url></url>
<url areafree="0" timefree="1"><playlist_create_url>first</playlist_create_url></url>
<url areafree="1" timefree="1"><playlist_create_url>areafree</playlist_create_url></url>
<url areafree="0" timefree="1"><playlist_create_url>second</playlist_create_url></url>
</urls>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("candidates = %v, want [first second]", got)
	}

	got, err = parseCandidates([]byte(`<urls><url areafree="1" timefree="1"><playlist_create_url>x</playlist_create_url></url></urls>`))
	if err != nil || len(got) != 0 {
		t.Fatalf("areafree-only: got %v, %v; want no candidates", got, err)
	}
}

func TestTimefreeCandidatesNone(t *testing.T) {
	f := newFake(t)
	// 偽のサーバーは TBS だけ持つ。ストリーム情報が無い局は取れない（4 分類のどれかに入る）。
	_, err := f.client().timefreeCandidates(context.Background(), "NOPE")
	if !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v, want ErrTemporary", err)
	}
}

func TestProgramsUnclassifiedStatusIsTemporary(t *testing.T) {
	f := newFake(t)
	_, err := f.client().Programs(context.Background(), "QRR", jst(2026, 10, 7, 0, 0, 0))
	if !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v, want ErrTemporary", err)
	}
}

func TestProgramsDayInJST(t *testing.T) {
	f := newFake(t)
	// UTC の 2026-10-06 16:00 は日本時間の 2026-10-07 01:00。偽のサーバーは 20261007 の URL だけ受け付ける。
	if _, err := f.client().Programs(context.Background(), "TBS", time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func TestFetchTimefreePlaylistType(t *testing.T) {
	f, c, tok, start, end := fetchEnv(t)
	f.wantType = "c"
	c.PlaylistType = "c"
	var out bytes.Buffer
	if _, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil); err != nil {
		t.Fatal(err)
	}
}

// fetchEnv は FetchTimefree の共通の準備。放送回は 10:00:00〜10:12:30（チャンク 300/300/150 秒）。
func fetchEnv(t *testing.T) (*fakeRadiko, *Client, Token, time.Time, time.Time) {
	t.Helper()
	f := newFake(t)
	c := f.client()
	c.Now = func() time.Time { return jst(2026, 10, 7, 12, 0, 0) }
	tok, err := c.Authenticate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start := jst(2026, 10, 7, 10, 0, 0)
	f.wantFT = start.Format(timeLayout)
	return f, c, tok, start, jst(2026, 10, 7, 10, 12, 30)
}

func TestFetchTimefree(t *testing.T) {
	f, c, tok, start, end := fetchEnv(t)
	var out bytes.Buffer
	res, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != expectedAudio(start, end) {
		t.Fatalf("audio mismatch: %q", out.String())
	}
	if res.Segments != 150 || res.DurationSeconds != 750 {
		t.Errorf("result = %+v", res)
	}
	if res.PlaylistCreateURL != f.srv.URL+"/tf/playlist.m3u8" {
		t.Errorf("url = %q", res.PlaylistCreateURL)
	}
	if f.authCount != 1 {
		t.Errorf("authCount = %d", f.authCount)
	}
}

func TestFetchTimefreeNotYet(t *testing.T) {
	_, c, tok, start, end := fetchEnv(t)
	c.Now = func() time.Time { return end.Add(-time.Second) }
	var out bytes.Buffer
	_, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if !errors.Is(err, ErrNotYetAvailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchTimefreeFallsBackToNextCandidate(t *testing.T) {
	f, c, tok, start, end := fetchEnv(t)
	f.badFirstCandidate = true
	var out bytes.Buffer
	res, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.badHits != 1 {
		t.Errorf("bad candidate hits = %d, want 1 (the next candidate is kept for later chunks)", f.badHits)
	}
	if out.String() != expectedAudio(start, end) || res.Segments != 150 {
		t.Errorf("audio mismatch (segments=%d)", res.Segments)
	}
}

func TestFetchTimefreeTokenExpiry(t *testing.T) {
	f, c, tok, start, end := fetchEnv(t)
	f.expireAtPlaylistReq = 2 // 2 チャンク目の前に失効
	var out bytes.Buffer
	_, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.authCount != 2 {
		t.Errorf("authCount = %d, want 2 (one re-authentication)", f.authCount)
	}
	if out.String() != expectedAudio(start, end) {
		t.Errorf("audio mismatch: chunk 2 must not be duplicated or lost")
	}
}

func TestFetchTimefreeTokenExpiresTwice(t *testing.T) {
	f, c, _, start, end := fetchEnv(t)
	// 古いトークンで 403 → 再認証 → 再試行の要求（2 回目）でもう一度失効させる。再認証は 1 回までなので諦める。
	f.expireAtPlaylistReq = 2
	var out bytes.Buffer
	_, err := c.FetchTimefree(context.Background(), Token{Value: "stale", AreaID: "JP13"}, "TBS", start, end, &out, nil)
	if !errors.Is(err, ErrTemporary) {
		t.Fatalf("err = %v, want ErrTemporary after a second rejection", err)
	}
	if f.authCount != 2 {
		t.Errorf("authCount = %d, want 2 (fetchEnv + one re-authentication)", f.authCount)
	}
}

func TestFetchTimefreeSeamTolerance(t *testing.T) {
	for _, tc := range []struct {
		gap     time.Duration
		wantErr bool
	}{
		{300 * time.Millisecond, false},
		{-300 * time.Millisecond, false},
		{700 * time.Millisecond, true},
		{-700 * time.Millisecond, true},
		{2 * time.Second, true},
	} {
		t.Run(tc.gap.String(), func(t *testing.T) {
			f, c, tok, start, end := fetchEnv(t)
			f.gapFromSeek = start.Add(300 * time.Second)
			f.gap = tc.gap
			var out bytes.Buffer
			_, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
			if !tc.wantErr {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrTemporary) || !strings.Contains(err.Error(), "seam") {
				t.Fatalf("err = %v, want a temporary seam error", err)
			}
			// ずれたチャンクは書き出さない。
			if out.String() != expectedAudio(start, start.Add(300*time.Second)) {
				t.Errorf("only the first chunk should have been written, got %d bytes", out.Len())
			}
		})
	}
}

func TestFetchTimefreeSeamFallsBackToNextCandidate(t *testing.T) {
	f, c, tok, start, end := fetchEnv(t)
	// 1 つ目の候補だけ、2 チャンク目からセグメントが 1 本（5 秒）欠ける。
	f.gapFromSeek = start.Add(300 * time.Second)
	f.gap = 5 * time.Second
	f.gapFirstOnly = true
	var out bytes.Buffer
	res, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != expectedAudio(start, end) || res.Segments != 150 {
		t.Errorf("audio mismatch (segments=%d)", res.Segments)
	}
	if !strings.HasSuffix(res.PlaylistCreateURL, "/tf/playlist.m3u8") {
		t.Errorf("PlaylistCreateURL = %q, want the second candidate", res.PlaylistCreateURL)
	}
}

// チャンクを書き終えるたびに、それまでの #EXTINF の合計で onChunk を呼ぶ。チャンクを別の候補で取り直しても、値は戻らない。
func TestFetchTimefreeOnChunk(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback bool
	}{{"通常", false}, {"継ぎ目で次の候補に取り直す", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, tok, start, end := fetchEnv(t)
			if tc.fallback {
				f.gapFromSeek = start.Add(300 * time.Second)
				f.gap = 5 * time.Second
				f.gapFirstOnly = true
			}
			var out bytes.Buffer
			var got []float64
			if _, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, func(s float64) {
				got = append(got, s)
			}); err != nil {
				t.Fatal(err)
			}
			if want := []float64{300, 600, 750}; !slices.Equal(got, want) {
				t.Errorf("onChunk = %v, want %v", got, want)
			}
		})
	}
}

func TestFetchTimefreeOverlap(t *testing.T) {
	f, c, tok, start, end := fetchEnv(t)
	f.sameSegmentURL = true
	var out bytes.Buffer
	_, err := c.FetchTimefree(context.Background(), tok, "TBS", start, end, &out, nil)
	if !errors.Is(err, ErrTemporary) || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("err = %v, want a temporary overlap error", err)
	}
}

func TestParseMedialist(t *testing.T) {
	segs, err := parseMedialist([]byte("#EXTM3U\n#EXTINF:5.005,\n#EXT-X-PROGRAM-DATE-TIME:2026-10-07T10:00:00.123+09:00\nseg1.aac\n"), "http://h/tf/medialist.m3u8?x=1")
	if err != nil || len(segs) != 1 {
		t.Fatalf("segs = %+v, %v", segs, err)
	}
	if segs[0].URL != "http://h/tf/seg1.aac" || segs[0].Duration != 5.005 || !segs[0].HasPDT {
		t.Errorf("seg = %+v", segs[0])
	}
	want := time.Date(2026, 10, 7, 10, 0, 5, 128000000, JST)
	if d := segs[0].end().Sub(want); d < -time.Millisecond || d > time.Millisecond {
		t.Errorf("end = %v, want %v", segs[0].end(), want)
	}
}
