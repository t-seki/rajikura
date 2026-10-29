package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 当たらない日（用語集）の記録（unmatched_days）。app の番組表の同期と番組を変えた直後の展開が、局・放送日ごとに
// domain.FindUnmatchedDays で求めて書き（ReplaceUnmatchedDays）、番組の一覧が読み（UnmatchedDays）、まだ通知していない
// ものを番組ごとに 1 通にまとめて通知する（AddUnmatchedDayNotifications）。

// unmatchedAiring は記録に残す放送回の事実（番組名・開始・終了）。
type unmatchedAiring struct {
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func toUnmatchedAiring(a domain.Airing) unmatchedAiring {
	return unmatchedAiring{Title: a.Title, Start: a.Start.UTC(), End: a.End.UTC()}
}

func (u unmatchedAiring) airing(station string) domain.Airing {
	return domain.Airing{StationID: station, Title: u.Title, Start: u.Start.In(domain.JST), End: u.End.In(domain.JST)}
}

// ReplaceUnmatchedDays は、局 station の放送日 day の当たらない日の記録を、求め直した結果 res（domain.FindUnmatchedDays）に
// 揃える。番組表を取れなかった・空だった局・放送日では呼ばない（その局・放送日の記録に触らない）。1 つのトランザクションで行う。
//
//   - res.Days の枠は、今も当たらない日として書く（番組表の事実は新しい値にする。通知した印は残す）
//   - 記録にあって res.Days に無い枠のうち、res.Targets にあるもの（調べ直したら当たった枠）は、通知していれば当たるように
//     なった印にして残し（通知を重ねないため）、通知していなければ消す
//   - res.Targets にも無い枠（番組を消した・無効にした・毎週の予約を変えた、枠の開始時刻を過ぎた）は消す
//   - どの局でも、放送日が today より前の記録は消す
func (s *Store) ReplaceUnmatchedDays(ctx context.Context, station string, day, today domain.BroadcastDay, res domain.UnmatchedResult) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM unmatched_days WHERE day < ?`, today.String()); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT show_name, start_at, notified_at FROM unmatched_days
			WHERE station_id = ? AND day = ?`, station, day.String())
		if err != nil {
			return err
		}
		type existing struct {
			name, start string
			notified    bool
			key         string
		}
		var olds []existing
		for rows.Next() {
			var e existing
			var notified string
			if err := rows.Scan(&e.name, &e.start, &notified); err != nil {
				rows.Close()
				return err
			}
			t, err := parseTime(e.start)
			if err != nil {
				rows.Close()
				return err
			}
			e.notified, e.key = notified != "", domain.UnmatchedKey(e.name, station, t)
			olds = append(olds, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range olds {
			if slices.ContainsFunc(res.Days, func(u domain.UnmatchedDay) bool { return u.Key() == e.key }) {
				continue
			}
			q := `DELETE FROM unmatched_days WHERE show_name = ? AND station_id = ? AND start_at = ?`
			if e.notified && slices.Contains(res.Targets, e.key) {
				q = `UPDATE unmatched_days SET active = 0 WHERE show_name = ? AND station_id = ? AND start_at = ?`
			}
			if _, err := tx.ExecContext(ctx, q, e.name, station, e.start); err != nil {
				return err
			}
		}
		for _, u := range res.Days {
			covering := ""
			if u.Covering != nil {
				b, err := json.Marshal(toUnmatchedAiring(*u.Covering))
				if err != nil {
					return err
				}
				covering = string(b)
			}
			titled := make([]unmatchedAiring, len(u.Titled))
			for i, a := range u.Titled {
				titled[i] = toUnmatchedAiring(a)
			}
			tb, err := json.Marshal(titled)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO unmatched_days
				(show_name, station_id, day, start_at, covering, titled, active) VALUES (?, ?, ?, ?, ?, ?, 1)
				ON CONFLICT (show_name, station_id, start_at) DO UPDATE SET
					day = excluded.day, covering = excluded.covering, titled = excluded.titled, active = 1`,
				u.ShowName, u.StationID, u.Day.String(), formatTime(u.Start), covering, string(tb)); err != nil {
				return fmt.Errorf("番組 %s の当たらない日を書けない: %w", u.ShowName, err)
			}
		}
		return nil
	})
}

// DeleteStaleUnmatchedDays は、放送日が today より前の記録と、有効な番組（DB にある番組で、無効でないもの）の名前でない記録を消し、
// 消した数を返す（番組表の同期の始めに呼ぶ。番組を消した・無効にした直後の展開で番組表を取れなかったときの取り残しを拾う）。
func (s *Store) DeleteStaleUnmatchedDays(ctx context.Context, today domain.BroadcastDay) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM unmatched_days WHERE day < ?
		OR show_name NOT IN (SELECT name FROM shows WHERE enabled = 1)`, today.String())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const unmatchedColumns = `u.show_name, u.station_id, u.day, u.start_at, u.covering, u.titled`

func scanUnmatchedDays(rows *sql.Rows) ([]domain.UnmatchedDay, error) {
	defer rows.Close()
	var out []domain.UnmatchedDay
	for rows.Next() {
		var u domain.UnmatchedDay
		var day, start, covering, titled string
		if err := rows.Scan(&u.ShowName, &u.StationID, &day, &start, &covering, &titled); err != nil {
			return nil, err
		}
		var err error
		if u.Day, err = domain.ParseBroadcastDay(day); err != nil {
			return nil, err
		}
		if u.Start, err = parseTime(start); err != nil {
			return nil, err
		}
		if covering != "" {
			var c unmatchedAiring
			if err := json.Unmarshal([]byte(covering), &c); err != nil {
				return nil, fmt.Errorf("当たらない日のその時刻を含む放送回を読めない: %w", err)
			}
			a := c.airing(u.StationID)
			u.Covering = &a
		}
		var ts []unmatchedAiring
		if err := json.Unmarshal([]byte(titled), &ts); err != nil {
			return nil, fmt.Errorf("当たらない日の番組名の合う放送回を読めない: %w", err)
		}
		for _, t := range ts {
			u.Titled = append(u.Titled, t.airing(u.StationID))
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UnmatchedDays は、番組の名前ごとに、今も当たらない日（当たるようになった枠を除く）のうち、枠の開始時刻が after より後の
// ものを、枠の開始時刻の順に返す。番組が無効かは見ない（呼び出し側で選ぶ）。
func (s *Store) UnmatchedDays(ctx context.Context, after time.Time) (map[string][]domain.UnmatchedDay, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+unmatchedColumns+` FROM unmatched_days u
		WHERE u.active = 1 AND u.start_at > ? ORDER BY u.show_name, u.start_at, u.station_id`, formatTime(after))
	if err != nil {
		return nil, err
	}
	days, err := scanUnmatchedDays(rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]domain.UnmatchedDay{}
	for _, u := range days {
		out[u.ShowName] = append(out[u.ShowName], u)
	}
	return out, nil
}

// AddUnmatchedDayNotifications は、有効な番組の、今も当たらない日のうち、まだ通知しておらず枠の開始時刻が now より後のものを
// 番組ごとにまとめ、番組ごとに build で作った通知を 1 件書き、同じトランザクションでその当たらない日に通知した印（now）を立てる。
// 書いた通知（ID 付き）を番組の名前の順に返す。build には番組の名前と、その番組の当たらない日（枠の開始時刻の順）を渡す。
// build の中で DB に触れてはいけない。
func (s *Store) AddUnmatchedDayNotifications(ctx context.Context, now time.Time,
	build func(name string, days []domain.UnmatchedDay) Notification) ([]Notification, error) {
	var out []Notification
	err := s.tx(ctx, func(tx *sql.Tx) error {
		out = nil
		rows, err := tx.QueryContext(ctx, `SELECT `+unmatchedColumns+` FROM unmatched_days u
			JOIN shows r ON r.name = u.show_name AND r.enabled = 1
			WHERE u.active = 1 AND u.notified_at = '' AND u.start_at > ?
			ORDER BY u.show_name, u.start_at, u.station_id`, formatTime(now))
		if err != nil {
			return err
		}
		days, err := scanUnmatchedDays(rows)
		if err != nil {
			return err
		}
		for i := 0; i < len(days); {
			j := i
			for j < len(days) && days[j].ShowName == days[i].ShowName {
				j++
			}
			group := days[i:j]
			n := build(group[0].ShowName, group)
			id, err := insertNotification(ctx, tx, n)
			if err != nil {
				return err
			}
			n.ID = id
			for _, u := range group {
				if _, err := tx.ExecContext(ctx, `UPDATE unmatched_days SET notified_at = ?
					WHERE show_name = ? AND station_id = ? AND start_at = ?`,
					formatTime(now), u.ShowName, u.StationID, formatTime(u.Start)); err != nil {
					return err
				}
			}
			out = append(out, n)
			i = j
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
