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
// 理由を分けて拒み、状態を変えない（ADR 0036）。
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
	if err := s.Rerecord(ctx, id, domain.MembershipFree, nil, now); err != nil {
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
		{"録音期限を過ぎた", insert(t, domain.StateDone, flat2), jst(2026, 10, 13, 5, 0), domain.ErrRerecordPastDeadline, domain.StateDone},
		{"無いジョブ", 999, now, ErrNotFound, ""},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Rerecord(ctx, tt.id, domain.MembershipFree, nil, tt.now); !errors.Is(err, tt.want) {
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

// 録り直しを受け付けるかと、書き換える録音期限は、今の会員種別で決める（ADR 0036）。DB の録音期限が非会員のころの
// 7 日のままでも、タイムフリー30 なら 30 日の録音期限になり、非会員なら断る。
func TestRerecordUsesCurrentMembership(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 20, 12, 0)  // flat2（10/5）から 15 日後
	stale := jst(2026, 10, 13, 5, 0) // 非会員のころの録音期限（7 日）
	insert := func(t *testing.T, a domain.Airing) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{ShowName: "x", Day: day, State: domain.StateDone, Airings: []domain.Airing{a},
			Attempts: 1, NextAttemptAt: jst(2026, 10, 5, 11, 5), Deadline: stale, OutputPath: "/lib/x.m4a"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	excluded := flat2
	excluded.TimeFree.PlusInNG = 1

	if err := s.Rerecord(ctx, insert(t, flat2), domain.MembershipFree, nil, now); !errors.Is(err, domain.ErrRerecordPastDeadline) {
		t.Errorf("非会員 = %v, want ErrRerecordPastDeadline", err)
	}
	if err := s.Rerecord(ctx, insert(t, excluded), domain.MembershipTimeFree30, nil, now); !errors.Is(err, domain.ErrRerecordPastDeadline) {
		t.Errorf("タイムフリー30・対象外の放送回 = %v, want ErrRerecordPastDeadline", err)
	}
	for _, m := range []domain.Membership{domain.MembershipTimeFree30, domain.MembershipDouble} {
		id := insert(t, flat2)
		if err := s.Rerecord(ctx, id, m, nil, now); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := jst(2026, 11, 5, 5, 0) // 放送日 10/5 の終わりに 30 日
		if j.State != domain.StateRetrying || !j.Deadline.Equal(want) {
			t.Errorf("%s: state=%s deadline=%v, want retrying %v", m, j.State, j.Deadline, want)
		}
	}
}

// 録り直しを受け付けるかと、書き換える録音期限は、今の自分のエリアの局一覧 own でも決める（ADR 0036）。録ったときは自分のエリアの
// 局（印は立っていない）で、今はエリアの外の局になった放送回は、ダブルプランで tsplus_out_ng が 0 でなければ、7 日を過ぎると断る。
// 受け付けたときに書く録音期限は、判定と同じ own で求める。
func TestRerecordUsesOwnStations(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a := flat2
	a.TimeFree.PlusOutNG = 1
	insert := func(t *testing.T) int64 {
		t.Helper()
		id, err := s.InsertJob(ctx, domain.Job{ShowName: "x", Day: day, State: domain.StateDone, Airings: []domain.Airing{a},
			Attempts: 1, NextAttemptAt: jst(2026, 10, 5, 11, 5), Deadline: jst(2026, 10, 13, 5, 0), OutputPath: "/lib/x.m4a"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	now := jst(2026, 10, 20, 12, 0) // flat2（10/5）から 15 日後
	if err := s.Rerecord(ctx, insert(t), domain.MembershipDouble, map[string]bool{"other": true}, now); !errors.Is(err, domain.ErrRerecordPastDeadline) {
		t.Errorf("今はエリアの外の局 = %v, want ErrRerecordPastDeadline", err)
	}
	id := insert(t)
	if err := s.Rerecord(ctx, id, domain.MembershipDouble, map[string]bool{a.StationID: true}, now); err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := jst(2026, 11, 5, 5, 0); j.State != domain.StateRetrying || !j.Deadline.Equal(want) {
		t.Errorf("今もエリアの中: state=%s deadline=%v, want retrying %v", j.State, j.Deadline, want)
	}
}
