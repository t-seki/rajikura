package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// PutShow は番組名をキーにして、番組とその毎週の予約を丸ごと置き換える（無ければ作る）。
// serve の API からは使わず、テストが番組を置くのに使う。値は書き込む前に Show.Validate で確かめる。
// 番組の行（id）は残すので、その番組の 1 回の予約も残る。show.Onces は見ない。
func (s *Store) PutShow(ctx context.Context, show domain.Show) error {
	if err := show.Validate(); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := putShow(ctx, tx, show)
		return err
	})
}

// PutShows は複数の番組を 1 つのトランザクションで PutShow する。
func (s *Store) PutShows(ctx context.Context, shows []domain.Show) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, show := range shows {
			if err := show.Validate(); err != nil {
				return err
			}
			if _, err := putShow(ctx, tx, show); err != nil {
				return err
			}
		}
		return nil
	})
}

// putShow は番組の行を upsert し、毎週の予約を入れ替えて、番組の id を返す。1 回の予約は触らない。
func putShow(ctx context.Context, tx *sql.Tx, show domain.Show) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO shows (name, join_airings, split, check_title, enabled) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET join_airings = excluded.join_airings, split = excluded.split,
			check_title = excluded.check_title, enabled = excluded.enabled
		RETURNING id`, show.Name, boolInt(show.Joins()), boolInt(show.IsSplit()), boolInt(show.CheckTitle), boolInt(!show.Disabled)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("番組 %s を保存できない: %w", show.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM weekly WHERE show_id = ?`, id); err != nil {
		return 0, err
	}
	for i, r := range show.Weekly {
		_, err := tx.ExecContext(ctx, `INSERT INTO weekly
			(show_id, position, station_id, weekday, start_hm, from_day, until_day)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, i, r.StationID, int(r.Weekday), r.Start.String(), nullDay(r.From), nullDay(r.Until))
		if err != nil {
			return 0, fmt.Errorf("番組 %s の毎週の予約を保存できない: %w", show.Name, err)
		}
	}
	return id, nil
}

// nullDay は放送日の列の値。零値（制限なし）なら NULL。
func nullDay(d domain.BroadcastDay) any {
	if d.IsZero() {
		return nil
	}
	return d.String()
}

// DeleteShow は番組とその毎週・1 回の予約を消す。ジョブは消さない。無ければ ErrNotFound。
func (s *Store) DeleteShow(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM shows WHERE name = ?`, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("番組 %s: %w", name, ErrNotFound)
	}
	return nil
}

// ErrExists は、同じ名前の番組がもうあること。
var ErrExists = errors.New("もうある")

// CreateShow は番組とその毎週の予約を新しく作る。同じ名前の番組があれば ErrExists（置き換えない）。
// Web UI の作成に使う。show.Onces は見ない（1 回の予約は CreateOnce で足す）。
func (s *Store) CreateShow(ctx context.Context, show domain.Show) error {
	if err := show.Validate(); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := showAbsent(ctx, tx, show.Name); err != nil {
			return err
		}
		_, err := putShow(ctx, tx, show)
		return err
	})
}

// ReplaceShow は、名前が oldName の番組を show で丸ごと置き換える。Web UI の編集に使う。
// oldName の番組が無ければ ErrNotFound。
//
// 名前を変えたとき（show.Name が oldName と違う）は、古い名前の番組を消して新しい名前の番組を作る。新しい名前の
// 番組がもうあれば ErrExists。1 回の予約は show.Onces を見ずに、古い番組のものを新しい番組に引き継ぐ。
// 同じトランザクションで、古い名前の、done でないジョブ（waiting・running・retrying・expired・skipped）の番組の名前を
// 新しい名前に付け替える（今日より前の放送日の waiting のジョブも、展開で消えずに新しい名前で録音する）。done のジョブは
// 古い名前のフォルダに保存済みなので触らない。録り直し中の retrying のジョブは、保存先（output_path）を持つが付け替わり、
// 新しい名前で保存して、古い名前の前のファイルは消さない（保存先が前の保存先と違うパスになるため。ADR 0025）。当たらない日の記録（unmatched.go）と、
// 番組を指す通知（Notification.Show）も新しい名前に付け替える。
func (s *Store) ReplaceShow(ctx context.Context, oldName string, show domain.Show) error {
	if err := show.Validate(); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var oldID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM shows WHERE name = ?`, oldName).Scan(&oldID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("番組 %s: %w", oldName, ErrNotFound)
		} else if err != nil {
			return err
		}
		onces, err := oncesOf(ctx, tx, oldID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM shows WHERE id = ?`, oldID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("番組 %s: %w", oldName, ErrNotFound)
		}
		// 古い名前の番組を消した後に確かめる（エラーならトランザクションごと巻き戻す）。
		if err := showAbsent(ctx, tx, show.Name); err != nil {
			return err
		}
		id, err := putShow(ctx, tx, show)
		if err != nil {
			return err
		}
		for _, o := range onces {
			if _, err := insertOnce(ctx, tx, id, o); err != nil {
				return fmt.Errorf("番組 %s の 1 回の予約を引き継げない: %w", show.Name, err)
			}
		}
		if show.Name != oldName {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET show_name = ? WHERE show_name = ? AND state != 'done'`,
				show.Name, oldName); err != nil {
				return fmt.Errorf("ジョブの番組の名前を %s に付け替えられない: %w", show.Name, err)
			}
			// 当たらない日の記録も付け替える（通知した印を残し、名前を変えただけで通知を重ねないため）。
			if _, err := tx.ExecContext(ctx, `UPDATE OR REPLACE unmatched_days SET show_name = ? WHERE show_name = ?`,
				show.Name, oldName); err != nil {
				return fmt.Errorf("当たらない日の記録の番組の名前を %s に付け替えられない: %w", show.Name, err)
			}
			// 番組を指す通知も付け替える（通知の画面とトーストが、新しい名前の番組の画面を開くため）。
			if _, err := tx.ExecContext(ctx, `UPDATE notifications SET show = ? WHERE show = ?`,
				show.Name, oldName); err != nil {
				return fmt.Errorf("通知の番組の名前を %s に付け替えられない: %w", show.Name, err)
			}
		}
		return nil
	})
}

func showAbsent(ctx context.Context, tx *sql.Tx, name string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shows WHERE name = ?`, name).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("番組 %s: %w", name, ErrExists)
	}
	return nil
}

// SetShowEnabled は番組の有効／無効を切り替える。毎週の予約とジョブは触らない。無ければ ErrNotFound。
func (s *Store) SetShowEnabled(ctx context.Context, name string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE shows SET enabled = ? WHERE name = ?`, boolInt(enabled), name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("番組 %s: %w", name, ErrNotFound)
	}
	return nil
}

// NextAirings は、番組の名前ごとに、その番組の waiting のジョブの放送回のうち、開始時刻が after より後の
// いちばん早い開始時刻を返す（次の放送予定）。放送が終わって最初の試行を待っている放送回は入れない。
// そうした放送回が無い番組は入らない。ジョブは番組の名前で引くので、番組が DB に無い名前も入りうる。
func (s *Store) NextAirings(ctx context.Context, after time.Time) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.show_name, MIN(a.start_at)
		FROM jobs j JOIN job_airings a ON a.job_id = j.id
		WHERE j.state = ? AND a.start_at > ? GROUP BY j.show_name`, string(domain.StateWaiting), formatTime(after))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var name, start string
		if err := rows.Scan(&name, &start); err != nil {
			return nil, err
		}
		t, err := parseTime(start)
		if err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, rows.Err()
}

// GetShow は番組を毎週・1 回の予約ごと読む。無ければ ErrNotFound。
func (s *Store) GetShow(ctx context.Context, name string) (domain.Show, error) {
	shows, err := listShows(ctx, s.db, `WHERE name = ?`, name)
	if err != nil {
		return domain.Show{}, err
	}
	if len(shows) == 0 {
		return domain.Show{}, fmt.Errorf("番組 %s: %w", name, ErrNotFound)
	}
	return shows[0], nil
}

// ListShows はすべての番組を毎週・1 回の予約ごと、名前の順に読む。
func (s *Store) ListShows(ctx context.Context) ([]domain.Show, error) {
	return listShows(ctx, s.db, ``)
}

// splitMode は shows の split と join_airings の列から、番組の分割番組の設定を読む（ADR 0003。列は 2 つの真偽値のまま
// 使い回す）。split が 0 なら join_airings によらず分割番組でない。書くときは split = IsSplit、join_airings = Joins にする。
func splitMode(split, join int) domain.SplitMode {
	switch {
	case split == 0:
		return domain.SplitNone
	case join != 0:
		return domain.SplitJoin
	}
	return domain.SplitSeparate
}

func listShows(ctx context.Context, q querier, where string, args ...any) ([]domain.Show, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, join_airings, split, check_title, enabled FROM shows `+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var shows []domain.Show
	for rows.Next() {
		var id int64
		var show domain.Show
		var join, split, checkTitle, enabled int
		if err := rows.Scan(&id, &show.Name, &join, &split, &checkTitle, &enabled); err != nil {
			rows.Close()
			return nil, err
		}
		show.Split = splitMode(split, join)
		show.CheckTitle = checkTitle != 0
		show.Disabled = enabled == 0
		ids = append(ids, id)
		shows = append(shows, show)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		weekly, err := weeklyOf(ctx, q, id)
		if err != nil {
			return nil, fmt.Errorf("番組 %s の毎週の予約を読めない: %w", shows[i].Name, err)
		}
		shows[i].Weekly = weekly
		if shows[i].Onces, err = oncesOf(ctx, q, id); err != nil {
			return nil, fmt.Errorf("番組 %s の 1 回の予約を読めない: %w", shows[i].Name, err)
		}
	}
	return shows, nil
}

func weeklyOf(ctx context.Context, q querier, showID int64) ([]domain.Weekly, error) {
	rows, err := q.QueryContext(ctx, `SELECT station_id, weekday, start_hm, from_day, until_day
		FROM weekly WHERE show_id = ? ORDER BY position`, showID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var weekly []domain.Weekly
	for rows.Next() {
		var r domain.Weekly
		var startHM string
		var weekday int
		var fromDay, untilDay sql.NullString
		if err := rows.Scan(&r.StationID, &weekday, &startHM, &fromDay, &untilDay); err != nil {
			return nil, err
		}
		if fromDay.Valid {
			if r.From, err = domain.ParseBroadcastDay(fromDay.String); err != nil {
				return nil, err
			}
		}
		if untilDay.Valid {
			if r.Until, err = domain.ParseBroadcastDay(untilDay.String); err != nil {
				return nil, err
			}
		}
		r.Weekday = time.Weekday(weekday)
		if r.Start, err = domain.ParseClock(startHM); err != nil {
			return nil, err
		}
		weekly = append(weekly, r)
	}
	return weekly, rows.Err()
}
