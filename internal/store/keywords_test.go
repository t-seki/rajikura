package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

func TestKeywordsCRUD(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id1, err := s.CreateKeyword(ctx, domain.Keyword{Words: "パンサー 向井", Exclude: "再放送", Stations: []string{"TBS", "QRR", "TBS"}})
	if err != nil {
		t.Fatal(err)
	}
	// 語の形が同じ（並びと全角・半角の違い）キーワードは作れない。
	if _, err := s.CreateKeyword(ctx, domain.Keyword{Words: "向井　ﾊﾟﾝｻｰ"}); !errors.Is(err, ErrExists) {
		t.Errorf("同じ形の語: %v", err)
	}
	if _, err := s.CreateKeyword(ctx, domain.Keyword{Words: " "}); err == nil {
		t.Error("語が空のキーワードを作った")
	}
	id2, err := s.CreateKeyword(ctx, domain.Keyword{Words: "ゲスト", Disabled: true})
	if err != nil || id2 <= id1 {
		t.Fatalf("CreateKeyword = %d, %v", id2, err)
	}
	ks, err := s.ListKeywords(ctx)
	if err != nil || len(ks) != 2 || ks[0].ID != id1 || ks[1].ID != id2 {
		t.Fatalf("ListKeywords = %+v, %v", ks, err)
	}
	if k := ks[0]; k.Words != "パンサー 向井" || k.Exclude != "再放送" || !slices.Equal(k.Stations, []string{"TBS", "QRR"}) || k.Disabled {
		t.Errorf("1 つ目 = %+v", k)
	}
	if k := ks[1]; len(k.Stations) != 0 || !k.Disabled {
		t.Errorf("2 つ目 = %+v", k)
	}

	// 変える。ほかのキーワードと同じ形にはできない。
	if err := s.UpdateKeyword(ctx, domain.Keyword{ID: id2, Words: "向井 パンサー"}); !errors.Is(err, ErrExists) {
		t.Errorf("ほかと同じ形に変えた: %v", err)
	}
	if err := s.UpdateKeyword(ctx, domain.Keyword{ID: id2, Words: "ゲスト 出演"}); err != nil {
		t.Fatal(err)
	}
	if k, err := s.GetKeyword(ctx, id2); err != nil || k.Words != "ゲスト 出演" || k.Disabled {
		t.Errorf("GetKeyword = %+v, %v", k, err)
	}
	if err := s.UpdateKeyword(ctx, domain.Keyword{ID: 999, Words: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いキーワードを変えた: %v", err)
	}
	if err := s.DeleteKeyword(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKeyword(ctx, id2); !errors.Is(err, ErrNotFound) {
		t.Errorf("2 回消せた: %v", err)
	}
	if _, err := s.GetKeyword(ctx, id2); !errors.Is(err, ErrNotFound) {
		t.Errorf("消したキーワードを読めた: %v", err)
	}
	// 消した ID は使い回さない。
	id3, err := s.CreateKeyword(ctx, domain.Keyword{Words: "三"})
	if err != nil || id3 <= id2 {
		t.Errorf("ID を使い回した: %d, %v", id3, err)
	}
}

// keywordPlanFor は、キーワード k が放送回 as を番組 name（無ければ作る）に足す変更を返す。
func keywordPlanFor(k domain.Keyword, name string, created bool, as ...domain.Airing) func(KeywordState) KeywordChanges {
	return func(KeywordState) KeywordChanges {
		add := domain.KeywordAddition{Keyword: k, Hit: domain.KeywordHit{Airing: as[0], StationName: "TBSラジオ"}, ShowName: name, Airings: as}
		if created {
			add.Created, add.Show = true, domain.NewShow(name)
		}
		return KeywordChanges{
			Plan:          domain.KeywordPlan{Additions: []domain.KeywordAddition{add}, LimitNotified: map[int64]bool{k.ID: true}},
			Notifications: []Notification{{Kind: notify.KindKeywordAdded, Message: "足した", CreatedAt: as[0].Start, Deliver: true}},
		}
	}
}

func TestApplyKeywords(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.CreateKeyword(ctx, domain.Keyword{Words: "向井"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.GetKeyword(ctx, id)
	a1 := domain.Airing{StationID: "TBS", Start: jstTime(2026, 10, 8, 13, 0), End: jstTime(2026, 10, 8, 14, 0), Title: "番組(1)"}
	a2 := domain.Airing{StationID: "TBS", Start: jstTime(2026, 10, 8, 14, 0), End: jstTime(2026, 10, 8, 15, 0), Title: "番組(2)"}
	now := jstTime(2026, 10, 7, 12, 0)
	today := domain.BroadcastDayOf(now)

	var seen KeywordState
	if _, err := s.ApplyKeywords(ctx, today, now, func(st KeywordState) KeywordChanges {
		seen = st
		return keywordPlanFor(k, "番組", true, a1, a2)(st)
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen.Keywords) != 1 || len(seen.Recorded) != 0 || seen.LimitNotified[id] {
		t.Errorf("渡した状態 = %+v", seen)
	}
	r, err := s.GetShow(ctx, "番組")
	if err != nil || len(r.Onces) != 2 || r.Onces[0].KeywordID != id || r.Onces[1].KeywordID != id || r.Split != domain.SplitNone || !r.CheckTitle {
		t.Fatalf("作った番組 = %+v, %v", r, err)
	}
	ns, err := s.Notifications(ctx)
	if err != nil || len(ns) != 1 || ns[0].Kind != notify.KindKeywordAdded {
		t.Errorf("通知 = %+v, %v", ns, err)
	}
	recs, err := s.KeywordAirings(ctx, id)
	if err != nil || len(recs) != 2 || recs[0].Key() != a2.Key() || recs[0].StationName != "TBSラジオ" || recs[0].ShowName != "番組" ||
		recs[0].Title != "番組(2)" || !recs[0].End.Equal(a2.End) || recs[0].State != KeywordAiringPlanned {
		t.Fatalf("記録 = %+v, %v", recs, err)
	}
	if p, err := s.KeywordPending(ctx); err != nil || p[id] != 2 {
		t.Errorf("KeywordPending = %v, %v", p, err)
	}

	// 次の判定には、記録と上限の印が渡る。
	if _, err := s.ApplyKeywords(ctx, today, now, func(st KeywordState) KeywordChanges {
		seen = st
		return KeywordChanges{}
	}); err != nil {
		t.Fatal(err)
	}
	if !seen.Recorded[a1.Key()] || !seen.Recorded[a2.Key()] || seen.Pending[id] != 2 || !seen.LimitNotified[id] || len(seen.Shows) != 1 {
		t.Errorf("2 回目に渡した状態 = %+v", seen)
	}

	// 作る番組がもうあれば、何も書かずに巻き戻す。
	if _, err := s.ApplyKeywords(ctx, today, now, keywordPlanFor(k, "番組", true, domain.Airing{StationID: "QRR", Start: a1.Start})); !errors.Is(err, ErrExists) {
		t.Fatalf("同じ名前の番組を作った: %v", err)
	}
	if ns, _ := s.Notifications(ctx); len(ns) != 1 {
		t.Errorf("巻き戻したのに通知が残った: %+v", ns)
	}

	// 番組の名前を変えても、キーワードの印は 1 回の予約と一緒に引き継ぐ。
	if err := s.ReplaceShow(ctx, "番組", domain.NewShow("新しい名前")); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetShow(ctx, "新しい名前"); err != nil || len(r.Onces) != 2 || r.Onces[0].KeywordID != id {
		t.Errorf("名前を変えた後 = %+v, %v", r, err)
	}

	// キーワードを消しても、1 回の予約と番組は残る（印は外れる）。記録は消える。
	if err := s.DeleteKeyword(ctx, id); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetShow(ctx, "新しい名前"); err != nil || len(r.Onces) != 2 || r.Onces[0].KeywordID != 0 {
		t.Errorf("キーワードを消した後 = %+v, %v", r, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM keyword_airings`).Scan(&n); err != nil || n != 0 {
		t.Errorf("記録が残った: %d, %v", n, err)
	}
}

func TestKeywordAiringStates(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.CreateKeyword(ctx, domain.Keyword{Words: "向井"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.GetKeyword(ctx, id)
	at := func(h int) domain.Airing {
		return domain.Airing{StationID: "TBS", Start: jstTime(2026, 10, 8, h, 0), End: jstTime(2026, 10, 8, h+1, 0), Title: "番組"}
	}
	planned, done, missed, removed, waiting := at(6), at(8), at(10), at(12), at(14)
	skipPlanned, skipped := at(16), at(18)
	now := jstTime(2026, 10, 7, 12, 0)
	today := domain.BroadcastDayOf(now)
	if _, err := s.ApplyKeywords(ctx, today, now, keywordPlanFor(k, "番組", true, planned, done, missed, removed, waiting, skipPlanned, skipped)); err != nil {
		t.Fatal(err)
	}
	day := domain.BroadcastDayOf(done.Start)
	for _, j := range []domain.Job{
		{ShowName: "番組", Day: day, Part: 1, State: domain.StateDone, Airings: []domain.Airing{done}},
		{ShowName: "番組", Day: day, Part: 2, State: domain.StateExpired, Airings: []domain.Airing{missed}},
		{ShowName: "番組", Day: day, Part: 3, State: domain.StateWaiting, Airings: []domain.Airing{waiting}},
		{ShowName: "番組", Day: day, Part: 4, State: domain.StateSkipped, ByPerson: true, Airings: []domain.Airing{skipPlanned}},
		{ShowName: "番組", Day: day, Part: 5, State: domain.StateSkipped, ByPerson: true, Settled: true, Airings: []domain.Airing{skipped}},
	} {
		if _, err := s.InsertJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	// 録音済み・録音できなかった・外された回の 1 回の予約は消える（removeDoneOnces・DeleteExpiredOnces・人が消す）。
	if _, err := s.DeleteOncesOfAirings(ctx, "番組", []domain.Airing{done, missed, removed, waiting}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.KeywordAirings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.Key()] = r.State
	}
	want := map[string]string{
		planned.Key(): KeywordAiringPlanned, done.Key(): KeywordAiringDone, missed.Key(): KeywordAiringMissed,
		removed.Key(): KeywordAiringRemoved, waiting.Key(): KeywordAiringPlanned,
		skipPlanned.Key(): KeywordAiringSkipPlanned, skipped.Key(): KeywordAiringSkipped,
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("%s の状態 = %q, want %q", key, got[key], w)
		}
	}
	// 新しい順。
	if !slices.IsSortedFunc(recs, func(a, b KeywordAiring) int { return b.Start.Compare(a.Start) }) {
		t.Errorf("新しい順でない: %+v", recs)
	}
	if p, err := s.KeywordPending(ctx); err != nil || p[id] != 2 {
		t.Errorf("KeywordPending = %v, %v（録音予定の 2 回だけ数える）", p, err)
	}
	if _, err := s.KeywordAirings(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("無いキーワードの記録: %v", err)
	}
}
