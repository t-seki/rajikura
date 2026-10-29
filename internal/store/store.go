// Package store は rajikura の状態（番組と毎週の予約、1 回の予約、キーワードとその足した放送回の記録、当たらない日の記録、ジョブ、ジョブの放送回、試行、通知、Web UI のパスワードの hash、Web UI のログインのセッション、API トークンの hash）を SQLite に保存する。
// 当たらない日の記録（unmatched_days）は、その当たらない日を通知したかの印（notified_at）も持ち、通知と同じトランザクションで立てる（unmatched.go）。
// キーワードの判定と、1 回の予約・番組・記録・通知の書き込みは 1 つのトランザクションで行う（keywords.go の ApplyKeywords）。
// 通知は 1 件ずつ notifications の行にする（種類・本文・作った時刻・送った時刻・送れなかった印・既読。notify.go）。
// ジョブの期限間近を通知した時刻（jobs.deadline_notified_at）は、期限間近を 2 度通知しないために持つ。
// ジョブの id は AUTOINCREMENT で振り、消したジョブの id を別のジョブに使い回さない（docs/adr/0026-job-id-not-reused.md）。
// 最後にジョブを保存した時刻は app_state に持ち、/metrics を再起動で 0 に戻さないために使う（app_state.go）。
// Web UI のパスワードの bcrypt の hash も app_state に持つ（password.go。docs/adr/0016-first-password-by-setup-code.md）。
// 最後に自動のバックアップを取れた時刻も app_state に持つ（app_state.go。取る先を変えたら忘れる）。
// 設定（秘密を除く値の JSON）は settings に、暗号化した秘密は secrets に持つ（settings.go。中身は解釈しない）。
// DB のバックアップは、VACUUM INTO で作った写しから sessions を消したもの（backup.go。docs/adr/0017-backup-by-db-copy.md）。
// 足したら直す: DB の表
//
// ドライバは modernc.org/sqlite（cgo 不要）。DB を開くのは serve だけ（CLI は serve の HTTP API を呼ぶ。
// docs/adr/0009-cli-over-http.md）だが、serve の中では HTTP のハンドラとジョブの実行のループが並んで DB に触りうる。
// 読み出しが書き込みを待たないように WAL モードにし、書き込みが重なったら待つように busy_timeout を設定し、
// 読み出してから書くトランザクションが途中で書き込みの順番待ちに負けないように BEGIN IMMEDIATE で始める。
// スキーマは埋め込んだ SQL（migrations/）を番号順に当て、適用済みの版を schema_migrations で管理する。
// DB がバイナリの知らない新しい版まで当たっていたら、開かずにエラーにする。
// 時刻は UTC の RFC 3339 で保存し、読み出したら日本時間に変換する。
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	_ "modernc.org/sqlite" // database/sql のドライバ "sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound は対象が無いこと。
var ErrNotFound = errors.New("見つからない")

// busyTimeout は、ほかの接続が書き込み中のときに待つ時間。
const busyTimeout = 5 * time.Second

// Store は SQLite の保存先。
type Store struct {
	db *sql.DB
}

// Open は path の SQLite ファイルを開き、マイグレーションを当てる。ファイルが無ければ作る。
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?" + strings.Join([]string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeout.Milliseconds()),
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}, "&")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("DB %s を開けない: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close は DB を閉じる。
func (s *Store) Close() error {
	return s.db.Close()
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("マイグレーション %s の名前に番号が無い", name)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i := 1; i < len(ms); i++ {
		if ms[i].version == ms[i-1].version {
			return nil, fmt.Errorf("マイグレーションの番号 %d が重なる", ms[i].version)
		}
	}
	return ms, nil
}

// migrate は未適用のマイグレーションを番号順に当てる。1 つずつトランザクションで当てる。
func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("schema_migrations を作れない: %w", err)
	}
	// DB がこのバイナリより新しい版まで当たっていたら（バイナリを古くした）、壊さないように止める。
	if applied, err := s.SchemaVersion(ctx); err != nil {
		return fmt.Errorf("DB の版を読めない: %w", err)
	} else if known := ms[len(ms)-1].version; applied > known {
		return fmt.Errorf("DB の版 %d は、このバイナリが知っている版 %d より新しい", applied, known)
	}
	for _, m := range ms {
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return nil
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				m.version, formatTime(time.Now()))
			return err
		})
		if err != nil {
			return fmt.Errorf("マイグレーション %s を当てられない: %w", m.name, err)
		}
	}
	return nil
}

// SchemaVersion は適用済みの最新の版。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return int(v.Int64), err
}

// tx は fn をトランザクションの中で実行する。fn がエラーを返したら巻き戻す。
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// timeFormat は保存する時刻の形。UTC の RFC 3339 で、ミリ秒まで固定幅にして文字列で比べられるようにする。
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("時刻 %q を読めない: %w", s, err)
	}
	return t.In(domain.JST), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
