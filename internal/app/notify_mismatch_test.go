package app

import (
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
)

// renamedSuppin は、すっぴんしゃんの放送回を、改称前の name で録る番組（番組表の番組名と食い違う）。毎週の予約だけに
// 当たる放送回は、番組名が食い違うと録らない予定・録らなかったになる（ADR 0019）ので、1 回の予約で当てる
// （1 回の予約の放送回は番組名を確かめずに録る）。
// 1 回の予約は番組とは別に足す（putRenamedSuppin）。
func renamedSuppin() domain.Show {
	return domain.NewShow("旧名のすっぴんしゃん")
}

// putRenamedSuppin は番組 r（renamedSuppin を変えたもの）と、すっぴんしゃんの放送回の 1 回の予約を足す。
// others はほかに足す番組。
func (e *env) putRenamedSuppin(r domain.Show, others ...domain.Show) {
	e.t.Helper()
	e.putShows(append(others, r)...)
	if _, err := e.store.CreateOnce(e.ctx, r.Name, domain.Once{StationID: "TBS", Start: suppin.start}); err != nil {
		e.t.Fatal(err)
	}
}

const suppinMismatchLine = "番組名の食い違い: 番組表の番組名 「イモトアヤコのすっぴんしゃん」 が番組の name で始まらない"

// 録音済みの通知の 2 行目に、番組名の食い違いを添える。食い違いが無ければ添えない。
func TestNotifyRecordedTitleMismatch(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	d := e.useDiscord()
	e.putRenamedSuppin(renamedSuppin(), flatShow)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue()
	e.runDue()
	e.checkNotifications()
	got := d.take()
	if len(got) != 2 {
		t.Fatalf("通知 = %q", got)
	}
	if strings.Contains(got[0], "食い違い") || !strings.Contains(got[0], flatShow.Name) {
		t.Errorf("一致する番組に食い違いを添えた: %q", got[0])
	}
	lines := strings.Split(got[1], "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "【録音済み】旧名のすっぴんしゃん・放送日 2026-10-07（") || lines[1] != suppinMismatchLine {
		t.Errorf("通知 = %q", got[1])
	}
}

// 録音できなかったの通知にも、番組名の食い違いを 2 行目に添える（直近のエラーはその後）。
func TestNotifyExpiredTitleMismatch(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	d := e.useDiscord()
	e.putRenamedSuppin(renamedSuppin())
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	e.checkNotifications()
	lines := strings.Split(d.expectOne(t, "【録音できなかった】旧名のすっぴんしゃん"), "\n")
	if len(lines) != 3 || lines[1] != suppinMismatchLine || !strings.HasPrefix(lines[2], "直近のエラー: ") {
		t.Errorf("通知 = %q", lines)
	}
}

// check_title = false の番組は、食い違いがあっても添えない。
func TestNotifyTitleMismatchUnchecked(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	d := e.useDiscord()
	r := renamedSuppin()
	r.CheckTitle = false
	e.putRenamedSuppin(r)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue()
	e.checkNotifications()
	if msg := d.expectOne(t, "【録音済み】旧名のすっぴんしゃん"); strings.Contains(msg, "食い違い") {
		t.Errorf("通知 = %q", msg)
	}
}

// 食い違いの行は、食い違う放送回の番組名を重ねずに並べる。一致する放送回（末尾の印の差など）は出さない。
func TestTitleMismatchLine(t *testing.T) {
	mk := func(name string, titles ...string) domain.Job {
		j := domain.Job{ShowName: name}
		for _, ti := range titles {
			j.Airings = append(j.Airings, domain.Airing{Title: ti})
		}
		return j
	}
	if got := titleMismatchLine(mk("パンサー向井のふらっと", "パンサー向井のふらっと (1)", "パンサー向井のふらっと (2)")); got != "" {
		t.Errorf("食い違いが無いのに行が出た: %q", got)
	}
	got := titleMismatchLine(mk("スカルプDpresents川島明のねごと", "イースト駅前クリニックpresents川島明のねごと", "特番", "特番", "スカルプDpresents川島明のねごと"))
	want := "\n番組名の食い違い: 番組表の番組名 「イースト駅前クリニックpresents川島明のねごと」「特番」 が番組の name で始まらない"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
