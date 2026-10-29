package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// unmatchedShow は TBS の水曜 9:00 の毎週の予約の番組。10-07 の番組表では 9:00 に始まる放送回が無く、
// 9:00 は 8:31〜10:00 のニュースが含む。番組名の合う放送回は flat1・flat2。
var unmatchedShow = domain.NewShow("パンサー向井のふらっと", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 9}))

// nextWeek は day の翌週の放送日（今日から 7 日先。同期の対象の最後の放送日）。
var nextWeek = day.AddDays(7)

func (e *env) unmatched(name string) []domain.UnmatchedDay {
	e.t.Helper()
	m, err := e.store.UnmatchedDays(e.ctx, e.clock.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return m[name]
}

func unmatchedStarts(us []domain.UnmatchedDay) []string {
	var out []string
	for _, u := range us {
		out = append(out, u.Start.In(domain.JST).Format("01-02 15:04"))
	}
	return out
}

// newUnmatchedEnv は、今が 10-07 の 6 時で、TBS の 10-07 と 10-14 の番組表に 9:00 に始まる放送回が無く、
// unmatchedShow がある環境を作る。
func newUnmatchedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.fake.setPrograms("TBS", nextWeek, shift(flat1, 7), shift(news, 7), shift(flat2, 7))
	e.putShows(unmatchedShow)
	return e
}

// shift は放送回 p を n 日ずらす。
func shift(p prog, n int) prog {
	p.start, p.end = p.start.AddDate(0, 0, n), p.end.AddDate(0, 0, n)
	return p
}

// 番組表の同期で当たらない日を記録し、同期 1 回・番組ごとに 1 通にまとめて通知する（notify.success = false でも送る）。
// 同期を繰り返しても、直ってからまた当たらなくなっても、通知は重ならない。
func TestSyncUnmatchedDays(t *testing.T) {
	e := newUnmatchedEnv(t)
	d := e.useDiscord()
	e.app.MuteSuccess = true
	e.sync()

	us := e.unmatched(unmatchedShow.Name)
	if got := unmatchedStarts(us); strings.Join(got, ",") != "10-07 09:00,10-14 09:00" {
		t.Fatalf("当たらない日 = %v", got)
	}
	if c := us[0].Covering; c == nil || c.Title != news.title || len(us[0].Titled) != 2 {
		t.Errorf("事実 = %+v", us[0])
	}
	ns := e.notifications()
	// 通知はその番組を指す。
	if len(ns) != 1 || ns[0].Kind != notify.KindUnmatchedDay || !ns[0].Deliver || ns[0].Show != unmatchedShow.Name || ns[0].JobID != 0 {
		t.Fatalf("通知 = %+v", ns)
	}
	e.checkNotifications()
	msg := d.expectOne(t, "【当たらない日】", "番組「パンサー向井のふらっと」",
		"\n- 10/7（水）09:00 は放送回が無い。その時刻は「ニュース」（08:31〜10:00）。番組名の合う放送回: 08:30〜08:31、10:00〜10:01",
		"\n- 10/14（水）09:00 は放送回が無い。")
	if strings.Count(msg, "\n- ") != 2 {
		t.Errorf("当たらない日は 2 行: %q", msg)
	}

	// 同期を繰り返しても通知しない。
	e.sync()
	e.checkNotifications()
	d.expectNone(t)

	// 10-07 が当たるようになったら一覧から消え、また当たらなくなっても通知しない。
	at9 := prog{id: "P9", start: jst(2026, 10, 7, 9, 0), end: jst(2026, 10, 7, 9, 30), title: "パンサー向井のふらっと 特番"}
	early := news
	early.end = at9.start
	e.fake.setPrograms("TBS", day, flat1, early, at9, flat2, suppin)
	e.sync()
	if got := unmatchedStarts(e.unmatched(unmatchedShow.Name)); strings.Join(got, ",") != "10-14 09:00" {
		t.Errorf("当たるようになった後 = %v", got)
	}
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.sync()
	if got := unmatchedStarts(e.unmatched(unmatchedShow.Name)); len(got) != 2 {
		t.Errorf("また当たらなくなった後 = %v", got)
	}
	e.checkNotifications()
	d.expectNone(t)

	// 新しく見つけた当たらない日だけを通知する。
	second := unmatchedShow
	second.Weekly = append(slices.Clone(second.Weekly), domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 1})) // 25:00
	e.putShows(second)
	e.sync()
	e.checkNotifications()
	msg = d.expectOne(t, "【当たらない日】", "10/7（水）25:00 は放送回が無い。その時刻を含む放送回も番組表に無い。", "10/14（水）25:00")
	if strings.Contains(msg, "09:00") {
		t.Errorf("通知した当たらない日は重ねない: %q", msg)
	}
}

// 番組表を取れなかった・空だった局・放送日では、記録を変えない。
func TestSyncUnmatchedDaysGuideFailure(t *testing.T) {
	e := newUnmatchedEnv(t)
	e.sync()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 2 {
		t.Fatalf("当たらない日 = %+v", got)
	}
	// 10-07 の番組表が空になった（当たるようになった枠と取り違えない）。
	e.fake.setPrograms("TBS", day)
	e.sync()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 2 {
		t.Errorf("番組表が空の後 = %+v", got)
	}
	// どの番組表も取れない。
	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	if err := e.app.Sync(e.ctx); err == nil {
		t.Fatal("同期が失敗しない")
	}
	if got := e.unmatched(unmatchedShow.Name); len(got) != 2 {
		t.Errorf("番組表を取れなかった後 = %+v", got)
	}
}

// 番組を変えた直後の展開でも当たらない日を記録・通知し、番組を無効にする・毎週の予約を変える・消すと記録が消える。
func TestExpandUnmatchedDays(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.fake.setPrograms("TBS", nextWeek, shift(flat1, 7), shift(news, 7), shift(flat2, 7))
	d := e.useDiscord()
	e.putShows(unmatchedShow)
	e.app.RequestExpand(unmatchedShow)
	e.expandPending()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 2 {
		t.Fatalf("当たらない日 = %+v", got)
	}
	e.checkNotifications()
	d.expectOne(t, "【当たらない日】", "10/7（水）09:00", "10/14（水）09:00")

	// 毎週の予約を放送回のある時刻に変えると消える。
	moved := domain.NewShow(unmatchedShow.Name, domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 8, Minute: 30}))
	e.putShows(moved)
	e.app.RequestExpand(unmatchedShow, moved)
	e.expandPending()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 0 {
		t.Errorf("時刻を変えた後 = %+v", got)
	}

	// 戻して無効にすると消える。
	e.putShows(unmatchedShow)
	e.app.RequestExpand(moved, unmatchedShow)
	e.expandPending()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 2 {
		t.Fatalf("戻した後 = %+v", got)
	}
	e.setEnabled(unmatchedShow.Name, false)
	e.app.RequestExpand(unmatchedShow)
	e.expandPending()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 0 {
		t.Errorf("無効にした後 = %+v", got)
	}

	// 有効に戻してから消すと消える。
	e.setEnabled(unmatchedShow.Name, true)
	e.app.RequestExpand(unmatchedShow)
	e.expandPending()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 2 {
		t.Fatalf("有効に戻した後 = %+v", got)
	}
	if err := e.store.DeleteShow(e.ctx, unmatchedShow.Name); err != nil {
		t.Fatal(err)
	}
	e.app.RequestExpand(unmatchedShow)
	e.expandPending()
	if got := e.unmatched(unmatchedShow.Name); len(got) != 0 {
		t.Errorf("消した後 = %+v", got)
	}
}

// 無効な番組、開始日より前の放送日、今日より前の放送日には、同期で記録を作らない。
func TestSyncUnmatchedDaysTargets(t *testing.T) {
	e := newUnmatchedEnv(t)
	// 前の週（今日より前）の番組表もあるが、記録しない。開始日は 10-14 なので、10-07 も記録しない。
	e.fake.setPrograms("TBS", day.AddDays(-7), shift(flat1, -7), shift(news, -7))
	from := domain.NewShow(unmatchedShow.Name, unmatchedShow.Weekly[0].WithFrom(nextWeek))
	disabled := domain.NewShow("無効", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 9, Minute: 15}))
	disabled.Disabled = true
	e.putShows(from, disabled)
	e.sync()
	if got := unmatchedStarts(e.unmatched(unmatchedShow.Name)); strings.Join(got, ",") != "10-14 09:00" {
		t.Errorf("当たらない日 = %v", got)
	}
	if got := e.unmatched("無効"); len(got) != 0 {
		t.Errorf("無効な番組 = %+v", got)
	}
}
