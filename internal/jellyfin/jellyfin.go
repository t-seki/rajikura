// Package jellyfin は Jellyfin にライブラリの再スキャン（用語集）を頼む。
//
// ライブラリにはネットワーク越しの共有で書くことがあり（ADR 0005）、そのときは Jellyfin のリアルタイム監視が効かない。そこで、録音を保存した後に
// 指定したライブラリだけを再スキャンさせる。手順は次の 2 つ。
//
//  1. GET /Library/VirtualFolders で、ライブラリの名前から ID を引く（見つからなければエラー）。
//  2. POST /Items/{ID}/Refresh で、そのライブラリを再スキャンさせる。全ライブラリの再スキャン（/Library/Refresh）は使わない。
//
// API キーは秘密なので、ヘッダでだけ送り、エラーにもログにも出さない。失敗しても送り直さない（呼び出し側が決める）。
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// timeout は Jellyfin に送るときの待ち時間の上限。
const timeout = 30 * time.Second

// Client は Jellyfin の API の呼び出し口。
type Client struct {
	// URL は Jellyfin のベース URL（例 http://jellyfin:8096）。
	URL string
	// APIKey は API キー。秘密なので、エラーにもログにも出さない。
	APIKey string
	// Library は再スキャンするライブラリの名前。
	Library string
	// HTTP は HTTP のクライアント。nil なら、待ち時間の上限を付けた既定のもの。
	HTTP *http.Client
}

// Rescan は Library という名前のライブラリを再スキャンさせる。
func (c *Client) Rescan(ctx context.Context) error {
	var folders []struct {
		Name   string `json:"Name"`
		ItemID string `json:"ItemId"`
	}
	if err := c.do(ctx, http.MethodGet, "/Library/VirtualFolders", nil, &folders); err != nil {
		return fmt.Errorf("jellyfin: ライブラリの一覧を取れない: %w", err)
	}
	id := ""
	for _, f := range folders {
		if f.Name == c.Library {
			id = f.ItemID
			break
		}
	}
	if id == "" {
		return fmt.Errorf("jellyfin: ライブラリ %q が見つからない", c.Library)
	}
	q := url.Values{
		"Recursive":           {"true"},
		"MetadataRefreshMode": {"Default"},
		"ImageRefreshMode":    {"Default"},
		"ReplaceAllMetadata":  {"false"},
		"ReplaceAllImages":    {"false"},
	}
	if err := c.do(ctx, http.MethodPost, "/Items/"+url.PathEscape(id)+"/Refresh", q, nil); err != nil {
		return fmt.Errorf("jellyfin: ライブラリ %q の再スキャンを頼めない: %w", c.Library, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, out any) error {
	u := strings.TrimRight(c.URL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return errors.New("URL を読めない")
	}
	req.Header.Set("X-Emby-Token", c.APIKey)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("状態 %d が返った", resp.StatusCode)
	}
	if out == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("応答を読めない: %w", err)
	}
	return nil
}
