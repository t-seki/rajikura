package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// Notification は通知の 1 行（notifications）。通知を作る処理は行を書くだけで、通知先には app が周ごとに送る（ADR 0011）。例外はテスト通知で、その場で送って結果（failed）を付けた行を書く（ADR 0011）。通知先なしなら、送らずに印の無い行だけを書く（ADR 0011）。
type Notification struct {
	ID int64
	// Kind は通知の種類（notify.Kinds のどれか）。
	Kind string
	// Anomaly は、サービスの異常の行（始まりと復旧）なら、どの異常か（notify.KindSync・notify.KindOutOfArea・notify.KindBackup・notify.KindWake・notify.KindRescan）。
	// ジョブの出来事は空。
	// 足したら直す: サービスの異常
	Anomaly string
	// Message は本文。
	Message string
	// CreatedAt は作った時刻。
	CreatedAt time.Time
	// Deliver は通知先に送るか。false なら送らない（通知先なしのときと、notify.success = false の録音済み）。
	Deliver bool
	// SentAt は通知先に送れた時刻。零値ならまだ。
	SentAt time.Time
	// FailedAt は、作ってから 1 時間を過ぎた後に送ろうとして送れず、諦めた時刻（「送れなかった」の印）。零値なら諦めていない。
	// テスト通知は送り直さないので、送れなければ送ろうとした時刻をそのまま付ける。
	FailedAt time.Time
	// Read は既読（Web UI の履歴で使う）。
	Read bool
	// JobID は、ジョブの出来事の通知なら、そのジョブの id。それ以外と、列を足す前からある行は 0（DB は NULL）。
	// 指す先のジョブが消えても（展開で作り直したなど）そのまま残す。
	JobID int64
	// Show は、キーワードの出来事（notify.KindKeywordAdded）と当たらない日（notify.KindUnmatchedDay）の通知なら、
	// その番組の名前。それ以外と、列を足す前からある行は空（DB は NULL）。番組の名前を変えたら付け替える
	// （ReplaceShow）。指す先の番組が消えてもそのまま残す。
	Show string
}

const notificationColumns = `id, kind, anomaly, message, created_at, deliver, sent_at, failed_at, read, job_id, show`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNotification(r rowScanner) (Notification, error) {
	var n Notification
	var created, sent, failed string
	var deliver, read int
	var jobID sql.NullInt64
	var show sql.NullString
	if err := r.Scan(&n.ID, &n.Kind, &n.Anomaly, &n.Message, &created, &deliver, &sent, &failed, &read, &jobID, &show); err != nil {
		return Notification{}, err
	}
	n.Deliver, n.Read = deliver != 0, read != 0
	n.JobID, n.Show = jobID.Int64, show.String
	var err error
	if n.CreatedAt, err = parseTime(created); err != nil {
		return Notification{}, err
	}
	if n.SentAt, err = parseTime(sent); err != nil {
		return Notification{}, err
	}
	if n.FailedAt, err = parseTime(failed); err != nil {
		return Notification{}, err
	}
	return n, nil
}

func queryNotifications(ctx context.Context, q querier, where string, args ...any) ([]Notification, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+notificationColumns+` FROM notifications `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func insertNotification(ctx context.Context, q querier, n Notification) (int64, error) {
	// 指す先が無ければ（JobID が 0、Show が空）NULL を書く。
	jobID := sql.NullInt64{Int64: n.JobID, Valid: n.JobID != 0}
	show := sql.NullString{String: n.Show, Valid: n.Show != ""}
	res, err := q.ExecContext(ctx, `INSERT INTO notifications (kind, anomaly, message, created_at, deliver, sent_at, failed_at, read, job_id, show)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Kind, n.Anomaly, n.Message, formatTime(n.CreatedAt), boolInt(n.Deliver), formatTime(n.SentAt), formatTime(n.FailedAt), boolInt(n.Read),
		jobID, show)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddNotification は通知 n を書き、ID を返す。ID は見ない。SentAt・FailedAt・Read もそのまま書く
// （ふつうは零値で、送った結果は後から付ける。テスト通知は送った後に、結果と既読を付けて書く）。
func (s *Store) AddNotification(ctx context.Context, n Notification) (int64, error) {
	return insertNotification(ctx, s.db, n)
}

// AddDeadlineNearNotification は、ジョブ jobID の期限間近の通知 n を書き、同じトランザクションでジョブに
// 期限間近を通知した時刻（n.CreatedAt）を立てる。ジョブが無ければ何も書かずに ErrNotFound。
func (s *Store) AddDeadlineNearNotification(ctx context.Context, jobID int64, n Notification) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE jobs SET deadline_notified_at = ? WHERE id = ?`, formatTime(n.CreatedAt), jobID)
		if err != nil {
			return err
		}
		if k, err := res.RowsAffected(); err != nil {
			return err
		} else if k == 0 {
			return ErrNotFound
		}
		id, err = insertNotification(ctx, tx, n)
		return err
	})
	return id, err
}

// AddAnomalyNotification は、サービスの異常 n.Anomaly の始まり（n.Kind が notify.KindRecovered でない）か
// 復旧（notify.KindRecovered）の通知を書く。始まりは、その異常が続いていない（AnomalyOngoing が false）ときだけ、
// 復旧は続いているときだけ書く。書いたら true。判定と書き込みは 1 つのトランザクションで行う。
func (s *Store) AddAnomalyNotification(ctx context.Context, n Notification) (bool, error) {
	if n.Anomaly == "" {
		return false, errors.New("サービスの異常の通知に anomaly が無い")
	}
	added := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ongoing, err := anomalyOngoing(ctx, tx, n.Anomaly)
		if err != nil {
			return err
		}
		if ongoing == (n.Kind == notify.KindRecovered) {
			_, err = insertNotification(ctx, tx, n)
			added = err == nil
		}
		return err
	})
	return added, err
}

// AnomalyOngoing は、サービスの異常 anomaly が続いているか（その異常の最後の行が始まりで、復旧が無い）を返す。
// 通知の行から判定するので、再起動しても変わらない。
func (s *Store) AnomalyOngoing(ctx context.Context, anomaly string) (bool, error) {
	return anomalyOngoing(ctx, s.db, anomaly)
}

func anomalyOngoing(ctx context.Context, q querier, anomaly string) (bool, error) {
	var kind string
	err := q.QueryRowContext(ctx, `SELECT kind FROM notifications WHERE anomaly = ? ORDER BY id DESC LIMIT 1`, anomaly).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return kind != notify.KindRecovered, nil
}

// OngoingAnomalies は、まだ復旧していないサービスの異常の始まりの行（その異常の最後の行が始まりのもの）を、異常の名前の順に読む。
// 判定は AnomalyOngoing と同じ。
func (s *Store) OngoingAnomalies(ctx context.Context) ([]Notification, error) {
	return queryNotifications(ctx, s.db, `WHERE anomaly != '' AND kind != ?
		AND id = (SELECT MAX(r.id) FROM notifications r WHERE r.anomaly = notifications.anomaly)
		ORDER BY anomaly`, notify.KindRecovered)
}

// PendingNotifications は、通知先に送る（Deliver）うえに、まだ送れておらず諦めてもいない通知を、古い順に読む。
func (s *Store) PendingNotifications(ctx context.Context) ([]Notification, error) {
	return queryNotifications(ctx, s.db, `WHERE deliver = 1 AND sent_at = '' AND failed_at = '' ORDER BY id`)
}

// Notifications は通知を新しい順に全部読む。
func (s *Store) Notifications(ctx context.Context) ([]Notification, error) {
	return queryNotifications(ctx, s.db, `ORDER BY id DESC`)
}

// MarkNotificationSent は通知 id を時刻 at に送れたと記録する。通知が無ければ ErrNotFound。
func (s *Store) MarkNotificationSent(ctx context.Context, id int64, at time.Time) error {
	return s.updateNotification(ctx, `UPDATE notifications SET sent_at = ? WHERE id = ?`, formatTime(at), id)
}

// MarkNotificationFailed は通知 id に、時刻 at に諦めた（「送れなかった」）印を付ける。通知が無ければ ErrNotFound。
func (s *Store) MarkNotificationFailed(ctx context.Context, id int64, at time.Time) error {
	return s.updateNotification(ctx, `UPDATE notifications SET failed_at = ? WHERE id = ?`, formatTime(at), id)
}

// MarkNotificationsRead は、未読の通知を全部既読にし、既読にした数を返す（通知の画面を開いたときに呼ぶ。1 件だけは MarkNotificationRead）。
func (s *Store) MarkNotificationsRead(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE notifications SET read = 1 WHERE read = 0`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkNotificationRead は通知 id を既読にする（トーストから通知が指す画面を開いたときに呼ぶ）。もう既読でも成功する。
// 通知が無ければ ErrNotFound。
func (s *Store) MarkNotificationRead(ctx context.Context, id int64) error {
	// 更新の対象は id の行だけなので、もう既読の行も RowsAffected に数えられ、「もう既読」と「行が無い」を分けられる。
	return s.updateNotification(ctx, `UPDATE notifications SET read = 1 WHERE id = ?`, id)
}

// UnreadNotificationCount は未読の通知の数を返す。
func (s *Store) UnreadNotificationCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE read = 0`).Scan(&n)
	return n, err
}

// FailedNotificationCount は、最後に通知先に送れた時刻（sent_at の最大値）より後に「送れなかった」の印を付けた通知の数を返す。
// 送れた通知が 1 つも無ければ、「送れなかった」の印の付いた通知の全部を数える。
// 0 でないことは、最後に送れなかった時刻より後に送れた通知が無い（通知先がまだ直っていない）ことと同じ。
func (s *Store) FailedNotificationCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE failed_at != ''
		AND failed_at > COALESCE((SELECT MAX(sent_at) FROM notifications WHERE sent_at != ''), '')`).Scan(&n)
	return n, err
}

func (s *Store) updateNotification(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteNotificationsBefore は、時刻 before より前に作った通知を消し、消した数を返す。ただし、まだ復旧していない
// サービスの異常の始まりの行（その異常の、それより後の復旧の行が無いもの）は消さない（異常が続いていることの元になる）。
func (s *Store) DeleteNotificationsBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notifications WHERE created_at < ?
		AND NOT (anomaly != '' AND kind != ? AND NOT EXISTS (
			SELECT 1 FROM notifications r WHERE r.anomaly = notifications.anomaly AND r.kind = ? AND r.id > notifications.id))`,
		formatTime(before), notify.KindRecovered, notify.KindRecovered)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeadlineNearJobs は、録音期限が now+within 以前で、まだ録音を終えていない（waiting・running・retrying。done・expired・skipped でない）
// うえに、期限間近をまだ通知していないジョブを、録音期限の順に読む。
func (s *Store) DeadlineNearJobs(ctx context.Context, now time.Time, within time.Duration) ([]domain.Job, error) {
	return queryJobs(ctx, s.db, `WHERE state IN ('waiting', 'running', 'retrying')
		AND deadline <= ? AND deadline_notified_at = ''
		ORDER BY deadline, id`, formatTime(now.Add(within)))
}
