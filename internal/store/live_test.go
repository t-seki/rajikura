package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// insertLiveAndTimefree は、タイムフリーから録るジョブと、ライブで録るジョブ（放送回の 1 つが ts_in_ng が 0 でない）を 1 つずつ作る。
// 次の試行の時刻はどちらも now の前。
func insertLiveAndTimefree(t *testing.T, s *Store, now time.Time) (timefree, live int64) {
	t.Helper()
	ctx := context.Background()
	tf, err := s.InsertJob(ctx, domain.Job{ShowName: "tf", Day: day, Airings: []domain.Airing{flat2},
		NextAttemptAt: now.Add(-2 * time.Minute), Deadline: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ng := flat1
	ng.TimeFree.InNG = 2
	lv, err := s.InsertJob(ctx, domain.Job{ShowName: "live", Day: day, Airings: []domain.Airing{ng, flat2},
		NextAttemptAt: now.Add(-time.Minute), Deadline: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return tf, lv
}

// タイムフリーの録音のループ（ClaimDue）はライブで録るジョブを取らず、ライブの録音役（ClaimDueLive）はライブで録るジョブだけを取る。
func TestClaimDueSeparatesLiveJobs(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 12, 0)
	tf, lv := insertLiveAndTimefree(t, s, now)

	j, ok, err := s.ClaimDue(ctx, now)
	if err != nil || !ok || j.ID != tf {
		t.Fatalf("ClaimDue = %d, %v, %v; want %d", j.ID, ok, err, tf)
	}
	if _, ok, err := s.ClaimDue(ctx, now); ok || err != nil {
		t.Fatalf("ClaimDue がライブで録るジョブを取った: %v, %v", ok, err)
	}
	j, ok, err = s.ClaimDueLive(ctx, now)
	if err != nil || !ok || j.ID != lv || j.State != domain.StateRunning || j.Attempts != 1 || !j.Live() {
		t.Fatalf("ClaimDueLive = %+v, %v, %v; want %d", j, ok, err, lv)
	}
	if _, ok, err := s.ClaimDueLive(ctx, now); ok || err != nil {
		t.Fatalf("ClaimDueLive が 2 度取った: %v, %v", ok, err)
	}
}

// 次の試行の時刻の前は取らない。人の手動再試行で retrying になったライブで録るジョブも取る。
func TestClaimDueLiveTimeAndRetrying(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 12, 0)
	_, lv := insertLiveAndTimefree(t, s, now)
	if _, ok, _ := s.ClaimDueLive(ctx, now.Add(-90*time.Second)); ok {
		t.Fatal("次の試行の時刻の前に取った")
	}
	if _, ok, _ := s.ClaimDueLive(ctx, now); !ok {
		t.Fatal("取れない")
	}
	if err := s.Expire(ctx, lv); err != nil {
		t.Fatal(err)
	}
	if err := s.ManualRetry(ctx, lv, now); err != nil {
		t.Fatal(err)
	}
	j, ok, err := s.ClaimDueLive(ctx, now)
	if err != nil || !ok || j.ID != lv || j.Attempts != 1 {
		t.Fatalf("手動再試行の後の ClaimDueLive = %+v, %v, %v", j, ok, err)
	}
}

// ClaimDueLive は録音期限の前のライブで録るジョブだけを、ClaimDueLiveSave は録音期限を過ぎたものだけを取る。
func TestClaimDueLiveSaveSplitsByDeadline(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 12, 0)
	_, lv := insertLiveAndTimefree(t, s, now)
	if _, ok, err := s.ClaimDueLiveSave(ctx, now); ok || err != nil {
		t.Fatalf("録音期限の前に ClaimDueLiveSave が取った: %v, %v", ok, err)
	}
	if _, ok, _ := s.ClaimDueLive(ctx, now); !ok {
		t.Fatal("ClaimDueLive が取れない")
	}
	past := now.Add(2 * time.Hour) // 録音期限（now + 1 時間）を過ぎた
	if err := s.Retry(ctx, lv, past); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimDueLive(ctx, past); ok || err != nil {
		t.Fatalf("録音期限を過ぎたものを ClaimDueLive が取った: %v, %v", ok, err)
	}
	j, ok, err := s.ClaimDueLiveSave(ctx, past)
	if err != nil || !ok || j.ID != lv || j.State != domain.StateRunning || j.Attempts != 2 {
		t.Fatalf("ClaimDueLiveSave = %+v, %v, %v", j, ok, err)
	}
}

// 録音期限を過ぎていても、ライブで録るジョブで、最後の試行が保存か検証の段階で失敗して 30 日以内の retrying か expired の
// ジョブは手動再試行を受け付ける。30 日を過ぎたもの、最後の試行が入手の段階の失敗のもの、タイムフリーから録るジョブは
// 今のまま ErrPastDeadline で拒む。
func TestManualRetryLiveKeptPastDeadline(t *testing.T) {
	ctx := context.Background()
	now := jst(2026, 10, 5, 12, 0)
	ng := flat1
	ng.TimeFree.InNG = 2
	deadline := now.Add(time.Hour)
	// setup はジョブを作って running にし、ended に終わる stage の失敗の試行を記録して、state にする。
	setup := func(t *testing.T, s *Store, live bool, stage domain.Stage, ended time.Time, state domain.State) int64 {
		t.Helper()
		airings := []domain.Airing{flat2}
		if live {
			airings = []domain.Airing{ng, flat2}
		}
		id, err := s.InsertJob(ctx, domain.Job{ShowName: "r", Day: day, Airings: airings, NextAttemptAt: now.Add(-time.Minute), Deadline: deadline})
		if err != nil {
			t.Fatal(err)
		}
		claim := s.ClaimDue
		if live {
			claim = s.ClaimDueLive
		}
		if _, ok, err := claim(ctx, now); err != nil || !ok {
			t.Fatalf("claim = %v, %v", ok, err)
		}
		if _, err := s.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: now, EndedAt: ended, FailedStage: stage, Error: "boom"}); err != nil {
			t.Fatal(err)
		}
		if state == domain.StateExpired {
			err = s.Expire(ctx, id)
		} else {
			err = s.Retry(ctx, id, ended.Add(time.Hour))
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ended := now.Add(30 * time.Minute)
	keep := ended.Add(domain.LiveKeepFor)
	for _, tc := range []struct {
		name  string
		live  bool
		stage domain.Stage
		state domain.State
		at    time.Time
		ok    bool
	}{
		{"ライブ・保存の失敗・expired・30 日以内", true, domain.StageSave, domain.StateExpired, keep.Add(-time.Minute), true},
		{"ライブ・検証の失敗・retrying・録音期限の直後", true, domain.StageVerify, domain.StateRetrying, deadline, true},
		{"ライブ・保存の失敗・expired・30 日ちょうど", true, domain.StageSave, domain.StateExpired, keep, false},
		{"ライブ・入手の失敗・expired", true, domain.StageFetch, domain.StateExpired, deadline.Add(time.Minute), false},
		{"タイムフリー・保存の失敗・expired", false, domain.StageSave, domain.StateExpired, deadline.Add(time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTemp(t)
			id := setup(t, s, tc.live, tc.stage, ended, tc.state)
			err := s.ManualRetry(ctx, id, tc.at)
			if !tc.ok {
				if !errors.Is(err, ErrPastDeadline) {
					t.Fatalf("ManualRetry = %v, want ErrPastDeadline", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ManualRetry = %v", err)
			}
			j, _ := s.GetJob(ctx, id)
			if j.State != domain.StateRetrying || !j.NextAttemptAt.Equal(tc.at) || j.Attempts != 0 {
				t.Errorf("job = %+v", j)
			}
		})
	}
}

// 起動時の回復は、running のライブで録るジョブを retrying に戻さず、RunningLive で読める。
func TestRecoverKeepsLiveRunning(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := jst(2026, 10, 5, 12, 0)
	tf, lv := insertLiveAndTimefree(t, s, now)
	s.ClaimDue(ctx, now)
	s.ClaimDueLive(ctx, now)

	n, err := s.Recover(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v; want 1（タイムフリーの分だけ）", n, err)
	}
	if j, _ := s.GetJob(ctx, tf); j.State != domain.StateRetrying {
		t.Errorf("タイムフリーのジョブ = %s, want retrying", j.State)
	}
	if j, _ := s.GetJob(ctx, lv); j.State != domain.StateRunning {
		t.Errorf("ライブで録るジョブ = %s, want running のまま", j.State)
	}
	running, err := s.RunningLive(ctx)
	if err != nil || len(running) != 1 || running[0].ID != lv || len(running[0].Airings) != 2 {
		t.Fatalf("RunningLive = %+v, %v", running, err)
	}
}
