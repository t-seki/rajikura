package radiko

// chapters.go は、radiko が放送の後に作るタイムフリーのチャプター（用語集の「チャプター」）を取る（ADR 0027）。
//
// 取り先は公開の文書の無い API（api.annex-cf.radiko.jp）で、予告なく変わりうる。認証は要らない（トークンを付けない）。
// 応答の形は 2026-10-26 時点に調べたもののとおり。
//
//  1. /v1/programs/list?broadcastDate=<YYYYMMDD>&stationId=<局>&limit=100 で、局・放送日の放送回の eventId を引き、
//     startAt（UTC）が放送回の開始時刻と一致するものを使う。分割番組のパートは eventId に枝番（-1 など）を持ち、
//     v3 の番組表の prog id では引けない。limit を付けないと、新しい側の 20 件しか返らなかった（2026-10-26、TBS で確かめた）。
//     broadcastDate は暦日ではなく、5 時で区切る放送日（v3 の番組表と同じ）で引く。2026-10-26 に TBS の broadcastDate=20261025 と
//     20261026（limit=100）を curl で引いて確かめた: 10-26 の 0〜4 時台（JST）に始まる放送回（14120505-1〜-3・14077807 など。
//     startAt は 2026-10-25T15:00Z〜19:00Z）は 20261025 の一覧にだけあり（各要素の broadcastDate も 20261025）、20261026 の一覧は
//     2026-10-25T20:00Z（10-26 の 5 時）から始まった。なので呼び出し側は、放送回の放送日（domain の Airing.Day）を渡す。
//  2. /v1/programs/<eventId>/chapters で、その放送回のチャプターを取る。できる前と、局に無いときは 404。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultChapterHost は、チャプターの API のホスト。
const DefaultChapterHost = "https://api.annex-cf.radiko.jp"

// chapterListLimit は、放送回の一覧（/v1/programs/list）で 1 回に取る数。放送日の全部の放送回が入る数にする。
const chapterListLimit = 100

var (
	// ErrNoEvent は、局・放送日の放送回の一覧に、開始時刻の一致する放送回が無いこと。
	ErrNoEvent = errors.New("radiko: no event for the airing")
	// ErrNoChapters は、チャプターの API が 404 を返したこと（まだできていない、または局に無い。応答では見分けられない）。
	ErrNoChapters = errors.New("radiko: chapters not found")
)

// Chapter は、radiko のチャプター 1 つ（応答の値のまま）。
type Chapter struct {
	// From と To は、その放送回の開始からの時刻（応答の fromSec・toSec）。分割番組では、パートごとに 0 から数える。
	From, To time.Duration
	// Type は種類（応答の chapterType。PROGRAM_CHAPTER_TYPE_TALK など）。
	Type string
	// Title と TitleLong は題（応答の title・titleLong）。
	Title, TitleLong string
	// IsMusic は曲の場面か。MusicTitle と MusicArtist は、その曲名とアーティスト。
	IsMusic                 bool
	MusicTitle, MusicArtist string
}

type chapterListJSON struct {
	Programs []struct {
		EventID string `json:"eventId"`
		StartAt string `json:"startAt"`
	} `json:"programs"`
}

type chaptersJSON struct {
	Chapters []struct {
		FromSec     float64 `json:"fromSec"`
		ToSec       float64 `json:"toSec"`
		ChapterType string  `json:"chapterType"`
		Title       string  `json:"title"`
		TitleLong   string  `json:"titleLong"`
		IsMusic     bool    `json:"isMusic"`
		MusicTitle  string  `json:"musicTitle"`
		MusicArtist string  `json:"musicArtist"`
	} `json:"chapters"`
}

func (c *Client) chapterHost() string {
	if c.ChapterHost != "" {
		return strings.TrimRight(c.ChapterHost, "/")
	}
	return DefaultChapterHost
}

// Chapters は、局 stationID の放送日 day（5 時で区切る放送日。年月日だけを使う）の、start に始まる放送回のチャプターを、応答の並びのまま返す。
// 要求は 2 回（放送回の一覧とチャプター）で、それぞれに RequestTimeout をかける。
//
//   - 一覧に開始時刻の一致する放送回が無い: ErrNoEvent
//   - チャプターの API が 404: ErrNoChapters
//   - 通信の失敗・200 と 404 のほかのステータス・読めない応答: ErrTemporary を包んだエラー
//
// チャプターが空の配列なら、空のスライスとエラー無しを返す。
func (c *Client) Chapters(ctx context.Context, stationID string, day, start time.Time) ([]Chapter, error) {
	q := url.Values{}
	q.Set("broadcastDate", day.In(JST).Format("20060102"))
	q.Set("stationId", stationID)
	q.Set("limit", fmt.Sprint(chapterListLimit))
	listURL := c.chapterHost() + "/v1/programs/list?" + q.Encode()
	resp, err := c.get(ctx, listURL, nil)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, statusError("GET "+listURL, resp.status)
	}
	var list chapterListJSON
	if err := json.Unmarshal(resp.body, &list); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrTemporary, listURL, err)
	}
	event := ""
	for _, p := range list.Programs {
		at, err := time.Parse(time.RFC3339, p.StartAt)
		if err != nil || p.EventID == "" {
			continue
		}
		if at.Equal(start) {
			event = p.EventID
			break
		}
	}
	if event == "" {
		return nil, fmt.Errorf("%w: %s %s", ErrNoEvent, stationID, start.In(JST).Format(timeLayout))
	}

	chURL := c.chapterHost() + "/v1/programs/" + url.PathEscape(event) + "/chapters"
	resp, err = c.get(ctx, chURL, nil)
	if err != nil {
		return nil, err
	}
	switch resp.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrNoChapters, event)
	default:
		return nil, statusError("GET "+chURL, resp.status)
	}
	var doc chaptersJSON
	if err := json.Unmarshal(resp.body, &doc); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrTemporary, chURL, err)
	}
	out := make([]Chapter, 0, len(doc.Chapters))
	for _, ch := range doc.Chapters {
		out = append(out, Chapter{
			From:        seconds(ch.FromSec),
			To:          seconds(ch.ToSec),
			Type:        ch.ChapterType,
			Title:       ch.Title,
			TitleLong:   ch.TitleLong,
			IsMusic:     ch.IsMusic,
			MusicTitle:  ch.MusicTitle,
			MusicArtist: ch.MusicArtist,
		})
	}
	return out, nil
}

// seconds は秒数（小数あり）を time.Duration にする。
func seconds(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}
