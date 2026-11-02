package radiko

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// chunkSeconds は 1 チャンクの通常の長さ（l の上限でもある）。
	chunkSeconds = 300
	// minChunkSeconds は l の下限。
	minChunkSeconds = 5
	// seamTolerance は、チャンクの継ぎ目で許すずれ。タイムフリーでは超えたらエラーにする。ライブでは重複と欠落の判定に使い、エラーにしない。
	seamTolerance = 500 * time.Millisecond
)

// FetchResult は FetchTimefree の結果。
type FetchResult struct {
	// DurationSeconds は #EXTINF の合計秒数。
	DurationSeconds float64
	// Segments は書き出したセグメントの数。
	Segments int
	// PlaylistCreateURL は最後に使った候補 URL（playlist_create_url）。チャンクを書くたびに、そのチャンクを取った候補で上書きする。
	PlaylistCreateURL string
}

type chunk struct {
	Seek time.Time // S
	End  time.Time // E = min(S+L, 放送回の終了時刻)
	L    int       // 秒
}

// planChunks は、放送回の start〜end を 5 分ずつのチャンクに分ける。
// 通常は L=300。最後のチャンクは残りの秒数を 5 の倍数に切り上げる（最小 5）。
// E は min(S+L, end) にする。end のままだと medialist に ENDLIST が付かず、サーバがライブとして等速で流し続けるため。
func planChunks(start, end time.Time) []chunk {
	var out []chunk
	for s := start; s.Before(end); {
		remain := int(math.Ceil(end.Sub(s).Seconds()))
		l := chunkSeconds
		if remain < chunkSeconds {
			l = (remain + minChunkSeconds - 1) / minChunkSeconds * minChunkSeconds
			if l < minChunkSeconds {
				l = minChunkSeconds
			}
		}
		e := s.Add(time.Duration(l) * time.Second)
		if e.After(end) {
			e = end
		}
		out = append(out, chunk{Seek: s, End: e, L: l})
		s = e
	}
	return out
}

type xmlStreamURL struct {
	Timefree          string `xml:"timefree,attr"`
	Areafree          string `xml:"areafree,attr"`
	PlaylistCreateURL string `xml:"playlist_create_url"`
}

type xmlStreams struct {
	URLs []xmlStreamURL `xml:"url"`
}

// parseCandidates は、ストリーム情報の XML から timefree=1 かつ areafree が areaFree（"0" か "1"）の playlist_create_url を
// 並び順のまま取り出す。エリアフリーのトークンでは areafree=1 の url を使う（login.go）。
func parseCandidates(data []byte, areaFree string) ([]string, error) {
	var doc xmlStreams
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: parse stream xml: %v", ErrTemporary, err)
	}
	var out []string
	for _, u := range doc.URLs {
		if u.Timefree == "1" && u.Areafree == areaFree {
			if v := strings.TrimSpace(u.PlaylistCreateURL); v != "" {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

type segment struct {
	URL      string
	Duration float64 // #EXTINF（秒）
	PDT      time.Time
	HasPDT   bool
}

func (s segment) end() time.Time {
	return s.PDT.Add(time.Duration(s.Duration * float64(time.Second)))
}

// resolveURL は ref を base からの相対として解決する。
func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// parseMaster はマスタプレイリストから medialist の URL を取り出す（#EXT-X-STREAM-INF の次の行）。
func parseMaster(body []byte, baseURL string) (string, error) {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF") {
			continue
		}
		for _, next := range lines[i+1:] {
			next = strings.TrimSpace(next)
			if next == "" || strings.HasPrefix(next, "#") {
				continue
			}
			return resolveURL(baseURL, next)
		}
	}
	return "", errors.New("no media playlist in master playlist")
}

var pdtLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"}

func parsePDT(s string) (time.Time, error) {
	var lastErr error
	for _, l := range pdtLayouts {
		t, err := time.Parse(l, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// parseMedialist は medialist のセグメントを読む。各セグメントの #EXTINF と #EXT-X-PROGRAM-DATE-TIME を拾う。
func parseMedialist(body []byte, baseURL string) ([]segment, error) {
	var (
		segs   []segment
		cur    segment
		hasDur bool
	)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			d, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("bad EXTINF %q: %w", line, err)
			}
			cur.Duration = d
			hasDur = true
		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			t, err := parsePDT(strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"))
			if err != nil {
				return nil, fmt.Errorf("bad PROGRAM-DATE-TIME %q: %w", line, err)
			}
			cur.PDT = t
			cur.HasPDT = true
		case strings.HasPrefix(line, "#"):
		default:
			if !hasDur {
				return nil, fmt.Errorf("segment %q without EXTINF", line)
			}
			u, err := resolveURL(baseURL, line)
			if err != nil {
				return nil, err
			}
			cur.URL = u
			segs = append(segs, cur)
			cur = segment{}
			hasDur = false
		}
	}
	return segs, nil
}

func newLsid() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// timefreeCandidates は、ストリーム情報から、トークン tok で使う playlist_create_url の候補を並び順で返す。
func (c *Client) timefreeCandidates(ctx context.Context, tok Token, stationID string) ([]string, error) {
	u := fmt.Sprintf("%s/v3/station/stream/pc_html5/%s.xml", strings.TrimRight(c.ProgramHost, "/"), url.PathEscape(stationID))
	data, err := c.getXML(ctx, u)
	if err != nil {
		return nil, err
	}
	cands, err := parseCandidates(data, areaFreeAttr(tok))
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: station %s has no timefree stream", ErrNotAvailable, stationID)
	}
	return cands, nil
}

type chunkData struct {
	body  []byte
	segs  []segment
	parts [][]byte // segs と同じ並びの、セグメントごとの中身
}

// fetchChunk は 1 チャンク分（マスタ → medialist → 全セグメント）を取って、メモリに溜めて返す。
// 失敗してもまだ w には何も書いていないので、認証や候補を変えてやり直せる。
func (c *Client) fetchChunk(ctx context.Context, token Token, candidate, stationID string, ft time.Time, ck chunk, lsid string) (*chunkData, error) {
	pu, err := url.Parse(candidate)
	if err != nil {
		return nil, fmt.Errorf("%w: bad playlist_create_url %q: %v", ErrTemporary, candidate, err)
	}
	q := pu.Query()
	q.Set("station_id", stationID)
	q.Set("start_at", ft.In(JST).Format(timeLayout))
	q.Set("ft", ft.In(JST).Format(timeLayout))
	q.Set("end_at", ck.End.In(JST).Format(timeLayout))
	q.Set("to", ck.End.In(JST).Format(timeLayout))
	q.Set("seek", ck.Seek.In(JST).Format(timeLayout))
	q.Set("l", strconv.Itoa(ck.L))
	q.Set("lsid", lsid)
	q.Set("type", playlistType(token))
	pu.RawQuery = q.Encode()
	playlistURL := pu.String()

	// マスタには X-Radiko-AuthToken だけが必須。medialist とセグメントはヘッダなしで取れる。
	resp, err := c.get(ctx, playlistURL, map[string]string{"X-Radiko-AuthToken": token.Value})
	if err != nil {
		return nil, err
	}
	switch {
	case resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden:
		return nil, errAuthExpired
	case resp.status == http.StatusBadRequest || resp.status == http.StatusNotFound || resp.status == http.StatusGone:
		return nil, fmt.Errorf("%w: playlist: status %d", ErrTemporary, resp.status)
	case resp.status != http.StatusOK:
		return nil, statusError("playlist", resp.status)
	}
	mediaURL, err := parseMaster(resp.body, playlistURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporary, err)
	}

	resp, err = c.get(ctx, mediaURL, nil)
	if err != nil {
		return nil, err
	}
	if resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden {
		return nil, errAuthExpired
	}
	if resp.status != http.StatusOK {
		return nil, statusError("medialist", resp.status)
	}
	segs, err := parseMedialist(resp.body, mediaURL)
	if err != nil {
		return nil, fmt.Errorf("%w: medialist: %v", ErrTemporary, err)
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("%w: medialist has no segments", ErrTemporary)
	}

	var buf bytes.Buffer
	var parts [][]byte
	for _, s := range segs {
		resp, err := c.get(ctx, s.URL, nil)
		if err != nil {
			return nil, err
		}
		if resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden {
			return nil, errAuthExpired
		}
		if resp.status != http.StatusOK {
			return nil, statusError("segment "+s.URL, resp.status)
		}
		buf.Write(resp.body)
		parts = append(parts, resp.body)
	}
	return &chunkData{body: buf.Bytes(), segs: segs, parts: parts}, nil
}

// checkChunk は、取ったチャンクが前のチャンクと欠けも重複もなくつながるかを確かめる。
// needPDT はチャンクが複数あるとき（継ぎ目があるとき）に true にする。
func checkChunk(cd *chunkData, ck chunk, needPDT bool, prevEnd time.Time, havePrev bool, seen map[string]bool) error {
	first, last := cd.segs[0], cd.segs[len(cd.segs)-1]
	if needPDT && (!first.HasPDT || !last.HasPDT) {
		return fmt.Errorf("%w: segment without PROGRAM-DATE-TIME at seek %s", ErrTemporary, ck.Seek.Format(timeLayout))
	}
	if havePrev {
		gap := first.PDT.Sub(prevEnd)
		if gap < -seamTolerance || gap > seamTolerance {
			return fmt.Errorf("%w: seam mismatch %v at seek %s", ErrTemporary, gap, ck.Seek.Format(timeLayout))
		}
	}
	for _, s := range cd.segs {
		if seen[s.URL] {
			return fmt.Errorf("%w: segment %s overlaps the previous chunk", ErrTemporary, s.URL)
		}
	}
	return nil
}

const (
	// bridgeBefore は、継ぎ目の欠けを埋める小さなチャンクの seek を、継ぎ目 B より前にずらす量。
	bridgeBefore = 5 * time.Second
	// bridgeLength は、その小さなチャンクの l（秒）。to=end_at=seek+l。
	bridgeLength = 10
)

// bridgeGap は、チャンク cd の頭が前のチャンクの終わり prevEnd より seamTolerance を超えて後（欠け）かを返す。
func bridgeGap(cd *chunkData, prevEnd time.Time, havePrev bool) bool {
	first := cd.segs[0]
	return havePrev && first.HasPDT && first.PDT.Sub(prevEnd) > seamTolerance
}

// bridgeChunk は、継ぎ目に欠けが出たチャンク cd の前に、同じ候補で取った小さなチャンクのセグメントを差し込んだチャンクを作る。
// 差し込めない（取れない・選べる切れ目が無い・差し込んでも継ぎ目の検査に通らない）ときは nil を返す。
//
// #36: 候補によっては、揃っていない分の継ぎ目 B の直前に始まる切れ目を、to=B のチャンクにも seek=B のチャンクにも入れない。
// seek=B-5s・l=10 の小さなチャンクにはその切れ目が載るので、終わりが prevEnd より後で頭が cd の頭より前のもの（PDT で選ぶ）を差し込む。
func (c *Client) bridgeChunk(ctx context.Context, token Token, candidate, stationID string, ft time.Time, ck chunk, lsid string, cd *chunkData, prevEnd time.Time) *chunkData {
	small := chunk{Seek: ck.Seek.Add(-bridgeBefore), End: ck.Seek.Add(-bridgeBefore + bridgeLength*time.Second), L: bridgeLength}
	sd, err := c.fetchChunk(ctx, token, candidate, stationID, ft, small, lsid)
	if err != nil {
		return nil
	}
	firstPDT := cd.segs[0].PDT
	urls := map[string]bool{}
	for _, s := range cd.segs {
		urls[s.URL] = true
	}
	out := &chunkData{}
	for i, s := range sd.segs {
		if !s.HasPDT || !s.end().After(prevEnd) || !s.PDT.Before(firstPDT) {
			continue
		}
		if urls[s.URL] {
			return nil
		}
		out.segs = append(out.segs, s)
		out.parts = append(out.parts, sd.parts[i])
		out.body = append(out.body, sd.parts[i]...)
	}
	if len(out.segs) == 0 {
		return nil
	}
	// 差し込んだ最後の切れ目と元のチャンクの頭も、継ぎ目の検査にかける。
	if gap := firstPDT.Sub(out.segs[len(out.segs)-1].end()); gap < -seamTolerance || gap > seamTolerance {
		return nil
	}
	out.segs = append(out.segs, cd.segs...)
	out.parts = append(out.parts, cd.parts...)
	out.body = append(out.body, cd.body...)
	return out
}

// FetchTimefree は、放送局 stationID の start〜end（1 放送回分）のタイムフリーの音声を取り、ADTS のまま w に書く。
//
// token は呼び出し側が 1 回の試行で使い回すために渡す。チャンクの途中でトークンが失効したら、
// 認証をやり直してそのチャンクから続ける（1 回の取得で 1 回まで。radiko のログインのセッションを付けたトークンなら、
// radiko のログインをし直す）。候補 URL と再生リストの type は token で決める（エリアフリーなら areafree=1 の url、セッションを付けたなら type=c）。
// 認証をやり直してトークンのエリアフリーが変わっても、候補は取り直さない。
// 候補 URL はチャンクごとに 1 つ目から並び順に試し、取れなかったり継ぎ目の検証に落ちたりしたら、そのチャンクを次の候補で取り直す
// （1 回の取得の中で候補が混ざることがある。継ぎ目は PDT の差で確かめる）。
// 継ぎ目に欠けが出たときは、次の候補に進む前に、同じ候補で seek=B-5s・l=10 の小さなチャンクを 1 回取り、
// 欠けた分の切れ目を差し込んで継ぎ目の検証をやり直す（bridgeChunk。差し込んだ切れ目は元のチャンクと一緒に検証を通ってから書く）。
// 終了時刻が未来ならネットワークに出る前に ErrNotYetAvailable を返す。
//
// onChunk が nil でなければ、チャンクを w に書き終えるたびに、それまでに書いた #EXTINF の合計秒数を渡して呼ぶ（録音中のジョブの
// 進捗を進めるため）。チャンクを別の候補で取り直しても、書く前に呼ばないので、渡す値は減らない。
//
// 失敗したとき、w にはそれまでに完了したチャンクが書かれていることがある。
// その途中までの中身は捨てること（再試行は最初から取り直す）。
func (c *Client) FetchTimefree(ctx context.Context, token Token, stationID string, start, end time.Time, w io.Writer, onChunk func(seconds float64)) (FetchResult, error) {
	var res FetchResult
	start = start.In(JST).Truncate(time.Second)
	end = end.In(JST).Truncate(time.Second)
	if !end.After(start) {
		return res, fmt.Errorf("radiko: invalid range %s - %s", start, end) // 呼び出し側の誤り
	}
	if end.After(c.now()) {
		return res, fmt.Errorf("%w: program ends at %s", ErrNotYetAvailable, end.Format(time.RFC3339))
	}
	cands, err := c.timefreeCandidates(ctx, token, stationID)
	if err != nil {
		return res, err
	}
	lsid, err := newLsid()
	if err != nil {
		return res, err
	}

	var (
		reauthed bool
		seen     = map[string]bool{}
		prevEnd  time.Time
		havePrev bool
	)
	chunks := planChunks(start, end)
	for _, ck := range chunks {
		var cd *chunkData
		idx := 0 // チャンクごとに 1 つ目の候補から試す
		for {
			cd, err = c.fetchChunk(ctx, token, cands[idx], stationID, start, ck, lsid)
			if err == nil {
				// 継ぎ目の検証に落ちたときも、取れなかったときと同じく次の候補で取り直す。
				// 候補によっては、seek の直前から始まるセグメントをどちらのチャンクにも入れないことがあるため。
				needPDT := len(chunks) > 1
				err = checkChunk(cd, ck, needPDT, prevEnd, havePrev, seen)
				if err != nil && needPDT && bridgeGap(cd, prevEnd, havePrev) {
					if merged := c.bridgeChunk(ctx, token, cands[idx], stationID, start, ck, lsid, cd, prevEnd); merged != nil {
						if checkChunk(merged, ck, needPDT, prevEnd, havePrev, seen) == nil {
							cd, err = merged, nil
						}
					}
				}
				if err == nil {
					break
				}
			}
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if errors.Is(err, errAuthExpired) {
				if reauthed {
					return res, fmt.Errorf("%w: token rejected again after re-authentication", ErrTemporary)
				}
				reauthed = true
				if token, err = c.reauthenticate(ctx, token); err != nil {
					return res, err
				}
				continue
			}
			if idx+1 < len(cands) {
				idx++
				continue
			}
			return res, err
		}

		last := cd.segs[len(cd.segs)-1]
		for _, s := range cd.segs {
			seen[s.URL] = true
			res.DurationSeconds += s.Duration
		}
		if _, err := w.Write(cd.body); err != nil {
			return res, fmt.Errorf("radiko: write audio: %w", err)
		}
		res.Segments += len(cd.segs)
		res.PlaylistCreateURL = cands[idx]
		prevEnd, havePrev = last.end(), last.HasPDT
		if onChunk != nil {
			onChunk(res.DurationSeconds)
		}
	}
	return res, nil
}
