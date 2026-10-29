package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// BackupContentType は GET /api/backup の答えの Content-Type（internal/server/backup.go と同じ）。
const BackupContentType = "application/vnd.sqlite3"

// Backup は serve の DB のバックアップ（ログインのセッションを空にした写し。秘密の鍵は含まない）を受け取り、w に書く
// （GET /api/backup）。書いたバイト数を返す。
func (c *Client) Backup(ctx context.Context, w io.Writer) (int64, error) {
	resp, err := c.send(ctx, http.MethodGet, "/api/backup", nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != BackupContentType {
		return 0, fmt.Errorf("serve の答えがバックアップではない（Content-Type %q。接続先が rajikura serve か確かめる）", ct)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("バックアップを受け取り終えられない: %w", err)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return n, fmt.Errorf("バックアップが途中で切れた（%d バイトのうち %d バイト）", resp.ContentLength, n)
	}
	return n, nil
}
