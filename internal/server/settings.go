package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/config"
)

// Settings は設定の API が使う設定の正本（app.Settings が満たす）。
type Settings interface {
	// View は今の設定の、秘密の値を除いた見え方。
	View() config.View
	// Current は今の設定（通知先の種類と会員種別を読む。秘密は API に出さない）。
	Current() config.Config
	// Apply は設定に p を当てて保存し、すぐに効かせる。値の誤りは *app.InvalidSettingsError。
	Apply(ctx context.Context, p config.Patch) (config.Config, error)
}

var _ Settings = (*app.Settings)(nil)

// maxSettingsBody は PATCH /api/settings の本文の大きさの上限。
const maxSettingsBody = 1 << 20

// registerSettings は設定の API を mux に載せる。Settings が nil なら載せない。
//
//   - GET /api/settings: 今の設定（秘密の値は返さず、秘密ごとに設定済みか・復号できないかを返す）と、保存先が決まっているか、
//     復号できない秘密の名前
//   - PATCH /api/settings: 本文に書いた項目だけを変える（キーは config.Patch の JSON のキー。秘密の空の文字列は消す）。誤りが 1 つでも
//     あれば 400 にして何も当てない。保存先を変えるときは、フォルダを作り、書けることを確かめてから保存する。答えは GET と同じ
func (u *UI) registerSettings(mux *http.ServeMux) {
	if u.Settings == nil {
		return
	}
	mux.HandleFunc("GET /api/settings", u.guard(u.authed(u.getSettings)))
	mux.HandleFunc("PATCH /api/settings", u.guard(u.authed(u.patchSettings)))
}

// settingsResponse は GET /api/settings と PATCH /api/settings の答え。設定の見え方に、最後に自動のバックアップを取れた時刻を足す。
type settingsResponse struct {
	config.View
	// LastBackupAt は最後に自動のバックアップを取れた時刻。まだ取れていない・読めないときは null。
	LastBackupAt *time.Time `json:"last_backup_at"`
}

func (u *UI) settingsResponse(ctx context.Context) settingsResponse {
	resp := settingsResponse{View: u.Settings.View()}
	if u.Backups != nil {
		switch t, err := u.Backups.LastBackupAt(ctx); {
		case err != nil:
			u.log().Warn("最後に自動のバックアップを取れた時刻を読めない", "err", err)
		case !t.IsZero():
			resp.LastBackupAt = &t
		}
	}
	return resp
}

func (u *UI) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, u.settingsResponse(r.Context()))
}

func (u *UI) patchSettings(w http.ResponseWriter, r *http.Request) {
	var p config.Patch
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSettingsBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		msg := "JSON が読めない"
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			msg = "知らないキー " + strings.TrimPrefix(err.Error(), "json: unknown field ")
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if _, err := u.Settings.Apply(r.Context(), p); err != nil {
		var invalid *app.InvalidSettingsError
		if errors.As(err, &invalid) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalid.Error()})
			return
		}
		u.log().Error("設定を保存できない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	writeJSON(w, http.StatusOK, u.settingsResponse(r.Context()))
}
