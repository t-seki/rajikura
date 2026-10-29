package radiko

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeRadiko は radiko の偽のサーバー。実際にはつながない。
type fakeRadiko struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	authCount   int
	validToken  string
	playlistReq int // マスタの要求の回数（候補ごとではなく全体）
	badHits     int // 壊れた候補への要求の回数

	// 挙動の切り替え。
	keyOffset, keyLength int
	areaBody             string
	badFirstCandidate    bool // 1 つ目の候補が 500 を返す
	expireAtPlaylistReq  int  // この回数目のマスタの要求の前にトークンを失効させる（0 なら失効させない）
	gapFromSeek          time.Time
	gap                  time.Duration // gapFromSeek 以降のチャンクの PROGRAM-DATE-TIME をずらす量
	gapFirstOnly         bool          // gap を 1 つ目の候補にだけ入れる
	sameSegmentURL       bool
	gzipPrograms         bool
	wantFT               string // 空でなければ ft / start_at がこの値であることを検査する
	wantType             string // プレイリストの type（空なら b）
}

func newFake(t *testing.T) *fakeRadiko {
	t.Helper()
	f := &fakeRadiko{t: t, keyOffset: 3, keyLength: 8, areaBody: "JP13,東京都,tokyo Japan\n"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/api/auth1", f.auth1)
	mux.HandleFunc("/v2/api/auth2", f.auth2)
	mux.HandleFunc("/v3/station/stream/pc_html5/TBS.xml", f.streamXML)
	mux.HandleFunc("/bad/playlist.m3u8", f.badPlaylist)
	mux.HandleFunc("/tf/playlist.m3u8", f.playlist)
	mux.HandleFunc("/tf/medialist.m3u8", f.medialist)
	mux.HandleFunc("/seg/", f.segment)
	mux.HandleFunc("/v3/program/station/date/", f.programs)
	mux.HandleFunc("/v3/program/date/", f.areaPrograms)
	mux.HandleFunc("/v3/station/region/full.xml", f.stations)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRadiko) client() *Client {
	c := NewClient()
	c.AuthHost = f.srv.URL
	c.ProgramHost = f.srv.URL
	return c
}

func (f *fakeRadiko) auth1(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Radiko-App") != "pc_html5" || r.Header.Get("X-Radiko-Device") != "pc" {
		http.Error(w, "bad headers", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.authCount++
	tok := fmt.Sprintf("tok%d", f.authCount)
	f.mu.Unlock()
	// 実際の radiko と同じく小文字で返す。
	w.Header()["x-radiko-authtoken"] = []string{tok}
	w.Header()["x-radiko-keyoffset"] = []string{strconv.Itoa(f.keyOffset)}
	w.Header()["x-radiko-keylength"] = []string{strconv.Itoa(f.keyLength)}
}

func (f *fakeRadiko) auth2(w http.ResponseWriter, r *http.Request) {
	want, _ := partialKey(f.keyOffset, f.keyLength)
	if r.Header.Get("X-Radiko-PartialKey") != want {
		http.Error(w, "bad partial key", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	f.validToken = r.Header.Get("X-Radiko-AuthToken")
	f.mu.Unlock()
	fmt.Fprint(w, f.areaBody)
}

func (f *fakeRadiko) streamXML(w http.ResponseWriter, r *http.Request) {
	first := f.srv.URL + "/tf/playlist.m3u8"
	if f.gapFirstOnly {
		// medialist で 2 つ目の候補と見分けられるようにする。
		first += "?cand=first"
	}
	if f.badFirstCandidate {
		first = f.srv.URL + "/bad/playlist.m3u8"
	}
	fmt.Fprintf(w, `<urls>
<url areafree="0" timefree="0"><playlist_create_url>%s/live/playlist.m3u8</playlist_create_url></url>
<url areafree="1" timefree="1"><playlist_create_url>%s/areafree/playlist.m3u8</playlist_create_url></url>
<url areafree="0" timefree="1"><playlist_create_url>%s</playlist_create_url></url>
<url areafree="0" timefree="1"><playlist_create_url>%s/tf/playlist.m3u8</playlist_create_url></url>
</urls>`, f.srv.URL, f.srv.URL, first, f.srv.URL)
}

func (f *fakeRadiko) badPlaylist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.badHits++
	f.mu.Unlock()
	http.Error(w, "boom", http.StatusInternalServerError)
}

func (f *fakeRadiko) playlist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.playlistReq++
	if f.expireAtPlaylistReq != 0 && f.playlistReq == f.expireAtPlaylistReq {
		f.validToken = "expired"
	}
	ok := r.Header.Get("X-Radiko-AuthToken") == f.validToken && f.validToken != ""
	f.mu.Unlock()
	if r.Header.Get("X-Radiko-AuthToken") == "" {
		http.Error(w, "no token", http.StatusUnauthorized)
		return
	}
	if !ok {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	l, _ := strconv.Atoi(q.Get("l"))
	if l < 5 || l > 300 {
		http.Error(w, "bad l", http.StatusBadRequest)
		return
	}
	wantType := f.wantType
	if wantType == "" {
		wantType = "b"
	}
	seek, errSeek := time.ParseInLocation(timeLayout, q.Get("seek"), JST)
	_, errTo := time.ParseInLocation(timeLayout, q.Get("to"), JST)
	switch {
	case q.Get("station_id") != "TBS", q.Get("type") != wantType, q.Get("lsid") == "",
		q.Get("ft") == "" || q.Get("ft") != q.Get("start_at"),
		q.Get("to") == "" || q.Get("to") != q.Get("end_at"),
		f.wantFT != "" && q.Get("ft") != f.wantFT,
		errSeek != nil, errTo != nil:
		http.Error(w, "bad query: "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}
	if end, _ := time.ParseInLocation(timeLayout, q.Get("to"), JST); !end.After(seek) {
		http.Error(w, "to must be after seek", http.StatusBadRequest)
		return
	}
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=48000\n/tf/medialist.m3u8?%s\n", r.URL.RawQuery)
}

func (f *fakeRadiko) medialist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	seek, err1 := time.ParseInLocation(timeLayout, q.Get("seek"), JST)
	end, err2 := time.ParseInLocation(timeLayout, q.Get("end_at"), JST)
	if err1 != nil || err2 != nil {
		http.Error(w, "bad time", http.StatusBadRequest)
		return
	}
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:5\n")
	for s := seek; s.Before(end); s = s.Add(5 * time.Second) {
		pdt := s
		if !f.gapFromSeek.IsZero() && !seek.Before(f.gapFromSeek) && (!f.gapFirstOnly || q.Get("cand") == "first") {
			pdt = pdt.Add(f.gap)
		}
		name := s.Format(timeLayout)
		if f.sameSegmentURL {
			name = "same"
		}
		fmt.Fprintf(&b, "#EXTINF:5.000,\n#EXT-X-PROGRAM-DATE-TIME:%s\nhttp://%s/seg/%s.aac\n", pdt.Format("2006-01-02T15:04:05.000-07:00"), r.Host, name)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	w.Write(b.Bytes())
}

func (f *fakeRadiko) segment(w http.ResponseWriter, r *http.Request) {
	// 中身は URL の名前をそのまま返す（数バイトのダミー）。
	fmt.Fprintf(w, "[%s]", filepath.Base(r.URL.Path))
}

func (f *fakeRadiko) programs(w http.ResponseWriter, r *http.Request) {
	// 放送日 2026-10-07 の TBS だけを返す。
	if r.URL.Path != "/v3/program/station/date/20261007/TBS.xml" {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join("testdata", "programs.xml"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if f.gzipPrograms {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write(data)
		zw.Close()
		// Content-Encoding を付けずに gzip のまま返す。
		w.Write(b.Bytes())
		return
	}
	w.Write(data)
}

func (f *fakeRadiko) areaPrograms(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v3/program/date/20261013/JP13.xml" {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join("testdata", "area_programs.xml"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

func (f *fakeRadiko) stations(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(filepath.Join("testdata", "full.xml"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

// expectedAudio は、start〜end の取得で期待する出力（5 秒ごとのダミーセグメントの連結）。
func expectedAudio(start, end time.Time) string {
	var b bytes.Buffer
	for s := start; s.Before(end); s = s.Add(5 * time.Second) {
		fmt.Fprintf(&b, "[%s.aac]", s.Format(timeLayout))
	}
	return b.String()
}
