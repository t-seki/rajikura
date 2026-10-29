// Package server は rajikura serve の HTTP の口。
//
//   - GET /healthz: k8s の liveness probe 用。認証の外に置く
//   - GET /metrics: Prometheus 用。ログインなし。WithMetrics で足す（メトリクスは internal/metrics）
//   - /api/: Web UI の JSON API（ログインは ui.go、パスワードを決める・変えるのは setup.go、番組の管理は shows.go、1 回の予約は onces.go、キーワードは keywords.go、ジョブは jobs.go、番組表は guide.go、API トークンは tokens.go、通知は notifications.go、設定は settings.go、バックアップは backup.go、フォルダの一覧は folders.go）。
//     POST /api/login と POST /api/setup 以外は、セッションの cookie か、有効な API トークン（Authorization: Bearer）が要り、無ければ 401
//   - それ以外のパス: Web UI の SPA（ui.go）。ファイルが無いパスは index.html を返す
//
// 足したら直す: 待ち受ける口
//
// 利用者は 1 人で、パスワードの bcrypt の hash（DB に置く）で認証する。パスワードがまだ無い間は、POST /api/setup
// （セットアップコードを入れてパスワードを決める）のほかの /api/ は 503 を返す（API トークンがあっても通さない）。SPA は返す。
// /api/ と SPA は、Host が IP のアドレス・localhost・serve の -allowed-hosts の名前のときだけ受け（ほかは 421）、/api/ の GET・HEAD
// 以外はほかのオリジンからの要求を 403 で断り、ログインのパスワードの間違いを全体で数えて続けば 429 で止める（protect.go）。
// 作りは docs/adr/0008-web-ui-shape.md と docs/adr/0016-first-password-by-setup-code.md、守りは docs/adr/0021-web-ui-origin-host-and-login-limit.md にある。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// StaleAfter は、ジョブの実行のループが最後に 1 周してからこれを過ぎると /healthz が 503 を返す時間。
// 記録は周（RunDue を 1 回呼ぶごと）の始まりなので、1 周（録音・通知の送信や、最初の試行の前の番組表の取り直しが展開役の同期・展開の後で
// 待たされている間を含む）が長引いても、この時間の内側なら 200 を返す。起動直後でまだ 1 周していないとき（起動時の
// 同期の間）は、起動時刻から数える。
const StaleAfter = 30 * time.Minute

// Health は /healthz のハンドラ。
type Health struct {
	// LastPoll はジョブの実行のループが最後に 1 周した（RunDue を呼んだ）時刻を返す。零値は、まだ記録が無いこと。
	LastPoll func() time.Time
	// Now は現在時刻。nil なら time.Now。テストで固定する。
	Now func() time.Time
}

func (h Health) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	last := h.LastPoll()
	ok := !last.IsZero() && now().Sub(last) <= StaleAfter
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	status := "ok"
	if !ok {
		status = "stale"
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	fmt.Fprintf(w, "%s: last loop %s\n", status, last.Format(time.RFC3339))
}

// Handler は GET /healthz と、ui が nil でなければ /api/ と SPA を持つハンドラを返す。/api/ と SPA には、Host とオリジンの
// 検査（protect.go）を掛ける。/healthz には掛けない（kubelet の probe は pod の IP の Host で叩く）。
func Handler(h Health, ui *UI) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", h)
	if ui != nil {
		inner := http.NewServeMux()
		ui.register(inner)
		mux.Handle("/", ui.protect(inner))
	}
	return mux
}

// WithMetrics は、h の前に GET /metrics を置いたハンドラを返す。metrics が nil なら h をそのまま返す。
// /healthz と同じくログインは要らない（認証の外に置くため、UI の手前で分ける）。
func WithMetrics(h http.Handler, metrics http.Handler) http.Handler {
	if metrics == nil {
		return h
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.Handle("/", h)
	return mux
}

// Serve は ln で handler を配り、ctx が取り消されたら止めて nil を返す。
func Serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
