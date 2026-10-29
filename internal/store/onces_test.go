package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

func jstTime(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, domain.JST)
}

func TestOnces(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	// 毎週の予約の無い番組も保存できる。
	if err := s.CreateShow(ctx, domain.NewShow("単発")); err != nil {
		t.Fatal(err)
	}
	late := domain.Once{StationID: "LFR", Start: jstTime(2026, 10, 6, 1, 0)}
	early := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 5, 8, 30)}
	id1, err := s.CreateOnce(ctx, "単発", late)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.CreateOnce(ctx, "単発", early)
	if err != nil || id2 == id1 {
		t.Fatalf("CreateOnce = %d, %v", id2, err)
	}
	// 同じ番組・同じ局・同じ開始時刻は ErrExists。
	if _, err := s.CreateOnce(ctx, "単発", late); !errors.Is(err, ErrExists) {
		t.Errorf("重なり: %v", err)
	}
	// 番組が無ければ ErrNotFound。
	if _, err := s.CreateOnce(ctx, "無い", late); !errors.Is(err, ErrNotFound) {
		t.Errorf("番組が無い: %v", err)
	}
	if _, err := s.CreateOnce(ctx, "単発", domain.Once{StationID: "LFR"}); err == nil {
		t.Error("開始時刻の無い 1 回の予約を通した")
	}

	got, err := s.ListOnces(ctx, "単発")
	if err != nil || len(got) != 2 || got[0].ID != id2 || got[1].ID != id1 ||
		got[0].Key() != early.Key() || !got[1].Start.Equal(late.Start) || got[1].Start.Location() != domain.JST {
		t.Fatalf("ListOnces = %+v, %v", got, err)
	}
	show, err := s.GetShow(ctx, "単発")
	if err != nil || len(show.Weekly) != 0 || len(show.Onces) != 2 || show.Onces[0].Key() != early.Key() {
		t.Errorf("GetShow = %+v, %v", show, err)
	}
	if _, err := s.ListOnces(ctx, "無い"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListOnces 番組が無い: %v", err)
	}

	if err := s.DeleteOnce(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOnce(ctx, id2); !errors.Is(err, ErrNotFound) {
		t.Errorf("2 回目の DeleteOnce: %v", err)
	}
	if got, _ := s.ListOnces(ctx, "単発"); len(got) != 1 || got[0].ID != id1 {
		t.Errorf("消した後: %+v", got)
	}
}

// CreateOnces は全部足すか 1 つも足さないか。途中の 1 件が ErrExists でも、その前の分が残らない。
func TestCreateOncesAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.CreateShow(ctx, domain.NewShow("単発")); err != nil {
		t.Fatal(err)
	}
	a := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 5, 8, 0)}
	b := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 5, 9, 0)}
	c := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 5, 10, 0)}
	if _, err := s.CreateOnce(ctx, "単発", b); err != nil {
		t.Fatal(err)
	}
	// 2 件目（b）がすでにある: a も入らない。
	if _, err := s.CreateOnces(ctx, "単発", []domain.Once{a, b, c}); !errors.Is(err, ErrExists) {
		t.Fatalf("重なり: %v", err)
	}
	// 入力の中の重なりも同じ。
	if _, err := s.CreateOnces(ctx, "単発", []domain.Once{a, a}); !errors.Is(err, ErrExists) {
		t.Fatalf("入力の中の重なり: %v", err)
	}
	// 値が正しくない 1 件があれば、1 つも足さない。
	if _, err := s.CreateOnces(ctx, "単発", []domain.Once{a, {}}); err == nil {
		t.Fatal("正しくない値を受け付けた")
	}
	if _, err := s.CreateOnces(ctx, "無い", []domain.Once{a}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("番組が無い: %v", err)
	}
	got, err := s.ListOnces(ctx, "単発")
	if err != nil || len(got) != 1 || got[0].Key() != b.Key() {
		t.Fatalf("途中の失敗で 1 回の予約が残った: %+v, %v", got, err)
	}
	ids, err := s.CreateOnces(ctx, "単発", []domain.Once{a, c})
	if err != nil || len(ids) != 2 {
		t.Fatalf("CreateOnces = %v, %v", ids, err)
	}
	if got, _ := s.ListOnces(ctx, "単発"); len(got) != 3 {
		t.Fatalf("got = %+v", got)
	}
}

func TestOncesFollowShow(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	o := domain.Once{StationID: "LFR", Start: jstTime(2026, 10, 5, 10, 0)}
	show := domain.NewShow("ふらっと", domain.NewWeekly("LFR", time.Monday, domain.Clock{Hour: 8, Minute: 30}))
	if err := s.PutShow(ctx, show); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOnce(ctx, "ふらっと", o); err != nil {
		t.Fatal(err)
	}
	keys := func(name string) []string {
		t.Helper()
		got, err := s.GetShow(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		var ks []string
		for _, o := range got.Onces {
			ks = append(ks, o.Key())
		}
		return ks
	}

	// 番組の置き換え（PutShow）は 1 回の予約を触らない。show.Onces が空でも残る。
	show.Weekly = nil
	if err := s.PutShows(ctx, []domain.Show{show}); err != nil {
		t.Fatal(err)
	}
	if ks := keys("ふらっと"); len(ks) != 1 || ks[0] != o.Key() {
		t.Errorf("PutShows の後: %v", ks)
	}

	// Web UI の置き換え（同じ名前）でも残る。
	if err := s.ReplaceShow(ctx, "ふらっと", show); err != nil {
		t.Fatal(err)
	}
	if ks := keys("ふらっと"); len(ks) != 1 {
		t.Errorf("置き換えの後: %v", ks)
	}
	// 名前の変更では新しい名前の番組に引き継ぐ。
	show.Name = "ふらっと2"
	if err := s.ReplaceShow(ctx, "ふらっと", show); err != nil {
		t.Fatal(err)
	}
	if ks := keys("ふらっと2"); len(ks) != 1 || ks[0] != o.Key() {
		t.Errorf("名前の変更の後: %v", ks)
	}
	// 名前の変更が ErrExists で巻き戻ったら、1 回の予約も元の番組に残る。
	if err := s.PutShow(ctx, domain.NewShow("ほか")); err != nil {
		t.Fatal(err)
	}
	show.Name = "ほか"
	if err := s.ReplaceShow(ctx, "ふらっと2", show); !errors.Is(err, ErrExists) {
		t.Fatalf("ReplaceShow = %v", err)
	}
	if ks := keys("ふらっと2"); len(ks) != 1 {
		t.Errorf("巻き戻した後: %v", ks)
	}

	// 番組を消すと（Web UI・API の削除）1 回の予約も消える。
	if err := s.DeleteShow(ctx, "ふらっと2"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM onces`).Scan(&n); err != nil || n != 0 {
		t.Errorf("番組を消した後の 1 回の予約 = %d, %v", n, err)
	}
}

func TestDeleteExpiredOnces(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.PutShow(ctx, domain.NewShow("単発")); err != nil {
		t.Fatal(err)
	}
	// 放送日 2026-10-05 の放送回の録音期限は、非会員で 2026-10-13 05:00。1 回の予約は 1 日遅らせて 10-14 05:00 に消す。
	old := domain.Once{StationID: "LFR", Start: jstTime(2026, 10, 6, 1, 0)}
	recent := domain.Once{StationID: "LFR", Start: jstTime(2026, 10, 6, 8, 30)}
	for _, o := range []domain.Once{old, recent} {
		if _, err := s.CreateOnce(ctx, "単発", o); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.DeleteExpiredOnces(ctx, jstTime(2026, 10, 14, 4, 59), domain.MembershipFree); err != nil || n != 0 {
		t.Errorf("期限の前 = %d, %v", n, err)
	}
	if n, err := s.DeleteExpiredOnces(ctx, jstTime(2026, 10, 14, 5, 0), domain.MembershipFree); err != nil || n != 1 {
		t.Errorf("期限ちょうど = %d, %v", n, err)
	}
	got, _ := s.ListOnces(ctx, "単発")
	if len(got) != 1 || got[0].Key() != recent.Key() {
		t.Errorf("残り = %+v", got)
	}
	// タイムフリー30 なら 30 日残る。
	if n, err := s.DeleteExpiredOnces(ctx, jstTime(2026, 10, 20, 5, 0), domain.MembershipTimeFree30); err != nil || n != 0 {
		t.Errorf("タイムフリー30 = %d, %v", n, err)
	}
}

// DeleteOncesOfAirings は、その番組の 1 回の予約のうち、放送回と局・開始時刻が同じものだけを消す。
// ほかの番組の、同じ局・同じ開始時刻の 1 回の予約は残す。番組が無ければ何もしない。
func TestDeleteOncesOfAirings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, name := range []string{"単発", "ほか"} {
		if err := s.CreateShow(ctx, domain.NewShow(name)); err != nil {
			t.Fatal(err)
		}
	}
	a1 := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 5, 8, 30)}
	a2 := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 5, 10, 0)}
	keep := domain.Once{StationID: "TBS", Start: jstTime(2026, 10, 6, 8, 30)}
	if _, err := s.CreateOnces(ctx, "単発", []domain.Once{a1, a2, keep}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOnce(ctx, "ほか", a1); err != nil {
		t.Fatal(err)
	}
	// 連結のジョブのように、放送回を複数渡す。1 回の予約に無い放送回（毎週の予約の放送回）も混ざる。
	airings := []domain.Airing{
		{StationID: "TBS", Start: a1.Start, End: a1.Start.Add(time.Minute)},
		{StationID: "TBS", Start: jstTime(2026, 10, 5, 9, 0), End: jstTime(2026, 10, 5, 9, 1)},
		{StationID: "TBS", Start: a2.Start, End: a2.Start.Add(time.Minute)},
	}
	n, err := s.DeleteOncesOfAirings(ctx, "単発", airings)
	if err != nil || n != 2 {
		t.Fatalf("DeleteOncesOfAirings = %d, %v", n, err)
	}
	if os, _ := s.ListOnces(ctx, "単発"); len(os) != 1 || os[0].Key() != keep.Key() {
		t.Errorf("単発 = %+v", os)
	}
	if os, _ := s.ListOnces(ctx, "ほか"); len(os) != 1 || os[0].Key() != a1.Key() {
		t.Errorf("ほかの番組の 1 回の予約まで消えた: %+v", os)
	}
	if n, err := s.DeleteOncesOfAirings(ctx, "無い", airings); err != nil || n != 0 {
		t.Errorf("番組が無い: %d, %v", n, err)
	}
}
