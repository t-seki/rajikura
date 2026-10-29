package server

import (
	"context"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 毎週の予約の開始日（from）は、API で書け、読み返せる。省けば制限なし。正しくなければ 400。
func TestShowsWeeklyFrom(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	body := `{"name":"開始日つき","weekly":[
		{"station":"TBS","weekday":3,"start":"13:00","from":"2026-10-14"},
		{"station":"TBS","weekday":4,"start":"13:00"}]}`
	var created showJSON
	e.call("POST", "/api/shows", body, 201, &created)
	if created.Weekly[0].From != "2026-10-14" || created.Weekly[1].From != "" {
		t.Errorf("作成の応答 = %+v", created.Weekly)
	}
	got, err := e.store.GetShow(ctx, "開始日つき")
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekly[0].From != domain.NewBroadcastDay(2026, 10, 14) || !got.Weekly[1].From.IsZero() {
		t.Errorf("保存した開始日 = %+v", got.Weekly)
	}
	var read showJSON
	e.call("GET", showPath("開始日つき"), "", 200, &read)
	if read.Weekly[0].From != "2026-10-14" || read.Weekly[1].From != "" {
		t.Errorf("読み返し = %+v", read.Weekly)
	}
	// 開始日を空にして置き換えられる。
	e.call("PUT", showPath("開始日つき"), `{"name":"開始日つき","weekly":[{"station":"TBS","weekday":3,"start":"13:00","from":""}]}`, 200, nil)
	got, _ = e.store.GetShow(ctx, "開始日つき")
	if len(got.Weekly) != 1 || !got.Weekly[0].From.IsZero() || got.Weekly[0].Weekday != time.Wednesday {
		t.Errorf("開始日を空にした後 = %+v", got.Weekly)
	}
	// 日付として読めなければ 400。
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":3,"start":"13:00","from":"10/14"}]}`, 400, nil)
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":3,"start":"13:00","from":"2026-02-30"}]}`, 400, nil)
}

// 毎週の予約の終了日（until）は、API で書け、読み返せる。省けば制限なし。正しくないか開始日より前なら 400。
// 開始日と同じ日は受け付ける。
func TestShowsWeeklyUntil(t *testing.T) {
	e := newShowsUI(t)
	ctx := context.Background()
	body := `{"name":"終了日つき","weekly":[
		{"station":"LFR","weekday":4,"start":"01:00","until":"2026-12-24"},
		{"station":"LFR","weekday":5,"start":"01:00","from":"2026-12-25","until":"2026-12-25"},
		{"station":"TBS","weekday":4,"start":"13:00"}]}`
	var created showJSON
	e.call("POST", "/api/shows", body, 201, &created)
	if created.Weekly[0].Until != "2026-12-24" || created.Weekly[1].From != "2026-12-25" || created.Weekly[1].Until != "2026-12-25" ||
		created.Weekly[2].Until != "" {
		t.Errorf("作成の応答 = %+v", created.Weekly)
	}
	got, err := e.store.GetShow(ctx, "終了日つき")
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekly[0].Until != domain.NewBroadcastDay(2026, 12, 24) || got.Weekly[1].Until != domain.NewBroadcastDay(2026, 12, 25) ||
		!got.Weekly[2].Until.IsZero() {
		t.Errorf("保存した終了日 = %+v", got.Weekly)
	}
	var read showJSON
	e.call("GET", showPath("終了日つき"), "", 200, &read)
	if read.Weekly[0].Until != "2026-12-24" || read.Weekly[2].Until != "" {
		t.Errorf("読み返し = %+v", read.Weekly)
	}
	// 終了日を空にして置き換えられる。
	e.call("PUT", showPath("終了日つき"), `{"name":"終了日つき","weekly":[{"station":"LFR","weekday":4,"start":"01:00","until":""}]}`, 200, nil)
	got, _ = e.store.GetShow(ctx, "終了日つき")
	if len(got.Weekly) != 1 || !got.Weekly[0].Until.IsZero() {
		t.Errorf("終了日を空にした後 = %+v", got.Weekly)
	}
	// 日付として読めなければ 400。開始日より前なら 400。
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":3,"start":"13:00","until":"12/24"}]}`, 400, nil)
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":3,"start":"13:00","until":"2026-02-30"}]}`, 400, nil)
	e.call("POST", "/api/shows", `{"name":"x","weekly":[{"station":"TBS","weekday":3,"start":"13:00","from":"2026-12-25","until":"2026-12-24"}]}`, 400, nil)
	e.call("PUT", showPath("終了日つき"), `{"name":"終了日つき","weekly":[{"station":"LFR","weekday":4,"start":"01:00","from":"2026-12-25","until":"2026-12-24"}]}`, 400, nil)
}
