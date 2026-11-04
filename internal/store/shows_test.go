package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

func TestShowEnabled(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	show := domain.NewShow("ふらっと", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetShow(ctx, "ふらっと"); got.Disabled {
		t.Errorf("既定は有効: %+v", got)
	}
	if err := s.SetShowEnabled(ctx, "ふらっと", false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetShow(ctx, "ふらっと")
	if !got.Disabled || len(got.Weekly) != 1 {
		t.Errorf("無効にした後（毎週の予約は残る）: %+v", got)
	}
	// 丸ごと置き換えるときは、show.Disabled のとおりにする（PUT /api/shows/{name} の置き換えと同じ）。
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetShow(ctx, "ふらっと"); got.Disabled {
		t.Errorf("PutShow で有効に戻る: %+v", got)
	}
	show.Disabled = true
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetShow(ctx, "ふらっと"); !got.Disabled {
		t.Errorf("PutShow で無効にする: %+v", got)
	}
	if err := s.SetShowEnabled(ctx, "無い", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("無い番組: %v", err)
	}
}

func TestCreateShow(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	show := domain.NewShow("ふらっと", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	if err := s.CreateShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	other := domain.NewShow("ふらっと", domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 22}))
	if err := s.CreateShow(ctx, other); !errors.Is(err, ErrExists) {
		t.Errorf("同じ名前: %v", err)
	}
	if got, _ := s.GetShow(ctx, "ふらっと"); got.Weekly[0].StationID != "LFR" {
		t.Errorf("置き換えていない: %+v", got)
	}
	if err := s.CreateShow(ctx, domain.Show{}); err == nil {
		t.Error("名前が空ならエラー")
	}
}

func TestReplaceShowRename(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	old := domain.NewShow("古い", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	keep := domain.NewShow("別の番組", domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 22}))
	if err := s.PutShows(ctx, []domain.Show{old, keep}); err != nil {
		t.Fatal(err)
	}
	// 古い名前のジョブを状態ごとに 1 つずつと、ほかの番組のジョブを 1 つ入れる。retrying・expired・done は保存先を持つ
	// （録り直しを待つ・録り直しが録れなかった・録音済み）。running は最初の録音の途中で、保存先がまだ無い（それでも移しの頼みを付ける）。
	ids := map[domain.State]int64{}
	outputs := map[domain.State]string{}
	for i, st := range []domain.State{domain.StateWaiting, domain.StateRunning, domain.StateRetrying, domain.StateExpired,
		domain.StateDone} {
		if st != domain.StateWaiting && st != domain.StateRunning {
			outputs[st] = fmt.Sprintf("/library/局/古い/古い 2026-10-05-%d.m4a", i+1)
		}
		job := domain.Job{ShowName: "古い", Day: day, Part: i + 1, State: st, Airings: []domain.Airing{flat1},
			NextAttemptAt: jst(2026, 10, 5, 11, 0), Deadline: jst(2026, 10, 13, 5, 0), OutputPath: outputs[st]}
		id, err := s.InsertJob(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		ids[st] = id
	}
	other, err := s.InsertJob(ctx, domain.Job{ShowName: "別の番組", Day: day, State: domain.StateWaiting,
		Airings: []domain.Airing{flat1}, NextAttemptAt: jst(2026, 10, 5, 11, 0), Deadline: jst(2026, 10, 13, 5, 0)})
	if err != nil {
		t.Fatal(err)
	}

	renamed := domain.NewShow("新しい", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}), domain.NewWeekly("LFR", time.Tuesday, domain.Clock{Hour: 8, Minute: 30}))
	renamed.Split = domain.SplitSeparate
	if err := s.ReplaceShow(ctx, "古い", renamed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetShow(ctx, "古い"); !errors.Is(err, ErrNotFound) {
		t.Errorf("古い名前の番組が残っている: %v", err)
	}
	got, err := s.GetShow(ctx, "新しい")
	if err != nil || got.Split != domain.SplitSeparate || len(got.Weekly) != 2 {
		t.Errorf("新しい名前の番組: %+v, %v", got, err)
	}
	// 古い名前の全部のジョブ（done を含む）が新しい名前に付け替わる（ADR 0039）。保存先のあるジョブ（状態によらない）と録音中のジョブ（保存先が
	// まだ無くても）にだけ保存先の移しの頼みの印が付く。保存先は変えない（移すのは録音のループ）。ほかの番組のジョブは触らない。
	for st, id := range ids {
		j, err := s.GetJob(ctx, id)
		if err != nil || j.ShowName != "新しい" {
			t.Errorf("%s のジョブ: %+v, %v（want 新しい）", st, j, err)
			continue
		}
		if want := j.OutputPath != "" || st == domain.StateRunning; j.MoveRequestedAt.IsZero() == want {
			t.Errorf("%s のジョブの移しの頼み = %v（保存先 %q）", st, j.MoveRequestedAt, j.OutputPath)
		}
		if outputs[st] != j.OutputPath {
			t.Errorf("%s のジョブの保存先 = %q、want %q", st, j.OutputPath, outputs[st])
		}
	}
	if j, err := s.GetJob(ctx, other); err != nil || j.ShowName != "別の番組" || !j.MoveRequestedAt.IsZero() {
		t.Errorf("ほかの番組のジョブ: %+v, %v", j, err)
	}
	if due, err := s.DueOutputMoves(ctx); err != nil || len(due) != 3 {
		// 保存先のある retrying・expired・done。running は録音が終わってから拾う。
		t.Errorf("DueOutputMoves = %d 件, %v; want 3", len(due), err)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM weekly`).Scan(&n)
	if n != 3 {
		t.Errorf("毎週の予約の数 = %d（古い名前の番組の毎週の予約が残っている？）", n)
	}

	// 新しい名前の番組がもうあれば ErrExists で、何も変えない。
	clash := domain.NewShow("別の番組", domain.NewWeekly("LFR", time.Sunday, domain.Clock{Hour: 1}))
	if err := s.ReplaceShow(ctx, "新しい", clash); !errors.Is(err, ErrExists) {
		t.Errorf("名前が重なる: %v", err)
	}
	if _, err := s.GetShow(ctx, "新しい"); err != nil {
		t.Errorf("重なったときに消している: %v", err)
	}
	if got, _ := s.GetShow(ctx, "別の番組"); got.Weekly[0].StationID != "TBS" {
		t.Errorf("重なった番組を置き換えている: %+v", got)
	}
	if j, err := s.GetJob(ctx, ids[domain.StateWaiting]); err != nil || j.ShowName != "新しい" {
		t.Errorf("重なったときにジョブを付け替えている: %+v, %v", j, err)
	}
	// 名前を変えない置き換え。
	same := domain.NewShow("新しい", domain.NewWeekly("LFR", time.Saturday, domain.Clock{Hour: 6}))
	same.Disabled = true
	if err := s.ReplaceShow(ctx, "新しい", same); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetShow(ctx, "新しい"); !got.Disabled || len(got.Weekly) != 1 || got.Weekly[0].Weekday != time.Saturday {
		t.Errorf("名前を変えない置き換え: %+v", got)
	}
	if err := s.ReplaceShow(ctx, "無い", domain.NewShow("無い2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("無い番組: %v", err)
	}
	if _, err := s.GetShow(ctx, "無い2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("無い番組の置き換えで作っている: %v", err)
	}
}

func TestNextAirings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	insert := func(show string, state domain.State, as ...domain.Airing) {
		t.Helper()
		if _, err := s.InsertJob(ctx, domain.Job{ShowName: show, Day: day, State: state, Airings: as,
			NextAttemptAt: jst(2026, 10, 5, 12, 0), Deadline: jst(2026, 10, 13, 5, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	insert("ふらっと", domain.StateWaiting, flat2)
	insert("ふらっと", domain.StateWaiting, flat1)
	insert("ニュース", domain.StateRetrying, news)
	got, err := s.NextAirings(ctx, jst(2026, 10, 5, 6, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["ふらっと"].Equal(flat1.Start) {
		t.Errorf("NextAirings = %v（waiting のジョブのいちばん早い放送回の開始。retrying は入れない）", got)
	}
	if loc := got["ふらっと"].Location(); loc != domain.JST {
		t.Errorf("時刻の場所 = %v", loc)
	}
	// 開始時刻が今より後の放送回だけから取る（今ちょうど始まった放送回も入れない）。
	got, err = s.NextAirings(ctx, flat1.Start)
	if err != nil || len(got) != 1 || !got["ふらっと"].Equal(flat2.Start) {
		t.Errorf("flat1 の開始の時点 = %v, %v（flat2 になる）", got, err)
	}
	got, err = s.NextAirings(ctx, flat2.Start.Add(time.Minute))
	if err != nil || len(got) != 0 {
		t.Errorf("どの放送回も始まった後 = %v, %v", got, err)
	}
}
