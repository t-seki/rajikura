package server

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ブラウザ経由の攻撃とログインの総当たりへの守り（docs/adr/0021-web-ui-origin-host-and-login-limit.md）。
//
//   - Host の検査: /api/ と SPA は、Host が IP のアドレス（v4・v6）・localhost・UI.AllowedHosts（serve の -allowed-hosts）の
//     名前のときだけ受け、ほかは 421 で断る（DNS rebinding への手当て）。/healthz・/metrics には掛けない
//   - オリジンの検査: /api/ の GET・HEAD 以外は、標準の http.CrossOriginProtection で、ほかのオリジンからの要求を 403 で断る
//     （Sec-Fetch-Site も Origin も無い要求は、CLI などブラウザ以外とみなして通す。OPTIONS も標準の検査が通すが、/api/ に OPTIONS の口は無い）
//   - ログインの総当たりの制限: POST /api/login のパスワードと PUT /api/password の今のパスワードの間違いを、接続元を区別せず
//     serve 全体で数える。LoginMaxFailures 回続けて間違えたら loginFirstLock の間受け付けず、その後も間違えるたびに止める
//     時間を倍にする（上限 loginMaxLock）。止めている間は正しいパスワードも 429 で断る。成功したら 0 に戻す

// LoginMaxFailures は、ログインのパスワードを続けて間違えてよい回数。これだけ間違えたら、しばらく受け付けない。
const LoginMaxFailures = 5

const (
	// loginFirstLock は、LoginMaxFailures 回続けて間違えたときに、初めに受け付けない時間。
	loginFirstLock = time.Minute
	// loginMaxLock は、間違えるたびに倍にする、受け付けない時間の上限。
	loginMaxLock = 15 * time.Minute
)

// hostAllowed は、要求の Host（ポートが付いていてもよい）が、IP のアドレス・localhost・allowed の名前のどれかかを返す。
// 名前は大文字小文字を区別せず、ポートは見ない。
func hostAllowed(host string, allowed []string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if h == "" {
		return false
	}
	// DNS rebinding の要求の Host は攻撃者のドメイン名になり、IP のアドレスにはならないので、IP のアドレスは無条件で通す。
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	for _, a := range allowed {
		if strings.EqualFold(h, a) {
			return true
		}
	}
	return false
}

// isAPIPath は、パスが /api/ の下かを返す。
func isAPIPath(p string) bool {
	c := path.Clean("/" + p)
	return c == "/api" || strings.HasPrefix(c, "/api/")
}

// protect は、/api/ と SPA の前に置く、Host とオリジンの検査。
func (u *UI) protect(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api := isAPIPath(r.URL.Path)
		if !hostAllowed(r.Host, u.AllowedHosts) {
			u.log().Warn("許していない Host の要求を断った（名前で開くなら serve の -allowed-hosts に足す）", "host", r.Host, "path", r.URL.Path)
			msg := fmt.Sprintf("Host %q は許していない。この名前で開くなら、serve の -allowed-hosts に足す", r.Host)
			if api {
				writeJSON(w, http.StatusMisdirectedRequest, map[string]string{"error": msg})
			} else {
				http.Error(w, msg, http.StatusMisdirectedRequest)
			}
			return
		}
		if api {
			if err := cop.Check(r); err != nil {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "ほかのオリジンからの要求は受け付けない"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// loginLimiter は、ログインのパスワードを続けて間違えた回数と、受け付けない時間。serve の中にだけ持ち、起動し直すと 0 に戻る。
type loginLimiter struct {
	mu       sync.Mutex
	failures int
	// lock は、最後に受け付けなくした時間の長さ（次に止めるときは倍にする）。
	lock time.Duration
	// until は、受け付けない時間の終わり。
	until time.Time
}

// checkPassword は、ログインの総当たりの制限の中で correct（パスワードを確かめる）を呼ぶ。止めている間は correct を呼ばず、
// 429 を書いて handled を true で返す。そうでなければ correct の結果を ok に返し、間違いを数える（応答は呼び出し側が書く）。
// 確かめてから数えるまでを 1 つにする（並行に叩かれても、止める前に通る数が増えない）。
func (u *UI) checkPassword(w http.ResponseWriter, correct func() bool) (ok, handled bool) {
	l := u.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	now := u.now()
	if now.Before(l.until) {
		secs := int(math.Ceil(l.until.Sub(now).Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":       fmt.Sprintf("パスワードを続けて間違えたので、受け付けを止めている。あと %d 秒待つ", secs),
			"retry_after": secs,
		})
		return false, true
	}
	if correct() {
		l.failures, l.lock, l.until = 0, 0, time.Time{}
		return true, false
	}
	l.failures++
	if l.failures >= LoginMaxFailures {
		if l.lock == 0 {
			l.lock = loginFirstLock
		} else {
			l.lock = min(l.lock*2, loginMaxLock)
		}
		l.until = now.Add(l.lock)
		u.log().Warn("パスワードを続けて間違えたので、ログインの受け付けを止める", "failures", l.failures, "for", l.lock)
	}
	return false, false
}
