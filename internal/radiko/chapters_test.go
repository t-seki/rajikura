package radiko

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// chapterListBody は /v1/programs/list の応答（2026-10-26 時点に調べた形。値は短くした）。
// 新しい側から並び、分割番組のパートは eventId に枝番を持つ。
const chapterListBody = `{"programs":[
 {"id":"14120505-2","eventId":"14120505-2","title":"深夜の番組","broadcastDate":"20261025","startAt":"2026-10-25T17:00:00Z","endAt":"2026-10-25T18:00:00Z"},
 {"id":"14120505-1","eventId":"14120505-1","title":"深夜の番組","broadcastDate":"20261025","startAt":"2026-10-25T16:00:00Z","endAt":"2026-10-25T17:00:00Z"},
 {"id":"14077784","eventId":"14077784","title":"朝の番組","broadcastDate":"20261025","startAt":"2026-10-24T23:00:00Z","endAt":"2026-10-25T01:00:00Z"}
]}`

// chaptersBody は /v1/programs/<eventId>/chapters の応答（同じく調査のコメントの形。index は飛び番、最初の index は省かれる）。
const chaptersBody = `{"chapters":[
 {"id":"A","fromSec":0,"toSec":336.7,"chapterType":"PROGRAM_CHAPTER_TYPE_GUEST","title":"ゲスト","titleLong":"ゲスト : 誰か - 話","performers":[],"cornerName":"","isMusic":false,"musicTitle":"","musicArtist":"","musicImageUrl":"","isHighlighted":true,"summaryLong":"…"},
 {"id":"B","fromSec":336.7,"toSec":600,"chapterType":"PROGRAM_CHAPTER_TYPE_MUSIC","title":"曲","titleLong":"曲 : 何か","performers":[],"cornerName":"","isMusic":true,"musicTitle":"歌","musicArtist":"歌手","musicImageUrl":"https://example.invalid/a.jpg","isHighlighted":false,"summaryLong":"","index":2}
],"stationId":"TBS","rSeasonId":"10013625","programId":"14120505-2","version":"20261026094311"}`

// chapterServer は、チャプターの API の偽のサーバー。chapters は eventId → 応答の本文（無ければ 404）。
func chapterServer(t *testing.T, chapters map[string]string) (*Client, *[]string) {
	t.Helper()
	var reqs []string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/programs/list", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.URL.RequestURI())
		if r.Header.Get("X-Radiko-AuthToken") != "" {
			t.Errorf("list: auth token sent")
		}
		w.Write([]byte(chapterListBody))
	})
	mux.HandleFunc("/v1/programs/{event}/chapters", func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.URL.RequestURI())
		body, ok := chapters[r.PathValue("event")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":5,"message":"ProgramChaptersNotFoundErr"}`))
			return
		}
		w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient()
	c.ChapterHost = srv.URL
	return c, &reqs
}

var chapterDay = time.Date(2026, 10, 25, 0, 0, 0, 0, JST)

// 放送日と局で一覧を引き、開始時刻の一致する放送回（分割番組のパートは枝番付き）のチャプターを取る。
func TestChapters(t *testing.T) {
	c, reqs := chapterServer(t, map[string]string{"14120505-2": chaptersBody})
	start := time.Date(2026, 10, 26, 2, 0, 0, 0, JST) // 2026-10-25T17:00:00Z
	got, err := c.Chapters(context.Background(), "TBS", chapterDay, start)
	if err != nil {
		t.Fatal(err)
	}
	want := []Chapter{
		{From: 0, To: 336700 * time.Millisecond, Type: "PROGRAM_CHAPTER_TYPE_GUEST", Title: "ゲスト", TitleLong: "ゲスト : 誰か - 話"},
		{From: 336700 * time.Millisecond, To: 600 * time.Second, Type: "PROGRAM_CHAPTER_TYPE_MUSIC", Title: "曲", TitleLong: "曲 : 何か",
			IsMusic: true, MusicTitle: "歌", MusicArtist: "歌手"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chapters: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chapter %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	wantReqs := []string{"/v1/programs/list?broadcastDate=20261025&limit=100&stationId=TBS", "/v1/programs/14120505-2/chapters"}
	if len(*reqs) != 2 || (*reqs)[0] != wantReqs[0] || (*reqs)[1] != wantReqs[1] {
		t.Errorf("requests = %q, want %q", *reqs, wantReqs)
	}
}

// 取れないときの理由を見分けられる（404・一覧に無い・空・読めない応答）。
func TestChaptersUnavailable(t *testing.T) {
	ctx := context.Background()
	c, _ := chapterServer(t, map[string]string{
		"14077784":   `{"chapters":[],"stationId":"TBS"}`,
		"14120505-1": `not json`,
	})
	if _, err := c.Chapters(ctx, "TBS", chapterDay, time.Date(2026, 10, 26, 2, 0, 0, 0, JST)); !errors.Is(err, ErrNoChapters) {
		t.Errorf("404: err = %v, want ErrNoChapters", err)
	}
	if _, err := c.Chapters(ctx, "TBS", chapterDay, time.Date(2026, 10, 25, 12, 0, 0, 0, JST)); !errors.Is(err, ErrNoEvent) {
		t.Errorf("no event: err = %v, want ErrNoEvent", err)
	}
	got, err := c.Chapters(ctx, "TBS", chapterDay, time.Date(2026, 10, 25, 8, 0, 0, 0, JST))
	if err != nil || len(got) != 0 {
		t.Errorf("empty: got %v, err = %v", got, err)
	}
	if _, err := c.Chapters(ctx, "TBS", chapterDay, time.Date(2026, 10, 26, 1, 0, 0, 0, JST)); !errors.Is(err, ErrTemporary) {
		t.Errorf("bad json: err = %v, want ErrTemporary", err)
	}
	// 通信の失敗も ErrTemporary。
	dead := NewClient()
	dead.ChapterHost = "http://127.0.0.1:1"
	if _, err := dead.Chapters(ctx, "TBS", chapterDay, time.Date(2026, 10, 25, 8, 0, 0, 0, JST)); !errors.Is(err, ErrTemporary) {
		t.Errorf("network: err = %v, want ErrTemporary", err)
	}
}
