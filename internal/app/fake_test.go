package app

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/media"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/store"
)

const radikoTime = "20060102150405"

func jst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, domain.JST)
}

// prog は偽の番組表の 1 項目。
type prog struct {
	id         string
	start, end time.Time
	title      string
	pfm        string
	img        string
	info       string // 放送回の説明。空なら「説明」
	ng         bool   // タイムフリーの対象外（ts_in_ng を 2 にする。ライブで録る）
}

// fakeRadiko は radiko の偽のサーバー。実際の radiko にはつながない。
type fakeRadiko struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// programs は "YYYYMMDD/局" → 番組表。無い放送日は 404 を返す。
	programs map[string][]prog
	// guideFail が true なら番組表を 500 で返す。
	guideFail bool
	// stationsFail が true なら局一覧を 500 で返す。
	stationsFail bool
	// stationsOmitTBS が true なら局一覧に TBS を載せない。
	stationsOmitTBS bool
	// streamFail が true ならストリーム情報を 500 で返す（一時的な失敗）。
	streamFail bool
	// outOfArea が true なら auth2 でエリア外（OUT）を返す。
	outOfArea bool
	// noTimefree が true ならタイムフリーの候補を返さない（恒久的に取れない）。
	noTimefree bool
	// failSeek にある時刻（radiko の表記）を seek とするプレイリストを 500 で返す。
	failSeek map[string]bool
	// segment はセグメントの中身。nil なら URL の名前を "[...]" で囲んだもの。
	segment []byte
	// onSegment があればセグメントを返す前に呼ぶ。
	onSegment func(r *http.Request)
	// playlists は seek ごとのマスタの要求の回数。
	playlists map[string]int
	auths     int

	// 以下はライブのストリーム（live_test.go）。時計は env の clock で、medialist を 1 回返すごとに liveTick だけ進める。
	// medialist は、マスタを取った時刻から 270 秒遡った所から、(今 - 60 秒) までの 5 秒刻みのセグメントを返す
	// （radiko のライブの実測 1 回の写し。テストの決め打ち）。
	clock *clock
	// liveTick は medialist を 1 回返すごとに時計を進める量。
	liveTick time.Duration
	// liveOutageFrom〜liveOutageTo の間（時計で見る）は、ライブのマスタと medialist を 500 で返す（時計は liveTick 進める）。
	liveOutageFrom, liveOutageTo time.Time
	// noLive が true ならライブの候補を返さない（局にライブのストリームが無い）。
	noLive bool
	// liveMasters はライブのマスタを要求された時計の時刻。
	liveMasters []time.Time
	// onLiveMedia があれば、ライブの medialist を返す前に呼ぶ（ロックの外）。
	onLiveMedia func()
}

func newFakeRadiko(t *testing.T) *fakeRadiko {
	t.Helper()
	f := &fakeRadiko{t: t, programs: map[string][]prog{}, failSeek: map[string]bool{}, playlists: map[string]int{},
		clock: &clock{}, liveTick: 5 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/api/auth1", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auths++
		f.mu.Unlock()
		w.Header().Set("X-Radiko-AuthToken", "tok")
		w.Header().Set("X-Radiko-KeyOffset", "0")
		w.Header().Set("X-Radiko-KeyLength", "16")
	})
	mux.HandleFunc("/v2/api/auth2", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		out := f.outOfArea
		f.mu.Unlock()
		if out {
			fmt.Fprint(w, "OUT\n")
			return
		}
		fmt.Fprint(w, "JP13,東京都,tokyo Japan\n")
	})
	mux.HandleFunc("/v3/station/region/full.xml", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fail, omit := f.stationsFail, f.stationsOmitTBS
		f.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		tbs := `<station><id>TBS</id><name>TBSラジオ</name><tf_max_delay>90</tf_max_delay></station>`
		if omit {
			tbs = ""
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<region><stations region_id="JP13">
%s
<station><id>QRR</id><name>文化放送</name><tf_max_delay>60</tf_max_delay></station>
</stations></region>`, tbs)
	})
	mux.HandleFunc("/v3/program/station/date/", f.programsXML)
	mux.HandleFunc("/v3/station/stream/pc_html5/", f.streamXML)
	mux.HandleFunc("/tf/playlist.m3u8", f.playlist)
	mux.HandleFunc("/tf/medialist.m3u8", f.medialist)
	mux.HandleFunc("/seg/", f.seg)
	mux.HandleFunc("/live/playlist.m3u8", f.livePlaylist)
	mux.HandleFunc("/live/medialist.m3u8", f.liveMedialist)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRadiko) client(now func() time.Time) *radiko.Client {
	c := radiko.NewClient()
	c.AuthHost = f.srv.URL
	c.ProgramHost = f.srv.URL
	c.ChapterHost = f.srv.URL // 本物の radiko に繋がない（チャプターの API は 404 になる）
	c.Now = now
	c.LivePollInterval = time.Millisecond
	c.LiveRetryInterval = time.Millisecond
	return c
}

func (f *fakeRadiko) setPrograms(station string, day domain.BroadcastDay, ps ...prog) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fmt.Sprintf("%04d%02d%02d/%s", day.Year, int(day.Month), day.Day, station)
	f.programs[key] = ps
}

func (f *fakeRadiko) set(fn func(f *fakeRadiko)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeRadiko) playlistCount(start time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.playlists[start.In(domain.JST).Format(radikoTime)]
}

func (f *fakeRadiko) programsXML(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v3/program/station/date/"), ".xml")
	station := key[strings.Index(key, "/")+1:]
	f.mu.Lock()
	ps, ok := f.programs[key]
	fail := f.guideFail
	f.mu.Unlock()
	if fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><radiko><stations><station id="%s"><progs>`, station)
	for _, p := range ps {
		if p.info == "" {
			p.info = "説明"
		}
		ng := 0
		if p.ng {
			ng = 2
		}
		fmt.Fprintf(&b, `<prog id="%s" ft="%s" to="%s"><title>%s</title><pfm>%s</pfm><info>%s</info><img>%s</img><ts_in_ng>%d</ts_in_ng></prog>`,
			p.id, p.start.In(domain.JST).Format(radikoTime), p.end.In(domain.JST).Format(radikoTime),
			html.EscapeString(p.title), html.EscapeString(p.pfm), html.EscapeString(p.info), html.EscapeString(p.img), ng)
	}
	b.WriteString(`</progs></station></stations></radiko>`)
	io.WriteString(w, b.String())
}

func (f *fakeRadiko) streamXML(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	fail, none, noLive := f.streamFail, f.noTimefree, f.noLive
	f.mu.Unlock()
	if fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if none {
		fmt.Fprint(w, `<urls><url areafree="0" timefree="0"><playlist_create_url>x</playlist_create_url></url></urls>`)
		return
	}
	live := ""
	if !noLive {
		live = fmt.Sprintf(`<url areafree="0" timefree="0"><playlist_create_url>%s/live/playlist.m3u8</playlist_create_url></url>`, f.srv.URL)
	}
	fmt.Fprintf(w, `<urls><url areafree="0" timefree="1"><playlist_create_url>%s/tf/playlist.m3u8</playlist_create_url></url>%s</urls>`, f.srv.URL, live)
}

// liveOutage は、時計が切れている間か（f.mu を持って呼ぶ）。
func (f *fakeRadiko) liveOutage(now time.Time) bool {
	return !f.liveOutageFrom.IsZero() && !now.Before(f.liveOutageFrom) && now.Before(f.liveOutageTo)
}

// livePlaylist はライブのマスタ。medialist の URL に、マスタを取った時刻（遡る起点）を入れる。
func (f *fakeRadiko) livePlaylist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	now := f.clock.Now()
	f.liveMasters = append(f.liveMasters, now)
	if f.liveOutage(now) {
		f.clock.Set(now.Add(f.liveTick))
		f.mu.Unlock()
		http.Error(w, "outage", http.StatusInternalServerError)
		return
	}
	f.mu.Unlock()
	if r.Header.Get("X-Radiko-AuthToken") == "" || r.URL.Query().Get("l") != "270" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=48000\n/live/medialist.m3u8?s0=%d\n", now.Unix())
}

// liveMedialist はライブの medialist。返したら時計を liveTick 進める。
func (f *fakeRadiko) liveMedialist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hook := f.onLiveMedia
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	s0, err := strconv.ParseInt(r.URL.Query().Get("s0"), 10, 64)
	if err != nil {
		http.Error(w, "bad s0", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	now := f.clock.Now()
	f.clock.Set(now.Add(f.liveTick))
	out := f.liveOutage(now)
	f.mu.Unlock()
	if out {
		http.Error(w, "outage", http.StatusInternalServerError)
		return
	}
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:5\n")
	from := time.Unix(s0, 0).In(domain.JST).Add(-270 * time.Second).Truncate(5 * time.Second)
	for s := from; !s.Add(5 * time.Second).After(now.Add(-60 * time.Second)); s = s.Add(5 * time.Second) {
		fmt.Fprintf(&b, "#EXTINF:5.000,\n#EXT-X-PROGRAM-DATE-TIME:%s\n/seg/%s.aac\n",
			s.Format("2006-01-02T15:04:05.000-07:00"), s.Format(radikoTime))
	}
	w.Write(b.Bytes())
}

func (f *fakeRadiko) playlist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.playlists[q.Get("ft")]++
	fail := f.failSeek[q.Get("seek")]
	f.mu.Unlock()
	if fail {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=48000\n/tf/medialist.m3u8?%s\n", r.URL.RawQuery)
}

func (f *fakeRadiko) medialist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	seek, err1 := time.ParseInLocation(radikoTime, q.Get("seek"), domain.JST)
	end, err2 := time.ParseInLocation(radikoTime, q.Get("end_at"), domain.JST)
	if err1 != nil || err2 != nil {
		http.Error(w, "bad time", http.StatusBadRequest)
		return
	}
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:5\n")
	for s := seek; s.Before(end); s = s.Add(5 * time.Second) {
		fmt.Fprintf(&b, "#EXTINF:5.000,\n#EXT-X-PROGRAM-DATE-TIME:%s\n/seg/%s.aac\n",
			s.Format("2006-01-02T15:04:05.000-07:00"), s.Format(radikoTime))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	w.Write(b.Bytes())
}

func (f *fakeRadiko) seg(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	data, hook := f.segment, f.onSegment
	f.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	if data == nil {
		data = []byte("[" + filepath.Base(r.URL.Path) + "]")
	}
	w.Write(data)
}

// fakeMedia は ffmpeg の代わり。Remux は入力をつないで書き、Duration は中身の "[" の数 × 5 秒を返す。
type fakeMedia struct {
	mu   sync.Mutex
	tags []media.Tags
	// covers は Remux に渡された画像の中身（渡されなければ nil）。
	covers [][]byte
	// chapters は RemuxWithChapters に渡されたチャプター（Remux で格納したときは nil）。tags と同じ並び。
	chapters [][]media.Chapter
	// offset は Duration に足す量（検証の失敗を起こす）。
	offset time.Duration
	// onDuration があれば Duration の中で呼ぶ（保存の直前に割り込む）。
	onDuration func()
	// remuxErr があれば Remux はそれを返す（保存の段階の失敗を起こす）。durationErr があれば Duration はそれを返す
	// （検証の段階で長さを測れない失敗を起こす）。
	remuxErr, durationErr error
}

// fail は Remux と Duration が返すエラーを差し替える（nil なら成功に戻す）。
func (m *fakeMedia) fail(remux, duration error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remuxErr, m.durationErr = remux, duration
}

func (m *fakeMedia) Remux(ctx context.Context, inputs []string, out string, tags media.Tags, cover string) error {
	return m.RemuxWithChapters(ctx, inputs, out, tags, cover, nil)
}

// RemuxWithChapters は Remux と同じく書き、chapters を記録する（ChapterRemuxer）。
func (m *fakeMedia) RemuxWithChapters(ctx context.Context, inputs []string, out string, tags media.Tags, cover string, chapters []media.Chapter) error {
	m.mu.Lock()
	rerr := m.remuxErr
	m.mu.Unlock()
	if rerr != nil {
		return rerr
	}
	var b bytes.Buffer
	for _, in := range inputs {
		d, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		b.Write(d)
	}
	m.mu.Lock()
	m.tags = append(m.tags, tags)
	var cdata []byte
	if cover != "" {
		cdata, _ = os.ReadFile(cover)
	}
	m.covers = append(m.covers, cdata)
	m.chapters = append(m.chapters, chapters)
	m.mu.Unlock()
	return os.WriteFile(out, b.Bytes(), 0o644)
}

func (m *fakeMedia) Duration(ctx context.Context, file string) (time.Duration, error) {
	d, err := os.ReadFile(file)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	hook, offset, derr := m.onDuration, m.offset, m.durationErr
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	if derr != nil {
		return 0, derr
	}
	return time.Duration(bytes.Count(d, []byte("[")))*5*time.Second + offset, nil
}

// clock は差し替えられる時計。
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// env はテストの環境（偽のサーバー、一時ディレクトリの DB・作業ディレクトリ・保存先）。
type env struct {
	t     *testing.T
	ctx   context.Context
	fake  *fakeRadiko
	clock *clock
	store *store.Store
	app   *App
	media *fakeMedia
	// stopExpander は newEnv が起こした展開役を止め、止まるまで待つ（Serve を試すテストが、Serve の展開役と
	// 2 本にならないように先に呼ぶ）。2 回目からは何もしない。
	stopExpander func()
}

func newEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFakeRadiko(t)
	c := &clock{t: now}
	f.clock = c
	client := f.client(c.Now)
	m := &fakeMedia{}
	a := &App{
		Store:      st,
		Guide:      RadikoGuide{Client: client},
		Fetcher:    client,
		Media:      m,
		Preset:     library.Jellyfin{},
		WorkDir:    filepath.Join(dir, "work"),
		Library:    filepath.Join(dir, "library"),
		Membership: domain.MembershipFree,
		Now:        c.Now,
		Log:        slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	// Startup・RunDue は展開役に頼むので、Serve を使わないテストのために展開役を起こしておく。
	ectx, cancel := context.WithCancel(ctx)
	expanderDone := make(chan struct{})
	go func() {
		defer close(expanderDone)
		if err := a.RunExpander(ectx); err != nil {
			t.Error(err)
		}
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		<-expanderDone
	})
	t.Cleanup(stop)
	return &env{t: t, ctx: ctx, fake: f, clock: c, store: st, app: a, media: m, stopExpander: stop}
}

// testWriter はログを t.Log に流す。
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (e *env) putShows(shows ...domain.Show) {
	e.t.Helper()
	if err := e.store.PutShows(e.ctx, shows); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) jobs() []domain.Job {
	e.t.Helper()
	jobs, err := e.store.ListJobs(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return jobs
}

func (e *env) job(show string) domain.Job {
	e.t.Helper()
	for _, j := range e.jobs() {
		if j.ShowName == show {
			return j
		}
	}
	e.t.Fatalf("番組 %s のジョブが無い: %+v", show, e.jobs())
	return domain.Job{}
}

func (e *env) attempts(id int64) []domain.Attempt {
	e.t.Helper()
	as, err := e.store.Attempts(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return as
}

// runDue は RunDue を 1 回呼び、実行したかを返す。
func (e *env) runDue() bool {
	e.t.Helper()
	ran, err := e.app.RunDue(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return ran
}
