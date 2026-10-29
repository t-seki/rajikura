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

// querier は *sql.DB と *sql.Tx の共通部分。
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ExpandDay は放送日 day の既存のジョブを読み、それを expand に渡して得た展開の差分を当てる。
// 当てた差分と、作ったジョブの ID（差分の Create と同じ並び）を返す。
//
// 読み出しから適用までを 1 つの immediate トランザクションで行うので、2 つの展開が同時に同じ放送日を展開しても、
// 同じジョブを二重に作らない。expand の中で DB に触れてはいけない。
// 矛盾（Conflicts）は保存しない。
func (s *Store) ExpandDay(ctx context.Context, day domain.BroadcastDay, expand func(jobs []domain.Job) domain.Plan) (domain.Plan, []int64, error) {
	var plan domain.Plan
	var ids []int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		jobs, err := jobsOnDay(ctx, tx, day)
		if err != nil {
			return err
		}
		plan = expand(jobs)
		ids, err = applyPlan(ctx, tx, plan)
		return err
	})
	if err != nil {
		return domain.Plan{}, nil, err
	}
	return plan, ids, nil
}

// applyPlan は展開の差分を tx の中で当て、作ったジョブの ID を Create と同じ並びで返す。
// 更新と削除は、ジョブがまだ展開の触るもの（waiting と、確定の前の skipped。domain.Job.Expandable）のときだけ行う。
func applyPlan(ctx context.Context, tx *sql.Tx, plan domain.Plan) ([]int64, error) {
	for _, j := range plan.Delete {
		if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id = ? AND `+expandable, j.ID); err != nil {
			return nil, fmt.Errorf("ジョブ %d を消せない: %w", j.ID, err)
		}
	}
	for _, j := range plan.Update {
		res, err := tx.ExecContext(ctx, `UPDATE jobs SET part = ?, next_attempt_at = ?, deadline = ?, state = ?, settled = ?
			WHERE id = ? AND `+expandable,
			j.Part, formatTime(j.NextAttemptAt), formatTime(j.Deadline), string(j.State), boolInt(j.Settled), j.ID)
		if err != nil {
			return nil, fmt.Errorf("ジョブ %d を更新できない: %w", j.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("ジョブ %d を更新できたか分からない: %w", j.ID, err)
		}
		if n == 0 {
			continue
		}
		if err := putAirings(ctx, tx, j.ID, j.Airings, nil, nil); err != nil {
			return nil, fmt.Errorf("ジョブ %d の放送回を保存できない: %w", j.ID, err)
		}
	}
	var ids []int64
	for _, j := range plan.Create {
		id, err := insertJob(ctx, tx, j)
		if err != nil {
			return nil, fmt.Errorf("番組 %s・%s のジョブを作れない: %w", j.ShowName, j.Day, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// expandable は、展開が触るジョブ（domain.Job.Expandable）に絞る SQL の条件。
const expandable = `(state = 'waiting' OR state = 'skipped' AND settled = 0)`

// InsertJob はジョブを 1 つ作り、ID を返す。状態が空なら waiting にする。
func (s *Store) InsertJob(ctx context.Context, j domain.Job) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = insertJob(ctx, tx, j)
		return err
	})
	return id, err
}

func insertJob(ctx context.Context, tx *sql.Tx, j domain.Job) (int64, error) {
	if j.State == "" {
		j.State = domain.StateWaiting
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO jobs
		(show_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path, by_person, settled,
		 chapters, chapters_check_at, chapters_refetch)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		j.ShowName, j.Day.String(), j.Part, string(j.State), formatTime(j.NextAttemptAt),
		j.Attempts, formatTime(j.Deadline), j.OutputPath, boolInt(j.ByPerson), boolInt(j.Settled),
		string(j.Chapters), formatTime(j.ChapterCheckAt), boolInt(j.ChapterRefetch)).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := putAirings(ctx, tx, id, j.Airings, j.Missing, j.ChapterWaits); err != nil {
		return 0, err
	}
	return id, nil
}

// putAirings はジョブの放送回を丸ごと置き換える。missing に含まれる放送回に欠落の印を付け、waits（Airing.Key → 待っている
// 様子）の放送回にチャプターの待ちを書く。
func putAirings(ctx context.Context, tx *sql.Tx, jobID int64, airings, missing []domain.Airing, waits map[string]domain.AiringChapterWait) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_airings WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	miss := map[string]bool{}
	for _, a := range missing {
		miss[a.Key()] = true
	}
	for i, a := range airings {
		_, err := tx.ExecContext(ctx, `INSERT INTO job_airings
			(job_id, position, airing_key, program_id, station_id, start_at, end_at,
			 title, performer, description, image_url, ts_in_ng, ts_out_ng, missing, chapter_wait)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			jobID, i, a.Key(), a.ProgramID, a.StationID, formatTime(a.Start), formatTime(a.End),
			a.Title, a.Performer, a.Description, a.ImageURL, a.TimeFree.InNG, a.TimeFree.OutNG,
			boolInt(miss[a.Key()]), int(waits[a.Key()]))
		if err != nil {
			return err
		}
	}
	return nil
}

const jobColumns = `id, show_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path, by_person, settled,
	chapters, chapters_check_at, chapters_refetch`

func scanJob(sc interface{ Scan(...any) error }) (domain.Job, error) {
	var j domain.Job
	var day, state, next, deadline, chapters, checkAt string
	var byPerson, settled, refetch int
	if err := sc.Scan(&j.ID, &j.ShowName, &day, &j.Part, &state, &next, &j.Attempts, &deadline, &j.OutputPath,
		&byPerson, &settled, &chapters, &checkAt, &refetch); err != nil {
		return j, err
	}
	j.ByPerson, j.Settled, j.ChapterRefetch = byPerson != 0, settled != 0, refetch != 0
	j.Chapters = domain.Chapters(chapters)
	var err error
	if j.ChapterCheckAt, err = parseTime(checkAt); err != nil {
		return j, err
	}
	if j.Day, err = domain.ParseBroadcastDay(day); err != nil {
		return j, err
	}
	if j.State, err = domain.ParseState(state); err != nil {
		return j, err
	}
	if j.NextAttemptAt, err = parseTime(next); err != nil {
		return j, err
	}
	if j.Deadline, err = parseTime(deadline); err != nil {
		return j, err
	}
	return j, nil
}

func loadAirings(ctx context.Context, q querier, j *domain.Job) error {
	rows, err := q.QueryContext(ctx, `SELECT program_id, station_id, start_at, end_at,
		title, performer, description, image_url, ts_in_ng, ts_out_ng, missing, chapter_wait
		FROM job_airings WHERE job_id = ? ORDER BY position`, j.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	j.Airings, j.Missing, j.ChapterWaits = nil, nil, nil
	for rows.Next() {
		var a domain.Airing
		var start, end string
		var missing, wait int
		if err := rows.Scan(&a.ProgramID, &a.StationID, &start, &end, &a.Title, &a.Performer,
			&a.Description, &a.ImageURL, &a.TimeFree.InNG, &a.TimeFree.OutNG, &missing, &wait); err != nil {
			return err
		}
		if a.Start, err = parseTime(start); err != nil {
			return err
		}
		if a.End, err = parseTime(end); err != nil {
			return err
		}
		j.Airings = append(j.Airings, a)
		if missing != 0 {
			j.Missing = append(j.Missing, a)
		}
		if wait != 0 {
			if j.ChapterWaits == nil {
				j.ChapterWaits = map[string]domain.AiringChapterWait{}
			}
			j.ChapterWaits[a.Key()] = domain.AiringChapterWait(wait)
		}
	}
	return rows.Err()
}

func queryJobs(ctx context.Context, q querier, where string, args ...any) ([]domain.Job, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs `+where, args...)
	if err != nil {
		return nil, err
	}
	var jobs []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range jobs {
		if err := loadAirings(ctx, q, &jobs[i]); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func getJob(ctx context.Context, q querier, id int64) (domain.Job, error) {
	jobs, err := queryJobs(ctx, q, `WHERE id = ?`, id)
	if err != nil {
		return domain.Job{}, err
	}
	if len(jobs) == 0 {
		return domain.Job{}, fmt.Errorf("ジョブ %d: %w", id, ErrNotFound)
	}
	return jobs[0], nil
}

// GetJob はジョブを放送回ごと読む。無ければ ErrNotFound。
func (s *Store) GetJob(ctx context.Context, id int64) (domain.Job, error) {
	return getJob(ctx, s.db, id)
}

// ListJobs はジョブを、放送日・番組名・パート番号の順に読む。states を渡せば、その状態のものに絞る。
func (s *Store) ListJobs(ctx context.Context, states ...domain.State) ([]domain.Job, error) {
	where, args := stateFilter(``, states)
	return queryJobs(ctx, s.db, where+` ORDER BY broadcast_day, show_name, part, id`, args...)
}

// ListJobsFromDay は放送日 day 以降のジョブを、ListJobs と同じ並びで読む。states を渡せば、その状態のものに絞る。
func (s *Store) ListJobsFromDay(ctx context.Context, day domain.BroadcastDay, states ...domain.State) ([]domain.Job, error) {
	where, args := stateFilter(day.String(), states)
	return queryJobs(ctx, s.db, where+` ORDER BY broadcast_day, show_name, part, id`, args...)
}

// stateFilter は WHERE 句を作る。from が空でなければ放送日 from 以降、states があればその状態に絞る。
func stateFilter(from string, states []domain.State) (string, []any) {
	var conds []string
	var args []any
	if from != `` {
		conds = append(conds, `broadcast_day >= ?`)
		args = append(args, from)
	}
	if len(states) > 0 {
		conds = append(conds, `state IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")+`)`)
		for _, st := range states {
			args = append(args, string(st))
		}
	}
	if len(conds) == 0 {
		return ``, nil
	}
	return `WHERE ` + strings.Join(conds, ` AND `), args
}

// JobsOnDay はその放送日のジョブを読む。展開には ExpandDay を使う（読み出しと適用を 1 つのトランザクションにする）。
func (s *Store) JobsOnDay(ctx context.Context, day domain.BroadcastDay) ([]domain.Job, error) {
	return jobsOnDay(ctx, s.db, day)
}

// JobsFromDay は放送日 day 以降のジョブを、放送日・番組名・パートの順に読む。
func (s *Store) JobsFromDay(ctx context.Context, day domain.BroadcastDay) ([]domain.Job, error) {
	return queryJobs(ctx, s.db, `WHERE broadcast_day >= ? ORDER BY broadcast_day, show_name, part, id`, day.String())
}

func jobsOnDay(ctx context.Context, q querier, day domain.BroadcastDay) ([]domain.Job, error) {
	return queryJobs(ctx, q, `WHERE broadcast_day = ? ORDER BY show_name, part, id`, day.String())
}

// liveJob は、ライブで録るジョブ（domain.Job.Live。放送回の 1 つでも ts_in_ng が 0 でない）に絞る SQL の条件。
const liveJob = `EXISTS (SELECT 1 FROM job_airings WHERE job_airings.job_id = jobs.id AND job_airings.ts_in_ng != 0)`

// ClaimDue は、次の試行の時刻が now を過ぎた waiting / retrying のジョブのうち、タイムフリーから録るもの（ライブで録る
// ジョブ（domain.Job.Live）を除く）を 1 件だけ原子的に running にし、試行回数を 1 つ増やして返す。無ければ false。
// タイムフリーの録音のループが使う（ライブで録るジョブは ClaimDueLive）。
//
// 録音期限を過ぎたジョブも返す。呼び出し側が録音期限を確かめ、過ぎていれば
// domain.JudgeAtDeadline で、取れた分を保存して done にするか expired にするかを決める（録り直しの試行は、一部しか取れて
// いなければ保存せずに expired にする。internal/app の finishAtDeadline。ADR 0025。チャプターの取り直しの試行なら、expired に
// せずに done に戻す。ADR 0027）。
func (s *Store) ClaimDue(ctx context.Context, now time.Time) (domain.Job, bool, error) {
	return s.claimDue(ctx, now, `NOT `+liveJob)
}

// ClaimDueLive は ClaimDue のライブで録るジョブ（domain.Job.Live）の側で、録音期限（deadline）が now より後のものを拾う。
// ライブの録音役（internal/app）が、同時に録る本数の枠の中で使う。retrying のジョブ（録れた分の保存が失敗して保存を
// やり直すもの、人が手動再試行したもの）も拾う。録音期限を過ぎたものは ClaimDueLiveSave。
func (s *Store) ClaimDueLive(ctx context.Context, now time.Time) (domain.Job, bool, error) {
	return s.claimDue(ctx, now, liveJob+` AND deadline > ?`, formatTime(now))
}

// ClaimDueLiveSave は ClaimDueLive の、録音期限（deadline）が now を過ぎたものを拾う側。録音期限を過ぎたライブで録るジョブの
// 試行は radiko に繋がず、作業用フォルダに残したセグメントから保存だけを行うので、ライブの録音役（internal/app）は同時に録る
// 本数の枠に数えずに拾う。
func (s *Store) ClaimDueLiveSave(ctx context.Context, now time.Time) (domain.Job, bool, error) {
	return s.claimDue(ctx, now, liveJob+` AND deadline <= ?`, formatTime(now))
}

// claimDue は ClaimDue・ClaimDueLive・ClaimDueLiveSave の中身。cond は方式などで絞る SQL の条件、args はその値。
func (s *Store) claimDue(ctx context.Context, now time.Time, cond string, args ...any) (domain.Job, bool, error) {
	var job domain.Job
	found := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs
			WHERE state IN ('waiting', 'retrying') AND next_attempt_at <= ? AND `+cond+`
			ORDER BY next_attempt_at, id LIMIT 1`, append([]any{formatTime(now)}, args...)...))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := domain.Transition(j.State, domain.StateRunning); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'running', attempts = attempts + 1 WHERE id = ?`, j.ID); err != nil {
			return err
		}
		job, err = getJob(ctx, tx, j.ID)
		found = err == nil
		return err
	})
	return job, found, err
}

// transition はジョブを to に遷移させる。許されない遷移は domain.ErrInvalidTransition を包んだエラーにする。
// set と args は state のほかに更新する列（"col = ?" の形）とその値。fn があれば同じトランザクションで続けて実行する。
func (s *Store) transition(ctx context.Context, id int64, to domain.State, fn func(tx *sql.Tx) error, set string, args ...any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id = ?`, id).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("ジョブ %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		from, err := domain.ParseState(cur)
		if err != nil {
			return err
		}
		if err := domain.Transition(from, to); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		q := `UPDATE jobs SET state = ?`
		if set != "" {
			q += `, ` + set
		}
		if _, err := tx.ExecContext(ctx, q+` WHERE id = ?`, append(append([]any{string(to)}, args...), id)...); err != nil {
			return err
		}
		if fn != nil {
			return fn(tx)
		}
		return nil
	})
}

// Retry は running のジョブを retrying にし、次の試行の時刻を next にする（試行が失敗し、
// 録音期限の内側に次の試行がある）。
//
// 次の試行が無くても、取れた放送回があるときは、next を録音期限にして呼ぶ（internal/app）。
// 録音期限を過ぎてから ClaimDue で取られ、入手はせずに取れた分を保存して done にする
// （domain.JudgeAtDeadline）。取れた分を捨てずに済ませるため。録り直しの試行は、一部しか取れていなければ保存せずに expired に
// する（internal/app の finishAtDeadline。ADR 0025）。チャプターの取り直しの試行なら、expired にせずに done に戻す（ADR 0027）。
//
// ライブで録るジョブも、録れた分の保存が失敗したときは、保存だけをやり直す次の試行の時刻（domain.LiveSaveRetryAt。録音期限を
// 過ぎてもよい）を next にして呼ぶ（internal/app）。
// 足したら直す: ジョブの状態の移り方
func (s *Store) Retry(ctx context.Context, id int64, next time.Time) error {
	return s.transition(ctx, id, domain.StateRetrying, nil, `next_attempt_at = ?`, formatTime(next))
}

// Expire は running のジョブを expired にする（次の試行が無い、または録音期限を過ぎた）。
func (s *Store) Expire(ctx context.Context, id int64) error {
	return s.transition(ctx, id, domain.StateExpired, nil, ``)
}

// Complete は running のジョブを done にし、保存先のパスを記録する。
// missing に渡した放送回に欠落の印を付ける（取れた分だけを保存したとき）。
// ch はチャプターの値（domain.ChaptersAfterSave。保存のたびに決め直す）で、待つなら次に API を引く時刻と待っている放送回も書く。
// チャプターの取り直しの印（chapters_refetch）は外す（ADR 0027）。
// 足したら直す: ジョブの状態の移り方
func (s *Store) Complete(ctx context.Context, id int64, outputPath string, missing []domain.Airing, ch domain.ChapterStatus) error {
	return s.transition(ctx, id, domain.StateDone, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE job_airings SET missing = 0 WHERE job_id = ?`, id); err != nil {
			return err
		}
		for _, a := range missing {
			if _, err := tx.ExecContext(ctx, `UPDATE job_airings SET missing = 1 WHERE job_id = ? AND airing_key = ?`, id, a.Key()); err != nil {
				return err
			}
		}
		return putChapterWaits(ctx, tx, id, ch.Waits)
	}, `output_path = ?, chapters = ?, chapters_check_at = ?, chapters_refetch = 0`, outputPath, string(ch.Chapters), formatTime(ch.CheckAt))
}

// putChapterWaits は、ジョブ id の放送回のチャプターの待ちを waits（Airing.Key → 待っている様子）に置き換える。
func putChapterWaits(ctx context.Context, tx *sql.Tx, id int64, waits map[string]domain.AiringChapterWait) error {
	if _, err := tx.ExecContext(ctx, `UPDATE job_airings SET chapter_wait = 0 WHERE job_id = ?`, id); err != nil {
		return err
	}
	for key, w := range waits {
		if _, err := tx.ExecContext(ctx, `UPDATE job_airings SET chapter_wait = ? WHERE job_id = ? AND airing_key = ?`, int(w), id, key); err != nil {
			return err
		}
	}
	return nil
}

// DueChapterChecks は、チャプターを待っている done のジョブ（domain.ChaptersWaiting）のうち、次にチャプターの API を引く時刻
// （chapters_check_at）が now を過ぎたものを、その時刻の古い順に読む（internal/app の checkChapters。ADR 0027）。
func (s *Store) DueChapterChecks(ctx context.Context, now time.Time) ([]domain.Job, error) {
	return queryJobs(ctx, s.db, `WHERE `+waitingChapters+` AND chapters_check_at != '' AND chapters_check_at <= ?
		ORDER BY chapters_check_at, id`, formatTime(now))
}

// waitingChapters は、チャプターを待っている done のジョブに絞る SQL の条件。
const waitingChapters = `state = 'done' AND chapters = 'waiting'`

// UpdateChapterWait は、チャプターを待っている done のジョブ id のチャプターの値を ch にする（待ち続けるなら次に引く時刻と
// 待っている放送回、待つのをやめるなら入らなかった）。読んだ後に人が録り直したなどで、もう待っていなければ何もせず false を返す。
func (s *Store) UpdateChapterWait(ctx context.Context, id int64, ch domain.ChapterStatus) (bool, error) {
	updated := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE jobs SET chapters = ?, chapters_check_at = ? WHERE id = ? AND `+waitingChapters,
			string(ch.Chapters), formatTime(ch.CheckAt), id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		updated = true
		return putChapterWaits(ctx, tx, id, ch.Waits)
	})
	return updated, err
}

// StartChapterRefetch は、チャプターを待っている done のジョブ id に、チャプターの取り直しの試行を起こす（ADR 0027）。移り方は
// 録り直し（Rerecord）と同じで、retrying にし、試行回数を 0 に戻して次の試行を now にする。保存先・欠落の印・期限間近を通知した
// 時刻は変えない。チャプターの取り直しの印（chapters_refetch）を立て、待ち（次に引く時刻と待っている放送回）を消す（値は
// 待っているのまま）。もう待っていなければ何もせず false を返す。録り直しを受け付けない（domain.CheckRerecord。録音期限を
// 過ぎたなど）なら、起こさずに入らなかったにして false を返す。
// 足したら直す: ジョブの状態の移り方
func (s *Store) StartChapterRefetch(ctx context.Context, id int64, now time.Time) (bool, error) {
	started := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if j.State != domain.StateDone || j.Chapters != domain.ChaptersWaiting {
			return nil
		}
		if err := putChapterWaits(ctx, tx, id, nil); err != nil {
			return err
		}
		if domain.CheckRerecord(j, now) != nil {
			_, err := tx.ExecContext(ctx, `UPDATE jobs SET chapters = 'not_added', chapters_check_at = '' WHERE id = ?`, id)
			return err
		}
		if err := domain.Transition(j.State, domain.StateRetrying); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'retrying', attempts = 0, next_attempt_at = ?,
			chapters_check_at = '', chapters_refetch = 1 WHERE id = ?`, formatTime(now), id); err != nil {
			return err
		}
		started = true
		return nil
	})
	return started, err
}

// RevertChapterRefetch は、チャプターの取り直しで起こした試行が尽きた（録音期限の時点で一部の放送回しか取れなかったときも）
// running のジョブを done に戻す（ADR 0027。人が頼んだ録り直しなら expired にするところ）。保存先・欠落の印は前のまま残し、
// チャプターの値は入らなかったにして、チャプターの取り直しの印を外す。
// 足したら直す: ジョブの状態の移り方
func (s *Store) RevertChapterRefetch(ctx context.Context, id int64) error {
	return s.transition(ctx, id, domain.StateDone, func(tx *sql.Tx) error {
		return putChapterWaits(ctx, tx, id, nil)
	}, `chapters = 'not_added', chapters_check_at = '', chapters_refetch = 0`)
}

// ErrPastDeadline は録音期限を過ぎていて、再試行しても取れないこと。errors.Is で見分ける。
var ErrPastDeadline = errors.New("録音期限を過ぎている")

// ManualRetry は retrying / expired / 確定した skipped（録らなかった）のジョブを retrying にして、次の試行を now にする（人が手動で再試行した）。
// 試行回数を 0 に戻して非会員の上限を数え直す。試行の履歴と取り終えた放送回は消さず、waiting には戻さない
// （番組表の取り直しと展開のし直しはしない）。retrying のジョブは状態を変えず、時刻と試行回数だけを書き換える。
// それ以外の状態（確定の前の skipped（録らない予定）を含む。放送の前に録りにいかないように、「録る」の RecordByPerson で
// waiting に戻す）は domain.ErrInvalidTransition、録音期限（deadline <= now）を過ぎたジョブは ErrPastDeadline、
// 無いジョブは ErrNotFound を包んだエラーにする。
//
// ただし、ライブで録るジョブで、最後の試行（attempts の表で試行の開始が最も新しいもの）が保存か検証の段階で失敗し、その試行の
// 終わりから 30 日以内（domain.LiveKeepUntil）の retrying か expired のジョブは、録音期限を過ぎていても受け付ける（作業用
// フォルダに残したセグメントから保存をやり直す）。判定は domain.ManualRetryInTime で、読み出しと同じトランザクションの中で行う。
// 足したら直す: ジョブの状態の移り方
func (s *Store) ManualRetry(ctx context.Context, id int64, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		return manualRetry(ctx, tx, j, now)
	})
}

// manualRetry は ManualRetry の中身。j は tx の中で読んだジョブ。
func manualRetry(ctx context.Context, tx *sql.Tx, j domain.Job, now time.Time) error {
	from := j.State
	// running → retrying は遷移表にあるが、人が動かしてよいのは retrying / expired / 確定した skipped だけ。
	if from != domain.StateRetrying && from != domain.StateExpired && !(from == domain.StateSkipped && j.Settled) {
		what := string(from)
		if from == domain.StateSkipped {
			what = "録らない予定（確定の前の skipped）"
		}
		return fmt.Errorf("ジョブ %d: %w: %s は手動で再試行できない", j.ID, domain.ErrInvalidTransition, what)
	}
	if from != domain.StateRetrying {
		if err := domain.Transition(from, domain.StateRetrying); err != nil {
			return fmt.Errorf("ジョブ %d: %w", j.ID, err)
		}
	}
	if !j.Deadline.After(now) {
		last, err := lastAttempt(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		if !domain.ManualRetryInTime(j, last, now) {
			return fmt.Errorf("ジョブ %d: %w", j.ID, ErrPastDeadline)
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'retrying', attempts = 0, next_attempt_at = ? WHERE id = ?`,
		formatTime(now), j.ID)
	return err
}

// Rerecord は、人が録り直しを選んだ done のジョブを retrying にして、次の試行を now にする（ADR 0025）。戻し方は手動再試行と
// 同じで、試行回数を 0 に戻し、試行の履歴は消さない。保存先（output_path）・欠落の印（job_airings.missing）・期限間近を
// 通知した時刻（deadline_notified_at）は変えない（録れなければ前の値のまま残し、期限間近は 1 度だけ通知する）。
// 作業用フォルダは done にしたときに消えているので、次の試行はどの放送回も radiko から取り直す。
// チャプターを待っていれば待ちを消して入らなかったにし（保存のたびに待つかを決め直す）、チャプターの取り直しの印は外す（ADR 0027）。
// 受け付けるかは domain.CheckRerecord で、読み出しと同じトランザクションの中で決める。done でないジョブは
// domain.ErrInvalidTransition、ライブで録るジョブは domain.ErrRerecordLive、録音期限を過ぎたジョブは
// domain.ErrRerecordPastDeadline、無いジョブは ErrNotFound を包んだエラーにする。通知はしない（人の操作）。
// 足したら直す: ジョブの状態の移り方
func (s *Store) Rerecord(ctx context.Context, id int64, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := domain.CheckRerecord(j, now); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		if err := domain.Transition(j.State, domain.StateRetrying); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		if err := putChapterWaits(ctx, tx, id, nil); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'retrying', attempts = 0, next_attempt_at = ?, chapters_check_at = '',
			chapters_refetch = 0, chapters = CASE chapters WHEN 'waiting' THEN 'not_added' ELSE chapters END WHERE id = ?`,
			formatTime(now), id)
		return err
	})
}

// SkipByPerson は、人が「録らない」を選んだジョブを skipped にし、人が決めた印（by_person）を付ける（ADR 0019）。
// 受け付けるのは、試行がまだ 1 度も無い waiting のジョブだけ（放送の前でも後でも）。次の試行の時刻（最初の試行の時刻）を
// 過ぎていれば（next_attempt_at <= now）確定した録らなかった、まだなら録らない予定にする。
// それ以外の状態（running・retrying・done・expired・skipped）は domain.ErrInvalidTransition、無いジョブは ErrNotFound を包んだ
// エラーにする。通知はしない（人の操作）。
func (s *Store) SkipByPerson(ctx context.Context, id int64, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if j.State != domain.StateWaiting || j.Attempts != 0 {
			return fmt.Errorf("ジョブ %d: %w: %s（試行 %d 回）は録らないにできない", id, domain.ErrInvalidTransition, j.State, j.Attempts)
		}
		if err := domain.Transition(j.State, domain.StateSkipped); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'skipped', by_person = 1, settled = ? WHERE id = ?`,
			boolInt(!now.Before(j.NextAttemptAt)), id)
		return err
	})
}

// RecordByPerson は、人が「録る」を選んだ skipped のジョブを録るに戻す（ADR 0019）。確定の前（録らない予定）なら waiting にして
// 人が決めた印（by_person）を付ける（次の試行の時刻はそのまま。番組名が食い違っていても、その後の展開は状態を変えない）。
// 確定した後（録らなかった）なら ManualRetry と同じく retrying にする（録音期限を過ぎていれば ErrPastDeadline）。
// skipped でないジョブは domain.ErrInvalidTransition、無いジョブは ErrNotFound を包んだエラーにする。通知はしない（人の操作）。
func (s *Store) RecordByPerson(ctx context.Context, id int64, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if j.State != domain.StateSkipped {
			return fmt.Errorf("ジョブ %d: %w: %s は録るに戻せない", id, domain.ErrInvalidTransition, j.State)
		}
		if j.Settled {
			return manualRetry(ctx, tx, j, now)
		}
		if err := domain.Transition(j.State, domain.StateWaiting); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'waiting', by_person = 1, settled = 0 WHERE id = ?`, id)
		return err
	})
}

// DueUnsettledSkipped は、確定の前の skipped のジョブ（録らない予定）のうち、最初の試行の時刻（next_attempt_at）が now を
// 過ぎたものを、その時刻の古い順に読む（確定させる対象。internal/app の refreshBeforeFirstAttempt）。
func (s *Store) DueUnsettledSkipped(ctx context.Context, now time.Time) ([]domain.Job, error) {
	return queryJobs(ctx, s.db, `WHERE state = 'skipped' AND settled = 0 AND next_attempt_at <= ? ORDER BY next_attempt_at, id`,
		formatTime(now))
}

// SettleSkipped は、確定の前の skipped のジョブ（録らない予定）を確定させる（録らなかったにする）。確定したら true。
// 最初の試行の時刻を過ぎたジョブを、展開に依らずに確定させるとき（人が録らないと決めたジョブ、番組表を取り直せないまま
// 録音期限を過ぎたジョブ、取り直しを待った局・放送日を展開し終えても録らない予定のまま残ったジョブ）に使う
// （internal/app の settleDueSkipped）。ほかの状態や確定したジョブ、無いジョブは何もせず false を返す。
func (s *Store) SettleSkipped(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET settled = 1 WHERE id = ? AND state = 'skipped' AND settled = 0`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Recover は起動時の回復。前回のプロセスが running のまま落ちていたタイムフリーから録るジョブを retrying に戻し、
// 戻した件数を返す。次の試行の時刻は変えない（すでに過ぎているので、すぐ実行される）。
// ライブで録るジョブ（domain.Job.Live）は retrying にせず running のまま残す（ライブの録音役が RunningLive で拾い、
// 取ったセグメントを残して繋ぎ直す）。
// 足したら直す: ジョブの状態の移り方
func (s *Store) Recover(ctx context.Context) (int, error) {
	if !domain.CanTransition(domain.StateRunning, domain.StateRetrying) {
		return 0, errors.New("running から retrying へは遷移できない")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'retrying' WHERE state = 'running' AND NOT `+liveJob)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RunningLive は、running のライブで録るジョブ（domain.Job.Live）を、次の試行の時刻と ID の順に読む。ライブの録音役
// （internal/app）が、前回のプロセスが録っている途中で終わったジョブを拾い直すのに使う。
func (s *Store) RunningLive(ctx context.Context) ([]domain.Job, error) {
	return queryJobs(ctx, s.db, `WHERE state = 'running' AND `+liveJob+` ORDER BY next_attempt_at, id`)
}

// RecordAttempt は試行を記録し、ID を返す。試行回数は ClaimDue が数える。
func (s *Store) RecordAttempt(ctx context.Context, a domain.Attempt) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO attempts
		(job_id, started_at, ended_at, failed_stage, error, extinf_total_ms, probe_duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		a.JobID, formatTime(a.StartedAt), formatTime(a.EndedAt), string(a.FailedStage), a.Error,
		a.ExtinfTotal.Milliseconds(), a.ProbeDuration.Milliseconds()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("ジョブ %d の試行を記録できない: %w", a.JobID, err)
	}
	return id, nil
}

const attemptCols = `id, job_id, started_at, ended_at, failed_stage, error, extinf_total_ms, probe_duration_ms`

func scanAttempt(sc interface{ Scan(...any) error }) (domain.Attempt, error) {
	var a domain.Attempt
	var started, ended, stage string
	var extinf, probe int64
	if err := sc.Scan(&a.ID, &a.JobID, &started, &ended, &stage, &a.Error, &extinf, &probe); err != nil {
		return a, err
	}
	var err error
	if a.StartedAt, err = parseTime(started); err != nil {
		return a, err
	}
	if a.EndedAt, err = parseTime(ended); err != nil {
		return a, err
	}
	a.FailedStage = domain.Stage(stage)
	a.ExtinfTotal = time.Duration(extinf) * time.Millisecond
	a.ProbeDuration = time.Duration(probe) * time.Millisecond
	return a, nil
}

// lastAttempt はジョブの最後の試行（試行の開始が最も新しいもの。Attempts の最後の 1 件と同じ）を読む。無ければ nil。
func lastAttempt(ctx context.Context, q querier, jobID int64) (*domain.Attempt, error) {
	a, err := scanAttempt(q.QueryRowContext(ctx, `SELECT `+attemptCols+` FROM attempts WHERE job_id = ?
		ORDER BY started_at DESC, id DESC LIMIT 1`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Attempts はジョブの試行を、始めた順に読む。
func (s *Store) Attempts(ctx context.Context, jobID int64) ([]domain.Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+attemptCols+` FROM attempts WHERE job_id = ? ORDER BY started_at, id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var as []domain.Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		as = append(as, a)
	}
	return as, rows.Err()
}

// LatestAttemptsFromDay は、放送日 day 以降のジョブについて、試行のあるジョブごとの直近の試行（Attempts の最後の 1 件）を、
// ジョブの ID で引ける形で 1 回の問い合わせで読む。放送日がそれより前のジョブの試行は読まない。
func (s *Store) LatestAttemptsFromDay(ctx context.Context, day domain.BroadcastDay) (map[int64]domain.Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+attemptCols+` FROM (
		SELECT attempts.*, ROW_NUMBER() OVER (PARTITION BY attempts.job_id ORDER BY attempts.started_at DESC, attempts.id DESC) AS rn
		FROM attempts JOIN jobs ON jobs.id = attempts.job_id WHERE jobs.broadcast_day >= ?
	) WHERE rn = 1`, day.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]domain.Attempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out[a.JobID] = a
	}
	return out, rows.Err()
}
