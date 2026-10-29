package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 1 回の予約（onces の表）。番組に属し、番組を消すと一緒に消える（ON DELETE CASCADE）。
// 番組の置き換え（PutShow）は番組の行を残すので 1 回の予約も残り、Web UI の名前の変更（ReplaceShow）は新しい名前の番組に
// 引き継ぐ（足したキーワードの印 keyword_id も一緒に引き継ぐ）。キーワードが足したものは keyword_id にキーワードの ID を持ち、
// キーワードを消すと NULL に戻る（ON DELETE SET NULL。1 回の予約は残る）。

// CreateOnce は番組 showName に 1 回の予約 o を足し、振った ID を返す。番組が無ければ ErrNotFound。
// 同じ番組に同じ局・同じ開始時刻の 1 回の予約がもうあれば ErrExists。o.ID は見ない。
func (s *Store) CreateOnce(ctx context.Context, showName string, o domain.Once) (int64, error) {
	ids, err := s.CreateOnces(ctx, showName, []domain.Once{o})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// CreateOnces は番組 showName に 1 回の予約 os を 1 つのトランザクションで足し、振った ID を os の順に返す。
// o.KeywordID があれば、そのキーワードが足したものとして書く（人が足すときは 0）。
// 全部足すか 1 つも足さないか: 1 つでも値が正しくない（Validate）・番組が無い（ErrNotFound）・同じ番組に同じ局・
// 同じ開始時刻の 1 回の予約がもうある、または os の中で重なっている（ErrExists）なら、1 つも足さない。
func (s *Store) CreateOnces(ctx context.Context, showName string, os []domain.Once) ([]int64, error) {
	for _, o := range os {
		if err := o.Validate(); err != nil {
			return nil, err
		}
	}
	var ids []int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ids = nil
		var showID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM shows WHERE name = ?`, showName).Scan(&showID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("番組 %s: %w", showName, ErrNotFound)
		} else if err != nil {
			return err
		}
		for _, o := range os {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM onces WHERE show_id = ? AND station_id = ? AND start_at = ?`,
				showID, o.StationID, formatTime(o.Start)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("番組 %s の 1 回の予約 %s: %w", showName, o.Key(), ErrExists)
			}
			id, err := insertOnce(ctx, tx, showID, o)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func insertOnce(ctx context.Context, tx *sql.Tx, showID int64, o domain.Once) (int64, error) {
	var id int64
	var keywordID any // NULL なら人が足したもの
	if o.KeywordID != 0 {
		keywordID = o.KeywordID
	}
	err := tx.QueryRowContext(ctx, `INSERT INTO onces (show_id, station_id, start_at, keyword_id) VALUES (?, ?, ?, ?) RETURNING id`,
		showID, o.StationID, formatTime(o.Start), keywordID).Scan(&id)
	return id, err
}

// DeleteOnce は 1 回の予約を消す。無ければ ErrNotFound。ジョブは触らない（waiting のジョブからは、次の展開で外れる）。
func (s *Store) DeleteOnce(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM onces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("1 回の予約 %d: %w", id, ErrNotFound)
	}
	return nil
}

// ListOnces は番組 showName の 1 回の予約を、開始時刻の順に読む。番組が無ければ ErrNotFound。
func (s *Store) ListOnces(ctx context.Context, showName string) ([]domain.Once, error) {
	var showID int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM shows WHERE name = ?`, showName).Scan(&showID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("番組 %s: %w", showName, ErrNotFound)
	} else if err != nil {
		return nil, err
	}
	return oncesOf(ctx, s.db, showID)
}

// DeleteOncesOfAirings は、番組 showName の 1 回の予約のうち、放送回 airings のどれかと局・開始時刻
// （domain.Once.Key と domain.Airing.Key）が同じものを消し、消した数を返す。ジョブが done になったときに、そのジョブの放送回で
// 呼ぶ（ジョブはどの予約から作られたかの印を持たないので、放送回で突き合わせる。連結のジョブは放送回を複数持つ）。
// 番組が無ければ何もせず 0 を返す。ジョブと試行の記録は触らない。
func (s *Store) DeleteOncesOfAirings(ctx context.Context, showName string, airings []domain.Airing) (int, error) {
	keys := map[string]bool{}
	for _, a := range airings {
		keys[a.Key()] = true
	}
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		n = 0
		var showID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM shows WHERE name = ?`, showName).Scan(&showID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		onces, err := oncesOf(ctx, tx, showID)
		if err != nil {
			return err
		}
		for _, o := range onces {
			if !keys[o.Key()] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM onces WHERE id = ?`, o.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteExpiredOnces は、録音期限（domain.Once.Deadline）が now 以前になった 1 回の予約を消し、消した数を返す。
// ジョブと試行の記録は触らない。
func (s *Store) DeleteExpiredOnces(ctx context.Context, now time.Time, m domain.Membership) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, station_id, start_at, keyword_id FROM onces`)
		if err != nil {
			return err
		}
		all, err := scanOnces(rows)
		if err != nil {
			return err
		}
		for _, o := range all {
			if now.Before(o.Deadline(m)) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM onces WHERE id = ?`, o.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// oncesOf は番組の id の 1 回の予約を、開始時刻の順に読む。無ければ nil。
func oncesOf(ctx context.Context, q querier, showID int64) ([]domain.Once, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, station_id, start_at, keyword_id FROM onces WHERE show_id = ? ORDER BY start_at, station_id`, showID)
	if err != nil {
		return nil, err
	}
	return scanOnces(rows)
}

func scanOnces(rows *sql.Rows) ([]domain.Once, error) {
	defer rows.Close()
	var out []domain.Once
	for rows.Next() {
		var o domain.Once
		var start string
		var keywordID sql.NullInt64
		if err := rows.Scan(&o.ID, &o.StationID, &start, &keywordID); err != nil {
			return nil, err
		}
		o.KeywordID = keywordID.Int64
		t, err := parseTime(start)
		if err != nil {
			return nil, err
		}
		o.Start = t
		out = append(out, o)
	}
	return out, rows.Err()
}
