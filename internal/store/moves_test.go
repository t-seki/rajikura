package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 人が頼む保存先の移し（ADR 0039）: 保存先のあるジョブだけを受け付け（録音中・保存先の無い・無いジョブは断る）、今ある番組を指定すると
// ジョブをその番組に付け替える（無い番組は断り、何も変えない）。状態・保存先は変えない。
func TestRequestOutputMove(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.PutShow(ctx, domain.NewShow("今の番組", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))); err != nil {
		t.Fatal(err)
	}
	insert := func(state domain.State, output string) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{ShowName: "古い名前", Day: day, State: state, Airings: []domain.Airing{flat1},
			NextAttemptAt: jst(2026, 10, 5, 11, 0), Deadline: jst(2026, 10, 13, 5, 0), OutputPath: output})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	now := jst(2026, 10, 6, 12, 0)
	done := insert(domain.StateDone, "/library/局/古い名前/古い名前 2026-10-05.m4a")
	expired := insert(domain.StateExpired, "/library/局/古い名前/古い名前 2026-10-05-2.m4a")
	running := insert(domain.StateRunning, "/library/局/古い名前/古い名前 2026-10-05-3.m4a")
	waiting := insert(domain.StateWaiting, "")

	if err := s.RequestOutputMove(ctx, running, "", now); !errors.Is(err, domain.ErrOutputMoveRunning) {
		t.Errorf("録音中: %v", err)
	}
	if err := s.RequestOutputMove(ctx, waiting, "", now); !errors.Is(err, domain.ErrOutputMoveNoOutput) {
		t.Errorf("保存先が無い: %v", err)
	}
	if err := s.RequestOutputMove(ctx, 9999, "", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}
	if err := s.RequestOutputMove(ctx, done, "無い番組", now); !errors.Is(err, ErrNoShow) || errors.Is(err, ErrNotFound) {
		t.Errorf("無い番組: %v", err)
	}
	if j, _ := s.GetJob(ctx, done); j.ShowName != "古い名前" || !j.MoveRequestedAt.IsZero() {
		t.Errorf("無い番組で変えた: %+v", j)
	}
	if err := s.RequestOutputMove(ctx, done, "今の番組", now); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestOutputMove(ctx, expired, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob(ctx, done)
	if j.ShowName != "今の番組" || !j.MoveRequestedAt.Equal(now) || j.State != domain.StateDone ||
		j.OutputPath != "/library/局/古い名前/古い名前 2026-10-05.m4a" {
		t.Errorf("受け付けたジョブ = %+v", j)
	}
	if j, _ := s.GetJob(ctx, expired); j.ShowName != "古い名前" || j.MoveRequestedAt.IsZero() || j.State != domain.StateExpired {
		t.Errorf("番組を指定しないジョブ = %+v", j)
	}
	due, err := s.DueOutputMoves(ctx)
	if err != nil || len(due) != 2 || due[0].ID != done || due[1].ID != expired {
		t.Errorf("DueOutputMoves = %+v, %v（頼んだ順）", due, err)
	}
}

// FinishOutputMove: 移したら保存先を付け替え、履歴に 1 行残し、頼みの印を消す。拾った後に頼み直されていれば印を残す。移すものが無かった
// ときは履歴に残さない。移さなかったときは保存先を変えない。
func TestFinishOutputMove(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	from := "/library/局/旧/旧 2026-10-05.m4a"
	to := "/library/局/新/新 2026-10-05.m4a"
	id, err := s.InsertJob(ctx, domain.Job{ShowName: "新", Day: day, State: domain.StateDone, Airings: []domain.Airing{flat1},
		NextAttemptAt: jst(2026, 10, 5, 11, 0), Deadline: jst(2026, 10, 13, 5, 0), OutputPath: from, Attempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	t1 := jst(2026, 10, 6, 12, 0)
	if err := s.RequestOutputMove(ctx, id, "", t1); err != nil {
		t.Fatal(err)
	}
	// 移せなかった: 保存先はそのまま、履歴に 1 行、印は消える。
	if err := s.FinishOutputMove(ctx, domain.OutputMove{JobID: id, At: t1.Add(time.Second), From: from, To: to,
		Result: domain.OutputMoveTargetExists}, true, t1); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob(ctx, id)
	if j.OutputPath != from || !j.MoveRequestedAt.IsZero() {
		t.Errorf("移さなかった後のジョブ = %+v", j)
	}
	// 拾った後に頼み直された: 移したが、印は残る。
	t2, t3 := t1.Add(time.Minute), t1.Add(2*time.Minute)
	if err := s.RequestOutputMove(ctx, id, "", t3); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishOutputMove(ctx, domain.OutputMove{JobID: id, At: t2, From: from, To: to, Result: domain.OutputMoved}, true, t2); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJob(ctx, id)
	if j.OutputPath != to || !j.MoveRequestedAt.Equal(t3) || j.State != domain.StateDone || j.Attempts != 2 {
		t.Errorf("移した後のジョブ = %+v（保存先を付け替え、頼み直しの印は残し、状態・試行回数は変えない）", j)
	}
	// 移すものが無かった: 履歴に残さず、印を消す。
	if err := s.FinishOutputMove(ctx, domain.OutputMove{JobID: id, At: t3, From: to, To: to}, false, t3); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.GetJob(ctx, id); !j.MoveRequestedAt.IsZero() || j.OutputPath != to {
		t.Errorf("移すものが無かった後のジョブ = %+v", j)
	}
	ms, err := s.OutputMoves(ctx, id)
	if err != nil || len(ms) != 2 {
		t.Fatalf("OutputMoves = %+v, %v", ms, err)
	}
	if ms[0].Result != domain.OutputMoveTargetExists || ms[1].Result != domain.OutputMoved || ms[1].From != from || ms[1].To != to ||
		!ms[1].At.Equal(t2) {
		t.Errorf("OutputMoves = %+v", ms)
	}
	if err := s.FinishOutputMove(ctx, domain.OutputMove{JobID: 9999, At: t3, Result: domain.OutputMoveNoFile}, true, t3); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いジョブ: %v", err)
	}
	// ジョブを消すと履歴も消える。
	if _, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if ms, err := s.OutputMoves(ctx, id); err != nil || len(ms) != 0 {
		t.Errorf("消したジョブの履歴 = %+v, %v", ms, err)
	}
}
