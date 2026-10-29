package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

func TestDeadlineNearJobs(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	insert := func(part int, deadline time.Time, state domain.State) int64 {
		t.Helper()
		a := flat1
		a.Start = a.Start.Add(time.Duration(part) * time.Hour)
		id, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, Part: part, State: state, Airings: []domain.Airing{a},
			NextAttemptAt: jst(2026, 10, 5, 12, 0), Deadline: deadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	now := jst(2026, 10, 12, 12, 0)
	near := insert(1, jst(2026, 10, 13, 5, 0), domain.StateRetrying)
	waiting := insert(2, jst(2026, 10, 13, 11, 0), domain.StateWaiting)
	insert(3, jst(2026, 10, 13, 13, 0), domain.StateRetrying) // 24 時間より先
	insert(4, jst(2026, 10, 13, 5, 0), domain.StateDone)
	insert(5, jst(2026, 10, 13, 5, 0), domain.StateExpired)

	jobs, err := s.DeadlineNearJobs(ctx, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != near || jobs[1].ID != waiting || len(jobs[0].Airings) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}

	if _, err := s.AddDeadlineNearNotification(ctx, near, Notification{Kind: notify.KindDeadlineNear, Message: "期限間近", CreatedAt: now, Deliver: true}); err != nil {
		t.Fatal(err)
	}
	if jobs, err = s.DeadlineNearJobs(ctx, now, 24*time.Hour); err != nil || len(jobs) != 1 || jobs[0].ID != waiting {
		t.Fatalf("通知した後: %+v, %v", jobs, err)
	}
	// ジョブが無ければ、通知も書かない。
	if _, err := s.AddDeadlineNearNotification(ctx, 9999, Notification{Kind: notify.KindDeadlineNear, Message: "x", CreatedAt: now}); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}
	if ns, err := s.Notifications(ctx); err != nil || len(ns) != 1 || ns[0].Message != "期限間近" {
		t.Errorf("通知 = %+v, %v", ns, err)
	}
}

// 通知は書いた順に、送るもの（Deliver）だけがまだ送っていない間 Pending に出る。送れた・諦めたものは出ない。
func TestNotificationsPending(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	now := jst(2026, 10, 12, 12, 0)
	add := func(kind string, deliver bool) int64 {
		t.Helper()
		id, err := s.AddNotification(ctx, Notification{Kind: kind, Message: "本文 " + kind, CreatedAt: now, Deliver: deliver})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := add(notify.KindRecorded, true)
	add(notify.KindRecorded, false) // 送らない（通知先なし・成功の通知を止めた）
	b := add(notify.KindExpired, true)
	c := add(notify.KindMissing, true)

	ids := func() []int64 {
		t.Helper()
		ns, err := s.PendingNotifications(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return out
	}
	if got := ids(); !slices.Equal(got, []int64{a, b, c}) {
		t.Fatalf("Pending = %v", got)
	}
	if err := s.MarkNotificationSent(ctx, a, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNotificationFailed(ctx, b, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []int64{c}) {
		t.Fatalf("送った・諦めた後の Pending = %v", got)
	}
	if err := s.MarkNotificationSent(ctx, 9999, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("無い通知: %v", err)
	}

	// 開き直しても残る。
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ns, err := s2.Notifications(ctx)
	if err != nil || len(ns) != 4 {
		t.Fatalf("Notifications = %+v, %v", ns, err)
	}
	got := ns[3]
	if got.ID != a || got.Kind != notify.KindRecorded || got.Message != "本文 recorded" || !got.CreatedAt.Equal(now) ||
		!got.Deliver || !got.SentAt.Equal(now.Add(time.Minute)) || !got.FailedAt.IsZero() || got.Read {
		t.Errorf("読み直した通知 = %+v", got)
	}
	if !ns[1].FailedAt.Equal(now.Add(time.Hour)) {
		t.Errorf("諦めた印 = %+v", ns[1])
	}
}

// サービスの異常の始まりは続いていないときだけ、復旧は続いているときだけ書く。判定は通知の行から行う。
func TestAnomalyNotification(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	now := jst(2026, 10, 12, 12, 0)
	add := func(kind, anomaly string) bool {
		t.Helper()
		ok, err := s.AddAnomalyNotification(ctx, Notification{Kind: kind, Anomaly: anomaly, Message: kind, CreatedAt: now, Deliver: true})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	ongoing := func(anomaly string) bool {
		t.Helper()
		ok, err := s.AnomalyOngoing(ctx, anomaly)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if add(notify.KindRecovered, notify.KindSync) {
		t.Error("始まりが無いのに復旧を書いた")
	}
	if !add(notify.KindSync, notify.KindSync) || !ongoing(notify.KindSync) {
		t.Fatal("始まりを書かない")
	}
	if ongoing(notify.KindOutOfArea) {
		t.Error("別の異常まで続いている扱い")
	}
	if add(notify.KindSync, notify.KindSync) {
		t.Error("続いている間に始まりをもう一度書いた")
	}

	// 開き直しても（再起動しても）続いている。
	s.Close()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !ongoing(notify.KindSync) {
		t.Fatal("開き直したら続いていない扱い")
	}
	if !add(notify.KindRecovered, notify.KindSync) || ongoing(notify.KindSync) {
		t.Fatal("復旧を書かない")
	}
	if add(notify.KindRecovered, notify.KindSync) {
		t.Error("復旧をもう一度書いた")
	}
	if !add(notify.KindSync, notify.KindSync) {
		t.Error("復旧の後の始まりを書かない")
	}
	if _, err := s.AddAnomalyNotification(ctx, Notification{Kind: notify.KindSync, Message: "x", CreatedAt: now}); err == nil {
		t.Error("anomaly が無いのに書けた")
	}
}

// 古い通知は消す。ただし、まだ復旧していないサービスの異常の始まりは消さない。
func TestDeleteNotificationsBefore(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	old := jst(2026, 7, 1, 12, 0)
	recent := jst(2026, 10, 12, 12, 0)
	add := func(kind, anomaly string, at time.Time) int64 {
		t.Helper()
		id, err := s.AddNotification(ctx, Notification{Kind: kind, Anomaly: anomaly, Message: kind, CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add(notify.KindRecorded, "", old)                                // 消す
	add(notify.KindSync, notify.KindSync, old)                       // 復旧済みの始まり: 消す
	add(notify.KindRecovered, notify.KindSync, old)                  // 消す
	keepArea := add(notify.KindOutOfArea, notify.KindOutOfArea, old) // まだ復旧していない: 残す
	add(notify.KindSync, notify.KindSync, old)                       // 2 度目の始まりで、復旧は新しい: 消す
	keepRecovered := add(notify.KindRecovered, notify.KindSync, recent)
	keepRecent := add(notify.KindExpired, "", recent)

	n, err := s.DeleteNotificationsBefore(ctx, recent.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ns, err := s.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, x := range ns {
		ids = append(ids, x.ID)
	}
	if n != 4 || !slices.Equal(ids, []int64{keepRecent, keepRecovered, keepArea}) {
		t.Fatalf("消した数 %d、残り %+v", n, ns)
	}
	if ok, err := s.AnomalyOngoing(ctx, notify.KindOutOfArea); err != nil || !ok {
		t.Errorf("消した後に、続いているエリア外が分からない: %v, %v", ok, err)
	}
}

// 版 13 までの DB（日次サマリを最後に送った日を notify_state に持つ）は、版 14 で notify_state を消し、notifications を作る。
func TestMigrateNotifications(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v13.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 13 {
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-10-01T00:00:00.000Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO notify_state (key, value) VALUES ('daily_summary_sent_on', '2026-10-14')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'notify_state'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("notify_state が残っている: %d, %v", n, err)
	}
	if _, err := s.AddNotification(ctx, Notification{Kind: notify.KindRecorded, Message: "x", CreatedAt: jst(2026, 10, 15, 12, 0)}); err != nil {
		t.Errorf("notifications に書けない: %v", err)
	}
}

// 未読の数は既読にするまで数え、既読にすると全部が既読になる。後から書いた通知はまた未読。
func TestMarkNotificationsRead(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 15, 12, 0)
	unread := func() int {
		t.Helper()
		n, err := s.UnreadNotificationCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := unread(); n != 0 {
		t.Fatalf("通知が無いのに未読 = %d", n)
	}
	for range 2 {
		if _, err := s.AddNotification(ctx, Notification{Kind: notify.KindRecorded, Message: "x", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if n := unread(); n != 2 {
		t.Fatalf("未読 = %d, want 2", n)
	}
	if k, err := s.MarkNotificationsRead(ctx); err != nil || k != 2 {
		t.Fatalf("既読にした数 = %d, %v", k, err)
	}
	if n := unread(); n != 0 {
		t.Fatalf("既読にした後の未読 = %d", n)
	}
	ns, err := s.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if !n.Read {
			t.Errorf("既読になっていない: %+v", n)
		}
	}
	if _, err := s.AddNotification(ctx, Notification{Kind: notify.KindExpired, Message: "y", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n := unread(); n != 1 {
		t.Fatalf("後から書いた通知の未読 = %d, want 1", n)
	}
	if k, err := s.MarkNotificationsRead(ctx); err != nil || k != 1 {
		t.Fatalf("2 度目に既読にした数 = %d, %v", k, err)
	}
}

// MarkNotificationRead は指した 1 件だけを既読にする。もう既読でも成功し、行が無ければ ErrNotFound。
func TestMarkNotificationRead(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 15, 12, 0)
	var ids []int64
	for range 2 {
		id, err := s.AddNotification(ctx, Notification{Kind: notify.KindRecorded, Message: "x", CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for i := range 2 {
		if err := s.MarkNotificationRead(ctx, ids[0]); err != nil {
			t.Fatalf("%d 回目: %v", i+1, err)
		}
	}
	if n, err := s.UnreadNotificationCount(ctx); err != nil || n != 1 {
		t.Fatalf("未読 = %d, %v, want 1", n, err)
	}
	if err := s.MarkNotificationRead(ctx, ids[1]+100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("行が無いとき = %v, want ErrNotFound", err)
	}
}

// 送れなかった通知の数は、最後に送れた時刻より後に送れなかった印を付けたものだけを数える。
// 送れなかった後に送れた通知があれば 0（通知先が直った）。
func TestFailedNotificationCount(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	base := jst(2026, 10, 15, 12, 0)
	add := func() int64 {
		t.Helper()
		id, err := s.AddNotification(ctx, Notification{Kind: notify.KindRecorded, Message: "x", CreatedAt: base, Deliver: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	count := func() int {
		t.Helper()
		n, err := s.FailedNotificationCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("通知が無いのに %d", n)
	}
	// 送れた通知だけなら 0。
	if err := s.MarkNotificationSent(ctx, add(), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("送れた通知だけなのに %d", n)
	}
	// 最後に送れた後に 2 つ送れなかった。
	for i := range 2 {
		if err := s.MarkNotificationFailed(ctx, add(), base.Add(time.Duration(2+i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(); n != 2 {
		t.Fatalf("送れなかった数 = %d, want 2", n)
	}
	// 送れなかった後に送れた通知があれば、直ったとみなして 0。送れなかった通知の印は残る。
	if err := s.MarkNotificationSent(ctx, add(), base.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("送れた後なのに %d", n)
	}
	ns, err := s.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, n := range ns {
		if !n.FailedAt.IsZero() {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("送れなかった印の付いた通知 = %d, want 2", failed)
	}
	// また送れなかったら、最後に送れた時刻より後のものだけを数える。
	if err := s.MarkNotificationFailed(ctx, add(), base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("また送れなかった数 = %d, want 1", n)
	}
}

// まだ復旧していないサービスの異常は、その異常の最後の行が始まりのもので、異常ごとに 1 行。
func TestOngoingAnomalies(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 15, 12, 0)
	add := func(kind, anomaly, msg string) {
		t.Helper()
		if _, err := s.AddAnomalyNotification(ctx, Notification{Kind: kind, Anomaly: anomaly, Message: msg, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	names := func() []string {
		t.Helper()
		ns, err := s.OngoingAnomalies(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range ns {
			out = append(out, n.Anomaly+":"+n.Message)
		}
		return out
	}
	if got := names(); len(got) != 0 {
		t.Fatalf("異常が無いのに %v", got)
	}
	// ジョブの出来事は異常ではない。
	if _, err := s.AddNotification(ctx, Notification{Kind: notify.KindExpired, Message: "x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	add(notify.KindSync, notify.KindSync, "同期1")
	add(notify.KindOutOfArea, notify.KindOutOfArea, "エリア外1")
	if got := names(); !slices.Equal(got, []string{"out_of_area:エリア外1", "sync:同期1"}) {
		t.Fatalf("続いている異常 = %v", got)
	}
	add(notify.KindRecovered, notify.KindSync, "復旧")
	if got := names(); !slices.Equal(got, []string{"out_of_area:エリア外1"}) {
		t.Fatalf("同期が復旧した後 = %v", got)
	}
	add(notify.KindSync, notify.KindSync, "同期2")
	if got := names(); !slices.Equal(got, []string{"out_of_area:エリア外1", "sync:同期2"}) {
		t.Fatalf("また同期が失敗した後 = %v", got)
	}
}

// 送れた通知が 1 つも無いときは、送れなかった印の付いた通知を全部数える。
func TestFailedNotificationCountNeverSent(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	base := jst(2026, 10, 15, 12, 0)
	id, err := s.AddNotification(ctx, Notification{Kind: notify.KindRecorded, Message: "x", CreatedAt: base, Deliver: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNotificationFailed(ctx, id, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FailedNotificationCount(ctx); err != nil || n != 1 {
		t.Fatalf("送れなかった数 = %d, %v, want 1", n, err)
	}
}
