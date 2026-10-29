package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/metrics"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// fakeNotifier は通知先の偽物。err を返し、受けた種類と本文を持つ。
type fakeNotifier struct {
	err   error
	kinds []string
	msgs  []string
}

func (f *fakeNotifier) Notify(_ context.Context, kind, msg string) error {
	f.kinds = append(f.kinds, kind)
	f.msgs = append(f.msgs, msg)
	return f.err
}

type testNotificationBody struct {
	Sent         bool      `json:"sent"`
	NoChannel    *bool     `json:"noChannel"`
	Error        string    `json:"error"`
	SentAt       time.Time `json:"sentAt"`
	Notification struct {
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Failed  bool   `json:"failed"`
		Read    bool   `json:"read"`
	} `json:"notification"`
}

// newTestNotificationUI は、偽の通知先 n（nil なら通知先なし）に送る app.App をテスト通知の送り手にし、
// 通知先の種類を channel にした通知の API を立てる。app.App は notify.success = false（MuteSuccess）にしておく。
func newTestNotificationUI(t *testing.T, n notify.Notifier, channel string) (http.Handler, *store.Store, *http.Cookie, *metrics.Metrics) {
	t.Helper()
	mt := metrics.New()
	mt.InitNotifyKinds(app.NotifyKinds...)
	h, st, cookie := newNotificationsUIWith(t, func(ui *UI, st *store.Store) {
		ui.NotifyChannel = channel
		a := &app.App{
			Store: st, Metrics: mt, MuteSuccess: true,
			Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
		}
		if n != nil {
			a.Notifier = n
		}
		ui.TestNotifier = a
	})
	return h, st, cookie, mt
}

// addFailed は「送れなかった」の印の付いた通知を 1 つ書く。
func addFailed(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	id, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindExpired, Message: "x", CreatedAt: at, Deliver: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNotificationFailed(ctx, id, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, h http.Handler, cookie *http.Cookie) notificationStatusBody {
	t.Helper()
	rec := do(h, "GET", "/api/notifications/status", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	var s notificationStatusBody
	decode(t, rec.Body.Bytes(), &s)
	return s
}

// テスト通知を送れたら、種類 test・既読・送れた時刻の付いた行を書き、送れなかった通知の帯が消える。
// notify.success = false でも送る。送った行は送り直さない。
func TestTestNotificationSent(t *testing.T) {
	fn := &fakeNotifier{}
	h, st, cookie, _ := newTestNotificationUI(t, fn, "discord")
	ctx := context.Background()
	addFailed(t, st)
	if s := status(t, h, cookie); s.Failed != 1 {
		t.Fatalf("送る前の送れなかった数 = %d, want 1", s.Failed)
	}

	rec := do(h, "POST", "/api/notifications/test", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("テスト通知 = %d %s", rec.Code, rec.Body)
	}
	var body testNotificationBody
	decode(t, rec.Body.Bytes(), &body)
	if !body.Sent || body.Error != "" || body.SentAt.IsZero() || body.Notification.Kind != notify.KindTest ||
		body.Notification.Failed || !body.Notification.Read || body.NoChannel != nil {
		t.Fatalf("応答 = %s", rec.Body)
	}
	if len(fn.kinds) != 1 || fn.kinds[0] != notify.KindTest || fn.msgs[0] != "【テスト】rajikura からのテスト通知。" {
		t.Fatalf("送った = %q %q", fn.kinds, fn.msgs)
	}
	ns, err := st.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 || ns[0].ID != body.Notification.ID || ns[0].Kind != notify.KindTest || ns[0].SentAt.IsZero() ||
		!ns[0].FailedAt.IsZero() || !ns[0].Read || !ns[0].Deliver {
		t.Fatalf("行 = %+v", ns)
	}
	if p, err := st.PendingNotifications(ctx); err != nil || len(p) != 0 {
		t.Fatalf("まだ送れていない通知 = %+v, %v", p, err)
	}
	// 送れなかった帯が消え、テスト通知は既読なので未読の数に入らない（未読は先に書いた 1 件だけ）。
	if s := status(t, h, cookie); s.Failed != 0 || s.Unread != 1 {
		t.Fatalf("送った後の status = %+v", s)
	}
}

// テスト通知を送れなければ、通知先のエラーを返し、「送れなかった」の印の付いた行を書く（帯の件数に入る）。
// 送り直さない。/metrics の rajikura_notify_failures_total では数えず、kind="test" の系列も出さない。
func TestTestNotificationFailed(t *testing.T) {
	fn := &fakeNotifier{err: errors.New("discord: 状態 500 が返った")}
	h, st, cookie, mt := newTestNotificationUI(t, fn, "discord")
	rec := do(h, "POST", "/api/notifications/test", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("テスト通知 = %d %s", rec.Code, rec.Body)
	}
	var body testNotificationBody
	decode(t, rec.Body.Bytes(), &body)
	if body.Sent || body.Error != "discord: 状態 500 が返った" || !body.SentAt.IsZero() || !body.Notification.Failed ||
		body.Notification.Kind != notify.KindTest || !body.Notification.Read || body.NoChannel != nil {
		t.Fatalf("応答 = %s", rec.Body)
	}
	ctx := context.Background()
	ns, err := st.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].FailedAt.IsZero() || !ns[0].SentAt.IsZero() {
		t.Fatalf("行 = %+v", ns)
	}
	if p, err := st.PendingNotifications(ctx); err != nil || len(p) != 0 {
		t.Fatalf("送り直す通知に入った: %+v, %v", p, err)
	}
	if s := status(t, h, cookie); s.Failed != 1 {
		t.Fatalf("送れなかった数 = %d, want 1", s.Failed)
	}
	mrec := httptest.NewRecorder()
	mt.Handler().ServeHTTP(mrec, httptest.NewRequest("GET", "/metrics", nil))
	got := mrec.Body.String()
	if strings.Contains(got, `kind="test"`) {
		t.Errorf("テスト通知の系列が出ている: %s", got)
	}
	if !strings.Contains(got, `rajikura_notify_failures_total{kind="expired"} 0`) {
		t.Errorf("ほかの種類の系列が無い: %s", got)
	}
}

// 通知先が none なら、テスト通知を送らずに行だけ書き、200 と noChannel を返す。
// 行は種類 test・既読で、通知先に送る印も、送れた・送れなかったの印も付けず、履歴（GET /api/notifications）に出る
// （トレイがふだんのポーリングで拾える）。送り直す通知には入らない。status は通知先 none を返し、
// 送れなかった通知があっても帯の件数を 0 にする（送れなかった個々の行は履歴に残る）。
func TestTestNotificationNone(t *testing.T) {
	h, st, cookie, _ := newTestNotificationUI(t, nil, "none")
	addFailed(t, st)
	rec := do(h, "POST", "/api/notifications/test", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("テスト通知 = %d %s", rec.Code, rec.Body)
	}
	var body testNotificationBody
	decode(t, rec.Body.Bytes(), &body)
	if body.Sent || body.NoChannel == nil || !*body.NoChannel || body.Error != "" || !body.SentAt.IsZero() ||
		body.Notification.Kind != notify.KindTest || body.Notification.Failed || !body.Notification.Read ||
		body.Notification.Message != "【テスト】rajikura からのテスト通知。" {
		t.Fatalf("応答 = %s", rec.Body)
	}
	for _, k := range []string{`"error"`, `"sentAt"`} {
		if strings.Contains(rec.Body.String(), k) {
			t.Errorf("応答に %s が付いた: %s", k, rec.Body)
		}
	}
	ctx := context.Background()
	ns, err := st.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 || ns[0].ID != body.Notification.ID || ns[0].Kind != notify.KindTest || !ns[0].Read ||
		ns[0].Deliver || !ns[0].SentAt.IsZero() || !ns[0].FailedAt.IsZero() || ns[1].FailedAt.IsZero() {
		t.Fatalf("行 = %+v（テスト通知を書き、送れなかった行は残る）", ns)
	}
	if p, err := st.PendingNotifications(ctx); err != nil || len(p) != 0 {
		t.Fatalf("送り直す通知に入った: %+v, %v", p, err)
	}
	// 履歴の API にテスト通知の行が先頭に出る。
	lrec := do(h, "GET", "/api/notifications", "", cookie)
	if lrec.Code != 200 {
		t.Fatalf("履歴 = %d %s", lrec.Code, lrec.Body)
	}
	var list struct {
		Notifications []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"notifications"`
	}
	decode(t, lrec.Body.Bytes(), &list)
	if len(list.Notifications) != 2 || list.Notifications[0].ID != body.Notification.ID || list.Notifications[0].Kind != notify.KindTest {
		t.Fatalf("履歴 = %s", lrec.Body)
	}
	if s := status(t, h, cookie); s.Channel != "none" || s.Failed != 0 {
		t.Fatalf("status = %+v（通知先 none、送れなかった数 0）", s)
	}
}

// テスト通知の送り手がつながっていなければ（TestNotifier が nil）、409 を返す。
func TestTestNotificationNoNotifier(t *testing.T) {
	h, _, cookie := newNotificationsUIWith(t, func(ui *UI, _ *store.Store) { ui.NotifyChannel = "discord" })
	if rec := do(h, "POST", "/api/notifications/test", "", cookie); rec.Code != http.StatusConflict {
		t.Fatalf("テスト通知 = %d %s", rec.Code, rec.Body)
	}
}

// テスト通知の API は API トークンでも叩ける。status は通知先の種類を返す。
func TestTestNotificationBearer(t *testing.T) {
	fn := &fakeNotifier{}
	h, st, _, _ := newTestNotificationUI(t, fn, "slack")
	_, token, err := st.CreateAPIToken(context.Background(), "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := doBearer(h, "POST", "/api/notifications/test", "", token); rec.Code != 200 {
		t.Fatalf("Bearer でテスト通知 = %d %s", rec.Code, rec.Body)
	}
	if len(fn.kinds) != 1 {
		t.Fatalf("送った数 = %d", len(fn.kinds))
	}
	rec := doBearer(h, "GET", "/api/notifications/status", "", token)
	var s notificationStatusBody
	decode(t, rec.Body.Bytes(), &s)
	if s.Channel != "slack" {
		t.Fatalf("channel = %q", s.Channel)
	}
}
