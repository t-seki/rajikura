// Package radiko は radiko からの取得（radiko のログイン・認証・番組表・局情報・タイムフリーの音声・ライブの音声・タイムフリーのチャプター）を担うクライアント。
//
// 取得は標準ライブラリだけで行い、yt-dlp や ffmpeg は使わない（ADR 0001）。
// 音声は ADTS のまま io.Writer に書く。
// ライブの音声（FetchLive）は、放送中のストリームを PDT で切って書き、取れなかった区間を LiveResult.Gaps で返す。
//
// 呼び出し側（ジョブの再試行）は、エラーを errors.Is で次のように見分けられる。
//
//   - ErrNotYetAvailable: まだ取れない（タイムフリーの公開前など）
//   - ErrNotAvailable:    恒久的に取れない（タイムフリーの対象外など）
//   - ErrOutOfArea:       エリア外
//   - ErrTemporary:       一時的な失敗（ネットワーク、5xx、タイムフリーのチャンクの継ぎ目の不一致、ライブの PDT の無いセグメント、Deadline までに 1 つも書けなかったライブ、番組表のキャッシュで 304 なのにキャッシュが無い）
//
// チャプター（Chapters）だけは、取れない理由をさらに ErrNoEvent（放送回の一覧に無い）と ErrNoChapters（404）で返す（chapters.go）。
package radiko

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
	_ "time/tzdata" // Windows とコンテナでも Asia/Tokyo を使えるように埋め込む
)

// 既定値。実在の radiko の URL を持つが、Client のフィールドで差し替えられる。
const (
	DefaultAuthHost       = "https://radiko.jp"
	DefaultProgramHost    = "https://radiko.jp"
	DefaultRequestTimeout = 30 * time.Second
)

var (
	// ErrNotYetAvailable は、まだ取れない（タイムフリーの公開前など）ことを表す。時間をおけば取れうる。
	ErrNotYetAvailable = errors.New("radiko: not yet available")
	// ErrNotAvailable は、恒久的に取れない（タイムフリーの対象外など）ことを表す。
	// このパッケージが返すのは、局にタイムフリー・ライブのストリームが無いときなど、確実に分かる場合だけ。
	// 放送回ごとに恒久的に取れないかは、番組表の ts_in_ng・ts_out_ng（Program.TSInNG・TSOutNG）から呼び出し側が判断する。
	// 再生の URL が 4xx を返す場合は公開前かもしれず、恒久と決められないので ErrTemporary にする。
	ErrNotAvailable = errors.New("radiko: not available")
	// ErrOutOfArea は、エリア外であることを表す。
	ErrOutOfArea = errors.New("radiko: out of area")
	// ErrTemporary は、一時的な失敗（ネットワーク、5xx、タイムフリーのチャンクの継ぎ目の不一致、ライブの PDT の無いセグメント、Deadline までに 1 つも書けなかったライブ）を表す。再試行してよい。
	ErrTemporary = errors.New("radiko: temporary failure")

	// errAuthExpired は、トークンが無効（401/403）だったことを表す。FetchTimefree と FetchLive の connect の中で、reauthRun が最大 2 段で認証をやり直す
	// （1 段目はセッションを捨てない。2 段目はセッションを捨てて radiko のログインをし直す。取得の途中の 401・403 がセッションの拒否かトークンの切れかは未確認）。
	errAuthExpired = errors.New("radiko: token rejected")
)

// JST は日本時間。時刻はすべてこの時間帯で扱う。
var JST = mustLoadJST()

func mustLoadJST() *time.Location {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		panic(err)
	}
	return loc
}

// timeLayout は radiko の時刻表記 YYYYMMDDhhmmss。
const timeLayout = "20060102150405"

// Client は radiko のクライアント。ゼロ値ではなく NewClient で作る。
type Client struct {
	// HTTP は使う http.Client。タイムアウトは RequestTimeout で要求ごとにかけるので、ここでは設定しない。
	HTTP *http.Client
	// AuthHost は auth1 / auth2 のホスト。
	AuthHost string
	// ProgramHost は番組表・局情報・ストリーム情報の XML のホスト。
	ProgramHost string
	// ChapterHost はチャプターの API（chapters.go）のホスト。空なら DefaultChapterHost。
	ChapterHost string
	// RequestTimeout は HTTP の要求 1 回ごとのタイムアウト。1 回の取得全体には ctx でかける。
	RequestTimeout time.Duration
	// Account は radiko のログインに使うログイン情報を返す（login.go）。nil か、false を返したら非会員として認証する。
	// 認証のたびに呼ぶので、設定を保存したら次の認証から効く（持っているセッションは ResetLogin で捨てるか、AdoptLogin で保存の時点のセッションに替える）。
	Account func() (Account, bool)
	// OnLogin は、radiko のログインを試すたびに、その結果を渡して呼ぶ（nil なら呼ばない）。認証の中で呼ぶので、待たせない。
	OnLogin func(LoginEvent)
	// Log は、取得の途中の 401・403 での認証のやり直し（auth.go の reauthRun）を INFO で出す先。nil なら出さない。トークンとセッションの値は出さない。
	Log *slog.Logger
	// Now は現在時刻。テストで差し替える。
	Now func() time.Time
	// LivePollInterval はライブの medialist を取り直す間隔、LiveRetryInterval は失敗してから繋ぎ直すまでの待ち。
	// 0 なら DefaultLivePollInterval / DefaultLiveRetryInterval。
	LivePollInterval, LiveRetryInterval time.Duration

	// progCache は番組表のキャッシュ。EnableProgramCache で有効にする。
	progCache *programCache
	// login は radiko のログインのセッション（login.go）。
	login loginState
}

// NewClient は既定値の Client を返す。
func NewClient() *Client {
	return &Client{
		HTTP:           &http.Client{},
		AuthHost:       DefaultAuthHost,
		ProgramHost:    DefaultProgramHost,
		ChapterHost:    DefaultChapterHost,
		RequestTimeout: DefaultRequestTimeout,
		Now:            time.Now,
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// playlistType はタイムフリーの再生リストの type。radiko のログインのセッションを付けたトークンなら c、それ以外は b。
func playlistType(tok Token) string {
	if tok.Session != "" {
		return "c"
	}
	return "b"
}

// areaFreeAttr は、トークンで使う再生リストの、ストリーム情報の areafree の値。エリアフリーのトークンなら 1、それ以外は 0。
func areaFreeAttr(tok Token) string {
	if tok.AreaFree {
		return "1"
	}
	return "0"
}

func (c *Client) timeout() time.Duration {
	if c.RequestTimeout > 0 {
		return c.RequestTimeout
	}
	return DefaultRequestTimeout
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// get は GET を 1 回行い、本体まで読む。要求 1 回ごとにタイムアウトをかける。
// ネットワークの失敗は ErrTemporary を包んで返す。ctx が終わっていたら ctx のエラーをそのまま返す。
func (c *Client) get(ctx context.Context, rawURL string, headers map[string]string) (*response, error) {
	rctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("radiko: build request %s: %w", rawURL, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: GET %s: %v", ErrTemporary, rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: read %s: %v", ErrTemporary, rawURL, err)
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// statusError は、想定外の HTTP ステータスをエラーにする。
// 4 分類のどれにも当たらないものは、呼び出し側が再試行できるよう ErrTemporary を包む。
func statusError(op string, code int) error {
	return fmt.Errorf("%w: %s: status %d", ErrTemporary, op, code)
}

// getXML は XML を取り、gzip なら展開して返す。200 以外はエラー。
func (c *Client) getXML(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := c.get(ctx, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		return nil, statusError("GET "+rawURL, resp.status)
	}
	body, err := maybeGunzip(resp.body)
	if err != nil {
		return nil, fmt.Errorf("%w: gunzip %s: %v", ErrTemporary, rawURL, err)
	}
	return body, nil
}

// maybeGunzip は、先頭 2 バイトが gzip のマジック（0x1f 0x8b）なら展開する。
// 応答が Content-Encoding なしで gzip のまま来ることがあるため。
func maybeGunzip(b []byte) ([]byte, error) {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
