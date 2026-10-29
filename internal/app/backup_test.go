package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// backupFiles は dir の中のファイルの名前（辞書順）。
func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// backup_dir が空なら取らない。
func TestBackupOffWhenDirEmpty(t *testing.T) {
	e := newEnv(t, time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC))
	e.app.BackupIfDue(e.ctx)
	if at, err := e.store.LastBackupAt(e.ctx); err != nil || !at.IsZero() {
		t.Fatalf("LastBackupAt = %v, %v", at, err)
	}
}

// まだ取れていなければ取り、前に取れてから 24 時間が過ぎていなければ取らず、過ぎていれば取る（時刻は差し替えた時計で進める）。
func TestBackupOnlyWhenDue(t *testing.T) {
	start := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	e := newEnv(t, start)
	dir := filepath.Join(t.TempDir(), "backups")
	e.app.BackupDir = dir

	e.app.BackupIfDue(e.ctx)
	first := store.BackupFileName(start)
	if got := backupFiles(t, dir); !slices.Equal(got, []string{first}) {
		t.Fatalf("1 回目: %v", got)
	}
	if at, err := e.store.LastBackupAt(e.ctx); err != nil || !at.Equal(start) {
		t.Fatalf("LastBackupAt = %v, %v", at, err)
	}

	e.clock.Set(start.Add(24*time.Hour - time.Second))
	e.app.BackupIfDue(e.ctx)
	if got := backupFiles(t, dir); len(got) != 1 {
		t.Fatalf("24 時間たつ前に取った: %v", got)
	}

	e.clock.Set(start.Add(24 * time.Hour))
	e.app.BackupIfDue(e.ctx)
	second := store.BackupFileName(start.Add(24 * time.Hour))
	if got := backupFiles(t, dir); !slices.Equal(got, []string{first, second}) {
		t.Fatalf("24 時間たった後: %v", got)
	}
	// 取ったものは DB として開ける写しで、ログインのセッションを含まない（store の Backup が保証する）。
	if fi, err := os.Stat(filepath.Join(dir, second)); err != nil || fi.Size() == 0 {
		t.Fatalf("写しが空: %v", err)
	}
	if ns := e.notifications(); len(ns) != 0 {
		t.Fatalf("取れたのに通知した: %+v", ns)
	}
}

// 名前が rajikura-<日時>.db の形のファイルだけを、新しいものから backup_keep 個残して消し、ほかのファイルには触らない。
func TestBackupPrunesOnlyMatchingNames(t *testing.T) {
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	e := newEnv(t, now)
	dir := t.TempDir()
	e.app.BackupDir = dir
	e.app.BackupKeep = 3
	old := []string{
		"rajikura-20260101-000000.db", "rajikura-20260102-000000.db", "rajikura-20260103-000000.db", "rajikura-20260104-000000.db",
	}
	others := []string{"notes.txt", "rajikura-latest.db", "rajikura-20260101-000000.db.bak", "my-rajikura-20260101-000000.db", "rajikura.db"}
	for _, n := range append(slices.Clone(old), others...) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 名前の形に合っても、フォルダは消さない。
	if err := os.Mkdir(filepath.Join(dir, "rajikura-20250101-000000.db"), 0o755); err != nil {
		t.Fatal(err)
	}

	e.app.BackupIfDue(e.ctx)

	want := append(slices.Clone(others), "rajikura-20250101-000000.db", old[2], old[3], store.BackupFileName(now))
	slices.Sort(want)
	if got := backupFiles(t, dir); !slices.Equal(got, want) {
		t.Fatalf("取った後 = %v\nwant %v", got, want)
	}
}

// 取れなかったらサービスの異常の始まりを 1 回だけ通知し（続けて失敗している間は増やさない）、次に取れたら復旧を通知する。
// 失敗している間は、前に取れた日時を変えないので、1 時間後の見回りでまた取ろうとする。
func TestBackupFailureAndRecovery(t *testing.T) {
	start := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	e := newEnv(t, start)
	d := e.useDiscord()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.BackupDir = filepath.Join(blocker, "backups") // ファイルの下にはフォルダを作れない

	e.app.BackupIfDue(e.ctx)
	e.checkNotifications()
	ns := e.notifications()
	if len(ns) != 1 || ns[0].Kind != notify.KindBackup || ns[0].Anomaly != notify.KindBackup {
		t.Fatalf("通知 = %+v", ns)
	}
	d.expectOne(t, "【バックアップの失敗】", blocker)
	if at, _ := e.store.LastBackupAt(e.ctx); !at.IsZero() {
		t.Fatalf("取れていないのに LastBackupAt = %v", at)
	}

	// 1 時間後の見回りでまた取ろうとして、また失敗しても、始まりは増やさない。
	e.clock.Set(start.Add(time.Hour))
	e.app.BackupIfDue(e.ctx)
	e.checkNotifications()
	if ns := e.notifications(); len(ns) != 1 {
		t.Fatalf("続けて失敗したのに通知が増えた: %+v", ns)
	}
	d.expectNone(t)

	// 取る先が直ったら、取れて、復旧を通知する。
	good := filepath.Join(t.TempDir(), "backups")
	e.app.BackupDir = good
	e.clock.Set(start.Add(2 * time.Hour))
	e.app.BackupIfDue(e.ctx)
	e.checkNotifications()
	ns = e.notifications()
	if len(ns) != 2 || ns[0].Kind != notify.KindRecovered || ns[0].Anomaly != notify.KindBackup {
		t.Fatalf("通知 = %+v", ns)
	}
	d.expectOne(t, "【復旧】")
	if got := backupFiles(t, good); len(got) != 1 {
		t.Fatalf("取れていない: %v", got)
	}

	// 復旧した後は、24 時間たつまで取らず、通知も増やさない。
	e.clock.Set(start.Add(3 * time.Hour))
	e.app.BackupIfDue(e.ctx)
	if got := backupFiles(t, good); len(got) != 1 || len(e.notifications()) != 2 {
		t.Fatalf("復旧の後に取った・通知した: %v", got)
	}
}

// 取れない異常が続いている間に取る先を空にしたら、次の見回りで、止めたことを復旧として通知して異常を閉じる。
// 異常が続いていないときは何も書かない。
func TestBackupDirEmptiedClosesAnomaly(t *testing.T) {
	start := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	e := newEnv(t, start)
	e.app.BackupIfDue(e.ctx) // 取る先が空で、異常も無い
	if ns := e.notifications(); len(ns) != 0 {
		t.Fatalf("異常が無いのに通知した: %+v", ns)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.BackupDir = filepath.Join(blocker, "backups")
	e.app.BackupIfDue(e.ctx)
	if on, err := e.store.AnomalyOngoing(e.ctx, notify.KindBackup); err != nil || !on {
		t.Fatalf("異常が始まっていない: %v, %v", on, err)
	}

	e.app.BackupDir = ""
	e.clock.Set(start.Add(time.Hour))
	e.app.BackupIfDue(e.ctx)
	ns := e.notifications()
	if len(ns) != 2 || ns[0].Kind != notify.KindRecovered || ns[0].Anomaly != notify.KindBackup || !strings.Contains(ns[0].Message, "止めた") {
		t.Fatalf("通知 = %+v", ns)
	}
	if on, _ := e.store.AnomalyOngoing(e.ctx, notify.KindBackup); on {
		t.Fatal("異常が閉じていない")
	}
	e.app.BackupIfDue(e.ctx)
	if len(e.notifications()) != 2 {
		t.Fatal("閉じた後に通知が増えた")
	}
}

// 途中で止まって残った .partial があっても、次の見回りで取れる。.partial は世代として数えない。
func TestBackupLeftoverPartial(t *testing.T) {
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	e := newEnv(t, now)
	dir := t.TempDir()
	e.app.BackupDir = dir
	partial := filepath.Join(dir, store.BackupFileName(now)+".partial")
	if err := os.WriteFile(partial, []byte("作りかけ"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.app.BackupIfDue(e.ctx)
	if got := backupFiles(t, dir); !slices.Equal(got, []string{store.BackupFileName(now)}) {
		t.Fatalf("取った後 = %v", got)
	}
}
