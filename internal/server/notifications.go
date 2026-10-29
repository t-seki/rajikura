package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// Notifications は通知の API が使う保存先（store.Store が満たす）。
type Notifications interface {
	Notifications(ctx context.Context) ([]store.Notification, error)
	MarkNotificationsRead(ctx context.Context) (int64, error)
	MarkNotificationRead(ctx context.Context, id int64) error
	UnreadNotificationCount(ctx context.Context) (int, error)
	FailedNotificationCount(ctx context.Context) (int, error)
	OngoingAnomalies(ctx context.Context) ([]store.Notification, error)
}

var _ Notifications = (*store.Store)(nil)

// TestNotifier はテスト通知を送る（app.App が満たす）。
type TestNotifier interface {
	SendTestNotification(ctx context.Context) (app.TestNotificationResult, error)
}

var _ TestNotifier = (*app.App)(nil)

// channelNone は通知先なし（config.ChannelNone と同じ値）。
const channelNone = "none"

// notifyChannel は通知先の種類。Settings があれば設定の写しから読む。空なら通知先なし。
func (u *UI) notifyChannel() string {
	if u.Settings != nil {
		if c := u.Settings.Current().NotifyChannel; c != "" {
			return c
		}
		return channelNone
	}
	if u.NotifyChannel == "" {
		return channelNone
	}
	return u.NotifyChannel
}

// registerNotifications は通知の API を mux に載せる。Notifications が nil なら載せない。
//
//   - GET /api/notifications: 通知の履歴（新しい順）。指す画面がある通知には link（notificationLink）を付ける
//   - POST /api/notifications/read: 未読の通知を全部既読にする（Web UI は履歴のページを開いたときに呼ぶ）
//   - POST /api/notifications/{id}/read: その 1 件を既読にして 204（もう既読でも 204。行が無い、または id が整数でなければ 404）。
//     トーストから通知が指す画面を開いたとき、Web UI が URL の notification=<id> で呼ぶ
//   - GET /api/notifications/status: ナビの未読の数と、どのページの上部にも出す帯の元
//     （まだ復旧していないサービスの異常と、通知先に送れなかった通知の数）と、通知先の種類
//   - POST /api/notifications/test: テスト通知をその場で通知先に送り、結果を付けた行を書く。通知先なしなら送らずに
//     行だけ書いて 200（noChannel）。テスト通知の送り手がつながっていなければ 409
func (u *UI) registerNotifications(mux *http.ServeMux) {
	if u.Notifications == nil {
		return
	}
	mux.HandleFunc("GET /api/notifications", u.guard(u.authed(u.listNotifications)))
	mux.HandleFunc("POST /api/notifications/read", u.guard(u.authed(u.readNotifications)))
	mux.HandleFunc("POST /api/notifications/{id}/read", u.guard(u.authed(u.readNotification)))
	mux.HandleFunc("GET /api/notifications/status", u.guard(u.authed(u.notificationStatus)))
	mux.HandleFunc("POST /api/notifications/test", u.guard(u.authed(u.sendTestNotification)))
}

type notificationView struct {
	ID int64 `json:"id"`
	// Kind は通知の種類（notify.Kinds のどれか）。
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
	// Failed は「送れなかった」の印（通知先に送ろうとして送れず、諦めた）。
	Failed bool `json:"failed"`
	Read   bool `json:"read"`
	// Link は、通知が指す画面の Web UI のパス（notificationLink）。指す先が無ければキーごと出さない。
	Link string `json:"link,omitempty"`
}

// notificationLink は、通知 n が指す Web UI の画面のパスを返す（ADR 0024 の「通知から画面を開く」）。Web UI の通知の画面とトレイのトーストが
// これを開く。接続先の URL は含めない。
//   - ジョブを持つ通知（ジョブの出来事）: /jobs/{id}
//   - 番組を持つ通知（keyword_added・unmatched_day）: /shows/{名前}（名前はパスの 1 区切りとしてエスケープする。`/` も %2F）
//   - keyword_limit: /keywords（種類から決める。列を足す前からある行も同じ）
//   - それ以外（サービスの異常・復旧・テスト通知と、列を足す前からある行）: 空（付けない）
//
// 指す先が今もあるかは確かめない（無ければ Web UI の画面が一覧へ移す）。
func notificationLink(n store.Notification) string {
	switch {
	case n.JobID != 0:
		return "/jobs/" + strconv.FormatInt(n.JobID, 10)
	case n.Show != "":
		return "/shows/" + url.PathEscape(n.Show)
	case n.Kind == notify.KindKeywordLimit:
		return "/keywords"
	}
	return ""
}

func (u *UI) listNotifications(w http.ResponseWriter, r *http.Request) {
	ns, err := u.Notifications.Notifications(r.Context())
	if err != nil {
		u.internalError(w, "通知を読めない", err)
		return
	}
	out := make([]notificationView, 0, len(ns))
	for _, n := range ns {
		out = append(out, newNotificationView(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out})
}

func newNotificationView(n store.Notification) notificationView {
	return notificationView{
		ID: n.ID, Kind: n.Kind, Message: n.Message, CreatedAt: n.CreatedAt,
		Failed: !n.FailedAt.IsZero(), Read: n.Read, Link: notificationLink(n),
	}
}

func (u *UI) readNotifications(w http.ResponseWriter, r *http.Request) {
	n, err := u.Notifications.MarkNotificationsRead(r.Context())
	if err != nil {
		u.internalError(w, "通知を既読にできない", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"marked": n})
}

func (u *UI) readNotification(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "通知が無い"})
		return
	}
	switch err := u.Notifications.MarkNotificationRead(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "通知が無い"})
	case err != nil:
		u.internalError(w, "通知を既読にできない", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type anomalyView struct {
	// Kind は異常の種類（notify.KindSync・notify.KindOutOfArea・notify.KindBackup・notify.KindWake・notify.KindRescan）。
	// 足したら直す: サービスの異常
	Kind string `json:"kind"`
	// Message は異常の始まりの通知の本文。Since はその通知を作った時刻。
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

func (u *UI) notificationStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	unread, err := u.Notifications.UnreadNotificationCount(ctx)
	if err != nil {
		u.internalError(w, "未読の通知を数えられない", err)
		return
	}
	// 通知先なしの間は「送れなかった通知」の帯を出さない（送れなかった個々の行は履歴に残る）。
	failed := 0
	if u.notifyChannel() != channelNone {
		failed, err = u.Notifications.FailedNotificationCount(ctx)
		if err != nil {
			u.internalError(w, "送れなかった通知を数えられない", err)
			return
		}
	}
	as, err := u.Notifications.OngoingAnomalies(ctx)
	if err != nil {
		u.internalError(w, "続いているサービスの異常を読めない", err)
		return
	}
	anomalies := make([]anomalyView, 0, len(as))
	for _, a := range as {
		anomalies = append(anomalies, anomalyView{Kind: a.Anomaly, Message: a.Message, Since: a.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"unread": unread, "failed": failed, "anomalies": anomalies, "channel": u.notifyChannel(),
	})
}

// sendTestNotification はテスト通知を送る。送れても送れなくても行を書いて 200 を返し、
// {"sent": <送れたか>, "error": <送れなかったときの通知先のエラー>, "notification": <書いた行>} を返す。
// 通知先なしなら送らずに行だけ書いて 200 と {"sent": false, "noChannel": true, "notification": <書いた行>} を返す。
// テスト通知の送り手がつながっていなければ（TestNotifier が nil）409。
func (u *UI) sendTestNotification(w http.ResponseWriter, r *http.Request) {
	if u.TestNotifier == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "テスト通知を送る口がつながっていない"})
		return
	}
	res, err := u.TestNotifier.SendTestNotification(r.Context())
	if err != nil {
		u.internalError(w, "テスト通知を書けない", err)
		return
	}
	body := map[string]any{"sent": res.SendErr == nil && !res.NoChannel, "notification": newNotificationView(res.Notification)}
	if res.NoChannel {
		body["noChannel"] = true
	} else if res.SendErr != nil {
		body["error"] = res.SendErr.Error()
	} else {
		body["sentAt"] = res.Notification.SentAt
	}
	writeJSON(w, http.StatusOK, body)
}
