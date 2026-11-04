package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 保存先の移し（用語集。docs/adr/0039-saved-files-follow-show-rename.md）の頼みの印（jobs.move_requested_at）と、移しの履歴
// （output_moves）。頼みを書くのは、番組の名前の変更（ReplaceShow）と人の頼み（RequestOutputMove）。移すのは録音のループ
// （internal/app の moveOutputs。DueOutputMoves で拾い、FinishOutputMove で結果を書く）。

// ErrNoShow は、保存先の移しの移し先に指定した番組が無いこと（RequestOutputMove）。errors.Is で見分ける。ジョブが無いこと
// （ErrNotFound）とは分ける。
var ErrNoShow = errors.New("番組が無い")

// RequestOutputMove は、人が頼んだジョブ id の保存先の移し（ADR 0039）を受け付け、頼みの印（move_requested_at）に now を書く。
// show が空でなく、ジョブの番組の名前と違えば、その番組（今あるもの）にジョブの番組の名前を付け替えてから印を書く（同じ
// トランザクション）。無い番組なら ErrNoShow を包んだエラーにする。ファイルを移すのは録音のループ（DueOutputMoves で拾う。保存先の
// ファイルを書く者を 1 つに保つ）。状態・試行回数・保存先は変えない。受け付けるかは domain.CheckOutputMove で、読み出しと同じ
// トランザクションの中で決める（保存先の無いジョブは domain.ErrOutputMoveNoOutput、録音中のジョブは domain.ErrOutputMoveRunning、
// 無いジョブは ErrNotFound を包んだエラー）。もう頼んでいれば、頼みの時刻を now にし直す。
// 足したら直す: DB の表
func (s *Store) RequestOutputMove(ctx context.Context, id int64, show string, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := domain.CheckOutputMove(j); err != nil {
			return fmt.Errorf("ジョブ %d: %w", id, err)
		}
		name := j.ShowName
		if show != "" && show != j.ShowName {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shows WHERE name = ?`, show).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("番組 %s: %w", show, ErrNoShow)
			}
			name = show
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET show_name = ?, move_requested_at = ? WHERE id = ?`, name, formatTime(now), id)
		return err
	})
}

// DueOutputMoves は、保存先の移しを頼まれたジョブのうち、保存先があり録音中（running）でないものを、頼んだ時刻の古い順に読む
// （internal/app の moveOutputs）。録音中のジョブは、試行が終わってから拾う（印は残る）。
func (s *Store) DueOutputMoves(ctx context.Context) ([]domain.Job, error) {
	return queryJobs(ctx, s.db, `WHERE move_requested_at != '' AND output_path != '' AND state != 'running'
		ORDER BY move_requested_at, id`)
}

// FinishOutputMove は、ジョブ m.JobID の保存先の移し（ADR 0039）の結果を書く。record なら、移しの履歴に m を 1 行足す（移し先が
// 今の保存先と同じで、移すものが無かったときは足さない）。m.Result が移した（domain.OutputMoved）なら、保存先が m.From のままのときだけ
// 保存先を m.To に付け替える。あわせて、拾ったときの頼みの時刻 requestedAt（domain.Job.MoveRequestedAt）のままなら頼みの印を消す（拾った
// 後に頼み直されていれば残し、次の見回りでもう一度移す）。どれも同じトランザクションで書く。状態・試行回数・欠落の印は変えない。
// ジョブが無ければ ErrNotFound を包んだエラーにする。
// 足したら直す: DB の表
func (s *Store) FinishOutputMove(ctx context.Context, m domain.OutputMove, record bool, requestedAt time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE id = ?`, m.JobID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("ジョブ %d: %w", m.JobID, ErrNotFound)
		}
		if record {
			if _, err := tx.ExecContext(ctx, `INSERT INTO output_moves (job_id, moved_at, from_path, to_path, result, error)
				VALUES (?, ?, ?, ?, ?, ?)`, m.JobID, formatTime(m.At), m.From, m.To, string(m.Result), m.Error); err != nil {
				return err
			}
		}
		if m.Result == domain.OutputMoved {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET output_path = ? WHERE id = ? AND output_path = ?`,
				m.To, m.JobID, m.From); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET move_requested_at = '' WHERE id = ? AND move_requested_at = ?`,
			m.JobID, formatTime(requestedAt))
		return err
	})
}

// OutputMoves は、ジョブ jobID の保存先の移しの履歴を、移した順に読む。
func (s *Store) OutputMoves(ctx context.Context, jobID int64) ([]domain.OutputMove, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, moved_at, from_path, to_path, result, error FROM output_moves
		WHERE job_id = ? ORDER BY moved_at, id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OutputMove
	for rows.Next() {
		var m domain.OutputMove
		var at, result string
		if err := rows.Scan(&m.ID, &m.JobID, &at, &m.From, &m.To, &result, &m.Error); err != nil {
			return nil, err
		}
		if m.At, err = parseTime(at); err != nil {
			return nil, err
		}
		m.Result = domain.OutputMoveResult(result)
		out = append(out, m)
	}
	return out, rows.Err()
}
