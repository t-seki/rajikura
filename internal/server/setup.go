package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/store"
)

// パスワードを決める・変える口（docs/adr/0016-first-password-by-setup-code.md）。
//
//   - POST /api/setup: パスワードが無い間に、セットアップコードを入れてパスワードを決める。決めたらログインした状態にする
//   - PUT /api/password: ログインしてから、今のパスワードを入れてパスワードを変える（今のパスワードの間違いは、ログインの総当たりの制限に数える。protect.go）

// SetupMaxFailures は、セットアップコードを続けて間違えてよい回数。これだけ間違えたら、次の起動まで受け付けない。
const SetupMaxFailures = 5

// setupAlphabet はセットアップコードの文字。人が写して入れるので、見分けにくい 0・O・1・I・L を除いた英大文字と数字。
const setupAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// setupCodeLen はセットアップコードの文字数（区切りの - を除く）。続けて間違えられるのは SetupMaxFailures 回なので、
// 31 の 8 乗（約 8.5 × 10^11）通りあれば当て推量では当たらない。
const setupCodeLen = 8

// maxPasswordBytes はパスワードの長さの上限（bcrypt が受け付けるのは 72 バイトまで）。
const maxPasswordBytes = 72

// Passwords は Web UI のパスワードの bcrypt の hash の保存先（store.Store が満たす）。
type Passwords interface {
	// PasswordHash はパスワードの hash を返す。まだ決めていなければ空。
	PasswordHash(ctx context.Context) (string, error)
	SetPasswordHash(ctx context.Context, hash string) error
}

var _ Passwords = (*store.Store)(nil)

// NewSetupCode はセットアップコード（XXXX-XXXX の形）を作る。
func NewSetupCode() (string, error) {
	var b strings.Builder
	n := big.NewInt(int64(len(setupAlphabet)))
	for i := range setupCodeLen {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b.WriteByte(setupAlphabet[k.Int64()])
	}
	return b.String(), nil
}

// normalizeSetupCode は、入れられたセットアップコードから区切りと空白を除き、英字を大文字にする。
func normalizeSetupCode(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t' || r == '　':
			return -1
		case 'a' <= r && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, s)
}

// SetupCode は、serve を起動したときに作ったセットアップコードと、続けて間違えた回数。起動のたびに作り直す
// （回数も起動のたびに 0 に戻る）。パスワードを決めたら無効になる。
type SetupCode struct {
	// Code はセットアップコード（NewSetupCode の形）。
	Code string
	// File はセットアップコードを書いたファイル（データのフォルダの setup-code.txt）。パスワードを決めたら消す。空なら消さない。
	File string

	mu       sync.Mutex
	failures int
	used     bool
}

// Locked は、続けて SetupMaxFailures 回間違えて、次の起動まで受け付けないかを返す。
func (s *SetupCode) Locked() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures >= SetupMaxFailures
}

// validPassword は、決める・変えるパスワードが使えるかを確かめ、使えなければ理由を返す。
func validPassword(pw string) string {
	switch {
	case pw == "":
		return "パスワードが空"
	case len(pw) > maxPasswordBytes:
		return "パスワードが長すぎる（72 バイトまで）"
	}
	return ""
}

// setupResponse は、パスワードが無い間に /api/me などが返す 503 の中身。
func (u *UI) setupResponse(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error":  "パスワードが決まっていない。セットアップコードを入れてパスワードを決める",
		"setup":  true,
		"locked": u.Setup.Locked(),
	})
}

// setup はセットアップコードを確かめてパスワードを決め、ログインした状態にする。
func (u *UI) setup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return
	}
	if msg := validPassword(req.Password); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	hash, err := u.Passwords.PasswordHash(r.Context())
	if err != nil {
		u.log().Error("パスワードを読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	s := u.Setup
	if hash != "" || s == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "パスワードはもう決まっている"})
		return
	}
	// 確かめてから保存するまでを 1 つにする（同時に 2 つ来ても、決めるのは 1 つだけ）。
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "パスワードはもう決まっている"})
		return
	}
	if s.failures >= SetupMaxFailures {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "セットアップコードを 5 回続けて間違えたので、次に serve を起動するまで受け付けない", "locked": true,
		})
		return
	}
	// 時間で漏れない比べ方にする（長さが違うときだけは、すぐに違うと分かる）。
	if subtle.ConstantTimeCompare([]byte(normalizeSetupCode(req.Code)), []byte(normalizeSetupCode(s.Code))) != 1 {
		s.failures++
		locked := s.failures >= SetupMaxFailures
		u.log().Warn("セットアップコードが違う", "failures", s.failures, "locked", locked)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "セットアップコードが違う", "remaining": SetupMaxFailures - s.failures, "locked": locked,
		})
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := u.Passwords.SetPasswordHash(r.Context(), string(b)); err != nil {
		u.log().Error("パスワードを保存できない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	s.used = true
	if s.File != "" {
		if err := os.Remove(s.File); err != nil && !errors.Is(err, os.ErrNotExist) {
			// パスワードは決まり、コードは無効になっている。ファイルが残るだけなので、ログに出して進める。
			u.log().Error("セットアップコードのファイルを消せない（手で消してよい）", "file", s.File, "err", err)
		}
	}
	u.log().Info("Web UI のパスワードを決めた")
	u.startSession(w, r)
}

// changePassword は、今のパスワードを確かめてからパスワードを変える。ログインのセッションと API トークンはそのまま残す。
func (u *UI) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return
	}
	if msg := validPassword(req.Password); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	hash, err := u.Passwords.PasswordHash(r.Context())
	if err != nil {
		u.log().Error("パスワードを読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	// 今のパスワードの間違いも、ログインと同じ数に入れる（止めている間は 429。protect.go）。
	ok, handled := u.checkPassword(w, func() bool {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)) == nil
	})
	if handled {
		return
	}
	// 401 はログインが切れたことに使うので、今のパスワードの違いは 403 にする。
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "今のパスワードが違う"})
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := u.Passwords.SetPasswordHash(r.Context(), string(b)); err != nil {
		u.log().Error("パスワードを保存できない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	u.log().Info("Web UI のパスワードを変えた")
	writeJSON(w, http.StatusOK, map[string]bool{"changed": true})
}

// startSession はセッションを作り、cookie を付けて {"authenticated": true} を返す（ログインと、パスワードを決めたとき）。
func (u *UI) startSession(w http.ResponseWriter, r *http.Request) {
	id, exp, err := u.Sessions.CreateSession(r.Context(), u.now())
	if err != nil {
		u.log().Error("セッションを作れない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: id, Path: "/", Expires: exp, MaxAge: int(store.SessionTTL / time.Second),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}
