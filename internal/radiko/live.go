package radiko

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// liveLookbackSeconds は、ライブのマスタの l に渡す秒数。radiko は l の秒数だけ過去に遡ってセグメントを返す。
	// 270 まで通り、300 以上は 0 件か 400 だった（実測。1 度測っただけ）。
	liveLookbackSeconds = 270
	// liveMaxFailures は、同じ候補で失敗が続いたら次の候補に移る回数。
	liveMaxFailures = 3
	// DefaultLivePollInterval は、ライブの medialist を取り直す間隔の既定値。
	// 何秒ごとが適切かは未確認（セグメントは 5 秒ごと。決め打ちした値）。
	DefaultLivePollInterval = 3 * time.Second
	// DefaultLiveRetryInterval は、失敗してから繋ぎ直すまでの待ちの既定値（未確認。決め打ちした値）。
	DefaultLiveRetryInterval = 5 * time.Second
)

// errWrite は、書き出し先の失敗（繋ぎ直しても直らない）。
var errWrite = errors.New("radiko: write failed")

// LiveRequest は FetchLive への依頼。
type LiveRequest struct {
	// StationID は放送局の ID。
	StationID string
	// Start と End は、保存する範囲（PDT で切る。前後の余白は付けない）。
	Start, End time.Time
	// Deadline は、繋ぎ直しを続ける期限（Client.Now の時計で見る）。
	// End に届かないまま過ぎたら、残りを欠落にして返す。
	Deadline time.Time
	// Resume は、前に書いたセグメントの終わり（前の LiveResult.Next）。
	// Start より後なら、そこまでは書かずに、そこから続ける。
	Resume time.Time
}

// LiveGap は、取れなかった区間 [From, To)。
type LiveGap struct {
	From, To time.Time
}

// LiveResult は FetchLive の結果。
type LiveResult struct {
	// DurationSeconds は、今回書いたセグメントの #EXTINF の合計秒数。
	DurationSeconds float64
	// Segments は、今回書いたセグメントの数。
	Segments int
	// Next は、書いた最後のセグメントの終わり。呼び出し側が覚えておき、再開のとき LiveRequest.Resume に渡す。
	// 何も書かず Resume もなければ Start。End までを見終えたとき（欠落で終えたときも）は End 以降。
	Next time.Time
	// Gaps は、繋ぎ直しても埋められなかった区間（古い順）。今回の呼び出しで見つかった分だけ。
	Gaps []LiveGap
	// Complete は、End までの時間を見終えたこと（End に届いたとき、または Deadline で終えたとき。欠落があっても true）。
	// false なのは、End にも Deadline にも届かないうちに error を返したとき（ctx が終わったとき、ErrOutOfArea・ErrNotAvailable・
	// 書き込みの失敗・不正な依頼、その他の失敗）。Deadline で終えて 1 つも書けず ErrTemporary を返したときは true。
	Complete bool
	// PlaylistCreateURL は、最後に繋いだ候補 URL。
	PlaylistCreateURL string
}

func (c *Client) livePollInterval() time.Duration {
	if c.LivePollInterval > 0 {
		return c.LivePollInterval
	}
	return DefaultLivePollInterval
}

func (c *Client) liveRetryInterval() time.Duration {
	if c.LiveRetryInterval > 0 {
		return c.LiveRetryInterval
	}
	return DefaultLiveRetryInterval
}

// parseLiveCandidates は、ストリーム情報の XML から timefree=0 かつ areafree=0 の playlist_create_url を並び順のまま取り出す。
func parseLiveCandidates(data []byte) ([]string, error) {
	var doc xmlStreams
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: parse stream xml: %v", ErrTemporary, err)
	}
	var out []string
	for _, u := range doc.URLs {
		if u.Timefree == "0" && u.Areafree == "0" {
			if v := strings.TrimSpace(u.PlaylistCreateURL); v != "" {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

func (c *Client) liveCandidates(ctx context.Context, stationID string) ([]string, error) {
	u := fmt.Sprintf("%s/v3/station/stream/pc_html5/%s.xml", strings.TrimRight(c.ProgramHost, "/"), url.PathEscape(stationID))
	data, err := c.getXML(ctx, u)
	if err != nil {
		return nil, err
	}
	cands, err := parseLiveCandidates(data)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: station %s has no live stream", ErrNotAvailable, stationID)
	}
	return cands, nil
}

// liveRun は FetchLive 1 回分の状態。
type liveRun struct {
	c   *Client
	req LiveRequest
	w   io.Writer
	end time.Time

	token    Token
	cands    []string
	idx      int
	fails    int
	reauthed bool
	lsid     string
	mediaURL string // 空なら繋ぎ直しが要る

	next time.Time
	res  LiveResult
}

func (r *liveRun) addGap(from, to time.Time) {
	if to.After(r.end) {
		to = r.end
	}
	if !to.After(from) {
		return
	}
	if n := len(r.res.Gaps); n > 0 && !r.res.Gaps[n-1].To.Before(from) {
		r.res.Gaps[n-1].To = to
		return
	}
	r.res.Gaps = append(r.res.Gaps, LiveGap{From: from, To: to})
}

func (r *liveRun) done() bool {
	return !r.next.Add(seamTolerance).Before(r.end)
}

// connect はマスタを取って medialist の URL を得る。l=270 で遡るので、切れていた間のセグメントもここで埋まる。
func (r *liveRun) connect(ctx context.Context) error {
	c := r.c
	pu, err := url.Parse(r.cands[r.idx])
	if err != nil {
		return fmt.Errorf("%w: bad playlist_create_url %q: %v", ErrTemporary, r.cands[r.idx], err)
	}
	q := pu.Query()
	q.Set("station_id", r.req.StationID)
	q.Set("l", strconv.Itoa(liveLookbackSeconds))
	q.Set("lsid", r.lsid)
	q.Set("type", c.playlistType())
	pu.RawQuery = q.Encode()
	playlistURL := pu.String()

	resp, err := c.get(ctx, playlistURL, map[string]string{"X-Radiko-AuthToken": r.token.Value})
	if err != nil {
		return err
	}
	switch {
	case resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden:
		return errAuthExpired
	case resp.status != http.StatusOK:
		return statusError("live playlist", resp.status)
	}
	mediaURL, err := parseMaster(resp.body, playlistURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTemporary, err)
	}
	r.mediaURL = mediaURL
	r.res.PlaylistCreateURL = r.cands[r.idx]
	return nil
}

// poll は medialist を 1 回取り、まだ持っていないセグメントを PDT の順に書く。
func (r *liveRun) poll(ctx context.Context) error {
	c := r.c
	resp, err := c.get(ctx, r.mediaURL, nil)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return statusError("live medialist", resp.status)
	}
	segs, err := parseMedialist(resp.body, r.mediaURL)
	if err != nil {
		return fmt.Errorf("%w: live medialist: %v", ErrTemporary, err)
	}
	for _, s := range segs {
		if !s.HasPDT {
			return fmt.Errorf("%w: live segment without PROGRAM-DATE-TIME", ErrTemporary)
		}
		// すでに持っている（または範囲の前の）セグメントは書かない。
		if !s.end().After(r.next.Add(seamTolerance)) {
			continue
		}
		// 範囲の終わり以降から始まるセグメントが来たのに、まだ終わりに届いていないなら、残りは欠落。
		if !s.PDT.Before(r.end) {
			r.addGap(r.next, r.end)
			r.next = r.end
			return nil
		}
		resp, err := c.get(ctx, s.URL, nil)
		if err != nil {
			return err
		}
		if resp.status != http.StatusOK {
			return statusError("live segment "+s.URL, resp.status)
		}
		if s.PDT.Sub(r.next) > seamTolerance {
			r.addGap(r.next, s.PDT)
		}
		if _, err := r.w.Write(resp.body); err != nil {
			return fmt.Errorf("%w: %v", errWrite, err)
		}
		r.next = s.end()
		r.res.Segments++
		r.res.DurationSeconds += s.Duration
		if r.done() {
			return nil
		}
	}
	return nil
}

// step は、候補・繋ぎ・medialist の取得を、足りないところから 1 段進める。
func (r *liveRun) step(ctx context.Context) error {
	if r.cands == nil {
		cands, err := r.c.liveCandidates(ctx, r.req.StationID)
		if err != nil {
			return err
		}
		r.cands = cands
	}
	if r.mediaURL == "" {
		err := r.connect(ctx)
		if errors.Is(err, errAuthExpired) && !r.reauthed {
			r.reauthed = true
			tok, aerr := r.c.Authenticate(ctx)
			if aerr != nil {
				return aerr
			}
			r.token = tok
			err = r.connect(ctx)
		}
		if err != nil {
			return err
		}
	}
	return r.poll(ctx)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// FetchLive は、放送局 StationID のライブのストリームから、Start〜End の音声を ADTS のまま w に書く。
//
// セグメントは PDT で見分け、すでに書いたものは書かない。範囲の前のセグメントは書かない（前後の余白は付けない）。
// 切れたら繋ぎ直し、l=270 で遡って埋める。遡れる範囲の外で取れなかった区間は、LiveResult.Gaps に入れて返す。
// 401/403 は認証をやり直し（失敗なしに 1 回まで。成功するとまた 1 回できる）、
// 同じ候補で失敗が続いたら次の候補に移る（最後の候補の次は最初に戻る）。
// End に届くか Deadline を過ぎるまで続ける。Deadline で終えたとき、End までの残りは欠落にして error なしで返す。
// ただし 1 つも書けないまま Deadline を過ぎたときは、最後の失敗を ErrTemporary で包んだ error も返す（このときの Complete は true）。
// ctx が終わったら、ctx のエラーとそれまでの結果を返す。
// 上の Deadline の ErrTemporary を除き、error を返したときの Complete は false（ctx・恒久的な失敗・不正な依頼・その他の失敗のどれも）。
// 恒久的な失敗（ErrOutOfArea、ErrNotAvailable、w の書き込みの失敗）もそこで返す。
// 書いた分は w に残るので、Next を Resume に渡せばそこから続けられる。
//
// 繋ぎ始める時刻（開始の 1 分前）と、番組表の取り直しは呼び出し側の仕事。
// 同時に繋げる数と、長く繋いだときにセッション・トークンが切れるかは未確認。
func (c *Client) FetchLive(ctx context.Context, token Token, req LiveRequest, w io.Writer) (result LiveResult, retErr error) {
	r := &liveRun{c: c, req: req, w: w, token: token}
	start := req.Start.In(JST).Truncate(time.Second)
	r.end = req.End.In(JST).Truncate(time.Second)
	if !r.end.After(start) {
		return r.res, fmt.Errorf("radiko: invalid range %s - %s", start, r.end) // 呼び出し側の誤り
	}
	if req.Deadline.IsZero() {
		return r.res, errors.New("radiko: live deadline is required")
	}
	r.next = start
	if req.Resume.After(start) {
		r.next = req.Resume
	}
	lsid, err := newLsid()
	if err != nil {
		return r.res, err
	}
	r.lsid = lsid
	defer func() { r.res.Next = r.next; result = r.res }()

	var lastErr error
	for !r.done() && c.now().Before(req.Deadline) {
		if err := ctx.Err(); err != nil {
			return r.res, err
		}
		err := r.step(ctx)
		if err == nil {
			r.fails = 0
			r.reauthed = false
			if !r.done() {
				if err := sleepCtx(ctx, c.livePollInterval()); err != nil {
					return r.res, err
				}
			}
			continue
		}
		if ctx.Err() != nil {
			return r.res, ctx.Err()
		}
		if errors.Is(err, ErrOutOfArea) || errors.Is(err, ErrNotAvailable) || errors.Is(err, errWrite) {
			return r.res, err
		}
		lastErr = err
		r.mediaURL = ""
		r.fails++
		if r.fails >= liveMaxFailures && len(r.cands) > 1 {
			r.idx = (r.idx + 1) % len(r.cands)
			r.fails = 0
		}
		if err := sleepCtx(ctx, c.liveRetryInterval()); err != nil {
			return r.res, err
		}
	}
	if !r.done() {
		r.addGap(r.next, r.end)
		r.next = r.end
	}
	r.res.Complete = true
	if r.res.Segments == 0 && lastErr != nil {
		return r.res, fmt.Errorf("%w: live: no segment before the deadline: %v", ErrTemporary, lastErr)
	}
	return r.res, nil
}
