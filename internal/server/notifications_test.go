package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

func newNotificationsUI(t *testing.T) (http.Handler, *store.Store, *http.Cookie) {
	t.Helper()
	return newNotificationsUIWith(t, func(*UI, *store.Store) {})
}

// newNotificationsUIWith は、通知先を discord にした通知の API を立てる。setup で UI を変えられる（store は作ったもの）。
func newNotificationsUIWith(t *testing.T, setup func(*UI, *store.Store)) (http.Handler, *store.Store, *http.Cookie) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ui := &UI{
		Sessions: st, Tokens: st, Notifications: st, Passwords: passwords(string(b)), NotifyChannel: "discord",
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
	}
	setup(ui, st)
	h := Handler(Health{LastPoll: time.Now}, ui)
	return h, st, login(t, h)
}

type notificationStatusBody struct {
	Unread    int    `json:"unread"`
	Failed    int    `json:"failed"`
	Channel   string `json:"channel"`
	Anomalies []struct {
		Kind    string    `json:"kind"`
		Message string    `json:"message"`
		Since   time.Time `json:"since"`
	} `json:"anomalies"`
}

func TestNotificationsRequireLogin(t *testing.T) {
	h, _, _ := newNotificationsUI(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/notifications"}, {"POST", "/api/notifications/read"}, {"GET", "/api/notifications/status"},
		{"POST", "/api/notifications/test"}, {"POST", "/api/notifications/1/read"},
	} {
		if rec := do(h, c.method, c.path, ""); rec.Code != 401 {
			t.Errorf("%s %s = %d、want 401", c.method, c.path, rec.Code)
		}
	}
}

// 履歴は新しい順で、送れなかった印と既読を付ける。履歴のページを開いたら（POST .../read）全部既読になり、未読の数が 0 になる。
func TestNotificationsHistoryAndRead(t *testing.T) {
	h, st, cookie := newNotificationsUI(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	first, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindRecorded, Message: "【録音済み】a", CreatedAt: at, Deliver: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindExpired, Message: "【録音できなかった】b", CreatedAt: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNotificationFailed(ctx, first, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	var status notificationStatusBody
	rec := do(h, "GET", "/api/notifications/status", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	decode(t, rec.Body.Bytes(), &status)
	if status.Unread != 2 || status.Failed != 1 || len(status.Anomalies) != 0 || status.Channel != "discord" {
		t.Fatalf("status = %+v", status)
	}

	var list struct {
		Notifications []struct {
			ID        int64     `json:"id"`
			Kind      string    `json:"kind"`
			Message   string    `json:"message"`
			CreatedAt time.Time `json:"createdAt"`
			Failed    bool      `json:"failed"`
			Read      bool      `json:"read"`
		} `json:"notifications"`
	}
	rec = do(h, "GET", "/api/notifications", "", cookie)
	if rec.Code != 200 {
		t.Fatalf("一覧 = %d %s", rec.Code, rec.Body)
	}
	decode(t, rec.Body.Bytes(), &list)
	ns := list.Notifications
	if len(ns) != 2 || ns[0].Kind != notify.KindExpired || ns[1].ID != first || !ns[1].Failed || ns[0].Failed || ns[0].Read ||
		!ns[1].CreatedAt.Equal(at) || ns[1].Message != "【録音済み】a" {
		t.Fatalf("一覧 = %+v", ns)
	}

	if rec := do(h, "POST", "/api/notifications/read", "", cookie); rec.Code != 200 {
		t.Fatalf("既読 = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/notifications/status", "", cookie)
	decode(t, rec.Body.Bytes(), &status)
	// 既読にしても、送れなかった通知の帯は消えない（既読とは関係が無い）。
	if status.Unread != 0 || status.Failed != 1 {
		t.Fatalf("既読にした後の status = %+v", status)
	}
	rec = do(h, "GET", "/api/notifications", "", cookie)
	decode(t, rec.Body.Bytes(), &list)
	for _, n := range list.Notifications {
		if !n.Read {
			t.Errorf("既読になっていない: %+v", n)
		}
	}
}

// POST /api/notifications/{id}/read はその 1 件だけを既読にして 204 を返す。もう既読でも 204、行が無い・id が整数でなければ 404。
// API トークンでも叩ける。GET /api/notifications の link にはクエリが付かない。
func TestReadOneNotification(t *testing.T) {
	h, st, cookie := newNotificationsUI(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	a, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindExpired, Message: "a", CreatedAt: at, JobID: 12})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindExpired, Message: "b", CreatedAt: at, JobID: 13})
	if err != nil {
		t.Fatal(err)
	}
	path := func(id string) string { return "/api/notifications/" + id + "/read" }
	for i := range 2 { // 2 度目はもう既読
		if rec := do(h, "POST", path(strconv.FormatInt(a, 10)), "", cookie); rec.Code != 204 {
			t.Fatalf("%d 回目の既読 = %d %s", i+1, rec.Code, rec.Body)
		}
	}
	var list struct {
		Notifications []struct {
			ID   int64  `json:"id"`
			Read bool   `json:"read"`
			Link string `json:"link"`
		} `json:"notifications"`
	}
	decode(t, do(h, "GET", "/api/notifications", "", cookie).Body.Bytes(), &list)
	if len(list.Notifications) != 2 {
		t.Fatalf("一覧 = %+v", list)
	}
	for _, n := range list.Notifications {
		switch n.ID {
		case a:
			if !n.Read || n.Link != "/jobs/12" {
				t.Errorf("既読にした通知 = %+v", n)
			}
		case b:
			if n.Read || n.Link != "/jobs/13" {
				t.Errorf("既読にしていない通知 = %+v", n)
			}
		}
	}
	for _, id := range []string{"999999", "abc", "1.5", "-"} {
		if rec := do(h, "POST", path(id), "", cookie); rec.Code != 404 {
			t.Errorf("id %q = %d、want 404", id, rec.Code)
		}
	}
	_, token, err := st.CreateAPIToken(ctx, "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := doBearer(h, "POST", path(strconv.FormatInt(b, 10)), "", token); rec.Code != 204 {
		t.Errorf("Bearer で既読 = %d", rec.Code)
	}
}

// 送れなかった通知の帯は、最後に送れなかった後に送れた通知があれば消える。送れなかった通知は履歴に印付きで残る。
func TestNotificationStatusFailedClearsAfterSent(t *testing.T) {
	h, st, cookie := newNotificationsUI(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	add := func() int64 {
		t.Helper()
		id, err := st.AddNotification(ctx, store.Notification{Kind: notify.KindRecorded, Message: "x", CreatedAt: at, Deliver: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for i := range 2 {
		if err := st.MarkNotificationFailed(ctx, add(), at.Add(time.Duration(1+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var status notificationStatusBody
	decode(t, do(h, "GET", "/api/notifications/status", "", cookie).Body.Bytes(), &status)
	if status.Failed != 2 {
		t.Fatalf("送れなかった数 = %d, want 2", status.Failed)
	}
	if err := st.MarkNotificationSent(ctx, add(), at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	decode(t, do(h, "GET", "/api/notifications/status", "", cookie).Body.Bytes(), &status)
	if status.Failed != 0 {
		t.Fatalf("送れた後の送れなかった数 = %d, want 0", status.Failed)
	}
}

// 帯のサービスの異常は、まだ復旧していないものだけを出す。トークンでも読める。
func TestNotificationStatusAnomalies(t *testing.T) {
	h, st, cookie := newNotificationsUI(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	add := func(kind, anomaly, msg string, created time.Time) {
		t.Helper()
		if _, err := st.AddAnomalyNotification(ctx, store.Notification{Kind: kind, Anomaly: anomaly, Message: msg, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	add(notify.KindOutOfArea, notify.KindOutOfArea, "【エリア外】x", at)
	add(notify.KindSync, notify.KindSync, "【番組表の同期の失敗】y", at.Add(time.Hour))
	add(notify.KindRecovered, notify.KindOutOfArea, "【復旧】x", at.Add(2*time.Hour))

	var status notificationStatusBody
	decode(t, do(h, "GET", "/api/notifications/status", "", cookie).Body.Bytes(), &status)
	if len(status.Anomalies) != 1 || status.Anomalies[0].Kind != notify.KindSync ||
		status.Anomalies[0].Message != "【番組表の同期の失敗】y" || !status.Anomalies[0].Since.Equal(at.Add(time.Hour)) {
		t.Fatalf("anomalies = %+v", status.Anomalies)
	}

	_, token, err := st.CreateAPIToken(ctx, "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/notifications"}, {"POST", "/api/notifications/read"}, {"GET", "/api/notifications/status"},
	} {
		if rec := doBearer(h, c.method, c.path, "", token); rec.Code != 200 {
			t.Errorf("Bearer で %s %s = %d", c.method, c.path, rec.Code)
		}
	}
}
