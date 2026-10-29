package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/store"
)

// Tokens は API トークンの保存先（store.Store が満たす）。
type Tokens interface {
	CreateAPIToken(ctx context.Context, name string, now time.Time) (store.APIToken, string, error)
	ListAPITokens(ctx context.Context) ([]store.APIToken, error)
	DeleteAPIToken(ctx context.Context, id int64) error
	APITokenValid(ctx context.Context, token string, now time.Time) (bool, error)
}

var _ Tokens = (*store.Store)(nil)

// bearerToken は Authorization: Bearer <トークン> のトークンを返す。ヘッダーが無ければ false。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const p = "bearer "
	if len(h) < len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	return strings.TrimSpace(h[len(p):]), true
}

// registerTokens は API トークンの発行・一覧・失効を mux に載せる。権限は 1 種類なので、トークンでも叩ける。
func (u *UI) registerTokens(mux *http.ServeMux) {
	if u.Tokens == nil {
		return
	}
	mux.HandleFunc("GET /api/tokens", u.guard(u.authed(u.listTokens)))
	mux.HandleFunc("POST /api/tokens", u.guard(u.authed(u.createToken)))
	mux.HandleFunc("DELETE /api/tokens/{id}", u.guard(u.authed(u.deleteToken)))
}

type tokenView struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// LastUsedAt は最後に使った日時。まだ使っていなければ null。
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func viewOf(t store.APIToken) tokenView {
	v := tokenView{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt}
	if !t.LastUsedAt.IsZero() {
		used := t.LastUsedAt
		v.LastUsedAt = &used
	}
	return v
}

func (u *UI) listTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := u.Tokens.ListAPITokens(r.Context())
	if err != nil {
		u.internalError(w, "API トークンを一覧できない", err)
		return
	}
	out := make([]tokenView, 0, len(ts))
	for _, t := range ts {
		out = append(out, viewOf(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// createToken はトークンを発行する。トークンそのものは、この応答に 1 回だけ載る。
func (u *UI) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "名前を入れてほしい"})
		return
	}
	t, secret, err := u.Tokens.CreateAPIToken(r.Context(), name, u.now())
	if err != nil {
		u.internalError(w, "API トークンを発行できない", err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		tokenView
		Token string `json:"token"`
	}{viewOf(t), secret})
}

func (u *UI) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID が整数ではない"})
		return
	}
	err = u.Tokens.DeleteAPIToken(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "トークンが無い"})
		return
	}
	if err != nil {
		u.internalError(w, "API トークンを失効できない", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
