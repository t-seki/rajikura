package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// 通知が指すジョブの id と番組の名前は、書いたまま読める。指す先が無い通知（0・空）は NULL で書き、0・空で読める。
func TestNotificationTargetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	for _, n := range []Notification{
		{Kind: notify.KindRecorded, Message: "ジョブ", CreatedAt: at, JobID: 12},
		{Kind: notify.KindKeywordAdded, Message: "番組", CreatedAt: at, Show: "a/b 番組"},
		{Kind: notify.KindSync, Anomaly: notify.KindSync, Message: "異常", CreatedAt: at},
	} {
		if _, err := s.AddNotification(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	ns, err := s.Notifications(ctx)
	if err != nil || len(ns) != 3 {
		t.Fatalf("Notifications = %+v, %v", ns, err)
	}
	if ns[2].JobID != 12 || ns[2].Show != "" || ns[1].JobID != 0 || ns[1].Show != "a/b 番組" ||
		ns[0].JobID != 0 || ns[0].Show != "" {
		t.Errorf("指す先 = %+v", ns)
	}
	var nulls int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE job_id IS NULL AND show IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("指す先の無い行の NULL = %d, %v", nulls, err)
	}
}

// 番組の名前を変えると、その番組を指していた通知も新しい名前を指す。ほかの番組を指す通知は触らない。
// 名前を変えない置き換えでは何も変わらない。
func TestReplaceShowRenamesNotificationTarget(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	old := domain.NewShow("古い", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	keep := domain.NewShow("別の番組", domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 22}))
	if err := s.PutShows(ctx, []domain.Show{old, keep}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	for _, n := range []Notification{
		{Kind: notify.KindKeywordAdded, Message: "古い 1", CreatedAt: at, Show: "古い"},
		{Kind: notify.KindUnmatchedDay, Message: "古い 2", CreatedAt: at, Show: "古い"},
		{Kind: notify.KindUnmatchedDay, Message: "別", CreatedAt: at, Show: "別の番組"},
		{Kind: notify.KindRecorded, Message: "ジョブ", CreatedAt: at, JobID: 3},
	} {
		if _, err := s.AddNotification(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	renamed := domain.NewShow("新しい", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	if err := s.ReplaceShow(ctx, "古い", renamed); err != nil {
		t.Fatal(err)
	}
	ns, err := s.Notifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"古い 1": "新しい", "古い 2": "新しい", "別": "別の番組", "ジョブ": ""}
	for _, n := range ns {
		if n.Show != want[n.Message] {
			t.Errorf("%s の番組 = %q、want %q", n.Message, n.Show, want[n.Message])
		}
	}
	if ns[0].JobID != 3 {
		t.Errorf("ジョブの通知の id = %d", ns[0].JobID)
	}
}

// 版 20 までの DB の通知の行は、版 21 で指す先の列（job_id・show）が NULL のまま足され、0・空で読める。
// 本文から埋め戻さない。
func TestMigrateNotificationTarget(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v20.db")
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
		if m.version > 20 {
			break
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-10-01T00:00:00.000Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO notifications (kind, message, created_at, deliver) VALUES
		('recorded', '【録音済み】古い番組・放送日 2026-10-01（30 分）', '2026-10-01T00:00:00.000Z', 1),
		('keyword_added', 'キーワード', '2026-10-01T00:00:00.000Z', 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(ctx); err != nil || v < 21 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	ns, err := s.Notifications(ctx)
	if err != nil || len(ns) != 2 {
		t.Fatalf("Notifications = %+v, %v", ns, err)
	}
	for _, n := range ns {
		if n.JobID != 0 || n.Show != "" {
			t.Errorf("前からある行の指す先 = %+v", n)
		}
	}
}
