package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 録り直しは、done のタイムフリーから録るジョブで録音期限の内側のものを retrying・試行回数 0・次の試行を今にする。保存先・
// 欠落の印・期限間近を通知した時刻・試行の履歴は変えない。done でない・ライブで録るジョブ・録音期限を過ぎたジョブは、
// 理由を分けて拒み、状態を変えない（ADR 0025）。
func TestRerecord(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 6, 12, 0)
	deadline := jst(2026, 10, 12, 5, 0)
	insert := func(t *testing.T, state domain.State, airings ...domain.Airing) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{
			ShowName: "x", Day: day, State: state, Airings: airings, Missing: airings[len(airings)-1:],
			Attempts: 3, NextAttemptAt: jst(2026, 10, 5, 11, 5), Deadline: deadline, OutputPath: "/lib/x.m4a",
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	id := insert(t, domain.StateDone, flat2, news)
	if _, err := s.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	notified := formatTime(now.Add(-2 * time.Hour))
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET deadline_notified_at = ? WHERE id = ?`, notified, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Rerecord(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != domain.StateRetrying || j.Attempts != 0 || !j.NextAttemptAt.Equal(now) {
		t.Errorf("録り直しの後: state=%s attempts=%d next=%v", j.State, j.Attempts, j.NextAttemptAt)
	}
	if j.OutputPath != "/lib/x.m4a" || len(j.Missing) != 1 {
		t.Errorf("保存先と欠落の印が変わった: output=%q missing=%d", j.OutputPath, len(j.Missing))
	}
	var got string
	if err := s.db.QueryRowContext(ctx, `SELECT deadline_notified_at FROM jobs WHERE id = ?`, id).Scan(&got); err != nil || got != notified {
		t.Errorf("deadline_notified_at = %q, %v; want %q", got, err, notified)
	}
	if as, _ := s.Attempts(ctx, id); len(as) != 1 {
		t.Errorf("試行の履歴 = %d 件, want 1", len(as))
	}
	// 拾われて録音のループに乗る。
	if c, ok, err := s.ClaimDue(ctx, now); err != nil || !ok || c.ID != id {
		t.Errorf("ClaimDue = %+v, %v, %v", c, ok, err)
	}

	refusals := []struct {
		name  string
		id    int64
		now   time.Time
		want  error
		state domain.State
	}{
		{"expired", insert(t, domain.StateExpired, flat2), now, domain.ErrInvalidTransition, domain.StateExpired},
		{"waiting", insert(t, domain.StateWaiting, flat2), now, domain.ErrInvalidTransition, domain.StateWaiting},
		{"ライブで録るジョブ", insert(t, domain.StateDone, liveAiring(flat2)), now, domain.ErrRerecordLive, domain.StateDone},
		{"録音期限を過ぎた", insert(t, domain.StateDone, flat2), deadline, domain.ErrRerecordPastDeadline, domain.StateDone},
		{"無いジョブ", 999, now, ErrNotFound, ""},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Rerecord(ctx, tt.id, tt.now); !errors.Is(err, tt.want) {
				t.Fatalf("Rerecord = %v, want %v", err, tt.want)
			}
			if tt.state == "" {
				return
			}
			if j, _ := s.GetJob(ctx, tt.id); j.State != tt.state || j.Attempts != 3 {
				t.Errorf("拒んだ後: state=%s attempts=%d", j.State, j.Attempts)
			}
		})
	}
}

// 手動再試行は、録り直しを足した後も done のジョブを拒む（ADR 0025）。
func TestManualRetryStillRefusesDone(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 6, 12, 0)
	id, err := s.InsertJob(ctx, domain.Job{
		ShowName: "x", Day: day, State: domain.StateDone, Airings: []domain.Airing{flat2},
		NextAttemptAt: jst(2026, 10, 5, 11, 5), Deadline: jst(2026, 10, 12, 5, 0), OutputPath: "/lib/x.m4a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ManualRetry(ctx, id, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("ManualRetry(done) = %v", err)
	}
	if j, _ := s.GetJob(ctx, id); j.State != domain.StateDone {
		t.Errorf("拒んだ後の状態 = %s", j.State)
	}
}

// liveAiring は a をタイムフリーの対象外（ライブで録る放送回）にしたもの。
func liveAiring(a domain.Airing) domain.Airing {
	a.TimeFree.InNG = 2
	return a
}
