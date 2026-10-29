package server

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// SessionCookie はセッションの id を渡す cookie の名前。
const SessionCookie = "rajikura_session"

// Sessions はセッションの保存先（store.Store が満たす）。
type Sessions interface {
	CreateSession(ctx context.Context, now time.Time) (string, time.Time, error)
	SessionValid(ctx context.Context, id string, now time.Time) (bool, error)
	DeleteSession(ctx context.Context, id string) error
}

// UI は Web UI の JSON API（/api/）と SPA の配信。利用者は 1 人で、パスワードの bcrypt の hash（DB に置く）で認証する。
type UI struct {
	Sessions Sessions
	// Passwords はパスワードの hash の保存先（setup.go）。パスワードが無い間は、POST /api/setup のほかの /api/（/api/me も）は 503 を返す。
	Passwords Passwords
	// Setup は serve を起動したときに作ったセットアップコード（setup.go）。パスワードが無い間に、パスワードを決めるのに使う。
	// nil なら、パスワードを決められない（POST /api/setup は 409）。
	Setup *SetupCode
	// Shows は番組の保存先（shows.go）。nil なら番組の API を載せない。
	Shows Shows
	// Onces は 1 回の予約の保存先（onces.go）。nil なら 1 回の予約の API を載せない。
	Onces Onces
	// Keywords はキーワードの保存先（keywords.go）。nil ならキーワードの API を載せない。
	Keywords Keywords
	// Guide は局の一覧と番組表の取得元（shows.go の新しい番組の補助）。nil なら /api/stations と /api/show-name を載せない。
	Guide app.Guide
	// AreaGuide はエリアの日別番組表の取得元（guide.go）。nil なら番組表の API と、1 回の予約を足す API
	// （onces.go。放送回を番組表で確かめる）を載せない。
	AreaGuide app.AreaGuide
	// Membership は会員種別（1 回の予約の録音期限の判定。onces.go）。Settings があれば使わず、設定の写しから読む。
	// どちらも無ければ非会員。
	Membership domain.Membership
	// Jobs はジョブの API の保存先（jobs.go）。
	Jobs Jobs
	// Expander は番組・キーワードを変えた直後の展開（shows.go・keywords.go）。nil なら展開せず、次の番組表の同期を待つ。
	Expander Expander
	// Recorder はジョブの「再試行」「録る」が成功した後に録音のループを起こす（jobs.go）。nil なら起こさず、次の見回りを待つ。
	// Progresses も満たせば（app.App は満たす）、ジョブの API に録音中のジョブの進捗を付ける（shows.go の Progresses）。
	Recorder Recorder
	// Tokens は API トークンの保存先（tokens.go）。nil なら API トークンの API を載せず、Bearer も受け付けない。
	Tokens Tokens
	// Notifications は通知の API の保存先（notifications.go）。nil なら通知の API を載せない。
	Notifications Notifications
	// TestNotifier はテスト通知を送る（notifications.go）。nil ならテスト通知の API は 409 を返す。
	TestNotifier TestNotifier
	// NotifyChannel は通知先の種類（notify.channel の値。discord・slack・email・none など）。Settings があれば使わず、
	// 設定の写しから読む。どちらも無ければ none として扱う。種類の名前だけを持ち、Webhook の URL などの秘密は持たない。
	NotifyChannel string
	// Settings は設定の正本（settings.go）。nil なら設定の API を載せない。
	Settings Settings
	// Backups はバックアップを作るもの（backup.go）。nil ならバックアップの API を載せない。
	Backups Backups
	// BackupTempDir は、GET /api/backup が返すまで写しを置く一時のフォルダの親。空なら OS の一時フォルダ。
	BackupTempDir string
	// Version は rajikura の版（/api/me で返し、Web UI のナビに出す）。空なら dev。
	Version string
	// Files は SPA のファイルの根。ファイルが無いパスは index.html を返す。
	Files fs.FS
	// Now は現在時刻。nil なら time.Now。
	Now func() time.Time
	// Log は nil なら slog.Default。
	Log *slog.Logger
	// AllowedHosts は、IP のアドレスと localhost のほかに、/api/ と SPA が受ける Host の名前（serve の -allowed-hosts。protect.go）。
	AllowedHosts []string

	// limiter はログインの総当たりの制限（protect.go）。register が作る。
	limiter *loginLimiter
}

var _ Sessions = (*store.Store)(nil)

func (u *UI) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *UI) membership() domain.Membership {
	if u.Settings != nil {
		return u.Settings.Current().Membership
	}
	if u.Membership != "" {
		return u.Membership
	}
	return domain.MembershipFree
}

func (u *UI) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.Default()
}

// register は /api/ と SPA を mux に載せる。/healthz は呼び出し側が載せる（認証の外）。
func (u *UI) register(mux *http.ServeMux) {
	u.limiter = &loginLimiter{}
	if hash, err := u.Passwords.PasswordHash(context.Background()); err != nil {
		u.log().Error("パスワードを読めない", "err", err)
	} else if hash == "" {
		u.log().Info("Web UI のパスワードが決まっていないので、セットアップコードを入れてパスワードを決めるまで、API は使えない")
	} else if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		u.log().Warn("パスワードの hash が bcrypt の hash として読めないので、ログインは必ず 401 になる（serve -reset-password で戻す）", "err", err)
	}
	mux.HandleFunc("POST /api/setup", u.setup)
	mux.HandleFunc("POST /api/login", u.guard(u.login))
	mux.HandleFunc("POST /api/logout", u.guard(u.authed(u.logout)))
	mux.HandleFunc("GET /api/me", u.guard(u.authed(u.me)))
	mux.HandleFunc("PUT /api/password", u.guard(u.authed(u.changePassword)))
	u.registerShows(mux)
	u.registerOnces(mux)
	u.registerKeywords(mux)
	u.registerJobs(mux)
	u.registerTokens(mux)
	u.registerNotifications(mux)
	u.registerGuide(mux)
	u.registerSettings(mux)
	u.registerBackup(mux)
	u.registerFolders(mux)
	mux.HandleFunc("/api/", u.guard(u.authed(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})))
	// SPA は、パスワードが無い間も返す（パスワードを決める画面を出すため）。
	mux.HandleFunc("/", u.spa)
}

// guard は、パスワードがまだ無いとき 503 を返す（API トークンがあっても通さない）。中身の setup が true で、
// locked はセットアップコードを続けて間違えて、次の起動まで受け付けないこと。
func (u *UI) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash, err := u.Passwords.PasswordHash(r.Context())
		if err != nil {
			u.log().Error("パスワードを読めない", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
			return
		}
		if hash == "" {
			u.setupResponse(w)
			return
		}
		next(w, r)
	}
}

// authed は、有効な API トークン（Authorization: Bearer）もセッション（cookie）も無いとき 401 を返す。
func (u *UI) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := bearerToken(r); ok {
			if u.Tokens == nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "トークンが無効"})
				return
			}
			valid, err := u.Tokens.APITokenValid(r.Context(), tok, u.now())
			if err != nil {
				u.log().Error("API トークンを確かめられない", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
				return
			}
			if !valid {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "トークンが無効"})
				return
			}
			next(w, r)
			return
		}
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしていない"})
			return
		}
		ok, err := u.Sessions.SessionValid(r.Context(), c.Value, u.now())
		if err != nil {
			u.log().Error("セッションを確かめられない", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしていない"})
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (u *UI) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return
	}
	hash, err := u.Passwords.PasswordHash(r.Context())
	if err != nil {
		u.log().Error("パスワードを読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	ok, handled := u.checkPassword(w, func() bool {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) == nil
	})
	if handled {
		return
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "パスワードが違う"})
		return
	}
	u.startSession(w, r)
}

func (u *UI) logout(w http.ResponseWriter, r *http.Request) {
	// トークンで叩かれたときは cookie が無い。消すセッションが無いだけで、成功として返す。
	if c, err := r.Cookie(SessionCookie); err == nil {
		if err := u.Sessions.DeleteSession(r.Context(), c.Value); err != nil {
			u.log().Error("セッションを消せない", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (u *UI) me(w http.ResponseWriter, r *http.Request) {
	version := u.Version
	if version == "" {
		version = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "version": version})
}

// spa は SPA のファイルを返す。ファイルが無いパスは index.html を返す（クライアント側のルーティングのため）。
func (u *UI) spa(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path.Clean(r.URL.Path)[1:]
	if name != "" {
		if fi, err := fs.Stat(u.Files, name); err == nil && !fi.IsDir() {
			http.ServeFileFS(w, r, u.Files, name)
			return
		}
	}
	// index.html を返す。ServeFileFS は /index.html への要求を ./ に飛ばすので、中身を直接書く。
	b, err := fs.ReadFile(u.Files, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
