package radiko

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// authKey は pc_html5 の固定キー（radiko の web プレイヤーの JS に載っている公開の値で、秘密ではない）。
// partialkey はこの [offset:offset+length] を base64 にしたもの。
const authKey = "bcd151073c03b352e1ef2fd66c32209da9ca0afa" // gitleaks:allow

// Token は認証で得たトークンとエリア ID。
type Token struct {
	Value  string
	AreaID string
}

// partialKey は auth2 に渡す partialkey を計算する。
func partialKey(offset, length int) (string, error) {
	if offset < 0 || length <= 0 || offset+length > len(authKey) {
		return "", fmt.Errorf("radiko: invalid key range offset=%d length=%d", offset, length)
	}
	return base64.StdEncoding.EncodeToString([]byte(authKey[offset : offset+length])), nil
}

// parseArea は auth2 の応答本体（例: "JP13,東京都,tokyo Japan"）から先頭のエリア ID を取り出す。
// OUT または空ならエリア外として ErrOutOfArea を返す。
func parseArea(body string) (string, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	area, _, _ := strings.Cut(line, ",")
	area = strings.TrimSpace(area)
	if area == "" || strings.EqualFold(area, "OUT") {
		return "", ErrOutOfArea
	}
	return area, nil
}

// Authenticate は auth1 / auth2 を呼んでトークンとエリア ID を得る。
// エリア外なら ErrOutOfArea を返す。
func (c *Client) Authenticate(ctx context.Context) (Token, error) {
	base := strings.TrimRight(c.AuthHost, "/")
	resp, err := c.get(ctx, base+"/v2/api/auth1", map[string]string{
		"X-Radiko-App":         "pc_html5",
		"X-Radiko-App-Version": "0.0.1",
		"X-Radiko-Device":      "pc",
		"X-Radiko-User":        "dummy_user",
	})
	if err != nil {
		return Token{}, err
	}
	if resp.status != http.StatusOK {
		return Token{}, statusError("auth1", resp.status)
	}
	// ヘッダ名の大文字・小文字は区別しない（Header.Get が正規化する）。offset と length は毎回ヘッダから読む。
	token := resp.header.Get("X-Radiko-AuthToken")
	offset, err1 := strconv.Atoi(strings.TrimSpace(resp.header.Get("X-Radiko-KeyOffset")))
	length, err2 := strconv.Atoi(strings.TrimSpace(resp.header.Get("X-Radiko-KeyLength")))
	if token == "" || err1 != nil || err2 != nil {
		return Token{}, fmt.Errorf("%w: auth1: missing or invalid token headers", ErrTemporary)
	}
	pk, err := partialKey(offset, length)
	if err != nil {
		return Token{}, fmt.Errorf("%w: auth1: %v", ErrTemporary, err)
	}

	resp, err = c.get(ctx, base+"/v2/api/auth2", map[string]string{
		"X-Radiko-AuthToken":  token,
		"X-Radiko-PartialKey": pk,
		"X-Radiko-Device":     "pc",
		"X-Radiko-User":       "dummy_user",
	})
	if err != nil {
		return Token{}, err
	}
	if resp.status != http.StatusOK {
		return Token{}, statusError("auth2", resp.status)
	}
	area, err := parseArea(string(resp.body))
	if err != nil {
		return Token{}, err
	}
	return Token{Value: token, AreaID: area}, nil
}
