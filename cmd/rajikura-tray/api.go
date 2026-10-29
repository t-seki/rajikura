package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// requestTimeout は、API の 1 回の要求を待つ長さ。
const requestTimeout = 15 * time.Second

// testFailTitle は、テスト通知を送れなかったときのトーストのタイトル。
const testFailTitle = "rajikura: テスト通知を送れない"

// errUnauthorized は、serve が 401 を返したこと（トークンが無い・失効した）。トレイは未設定として扱う。
var errUnauthorized = errors.New("API トークンが受け付けられない（401）")

// notification は GET /api/notifications の 1 件（internal/server/notifications.go の notificationView のうち、トレイが使うもの）。
type notification struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Link は通知が指す Web UI の画面のパス（/jobs/{id} など）。指す先が無ければ空（serve がキーごと出さない）。
	Link string `json:"link"`
}

// anomaly は GET /api/notifications/status の anomalies の 1 件。
type anomaly struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// snapshot は 1 回のポーリングで読んだ serve の状態。
type snapshot struct {
	// Notifications は通知の履歴（新しい順）。
	Notifications []notification
	// Unread は未読の通知の数、Failed は通知先に送れなかった通知の数、Anomalies は続いているサービスの異常。
	Unread    int
	Failed    int
	Anomalies []anomaly
	// Running は state=running のジョブの数（録音中）。
	Running int
}

// apiClient は serve の HTTP API を API トークンで読む。
type apiClient struct {
	http *http.Client
}

// poll は 3 つの口を読む。401 を受けたら errUnauthorized を返す（errors.Is で見分ける）。
// ほかの失敗（つながらない、2xx でない、JSON が読めない）はそのままエラーにする。
func (a apiClient) poll(ctx context.Context, conn connection) (snapshot, error) {
	var s snapshot
	var list struct {
		Notifications []notification `json:"notifications"`
	}
	if err := a.get(ctx, conn, "/api/notifications", &list); err != nil {
		return s, err
	}
	var status struct {
		Unread    int       `json:"unread"`
		Failed    int       `json:"failed"`
		Anomalies []anomaly `json:"anomalies"`
	}
	if err := a.get(ctx, conn, "/api/notifications/status", &status); err != nil {
		return s, err
	}
	var jobs struct {
		Jobs []struct {
			ID int64 `json:"id"`
		} `json:"jobs"`
	}
	if err := a.get(ctx, conn, "/api/jobs?state=running", &jobs); err != nil {
		return s, err
	}
	s.Notifications = list.Notifications
	s.Unread, s.Failed, s.Anomalies = status.Unread, status.Failed, status.Anomalies
	s.Running = len(jobs.Jobs)
	return s, nil
}

// sendTest は POST /api/notifications/test を呼ぶ。200 なら nil（通知先に送れたかは見ない。送れなかったことは Web UI の履歴と帯に出る）。
// 届かない・401・200 以外はエラーにする（呼び出し側がトーストの本文に出す）。
func (a apiClient) sendTest(ctx context.Context, conn connection) error {
	var out struct {
		Sent bool `json:"sent"`
	}
	return a.do(ctx, http.MethodPost, conn, "/api/notifications/test", &out)
}

func (a apiClient) get(ctx context.Context, conn connection, path string, out any) error {
	return a.do(ctx, http.MethodGet, conn, path, out)
}

func (a apiClient) do(ctx context.Context, method string, conn connection, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, conn.URL+path, nil)
	if err != nil {
		return fmt.Errorf("%s %s を作れない: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Accept", "application/json")
	c := a.http
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		var body struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = json.Unmarshal(b, &body)
		if body.Error != "" {
			return fmt.Errorf("%s %s: %s（%s）", method, path, resp.Status, body.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("%s %s の答えを読めない: %w", method, path, err)
	}
	return nil
}
