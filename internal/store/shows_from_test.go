package store

import (
	"context"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 毎週の予約の開始日は、保存して読み直しても残る。空（制限なし）はそのまま空に戻る。
func TestShowWeeklyFrom(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	show := domain.NewShow("ふらっと",
		domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}).WithFrom(domain.NewBroadcastDay(2026, 10, 12)),
		domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 22}),
	)
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetShow(ctx, "ふらっと")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Weekly) != 2 || got.Weekly[0].From != domain.NewBroadcastDay(2026, 10, 12) || !got.Weekly[1].From.IsZero() {
		t.Errorf("開始日 = %+v", got.Weekly)
	}
	// 開始日を空にして置き換えると、制限なしに戻る。
	show.Weekly[0].From = domain.BroadcastDay{}
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetShow(ctx, "ふらっと"); !got.Weekly[0].From.IsZero() {
		t.Errorf("開始日を空にできていない: %+v", got.Weekly)
	}
}

// 毎週の予約の終了日も、保存して読み直しても残る。空（制限なし）はそのまま空に戻る。
func TestShowWeeklyUntil(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	show := domain.NewShow("ANN",
		domain.NewWeekly("LFR", time.Thursday, domain.Clock{Hour: 1}).
			WithFrom(domain.NewBroadcastDay(2026, 10, 1)).WithUntil(domain.NewBroadcastDay(2026, 12, 24)),
		domain.NewWeekly("LFR", time.Friday, domain.Clock{Hour: 3}).WithUntil(domain.NewBroadcastDay(2026, 12, 25)),
		domain.NewWeekly("TBS", time.Friday, domain.Clock{Hour: 22}),
	)
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetShow(ctx, "ANN")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Weekly) != 3 || got.Weekly[0] != show.Weekly[0] || got.Weekly[1] != show.Weekly[1] || got.Weekly[2] != show.Weekly[2] {
		t.Errorf("終了日 = %+v", got.Weekly)
	}
	// 終了日を空にして置き換えると、制限なしに戻る。
	show.Weekly[0].Until = domain.BroadcastDay{}
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetShow(ctx, "ANN"); !got.Weekly[0].Until.IsZero() || got.Weekly[0].From.IsZero() {
		t.Errorf("終了日を空にできていない: %+v", got.Weekly)
	}
}
