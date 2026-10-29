// Package client は、CLI（rajikura jobs・retry・skip・record・rerecord・backup）が serve の HTTP API を呼ぶためのクライアント。
// jobs・retry・skip・record・rerecord はジョブを扱う（GET /api/jobs、GET /api/jobs/{id}、POST /api/jobs/{id}/retry・skip・record・rerecord）。
// backup は DB のバックアップを受け取る（GET /api/backup。backup.go）。
// CLI は DB を開かず、この API だけを使う（docs/adr/0009-cli-over-http.md）。
// 足したら直す: CLI のサブコマンド
//
// 接続先とトークンは、フラグ → 環境変数（RAJIKURA_URL・RAJIKURA_TOKEN）→ 設定ファイル（os.UserConfigDir の下の
// rajikura/client.toml）の順に読む（settings.go）。トークンは Authorization: Bearer で送る。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// Client は serve の HTTP API のクライアント。
type Client struct {
	base  string
	token string
	// HTTP は使う HTTP クライアント。nil なら http.DefaultClient。
	HTTP *http.Client
}

// New は接続先 s.URL にトークン s.Token でつなぐクライアントを作る。URL のパスは API の前に付ける（末尾の / は落とす）。
func New(s Settings) (*Client, error) {
	u, err := parseBaseURL(s.URL)
	if err != nil {
		return nil, err
	}
	return &Client{base: strings.TrimRight(u.String(), "/"), token: s.Token}, nil
}

// ErrUnreachable は serve につながらないこと（止まっている、URL が違うなど）。
var ErrUnreachable = errors.New("serve につながらない")

// APIError は serve が 2xx でない状態を返したこと。Message は答えの error（無ければ空）。
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Status == http.StatusUnauthorized {
		return fmt.Sprintf("serve が API トークンを受け付けない（%d: %s）。トークンが正しいか、失効していないかを確かめる", e.Status, msg)
	}
	return fmt.Sprintf("serve が %d を返した: %s", e.Status, msg)
}

// IsStatus は err が状態 status の APIError であること。
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// send は API を呼び、答えを返す。2xx でなければ答えを閉じて APIError を返す。呼んだ側が答えの Body を閉じる。
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w（%s）: %v", ErrUnreachable, c.base, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("serve の答えを読めない: %w", err)
		}
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return nil, &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	return resp, nil
}

// do は API を呼び、2xx なら答えを out に読む（out が nil なら読まない）。
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("serve の答えを読めない: %w", err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("serve の答えが JSON として読めない（接続先が rajikura serve か確かめる）: %w", err)
	}
	return nil
}

// Job はジョブの一覧の 1 行（GET /api/jobs）。
type Job struct {
	ID       int64  `json:"id"`
	ShowName string `json:"showName"`
	Day      string `json:"day"`
	Part     int    `json:"part"`
	State    string `json:"state"`
	// Settled は skipped のジョブが確定したか（false なら録らない予定、true なら録らなかった）。ByPerson は人が決めたジョブか。
	// Live はライブで録るジョブか（録る前のジョブでは、番組表の同期で変わりうる見込み）。
	Settled         bool      `json:"settled"`
	ByPerson        bool      `json:"byPerson"`
	Live            bool      `json:"live"`
	Attempts        int       `json:"attempts"`
	NextAttemptAt   time.Time `json:"nextAttemptAt"`
	Deadline        time.Time `json:"deadline"`
	OutputPath      string    `json:"outputPath"`
	MissingCount    int       `json:"missingCount"`
	LastFailedStage string    `json:"lastFailedStage"`
	LastError       string    `json:"lastError"`
}

// ListJobs はジョブを一覧する（GET /api/jobs）。states が空でなければ、その状態のジョブだけ。
func (c *Client) ListJobs(ctx context.Context, states ...domain.State) ([]Job, error) {
	path := "/api/jobs"
	if len(states) > 0 {
		ss := make([]string, len(states))
		for i, s := range states {
			ss[i] = string(s)
		}
		path += "?state=" + url.QueryEscape(strings.Join(ss, ","))
	}
	var res struct {
		Jobs []Job `json:"jobs"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, err
	}
	return res.Jobs, nil
}

// GetJob はジョブを 1 つ読む（GET /api/jobs/{id}）。
func (c *Client) GetJob(ctx context.Context, id int64) (Job, error) {
	var j Job
	err := c.do(ctx, http.MethodGet, "/api/jobs/"+strconv.FormatInt(id, 10), nil, &j)
	return j, err
}

// RetryJob はジョブを手動再試行にする（POST /api/jobs/{id}/retry）。無いジョブは 404、受け付けない状態・録音期限を
// 過ぎたジョブは 409 の APIError。
func (c *Client) RetryJob(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/api/jobs/"+strconv.FormatInt(id, 10)+"/retry", nil, nil)
}

// SkipJob はジョブを「録らない」にする（POST /api/jobs/{id}/skip。ADR 0019）。無いジョブは 404、受け付けない状態
// （試行のある・waiting でないジョブ）は 409 の APIError。
func (c *Client) SkipJob(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/api/jobs/"+strconv.FormatInt(id, 10)+"/skip", nil, nil)
}

// RerecordJob は done のジョブを録り直す（POST /api/jobs/{id}/rerecord。ADR 0025）。無いジョブは 404、受け付けないジョブ
// （done でない・ライブ録音・録音期限を過ぎた）は 409 の APIError。
func (c *Client) RerecordJob(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/api/jobs/"+strconv.FormatInt(id, 10)+"/rerecord", nil, nil)
}

// RecordJob は skipped のジョブを「録る」に戻す（POST /api/jobs/{id}/record。ADR 0019）。無いジョブは 404、受け付けない状態
// （skipped でない）・録音期限を過ぎた録らなかったのジョブは 409 の APIError。
func (c *Client) RecordJob(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/api/jobs/"+strconv.FormatInt(id, 10)+"/record", nil, nil)
}
