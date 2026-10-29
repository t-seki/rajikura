package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// キーワード（keywords の表）と、キーワードが足した放送回の記録（keyword_airings）。ADR 0014。
// キーワードは語の形（domain.KeywordKey。words_key）で見分け、同じ形のキーワードは 1 つだけ（同じ形を作る・変えると ErrExists）。
// キーワードを変えても消しても、足した 1 回の予約と作った番組は残す（消すと onces.keyword_id は NULL に戻り、記録は消える）。

// 足した放送回の記録の状態（GET /api/keywords/{id}/airings の state）。
const (
	// KeywordAiringPlanned は録音予定（まだ録音を終えていない）。上限で数えるのはこれだけ。
	KeywordAiringPlanned = "planned"
	// KeywordAiringDone は録音済み（その放送回のジョブが done）。
	KeywordAiringDone = "done"
	// KeywordAiringMissed は録音できなかった（その放送回のジョブが expired）。
	KeywordAiringMissed = "missed"
	// KeywordAiringSkipPlanned は録らない予定（その放送回のジョブが確定の前の skipped）。録音予定には数えない。
	KeywordAiringSkipPlanned = "skip_planned"
	// KeywordAiringSkipped は録らなかった（その放送回のジョブが確定した skipped）。録音予定には数えない。
	KeywordAiringSkipped = "skipped"
	// KeywordAiringRemoved は、録音を終える前に 1 回の予約が外された（記録にあるのに、その放送回の 1 回の
	// 予約もジョブも無い）。
	KeywordAiringRemoved = "removed"
)

// KeywordAiring は、キーワードが足した放送回の記録の 1 行。
type KeywordAiring struct {
	KeywordID   int64
	StationID   string
	StationName string
	Start       time.Time
	End         time.Time
	Title       string
	// ShowName は足した先の番組の名前（足したときのもの）。
	ShowName string
	AddedAt  time.Time
	// State は KeywordAiringPlanned・Done・Missed・SkipPlanned・Skipped・Removed のどれか（読むときに決める）。
	State string
}

// Key は記録の放送回の Key（domain.Airing.Key と同じ形）。
func (r KeywordAiring) Key() string {
	return domain.Airing{StationID: r.StationID, Start: r.Start}.Key()
}

const keywordColumns = `id, words, exclude, stations, enabled, limit_notified`

func scanKeyword(r rowScanner) (domain.Keyword, bool, error) {
	var k domain.Keyword
	var stations string
	var enabled, limitNotified int
	if err := r.Scan(&k.ID, &k.Words, &k.Exclude, &stations, &enabled, &limitNotified); err != nil {
		return domain.Keyword{}, false, err
	}
	if stations != "" {
		k.Stations = strings.Split(stations, ",")
	}
	k.Disabled = enabled == 0
	return k, limitNotified != 0, nil
}

// keywordStations は局の ID を、重ねを除いて前後の空白を落とし、カンマでつなぐ。
func keywordStations(stations []string) string {
	var out []string
	for _, st := range stations {
		st = strings.TrimSpace(st)
		if st == "" || strings.Contains(st, ",") {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == st
		}
		if !dup {
			out = append(out, st)
		}
	}
	return strings.Join(out, ",")
}

func validateKeyword(k domain.Keyword) error {
	if err := k.Validate(); err != nil {
		return err
	}
	for _, st := range k.Stations {
		if strings.Contains(st, ",") {
			return fmt.Errorf("キーワードの局 %q にカンマがある", st)
		}
	}
	return nil
}

// listKeywords はキーワードを ID の順（作った順）に読み、上限の通知の印も返す。
func listKeywords(ctx context.Context, q querier, where string, args ...any) ([]domain.Keyword, map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+keywordColumns+` FROM keywords `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []domain.Keyword
	notified := map[int64]bool{}
	for rows.Next() {
		k, n, err := scanKeyword(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, k)
		notified[k.ID] = n
	}
	return out, notified, rows.Err()
}

// ListKeywords はキーワードを作った順（ID の順）に読む。
func (s *Store) ListKeywords(ctx context.Context) ([]domain.Keyword, error) {
	ks, _, err := listKeywords(ctx, s.db, ``)
	return ks, err
}

// GetKeyword はキーワード id を読む。無ければ ErrNotFound。
func (s *Store) GetKeyword(ctx context.Context, id int64) (domain.Keyword, error) {
	ks, _, err := listKeywords(ctx, s.db, `WHERE id = ?`, id)
	if err != nil {
		return domain.Keyword{}, err
	}
	if len(ks) == 0 {
		return domain.Keyword{}, fmt.Errorf("キーワード %d: %w", id, ErrNotFound)
	}
	return ks[0], nil
}

// keywordKeyTaken は、語の形が key のキーワードが、id のほかにあれば ErrExists を返す。
func keywordKeyTaken(ctx context.Context, q querier, key string, id int64) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM keywords WHERE words_key = ? AND id != ?`, key, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("キーワード %q: %w", key, ErrExists)
	}
	return nil
}

func insertKeyword(ctx context.Context, tx *sql.Tx, k domain.Keyword) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO keywords (words, words_key, exclude, stations, enabled) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		k.Words, k.Key(), k.Exclude, keywordStations(k.Stations), boolInt(!k.Disabled)).Scan(&id)
	return id, err
}

func updateKeyword(ctx context.Context, tx *sql.Tx, k domain.Keyword) error {
	res, err := tx.ExecContext(ctx, `UPDATE keywords SET words = ?, words_key = ?, exclude = ?, stations = ?, enabled = ? WHERE id = ?`,
		k.Words, k.Key(), k.Exclude, keywordStations(k.Stations), boolInt(!k.Disabled), k.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("キーワード %d: %w", k.ID, ErrNotFound)
	}
	return nil
}

// CreateKeyword はキーワード k を作り、振った ID を返す。k.ID は見ない。語の形が同じキーワードがあれば ErrExists。
func (s *Store) CreateKeyword(ctx context.Context, k domain.Keyword) (int64, error) {
	if err := validateKeyword(k); err != nil {
		return 0, err
	}
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := keywordKeyTaken(ctx, tx, k.Key(), 0); err != nil {
			return err
		}
		var err error
		id, err = insertKeyword(ctx, tx, k)
		return err
	})
	return id, err
}

// UpdateKeyword はキーワード k.ID を k で置き換える（語・除外する語・局・有効／無効）。無ければ ErrNotFound、語の形が同じ
// ほかのキーワードがあれば ErrExists。足した放送回の記録と、足した 1 回の予約は触らない。
func (s *Store) UpdateKeyword(ctx context.Context, k domain.Keyword) error {
	if err := validateKeyword(k); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := keywordKeyTaken(ctx, tx, k.Key(), k.ID); err != nil {
			return err
		}
		return updateKeyword(ctx, tx, k)
	})
}

// DeleteKeyword はキーワード id と、その足した放送回の記録を消す。足した 1 回の予約と作った番組は残す
// （1 回の予約の keyword_id は NULL に戻る）。無ければ ErrNotFound。
func (s *Store) DeleteKeyword(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM keywords WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("キーワード %d: %w", id, ErrNotFound)
	}
	return nil
}

// keywordAirings は記録を読む（where で絞る）。State はまだ決めない。
func keywordAirings(ctx context.Context, q querier, where string, args ...any) ([]KeywordAiring, error) {
	rows, err := q.QueryContext(ctx, `SELECT keyword_id, station_id, station_name, start_at, end_at, title, show_name, added_at
		FROM keyword_airings `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeywordAiring
	for rows.Next() {
		var r KeywordAiring
		var start, end, added string
		if err := rows.Scan(&r.KeywordID, &r.StationID, &r.StationName, &start, &end, &r.Title, &r.ShowName, &added); err != nil {
			return nil, err
		}
		if r.Start, err = parseTime(start); err != nil {
			return nil, err
		}
		if r.End, err = parseTime(end); err != nil {
			return nil, err
		}
		if r.AddedAt, err = parseTime(added); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// jobsFromRecords は、記録 records の放送日のうちいちばん早いものと from の早い方から先のジョブを読む（JobsByAiring の形）。
// from が零値なら記録の放送日だけで決める。記録が無く from も零値なら空。
func jobsFromRecords(ctx context.Context, q querier, records []KeywordAiring, from domain.BroadcastDay) (map[string]domain.Job, error) {
	first := from
	for _, r := range records {
		if d := domain.BroadcastDayOf(r.Start); first.IsZero() || d.Before(first) {
			first = d
		}
	}
	if first.IsZero() {
		return map[string]domain.Job{}, nil
	}
	jobs, err := queryJobs(ctx, q, `WHERE broadcast_day >= ?`, first.String())
	if err != nil {
		return nil, err
	}
	return domain.JobsByAiring(jobs), nil
}

// onceKeys は、すべての番組の 1 回の予約の放送回の Key。
func onceKeys(ctx context.Context, q querier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, station_id, start_at, keyword_id FROM onces`)
	if err != nil {
		return nil, err
	}
	all, err := scanOnces(rows)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, o := range all {
		out[o.Key()] = true
	}
	return out, nil
}

// recordState は記録 r の状態を決める。その放送回にジョブがあれば、ジョブの状態で決める（done なら録音済み、expired なら
// 録音できなかった、確定の前の skipped なら録らない予定、確定した skipped なら録らなかった、ほかは録音予定）。ジョブが無ければ、1 回の予約があれば録音予定、無ければ外された。
func recordState(r KeywordAiring, jobs map[string]domain.Job, onces map[string]bool) string {
	if j, ok := jobs[r.Key()]; ok {
		switch j.State {
		case domain.StateDone:
			return KeywordAiringDone
		case domain.StateExpired:
			return KeywordAiringMissed
		case domain.StateSkipped:
			if !j.Settled {
				return KeywordAiringSkipPlanned
			}
			return KeywordAiringSkipped
		default:
			return KeywordAiringPlanned
		}
	}
	if onces[r.Key()] {
		return KeywordAiringPlanned
	}
	return KeywordAiringRemoved
}

// withStates は記録 records に状態を付ける。
func withStates(ctx context.Context, q querier, records []KeywordAiring) ([]KeywordAiring, error) {
	if len(records) == 0 {
		return records, nil
	}
	jobs, err := jobsFromRecords(ctx, q, records, domain.BroadcastDay{})
	if err != nil {
		return nil, err
	}
	onces, err := onceKeys(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].State = recordState(records[i], jobs, onces)
	}
	return records, nil
}

// KeywordAirings は、キーワード id が足した放送回の記録を、開始時刻の新しい順に、状態を付けて読む。キーワードが無ければ ErrNotFound。
func (s *Store) KeywordAirings(ctx context.Context, id int64) ([]KeywordAiring, error) {
	if _, err := s.GetKeyword(ctx, id); err != nil {
		return nil, err
	}
	records, err := keywordAirings(ctx, s.db, `WHERE keyword_id = ? ORDER BY start_at DESC, station_id`, id)
	if err != nil {
		return nil, err
	}
	return withStates(ctx, s.db, records)
}

// countPending は記録 records（状態付き）から、キーワードの ID → 録音予定の数を数える。
func countPending(records []KeywordAiring) map[int64]int {
	out := map[int64]int{}
	for _, r := range records {
		if r.State == KeywordAiringPlanned {
			out[r.KeywordID]++
		}
	}
	return out
}

// KeywordPending は、キーワードの ID → 足した放送回のうち、まだ録音を終えていない（録音予定の）回の数を返す。
// 0 のキーワードは入らない。
func (s *Store) KeywordPending(ctx context.Context) (map[int64]int, error) {
	records, err := keywordAirings(ctx, s.db, ``)
	if err != nil {
		return nil, err
	}
	records, err = withStates(ctx, s.db, records)
	if err != nil {
		return nil, err
	}
	return countPending(records), nil
}

// KeywordState は ApplyKeywords が渡す、トランザクションの中で読んだ今の状態。
type KeywordState struct {
	// Keywords はすべてのキーワード（ID の順。無効なものも）。
	Keywords []domain.Keyword
	// LimitNotified はキーワードの ID → 上限の通知の印。
	LimitNotified map[int64]bool
	// Shows はすべての番組（1 回の予約ごと）。
	Shows []domain.Show
	// Jobs は、記録の放送日と from のうち早い方から先のジョブ（domain.JobsByAiring の形）。
	Jobs map[string]domain.Job
	// Recorded は、どれかのキーワードの記録にある放送回の Key。
	Recorded map[string]bool
	// Pending はキーワードの ID → まだ録音を終えていない足した回の数。
	Pending map[int64]int
}

// KeywordChanges は ApplyKeywords で当てる変更。
type KeywordChanges struct {
	Plan domain.KeywordPlan
	// Notifications は同じトランザクションで書く通知（キーワードの出来事）。
	Notifications []Notification
}

// ApplyKeywords は、キーワードの判定と書き込みを 1 つのトランザクション（BEGIN IMMEDIATE）で行う。トランザクションの中で
// 今の状態（KeywordState。ジョブは放送日 from から先を含む）を読んで plan に渡し、返った変更を当てる。
//
//   - plan.Additions: Created なら番組を作り（同じ名前の番組がもうあればエラーで巻き戻す）、放送回ごとに 1 回の予約を
//     keyword_id 付きで足し、足した放送回の記録（足した時刻 now）を書く
//   - plan.LimitNotified: キーワードの上限の通知の印を変える
//   - Notifications: 通知の行を書く
//
// 当てた変更を返す。どこかで失敗したら何も変えない。
func (s *Store) ApplyKeywords(ctx context.Context, from domain.BroadcastDay, now time.Time, plan func(KeywordState) KeywordChanges) (KeywordChanges, error) {
	var changes KeywordChanges
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var st KeywordState
		var err error
		if st.Keywords, st.LimitNotified, err = listKeywords(ctx, tx, ``); err != nil {
			return err
		}
		if st.Shows, err = listShows(ctx, tx, ``); err != nil {
			return err
		}
		records, err := keywordAirings(ctx, tx, ``)
		if err != nil {
			return err
		}
		if st.Jobs, err = jobsFromRecords(ctx, tx, records, from); err != nil {
			return err
		}
		onces, err := onceKeys(ctx, tx)
		if err != nil {
			return err
		}
		st.Recorded = map[string]bool{}
		for i := range records {
			records[i].State = recordState(records[i], st.Jobs, onces)
			st.Recorded[records[i].Key()] = true
		}
		st.Pending = countPending(records)

		changes = plan(st)
		for _, add := range changes.Plan.Additions {
			if err := applyKeywordAddition(ctx, tx, add, now); err != nil {
				return err
			}
		}
		for id, v := range changes.Plan.LimitNotified {
			if _, err := tx.ExecContext(ctx, `UPDATE keywords SET limit_notified = ? WHERE id = ?`, boolInt(v), id); err != nil {
				return err
			}
		}
		for _, n := range changes.Notifications {
			if _, err := insertNotification(ctx, tx, n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return KeywordChanges{}, err
	}
	return changes, nil
}

func applyKeywordAddition(ctx context.Context, tx *sql.Tx, add domain.KeywordAddition, now time.Time) error {
	var showID int64
	if add.Created {
		if err := add.Show.Validate(); err != nil {
			return err
		}
		if err := showAbsent(ctx, tx, add.Show.Name); err != nil {
			return err
		}
		id, err := putShow(ctx, tx, add.Show)
		if err != nil {
			return err
		}
		showID = id
	} else {
		err := tx.QueryRowContext(ctx, `SELECT id FROM shows WHERE name = ?`, add.ShowName).Scan(&showID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("番組 %s: %w", add.ShowName, ErrNotFound)
		} else if err != nil {
			return err
		}
	}
	for _, a := range add.Airings {
		o := domain.Once{StationID: a.StationID, Start: a.Start, KeywordID: add.Keyword.ID}
		if err := o.Validate(); err != nil {
			return err
		}
		if _, err := insertOnce(ctx, tx, showID, o); err != nil {
			return fmt.Errorf("番組 %s に 1 回の予約 %s を足せない: %w", add.ShowName, o.Key(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO keyword_airings
			(keyword_id, station_id, station_name, start_at, end_at, title, show_name, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			add.Keyword.ID, a.StationID, add.Hit.StationName, formatTime(a.Start), formatTime(a.End), a.Title,
			add.ShowName, formatTime(now)); err != nil {
			return fmt.Errorf("キーワード %d の記録を書けない: %w", add.Keyword.ID, err)
		}
	}
	return nil
}
