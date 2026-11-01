package radiko

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	// Session は、認証に付けた radiko のログインのセッション（login.go）。非会員として認証したなら空。
	// 空でなければ、タイムフリーの再生リストの type を c にする。
	Session string
	// AreaFree は、radiko のログインの応答の areafree が 1 だったか。true なら、エリアフリーの再生リスト（ストリーム情報の
	// areafree が 1 の url）を使う。非会員として認証したなら false。
	AreaFree bool

	// sess は、Session を得た radiko のログインのセッション。トークンが拒まれたときに、そのセッションを捨てるために持つ。
	sess *session
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
//
// 会員種別が非会員でなければ（Client.Account）、radiko のログインのセッションを auth2 に付ける（login.go）。持っていなければ
// radiko のログインをする。radiko がセッションを拒んだら、1 回だけ radiko のログインをし直す。radiko が radiko のログインを拒んだら、非会員として認証する。
func (c *Client) Authenticate(ctx context.Context) (Token, error) {
	s, err := c.loginSession(ctx)
	if err != nil {
		return Token{}, err
	}
	tok, err := c.authenticate(ctx, s)
	if !errors.Is(err, errSessionRejected) {
		return tok, err
	}
	c.dropSession(s)
	if s, err = c.loginSession(ctx); err != nil {
		return Token{}, err
	}
	tok, err = c.authenticate(ctx, s)
	if errors.Is(err, errSessionRejected) {
		return Token{}, fmt.Errorf("%w: auth2: session rejected again after logging in again", ErrTemporary)
	}
	return tok, err
}

// reauthenticate は、取得の途中でトークン tok が拒まれた（401・403）ときに認証し直す。tok が radiko のログインのセッションを
// 付けたものなら、そのセッションも拒まれたものとして捨て、radiko のログインをし直す。
func (c *Client) reauthenticate(ctx context.Context, tok Token) (Token, error) {
	if tok.sess != nil {
		c.dropSession(tok.sess)
	}
	return c.Authenticate(ctx)
}

// authenticate は auth1 / auth2 を 1 回ずつ呼ぶ。s が nil でなければ、auth2 に radiko_session を付ける。
// セッションを付けた auth2 が 401・403 なら errSessionRejected を返す。
func (c *Client) authenticate(ctx context.Context, s *session) (Token, error) {
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

	auth2 := base + "/v2/api/auth2"
	if s != nil {
		auth2 += "?" + url.Values{sessionField: {s.id}}.Encode()
	}
	resp, err = c.get(ctx, auth2, map[string]string{
		"X-Radiko-AuthToken":  token,
		"X-Radiko-PartialKey": pk,
		"X-Radiko-Device":     "pc",
		"X-Radiko-User":       "dummy_user",
	})
	if err != nil {
		// get のエラーは URL を含むので、セッションの値が出ないように言い換える。
		if s != nil && ctx.Err() == nil {
			return Token{}, fmt.Errorf("%w: auth2 with radiko_session failed", ErrTemporary)
		}
		return Token{}, err
	}
	if s != nil && (resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden) {
		return Token{}, errSessionRejected
	}
	if resp.status != http.StatusOK {
		return Token{}, statusError("auth2", resp.status)
	}
	area, err := parseArea(string(resp.body))
	if err != nil {
		return Token{}, err
	}
	tok := Token{Value: token, AreaID: area}
	if s != nil {
		tok.Session, tok.AreaFree, tok.sess = s.id, s.areaFree, s
	}
	return tok, nil
}
