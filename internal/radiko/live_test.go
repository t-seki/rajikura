package radiko

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLive は radiko のライブの偽のサーバー。時計は仮想で、medialist を 1 回返すごとに 5 秒進む。
// medialist は、マスタを取った時刻 s0 から l 秒遡った所から、(今 - 遅れ) までの 5 秒刻みのセグメントを返す
// （実際の radiko の挙動の写し。遅れの値は実測 1 回のもので、テストの決め打ち）。
type fakeLive struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	clock      time.Time
	lag        time.Duration
	authCount  int
	validToken string

	outageFrom, outageTo time.Time // この間は medialist とマスタが 500 を返す（時計は進める）
	failMediaOnce        int       // この回数目の medialist で、トークンを失効させて 500 を返す（0 なら無効）
	mediaReq             int
	badA                 bool // 1 つ目の候補を 500 にする
	hitsA, hitsB         int
	masters              []string // マスタの l（検査用）
	noLive               bool
}

func newFakeLive(t *testing.T, clock time.Time) *fakeLive {
	t.Helper()
	f := &fakeLive{t: t, clock: clock, lag: 60 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/api/auth1", f.auth1)
	mux.HandleFunc("/v2/api/auth2", f.auth2)
	mux.HandleFunc("/v3/station/stream/pc_html5/TBS.xml", f.streamXML)
	mux.HandleFunc("/liveA/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) { f.master(w, r, "liveA") })
	mux.HandleFunc("/liveB/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) { f.master(w, r, "liveB") })
	mux.HandleFunc("/media.m3u8", f.medialist)
	mux.HandleFunc("/seg/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "[%s]", strings.TrimPrefix(r.URL.Path, "/seg/"))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLive) client() *Client {
	c := NewClient()
	c.AuthHost = f.srv.URL
	c.ProgramHost = f.srv.URL
	c.Now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.clock
	}
	c.LivePollInterval = time.Millisecond
	c.LiveRetryInterval = time.Millisecond
	return c
}

func (f *fakeLive) auth1(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authCount++
	tok := fmt.Sprintf("tok%d", f.authCount)
	f.mu.Unlock()
	w.Header()["x-radiko-authtoken"] = []string{tok}
	w.Header()["x-radiko-keyoffset"] = []string{"3"}
	w.Header()["x-radiko-keylength"] = []string{"8"}
}

func (f *fakeLive) auth2(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.validToken = r.Header.Get("X-Radiko-AuthToken")
	f.mu.Unlock()
	fmt.Fprint(w, "JP13,東京都,tokyo Japan\n")
}

func (f *fakeLive) streamXML(w http.ResponseWriter, r *http.Request) {
	if f.noLive {
		fmt.Fprintf(w, `<urls><url areafree="0" timefree="1"><playlist_create_url>%s/tf/playlist.m3u8</playlist_create_url></url></urls>`, f.srv.URL)
		return
	}
	fmt.Fprintf(w, `<urls>
<url areafree="0" timefree="1"><playlist_create_url>%[1]s/tf/playlist.m3u8</playlist_create_url></url>
<url areafree="1" timefree="0"><playlist_create_url>%[1]s/areafree/playlist.m3u8</playlist_create_url></url>
<url areafree="0" timefree="0"><playlist_create_url>%[1]s/liveA/playlist.m3u8</playlist_create_url></url>
<url areafree="0" timefree="0"><playlist_create_url>%[1]s/liveB/playlist.m3u8</playlist_create_url></url>
</urls>`, f.srv.URL)
}

func (f *fakeLive) inOutage() bool {
	return !f.outageFrom.IsZero() && !f.clock.Before(f.outageFrom) && f.clock.Before(f.outageTo)
}

func (f *fakeLive) master(w http.ResponseWriter, r *http.Request, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "liveA" {
		f.hitsA++
	} else {
		f.hitsB++
	}
	if name == "liveA" && f.badA {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if f.inOutage() {
		f.clock = f.clock.Add(5 * time.Second) // 切れている間も時間は進む
		http.Error(w, "outage", http.StatusInternalServerError)
		return
	}
	tok := r.Header.Get("X-Radiko-AuthToken")
	if tok == "" {
		http.Error(w, "no token", http.StatusUnauthorized)
		return
	}
	if tok != f.validToken {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	if q.Get("station_id") != "TBS" || q.Get("type") != "b" || q.Get("lsid") == "" || q.Get("timefree") != "" {
		http.Error(w, "bad query: "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}
	f.masters = append(f.masters, q.Get("l"))
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=48000\n/media.m3u8?l=%s&s0=%d\n", q.Get("l"), f.clock.Unix())
}

func (f *fakeLive) medialist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mediaReq++
	if f.failMediaOnce != 0 && f.mediaReq == f.failMediaOnce {
		f.validToken = "expired"
		f.clock = f.clock.Add(5 * time.Second)
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if f.inOutage() {
		f.clock = f.clock.Add(5 * time.Second)
		http.Error(w, "outage", http.StatusInternalServerError)
		return
	}
	l, _ := strconv.Atoi(r.URL.Query().Get("l"))
	s0, _ := strconv.ParseInt(r.URL.Query().Get("s0"), 10, 64)
	oldest := s0 - int64(l) - int64(f.lag/time.Second)
	oldest = (oldest + 4) / 5 * 5
	newest := f.clock.Add(-f.lag).Unix()
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:5\n")
	for s := oldest; s+5 <= newest; s += 5 {
		t := time.Unix(s, 0).In(JST)
		fmt.Fprintf(&b, "#EXTINF:5.000,\n#EXT-X-PROGRAM-DATE-TIME:%s\n%s/seg/%s.aac\n", t.Format("2006-01-02T15:04:05.000-07:00"), f.srv.URL, t.Format(timeLayout))
	}
	f.clock = f.clock.Add(5 * time.Second)
	w.Write(b.Bytes())
}

// liveStart は 5 秒刻みの境界に載せた放送回の開始。
var liveStart = time.Date(2026, 10, 20, 5, 0, 0, 0, JST)

func liveReq(minutes int) LiveRequest {
	end := liveStart.Add(time.Duration(minutes) * time.Minute)
	return LiveRequest{StationID: "TBS", Start: liveStart, End: end, Deadline: end.Add(5 * time.Minute)}
}

func runLive(t *testing.T, f *fakeLive, req LiveRequest) (LiveResult, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := f.client()
	tok, err := c.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	res, err := c.FetchLive(ctx, tok, req, &buf)
	return res, buf.String(), err
}

func TestFetchLive_ConnectBeforeStart(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	req := liveReq(2)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if want := expectedAudio(req.Start, req.End); got != want {
		t.Errorf("audio = %q, want %q", got, want)
	}
	if !res.Complete || len(res.Gaps) != 0 || res.Segments != 24 || res.DurationSeconds != 120 {
		t.Errorf("res = %+v", res)
	}
	if !res.Next.Equal(req.End) {
		t.Errorf("Next = %v, want %v", res.Next, req.End)
	}
	if !strings.HasSuffix(res.PlaylistCreateURL, "/liveA/playlist.m3u8") {
		t.Errorf("PlaylistCreateURL = %q", res.PlaylistCreateURL)
	}
	if f.masters[0] != "270" {
		t.Errorf("l = %q, want 270", f.masters[0])
	}
}

func TestFetchLive_LateJoinBackfills(t *testing.T) {
	// 開始から 2 分遅れて繋ぐ。遅れ 60 秒 + 遡り 270 秒の内なので、開始から欠けずに埋まる。
	f := newFakeLive(t, liveStart.Add(2*time.Minute))
	req := liveReq(5)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if want := expectedAudio(req.Start, req.End); got != want {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	if len(res.Gaps) != 0 || !res.Complete {
		t.Errorf("res = %+v", res)
	}
}

func TestFetchLive_LateJoinBeyondRangeIsGap(t *testing.T) {
	// 開始から 10 分遅れて繋ぐ。遡れるのは (10 分 - 60 秒 - 270 秒) = 開始から 270 秒の所まで。それより前は欠落。
	f := newFakeLive(t, liveStart.Add(10*time.Minute))
	req := liveReq(15)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	gap := res.Gaps[0]
	if !gap.From.Equal(req.Start) || !gap.To.Equal(liveStart.Add(270*time.Second)) {
		t.Errorf("gap = %v - %v", gap.From, gap.To)
	}
	if want := expectedAudio(gap.To, req.End); got != want {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	if !res.Complete {
		t.Errorf("res = %+v", res)
	}
}

func TestFetchLive_OutageWithinLookbackIsFilled(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	f.outageFrom, f.outageTo = liveStart.Add(30*time.Second), liveStart.Add(30*time.Second+100*time.Second)
	req := liveReq(5)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("gaps = %+v", res.Gaps)
	}
	if want := expectedAudio(req.Start, req.End); got != want {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	if len(f.masters) < 2 {
		t.Errorf("masters = %v, want a reconnect", f.masters)
	}
}

func TestFetchLive_OutageBeyondLookbackIsGap(t *testing.T) {
	// 繋がっていた 30 秒目までを持ち、500 秒切れる。復帰時に遡れるのは 60 + 270 秒前まで。
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	f.outageFrom, f.outageTo = liveStart.Add(90*time.Second), liveStart.Add(90*time.Second+500*time.Second)
	req := liveReq(15)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	gap := res.Gaps[0]
	// 復帰の時刻は 590 秒目より後。遡れる先頭は (復帰の時刻 - 330 秒) で、それより前が欠落。
	if gap.From.Before(liveStart) || !gap.To.After(gap.From) || gap.To.After(liveStart.Add(400*time.Second)) {
		t.Errorf("gap = %v - %v", gap.From.Sub(liveStart), gap.To.Sub(liveStart))
	}
	if want := expectedAudio(req.Start, gap.From) + expectedAudio(gap.To, req.End); got != want {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestFetchLive_DeadlineMakesRestAGap(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	req := liveReq(5)
	req.Deadline = liveStart.Add(95 * time.Second) // 遅れ 60 秒のため、取れているのは 35 秒目まで
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 1 || !res.Gaps[0].To.Equal(req.End) {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	if want := expectedAudio(req.Start, res.Gaps[0].From); got != want || got == "" {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestFetchLive_ResumeDoesNotWriteTwice(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	req := liveReq(3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := f.client()
	tok, err := c.Authenticate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	// 1 回目は ctx を途中で止める（serve の再起動に当たる）。
	w := &cancelAfter{buf: &buf, n: 10, cancel: cancel}
	first, err := c.FetchLive(ctx, tok, req, w)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first err = %v, want context.Canceled", err)
	}
	if first.Complete || first.Segments != 10 || !first.Next.Equal(req.Start.Add(50*time.Second)) {
		t.Fatalf("first = %+v", first)
	}

	// 2 回目は、Next から続ける。medialist は遡るので、持っている分も返ってくるが、書かれない。
	req.Resume = first.Next
	second, err := c.FetchLive(context.Background(), tok, req, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if want := expectedAudio(req.Start, req.End); buf.String() != want {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", buf.Len(), len(want))
	}
	if second.Segments != 36-10 || len(second.Gaps) != 0 || !second.Complete {
		t.Errorf("second = %+v", second)
	}
}

// cancelAfter は n 回書いたら cancel を呼ぶ Writer。
type cancelAfter struct {
	buf    *bytes.Buffer
	n      int
	cancel context.CancelFunc
}

func (w *cancelAfter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	w.n--
	if w.n == 0 {
		w.cancel()
	}
	return n, err
}

func TestFetchLive_ReauthOn403(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	f.failMediaOnce = 20 // 20 回目の medialist でトークンを失効させて切る。繋ぎ直しのマスタが 403 になる
	req := liveReq(3)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if f.authCount != 2 {
		t.Errorf("authCount = %d, want 2 (initial + re-auth)", f.authCount)
	}
	if want := expectedAudio(req.Start, req.End); got != want || len(res.Gaps) != 0 {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes, gaps %+v", len(got), len(want), res.Gaps)
	}
}

func TestFetchLive_SwitchesCandidateAfterRepeatedFailures(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	f.badA = true
	req := liveReq(2)
	res, got, err := runLive(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if f.hitsA != liveMaxFailures {
		t.Errorf("hitsA = %d, want %d", f.hitsA, liveMaxFailures)
	}
	if !strings.HasSuffix(res.PlaylistCreateURL, "/liveB/playlist.m3u8") {
		t.Errorf("PlaylistCreateURL = %q", res.PlaylistCreateURL)
	}
	if want := expectedAudio(req.Start, req.End); got != want {
		t.Errorf("audio mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestFetchLive_NoLiveCandidate(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	f.noLive = true
	_, _, err := runLive(t, f, liveReq(2))
	if !errors.Is(err, ErrNotAvailable) {
		t.Errorf("err = %v, want ErrNotAvailable", err)
	}
}

func TestFetchLive_NothingBeforeDeadlineIsError(t *testing.T) {
	f := newFakeLive(t, liveStart.Add(-time.Minute))
	f.outageFrom, f.outageTo = liveStart.Add(-time.Hour), liveStart.Add(time.Hour)
	res, _, err := runLive(t, f, liveReq(1))
	if !errors.Is(err, ErrTemporary) {
		t.Errorf("err = %v, want ErrTemporary", err)
	}
	if len(res.Gaps) != 1 || res.Segments != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestFetchLive_InvalidRequest(t *testing.T) {
	f := newFakeLive(t, liveStart)
	req := liveReq(1)
	req.End = req.Start
	if _, _, err := runLive(t, f, req); err == nil {
		t.Error("want error for empty range")
	}
	req = liveReq(1)
	req.Deadline = time.Time{}
	if _, _, err := runLive(t, f, req); err == nil {
		t.Error("want error for missing deadline")
	}
}
