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

// reauthRun は、取得 1 回分（FetchTimefree 1 回、または FetchLive の接続の 1 まとまり）の、取得の途中の 401・403 での認証のやり直し。
// 段は最大 2 つ。
//
//  1. 1 段目: セッションを捨てずに認証し直す（Authenticate を呼ぶ。auth2 がセッションを拒めば、Authenticate の中で radiko のログインをし直す）。
//  2. 2 段目: 1 段目で得たトークンでも 401・403 が来て、そのトークンにセッションが付いているときだけ、セッションを捨てて radiko のログインをし直す。
//
// セッションの付いていないトークン（非会員）は 1 段目だけ。段が尽きたら、呼び出し側が ErrTemporary で返す。
// どの段で何が起きたかは Client.Log に INFO で出す（トークンとセッションの値は出さない）。
type reauthRun struct {
	c       *Client
	kind    string // ログに出す取得の種別（"timefree" か "live"）
	station string
	n       int // やり直した段の数
	logged  int // 「取れた」をログに出した段の数
}

func (c *Client) newReauthRun(kind, station string) *reauthRun {
	return &reauthRun{c: c, kind: kind, station: station}
}

// next は、取得の途中でトークン tok が拒まれた（401・403）ときに、次の段で認証し直す。段が尽きていたら ok=false を返す（認証はしない）。
func (r *reauthRun) next(ctx context.Context, tok Token) (nt Token, ok bool, err error) {
	member := tok.sess != nil
	if r.n == 0 {
		r.c.logInfo("取得の途中でトークンが拒まれた（401・403）", "kind", r.kind, "station", r.station, "member", member)
	}
	if r.n >= 2 || (r.n == 1 && !member) {
		r.c.logInfo("認証し直した後のトークンも拒まれた。取得を諦める", "kind", r.kind, "station", r.station, "stages_done", r.n)
		return Token{}, false, nil
	}
	r.n++
	if r.n == 2 {
		r.c.logInfo("2 段目に進む（セッションを捨てて radiko のログインをし直す）", "kind", r.kind, "station", r.station)
		r.c.dropSession(tok.sess)
	} else {
		r.c.logInfo("1 段目で認証し直す（セッションを捨てない）", "kind", r.kind, "station", r.station, "member", member)
	}
	nt, err = r.c.Authenticate(ctx)
	if err != nil {
		r.c.logInfo("認証し直しに失敗した", "kind", r.kind, "station", r.station, "stage", r.n, "error", err.Error())
		return Token{}, true, err
	}
	return nt, true, nil
}

// succeeded は、認証し直した後に取得が通ったときに呼ぶ（段ごとに 1 回だけログに出す）。
func (r *reauthRun) succeeded() {
	if r.n > r.logged {
		r.logged = r.n
		r.c.logInfo("認証し直した後に取れた", "kind", r.kind, "station", r.station, "stage", r.n)
	}
}

// reset は段の数を 0 に戻す（ライブで、1 まとまりの接続が成功したあと）。
func (r *reauthRun) reset() { r.n, r.logged = 0, 0 }

// logInfo は Client.Log が nil でなければ INFO を出す。
func (c *Client) logInfo(msg string, args ...any) {
	if c.Log != nil {
		c.Log.Info(msg, args...)
	}
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
