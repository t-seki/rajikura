package store

import (
	"context"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

func TestUnmatchedDays(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	day := domain.NewBroadcastDay(2026, 10, 19)
	today := domain.NewBroadcastDay(2026, 10, 18)
	now := jst(2026, 10, 18, 12, 0)
	chari := domain.NewShow("チャリで30分", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 17, Minute: 40}))
	if err := s.PutShow(ctx, chari); err != nil {
		t.Fatal(err)
	}
	covering := domain.Airing{StationID: "LFR", Start: jst(2026, 10, 19, 15, 30), End: jst(2026, 10, 19, 18, 0), Title: "辛坊"}
	titled := domain.Airing{StationID: "LFR", Start: jst(2026, 10, 19, 19, 0), End: jst(2026, 10, 19, 19, 30), Title: "チャリで30分"}
	u := domain.UnmatchedDay{ShowName: chari.Name, StationID: "LFR", Day: day, Start: jst(2026, 10, 19, 17, 40),
		Covering: &covering, Titled: []domain.Airing{titled}}
	unmatched := domain.UnmatchedResult{Days: []domain.UnmatchedDay{u}, Targets: []string{u.Key()}}
	matched := domain.UnmatchedResult{Targets: []string{u.Key()}}

	list := func() []domain.UnmatchedDay {
		t.Helper()
		m, err := s.UnmatchedDays(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		return m[chari.Name]
	}
	msg := func(name string, days []domain.UnmatchedDay) Notification {
		return Notification{Kind: "unmatched_day", Message: name, CreatedAt: now, Deliver: true}
	}
	notifyAll := func() int {
		t.Helper()
		ns, err := s.AddUnmatchedDayNotifications(ctx, now, msg)
		if err != nil {
			t.Fatal(err)
		}
		return len(ns)
	}

	if err := s.ReplaceUnmatchedDays(ctx, "LFR", day, today, unmatched); err != nil {
		t.Fatal(err)
	}
	got := list()
	if len(got) != 1 || got[0].Covering == nil || got[0].Covering.Title != "辛坊" || !got[0].Covering.End.Equal(covering.End) ||
		len(got[0].Titled) != 1 || !got[0].Titled[0].Start.Equal(titled.Start) || !got[0].Start.Equal(u.Start) || got[0].Day != day {
		t.Fatalf("記録 = %+v", got)
	}
	// 枠の開始時刻を過ぎた記録は一覧に出さない。
	if m, _ := s.UnmatchedDays(ctx, u.Start); len(m) != 0 {
		t.Errorf("開始時刻を過ぎた記録 = %+v", m)
	}

	// 通知は 1 回だけ。
	if n := notifyAll(); n != 1 {
		t.Errorf("通知 = %d 件", n)
	}
	if n := notifyAll(); n != 0 {
		t.Errorf("2 回目の通知 = %d 件", n)
	}
	// 当たるようになったら一覧に出ず、また当たらなくなっても通知を重ねない。
	if err := s.ReplaceUnmatchedDays(ctx, "LFR", day, today, matched); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 0 {
		t.Errorf("当たるようになった後 = %+v", got)
	}
	if err := s.ReplaceUnmatchedDays(ctx, "LFR", day, today, unmatched); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 1 {
		t.Errorf("また当たらなくなった後 = %+v", got)
	}
	if n := notifyAll(); n != 0 {
		t.Errorf("また当たらなくなった後の通知 = %d 件", n)
	}

	// 名前を変えたら付け替え、通知した印も残る。
	renamed := chari
	renamed.Name = "チャリ"
	if err := s.ReplaceShow(ctx, chari.Name, renamed); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.UnmatchedDays(ctx, now); len(m["チャリ"]) != 1 || len(m[chari.Name]) != 0 {
		t.Errorf("名前を変えた後 = %+v", m)
	}
	if n := notifyAll(); n != 0 {
		t.Errorf("名前を変えた後の通知 = %d 件", n)
	}

	// 対象でなくなった枠（番組を変えた）は消す。通知した印も消える。
	if err := s.ReplaceUnmatchedDays(ctx, "LFR", day, today, domain.UnmatchedResult{}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM unmatched_days`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("対象でなくなった後の行 = %d, %v", rows, err)
	}

	// 通知していない記録は、当たるようになったら消す。
	if err := s.ReplaceUnmatchedDays(ctx, "LFR", day, today, unmatched); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceUnmatchedDays(ctx, "LFR", day, today, matched); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM unmatched_days`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("通知していない記録が当たるようになった後の行 = %d, %v", rows, err)
	}
}

func TestUnmatchedDaysStale(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	day := domain.NewBroadcastDay(2026, 10, 19)
	// put は局・放送日の記録を置き換えるので、同じ放送日の記録はまとめて書く。
	put := func(d domain.BroadcastDay, names []string, starts ...time.Time) {
		t.Helper()
		var res domain.UnmatchedResult
		for i, name := range names {
			res.Days = append(res.Days, domain.UnmatchedDay{ShowName: name, StationID: "LFR", Day: d, Start: starts[i]})
		}
		if err := s.ReplaceUnmatchedDays(ctx, "LFR", d, d.AddDays(-1), res); err != nil {
			t.Fatal(err)
		}
	}
	enabled := domain.NewShow("有効", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 17, Minute: 40}))
	disabled := domain.NewShow("無効", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 18, Minute: 0}))
	disabled.Disabled = true
	if err := s.PutShows(ctx, []domain.Show{enabled, disabled}); err != nil {
		t.Fatal(err)
	}
	put(day, []string{"有効", "無効", "消した"}, jst(2026, 10, 19, 17, 40), jst(2026, 10, 19, 18, 0), jst(2026, 10, 19, 18, 30))
	put(day.AddDays(-7), []string{"有効"}, jst(2026, 10, 12, 17, 40))

	// 無効な番組の記録は一覧に出る（呼び出し側で選ぶ）が、通知はしない。
	ns, err := s.AddUnmatchedDayNotifications(ctx, jst(2026, 10, 12, 6, 0), func(name string, days []domain.UnmatchedDay) Notification {
		return Notification{Kind: "unmatched_day", Message: name, CreatedAt: jst(2026, 10, 12, 6, 0)}
	})
	if err != nil || len(ns) != 1 || ns[0].Message != "有効" || ns[0].ID == 0 {
		t.Errorf("通知 = %+v, %v（有効な番組だけ、番組ごとに 1 件）", ns, err)
	}

	n, err := s.DeleteStaleUnmatchedDays(ctx, domain.NewBroadcastDay(2026, 10, 18))
	if err != nil || n != 3 {
		t.Errorf("消した数 = %d, %v（過ぎた放送日・無効な番組・消した番組）", n, err)
	}
	m, err := s.UnmatchedDays(ctx, time.Time{})
	if err != nil || len(m) != 1 || len(m["有効"]) != 1 || m["有効"][0].Day != day {
		t.Errorf("残った記録 = %+v, %v", m, err)
	}
}
