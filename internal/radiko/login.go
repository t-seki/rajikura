package radiko

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// radiko のログイン（ADR 0030）。
//
// 会員種別が非会員でないとき、Client.Account が返すログイン情報で radiko のログイン（POST /v4/api/member/login。本文は
// mail と pass）を行い、応答の JSON の radiko_session を auth2 に付けて認証する。手順は、公開されている第三者のスクリプト
// （https://github.com/uru2/rec_radiko_ts の rec_radiko_ts.sh）に書かれている範囲で作った。radiko 自身の挙動は、本物の
// アカウントでは確かめていない（未確認）。
//
//   - radiko のログインは、起動の後に初めて認証が要るとき（Authenticate）に行う。得たセッションはメモリにだけ持ち、録音と
//     番組表で使い回す。radiko がセッションを拒んだとき（セッションを付けた認証か、そのトークンでの取得が 401・403 だったとき。
//     本物の radiko で拒まれる応答は確かめていない）だけ、radiko のログインをし直す。ResetLogin で捨てる。ログアウトの API は呼ばない。
//   - radiko が radiko のログインを拒んだら（応答が 200 でも 5xx でもない、または radiko_session が空。拒む応答の形は確かめていない）、
//     非会員として認証する。ResetLogin が呼ばれるまで、radiko のログインをし直さない。
//   - ネットワークの失敗と 5xx は、今までの一時的な失敗（ErrTemporary）として返す。次の認証で、もう一度 radiko のログインをする。
//   - radiko のログインを試すたびに、結果を Client.OnLogin に渡す（呼び出し側が、サービスの異常の始まりと復旧を書く）。

// ErrLoginRejected は、radiko が radiko のログインを拒んだこと（LoginEvent.Err）を表す。
var ErrLoginRejected = errors.New("radiko: login rejected")

// errSessionRejected は、radiko が、セッションを付けた認証を拒んだ（auth2 が 401・403）ことを表す。Authenticate の中で radiko のログインをし直す。
var errSessionRejected = errors.New("radiko: session rejected")

// loginPath は radiko のログインの API のパス（AuthHost に付ける）。
const loginPath = "/v4/api/member/login"

// sessionField は radiko のログインの応答のうち、セッションの欄の名前。値はログに出さない。
const sessionField = "radiko_session"

// Account は radiko のログインに使うログイン情報と、選んだ会員種別がエリアフリーを含むか。
type Account struct {
	// Mail と Password は radiko のアカウントのメールアドレスとパスワード。Password はエラーにもログにも出さない。
	Mail, Password string
	// AreaFree は、選んだ会員種別がエリアフリーを含む（エリアフリー・ダブルプラン）か。radiko のログインの応答の areafree と食い違えば、
	// LoginEvent.Mismatch を立てる。
	AreaFree bool
}

// LoginField は radiko のログインの応答の欄の 1 つ（名前と、JSON のままの値）。
type LoginField struct {
	Name, Value string
}

// LoginEvent は radiko のログインを 1 回試した結果（Client.OnLogin に渡す）。一時的な失敗（ErrTemporary）では渡さない。
type LoginEvent struct {
	// Err は、radiko が radiko のログインを拒んだとき ErrLoginRejected を包んだエラー。通ったら nil。
	Err error
	// AreaFree は、radiko のログインの応答の areafree が 1 か（通ったときだけ意味がある）。
	AreaFree bool
	// AreaFreeValue は、radiko のログインの応答の areafree の値（JSON のまま。無ければ空）。
	AreaFreeValue string
	// Mismatch は、通ったが、応答の areafree が Account.AreaFree と食い違うこと。
	Mismatch bool
	// Fields は、通ったときの応答の欄（名前の順）。radiko_session の値は伏せてある。
	Fields []LoginField
}

// session は radiko のログインで得たセッション。
type session struct {
	id       string
	areaFree bool
}

// loginState は radiko のログインのセッションの持ち方。Client が持つ。
type loginState struct {
	// mu は state を守る。radiko のログインの要求の間も持つ（同時に認証が要っても、radiko のログインは 1 回にする）。
	mu sync.Mutex
	// sess は持っているセッション。nil ならまだ持っていない。
	sess *session
	// rejected は、radiko が radiko のログインを拒んだこと。ResetLogin まで、radiko のログインをし直さない。
	rejected bool
}

// ResetLogin は、持っている radiko のログインのセッションを捨てる（会員種別かログイン情報を保存したとき）。
// 次に認証が要るときに、その時点のログイン情報で radiko のログインをし直す（非会員なら、radiko のログインをしない）。
func (c *Client) ResetLogin() {
	c.login.mu.Lock()
	defer c.login.mu.Unlock()
	c.login.sess, c.login.rejected = nil, false
}

// account は今のログイン情報。Account が nil か、false を返したら非会員。
func (c *Client) account() (Account, bool) {
	if c.Account == nil {
		return Account{}, false
	}
	return c.Account()
}

// loginSession は認証に付けるセッションを返す。非会員のとき・radiko が radiko のログインを拒んだ後は nil を返す。
// 持っていなければ radiko のログインをする。ネットワークの失敗・5xx はエラー（ErrTemporary）を返す。
func (c *Client) loginSession(ctx context.Context) (*session, error) {
	acc, ok := c.account()
	if !ok {
		return nil, nil
	}
	c.login.mu.Lock()
	if c.login.sess != nil || c.login.rejected {
		s := c.login.sess
		c.login.mu.Unlock()
		return s, nil
	}
	ev, s, err := c.doLogin(ctx, acc)
	if err != nil {
		c.login.mu.Unlock()
		return nil, err
	}
	if s == nil {
		c.login.rejected = true
	}
	c.login.sess = s
	c.login.mu.Unlock()
	if c.OnLogin != nil {
		c.OnLogin(ev)
	}
	return s, nil
}

// dropSession は、radiko が拒んだセッション s を捨てる（ほかの呼び出しが、もう radiko のログインをし直していれば何もしない）。
func (c *Client) dropSession(s *session) {
	c.login.mu.Lock()
	defer c.login.mu.Unlock()
	if c.login.sess == s {
		c.login.sess = nil
	}
}

// doLogin は radiko のログインの API を 1 回呼ぶ。拒まれたら、Err に ErrLoginRejected を包んだ LoginEvent と nil のセッションを返す。
// ネットワークの失敗・5xx は ErrTemporary を包んだエラーを返す。
func (c *Client) doLogin(ctx context.Context, acc Account) (LoginEvent, *session, error) {
	form := url.Values{"mail": {acc.Mail}, "pass": {acc.Password}}
	resp, err := c.post(ctx, strings.TrimRight(c.AuthHost, "/")+loginPath, form)
	if err != nil {
		return LoginEvent{}, nil, err
	}
	if resp.status >= 500 {
		return LoginEvent{}, nil, statusError("login", resp.status)
	}
	if resp.status != http.StatusOK {
		return LoginEvent{Err: fmt.Errorf("%w: status %d", ErrLoginRejected, resp.status)}, nil, nil
	}
	fields, id, areaFree, ok := parseLogin(resp.body)
	if !ok || id == "" {
		return LoginEvent{Err: fmt.Errorf("%w: no %s in the response", ErrLoginRejected, sessionField)}, nil, nil
	}
	ev := LoginEvent{Fields: fields, AreaFreeValue: areaFree, AreaFree: areaFree == "1"}
	ev.Mismatch = ev.AreaFree != acc.AreaFree
	return ev, &session{id: id, areaFree: ev.AreaFree}, nil
}

// parseLogin は radiko のログインの応答の JSON を読み、欄（名前の順。radiko_session の値は伏せる）と、radiko_session と areafree の値を返す。
// areafree は、文字列なら中身、数ならその表記（無ければ空）。JSON のオブジェクトでなければ ok が false。
func parseLogin(body []byte) (fields []LoginField, id, areaFree string, ok bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return nil, "", "", false
	}
	for name, v := range raw {
		value := string(v)
		if name == sessionField {
			value = `"***"`
		}
		fields = append(fields, LoginField{Name: name, Value: value})
	}
	slices.SortFunc(fields, func(a, b LoginField) int { return strings.Compare(a.Name, b.Name) })
	return fields, jsonScalar(raw[sessionField]), jsonScalar(raw["areafree"]), true
}

// jsonScalar は JSON の値が文字列なら中身を、それ以外（数など）なら表記のまま返す。無ければ空。
func jsonScalar(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return strings.TrimSpace(s)
	}
	if string(v) == "null" {
		return ""
	}
	return strings.TrimSpace(string(v))
}

// post はフォームを POST し、本体まで読む（get と同じく、要求 1 回ごとにタイムアウトをかけ、ネットワークの失敗は ErrTemporary を包む）。
// 本文にパスワードを含むので、エラーには URL と本文を出さない。
func (c *Client) post(ctx context.Context, rawURL string, form url.Values) (*response, error) {
	rctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("radiko: build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: login: %v", ErrTemporary, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: read login response: %v", ErrTemporary, err)
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}
