package app

import (
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// newsOnce は毎週の予約を持たず、10-07 のニュースを 1 回の予約に持つ番組を作る。
func (e *env) newsOnce() domain.Show {
	e.t.Helper()
	show := domain.NewShow("ニュース")
	e.putShows(show)
	if _, err := e.store.CreateOnce(e.ctx, show.Name, domain.Once{StationID: "TBS", Start: news.start}); err != nil {
		e.t.Fatal(err)
	}
	got, err := e.store.GetShow(e.ctx, show.Name)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// 1 回の予約の放送回は、番組表の同期でジョブになり、次の同期でも消えない。局は 1 回の予約にしか出てこなくても同期する。
func TestSyncOnce(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.newsOnce()
	e.sync()
	j := e.job("ニュース")
	if j.State != domain.StateWaiting || len(j.Airings) != 1 || !j.Airings[0].Start.Equal(news.start) {
		t.Fatalf("job = %+v", j)
	}
	e.sync()
	if jobs := e.jobs(); len(jobs) != 1 || jobs[0].ID != j.ID {
		t.Errorf("取り直しの後: %+v", jobs)
	}
	// 番組表から消えたら waiting のジョブから外れ（消え）、1 回の予約は残る。
	e.fake.setPrograms("TBS", day, flat1, flat2, suppin)
	e.sync()
	if jobs := e.jobs(); len(jobs) != 0 {
		t.Errorf("番組表から消えた後: %+v", jobs)
	}
	if os, err := e.store.ListOnces(e.ctx, "ニュース"); err != nil || len(os) != 1 {
		t.Errorf("1 回の予約 = %+v, %v", os, err)
	}
}

// 同期の対象に、有効な番組の 1 回の予約の、録音期限の内側の過去の放送日が入る（waiting のジョブが無くても）。
// 無効な番組の 1 回の予約の局・放送日は入らない。過去の放送日では、ほかの番組の毎週の予約に当たる放送済みの回は
// ジョブにならない（1 回の予約の放送回を含むジョブだけを作る）。
func TestSyncPastOnceDay(t *testing.T) {
	e := newEnv(t, afterBroadcast.AddDate(0, 0, 2))
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	e.newsOnce()
	e.setEnabled("ニュース", false)
	// 今日から先の番組表は 404 なので、過去の放送日が対象に入らなければ同期は失敗する。
	if err := e.app.Sync(e.ctx); err == nil {
		t.Error("過去の放送日を同期した")
	}
	if jobs := e.jobs(); len(jobs) != 0 {
		t.Fatalf("無効な番組: %+v", jobs)
	}
	e.setEnabled("ニュース", true)
	e.sync()
	if jobs := e.jobs(); len(jobs) != 1 {
		t.Fatalf("ほかの番組の毎週の予約の放送済みの回がジョブになった: %+v", jobs)
	}
	// 2 回目の同期でも同じ（1 回の予約の放送回を含むジョブしか無い日は、1 回の予約のためだけの日のまま）。
	e.sync()
	if jobs := e.jobs(); len(jobs) != 1 {
		t.Fatalf("2 回目の同期で、ほかの番組の毎週の予約の放送済みの回がジョブになった: %+v", jobs)
	}
	if j := e.job("ニュース"); j.Day != day || j.State != domain.StateWaiting {
		t.Errorf("job = %+v", j)
	}
	// 番組を変えた直後の展開でも同じ。
	e.app.expandShows(e.ctx, flatShow, suppinShow) // 今日から先の番組表は 404 なのでエラーになる
	if jobs := e.jobs(); len(jobs) != 1 {
		t.Errorf("expandShows の後: %+v", jobs)
	}
}

// 開始日を持たない毎週の予約は今日から先の放送日にだけ当たる。今日より前の放送日では、その日に waiting のジョブがあっても、
// その毎週の予約だけに当たる放送回からジョブを作らない（同期でも、番組を変えた直後の展開でも）。すでにある waiting の
// ジョブはそのまま残る。
func TestSyncPastDayCreatesNoWeeklyJobs(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(suppinShow)
	e.sync()
	// 翌日。10-07 のすっぴんのジョブは waiting のまま（最初の試行の前）。放送の後に、同じ放送日に当たる毎週の予約の番組を足した。
	e.clock.Set(afterBroadcast.AddDate(0, 0, 1))
	e.putShows(flatShow, suppinShow)
	e.app.Sync(e.ctx) // 今日から先の番組表は 404 なので、一部の局・放送日は展開できない
	if e.hasJob(flatShow.Name) {
		t.Errorf("同期で、過去の放送日に毎週の予約からジョブができた: %+v", e.jobs())
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateWaiting || j.Day != day {
		t.Errorf("すっぴん = %+v", j)
	}
	e.app.expandShows(e.ctx, flatShow) // 今日から先の番組表は 404 なのでエラーになる
	if e.hasJob(flatShow.Name) {
		t.Errorf("番組を変えた直後の展開で、過去の放送日に毎週の予約からジョブができた: %+v", e.jobs())
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateWaiting || j.Day != day {
		t.Errorf("展開の後のすっぴん = %+v", j)
	}
}

// 今日より前の放送日で、waiting のジョブの放送回の開始時刻が変わったら（開始時刻は後からずれない前提で、起きないとみなす）、
// 元の放送回は外れてジョブは消え、ずれた後の放送回から毎週の予約でジョブを作り直さない。
func TestSyncPastDayDoesNotRecreateMovedAiring(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	// ずれた後の放送回にも当たるように、ずれた後の開始時刻の毎週の予約も持つ。
	noJoin := domain.Show{Name: suppinShow.Name, CheckTitle: true, Weekly: []domain.Weekly{
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 11}),
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 11, Minute: 5}),
	}}
	e.putShows(noJoin)
	e.sync()
	e.job(noJoin.Name)
	// 翌日。10-07 のジョブは waiting のまま（最初の試行の前）。番組表の更新で開始時刻が 5 分ずれた。
	e.clock.Set(afterBroadcast.AddDate(0, 0, 1))
	moved := suppin
	moved.start = suppin.start.Add(5 * time.Minute)
	moved.end = suppin.end.Add(5 * time.Minute)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, moved)
	e.sync()
	if e.hasJob(noJoin.Name) {
		t.Errorf("過去の放送日に毎週の予約でジョブを作り直した: %+v", e.jobs())
	}
}

// expandShows は、渡した番組の waiting のジョブがある過去の放送日も展開するので、無効にした番組の、放送済みで
// 最初の試行を待つ waiting のジョブもすぐ消える。
func TestExpandShowsPastWaitingJob(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	e.sync()
	// 翌日。10-07 のジョブは waiting のまま（最初の試行の前）。
	e.clock.Set(afterBroadcast.AddDate(0, 0, 1))
	if j := e.job(flatShow.Name); j.State != domain.StateWaiting || j.Day != day {
		t.Fatalf("job = %+v", j)
	}
	e.setEnabled(flatShow.Name, false)
	disabled, err := e.store.GetShow(e.ctx, flatShow.Name)
	if err != nil {
		t.Fatal(err)
	}
	e.app.expandShows(e.ctx, disabled)
	for _, j := range e.jobs() {
		if j.ShowName == flatShow.Name {
			t.Errorf("無効にした番組の過去の waiting のジョブが残っている: %+v", j)
		}
	}
	// ほかの番組のジョブは残る。
	if j := e.job(suppinShow.Name); j.State != domain.StateWaiting {
		t.Errorf("すっぴん = %+v", j)
	}
	// 有効に戻しても、過去の放送日に開始日を持たない毎週の予約から新しいジョブはできない。
	e.setEnabled(flatShow.Name, true)
	e.app.expandShows(e.ctx, flatShow)
	for _, j := range e.jobs() {
		if j.ShowName == flatShow.Name {
			t.Errorf("過去の放送日に毎週の予約からジョブができた: %+v", j)
		}
	}
}

// 録音期限を過ぎた 1 回の予約は、同期のときに消える。ジョブは残る。
func TestSyncDeletesExpiredOnce(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.newsOnce()
	e.sync()
	// 放送日 10-07 の録音期限（非会員）は 10-15 05:00。1 回の予約は 1 日遅らせて 10-16 05:00 に消す。
	e.clock.Set(jst(2026, 10, 16, 4, 59))
	e.sync()
	if os, _ := e.store.ListOnces(e.ctx, "ニュース"); len(os) != 1 {
		t.Fatalf("期限の前に消えた: %+v", os)
	}
	e.clock.Set(jst(2026, 10, 16, 5, 0))
	e.sync()
	if os, _ := e.store.ListOnces(e.ctx, "ニュース"); len(os) != 0 {
		t.Errorf("期限の後に残っている: %+v", os)
	}
	if n := len(e.jobs()); n != 1 {
		t.Errorf("ジョブが消えた: %d", n)
	}
}

// expandShows は、渡した番組の予約の局・放送日を、同期を待たずに展開する。
func TestExpandShows(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	show := e.newsOnce()
	e.putShows(flatShow)
	if err := e.app.expandShows(e.ctx, show); err == nil {
		// 10-07 のほかの放送日の番組表は 404 なので、展開の失敗をまとめて返す。
		t.Error("取れない放送日があるのにエラーにならない")
	}
	if jobs := e.jobs(); len(jobs) != 2 {
		// 展開は今の番組の一覧（ふらっとも）で行う。
		t.Errorf("jobs = %+v", jobs)
	}
	e.job("ニュース")

	// 無効にした番組を渡すと、その番組の waiting のジョブがすぐ消える。
	e.setEnabled("ニュース", false)
	e.app.expandShows(e.ctx, show)
	for _, j := range e.jobs() {
		if j.ShowName == "ニュース" {
			t.Errorf("無効にした後も残っている: %+v", j)
		}
	}

	// 過去の 1 回の予約の放送日も展開する。
	e.setEnabled("ニュース", true)
	e.clock.Set(afterBroadcast.AddDate(0, 0, 2))
	e.app.expandShows(e.ctx, show)
	if j := e.job("ニュース"); j.Day != day {
		t.Errorf("過去の放送日: %+v", j)
	}
	// 予約の無い番組なら何もしない。
	if err := e.app.expandShows(e.ctx, domain.NewShow("空")); err != nil {
		t.Errorf("番組の無い番組: %v", err)
	}
}

// 最初の試行の直前の取り直しも、同期と同じ判定を使う。1 回の予約で作った過去の放送日のジョブを実行する直前の
// 取り直しで、同じ局・同じ放送日のほかの番組の毎週の予約（放送の後に足した）に当たる放送済みの回はジョブにならない。
func TestRefreshPastOnceDayKeepsOtherWeekly(t *testing.T) {
	e := newEnv(t, afterBroadcast.AddDate(0, 0, 2))
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	show := e.newsOnce()
	e.app.expandShows(e.ctx, show) // 今日から先の番組表は 404 なのでエラーになる
	e.job("ニュース")
	// 放送の後に、同じ局・同じ放送日に当たる毎週の予約の番組を足した。
	e.putShows(suppinShow)
	if !e.runDue() {
		t.Fatal("ジョブを実行しなかった")
	}
	for _, j := range e.jobs() {
		if j.ShowName != "ニュース" {
			t.Errorf("取り直しで、ほかの番組の毎週の予約の放送済みの回がジョブになった: %+v", j)
		}
	}
	if j := e.job("ニュース"); j.State != domain.StateDone {
		t.Errorf("ニュース = %+v", j)
	}
}

// 最初の試行の直前の取り直しでも、今日より前の放送日では、その日に waiting のジョブがあっても、開始日を持たない毎週の予約だけに
// 当たる放送回からジョブを作らない。取り直したジョブはそのまま録音する。
func TestRefreshPastWeeklyDayCreatesNoWeeklyJobs(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(suppinShow)
	e.sync()
	// 翌日。10-07 のすっぴんのジョブは waiting のまま、最初の試行の時刻を過ぎている。放送の後に、同じ放送日に当たる
	// 毎週の予約の番組を足した。
	e.clock.Set(afterBroadcast.AddDate(0, 0, 1))
	e.putShows(flatShow, suppinShow)
	if !e.runDue() {
		t.Fatal("ジョブを実行しなかった")
	}
	if e.hasJob(flatShow.Name) {
		t.Errorf("取り直しで、過去の放送日に毎週の予約からジョブができた: %+v", e.jobs())
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Errorf("すっぴん = %+v", j)
	}
}

// laterOnce は、まだ録音していない（番組表の無い）先の放送日の 1 回の予約。done になったジョブの放送回ではないので残る。
var laterOnce = domain.Once{StationID: "TBS", Start: jst(2026, 10, 14, 8, 30)}

// createOnces は番組 name に 1 回の予約 os を足す。
func (e *env) createOnces(name string, os ...domain.Once) {
	e.t.Helper()
	if _, err := e.store.CreateOnces(e.ctx, name, os); err != nil {
		e.t.Fatal(err)
	}
}

// onceKeys は番組 name の 1 回の予約の Key。
func (e *env) onceKeys(name string) []string {
	e.t.Helper()
	os, err := e.store.ListOnces(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	var keys []string
	for _, o := range os {
		keys = append(keys, o.Key())
	}
	return keys
}

// 1 回の予約は、その放送回のジョブが done になったら消える。連結のジョブ（毎週の予約の放送回と同じジョブに
// 入る）でも、連結しないジョブでも同じ。done になったジョブの放送回でない 1 回の予約は残る。
func TestDoneJobDeletesOnces(t *testing.T) {
	e := newFlowEnv(t)
	// ふらっと（連結）は、間のニュースを 1 回の予約で足す。すっぴんしゃんは連結しない番組にする。
	noJoin := suppinShow
	noJoin.Split = domain.SplitNone
	e.putShows(flatShow, noJoin)
	e.createOnces(flatShow.Name, domain.Once{StationID: "TBS", Start: news.start}, laterOnce)
	e.createOnces(noJoin.Name, domain.Once{StationID: "TBS", Start: suppin.start})
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if fj := e.job(flatShow.Name); len(fj.Airings) != 3 {
		t.Fatalf("ふらっと = %+v", fj)
	}
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("ふらっと = %+v", j)
	}
	if j := e.job(noJoin.Name); j.State != domain.StateDone {
		t.Fatalf("すっぴんしゃん = %+v", j)
	}
	if got := e.onceKeys(flatShow.Name); len(got) != 1 || got[0] != laterOnce.Key() {
		t.Errorf("ふらっとの 1 回の予約 = %v, want [%s]", got, laterOnce.Key())
	}
	if got := e.onceKeys(noJoin.Name); len(got) != 0 {
		t.Errorf("すっぴんしゃんの 1 回の予約 = %v", got)
	}
}

// 録音期限を過ぎて取れた分だけを保存した（部分保存で done になった）ときも、1 回の予約は消える（取れなかった
// 放送回の 1 回の予約も）。done になる前（retrying の間）は消えない。
func TestPartialDoneDeletesOnces(t *testing.T) {
	e := newFlowEnv(t)
	e.putShows(flatShow)
	e.createOnces(flatShow.Name, domain.Once{StationID: "TBS", Start: news.start}, laterOnce)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[news.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	if got := e.onceKeys(flatShow.Name); len(got) != 2 {
		t.Errorf("done になる前に 1 回の予約が消えた: %v", got)
	}
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	j = e.job(flatShow.Name)
	// 入手は失敗した放送回で止まるので、ニュースの後のふらっと (2) も欠ける。
	if j.State != domain.StateDone || len(j.Missing) == 0 || !j.Missing[0].Start.Equal(news.start) {
		t.Fatalf("job = %+v", j)
	}
	if got := e.onceKeys(flatShow.Name); len(got) != 1 || got[0] != laterOnce.Key() {
		t.Errorf("1 回の予約 = %v, want [%s]", got, laterOnce.Key())
	}
}

// 録音中（running）に Web UI・API で番組の名前を変えると、ジョブは新しい名前に付け替わり、1 回の予約も新しい名前の
// 番組に引き継がれる。done にしたときは、録音を始めたときの名前ではなく、ジョブの今の名前の番組の 1 回の予約を消す。
func TestDoneJobDeletesOncesAfterRenameWhileRunning(t *testing.T) {
	e := newFlowEnv(t)
	e.putShows(flatShow)
	e.createOnces(flatShow.Name, domain.Once{StationID: "TBS", Start: news.start}, laterOnce)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	renamed := flatShow
	renamed.Name = "ふらっと（新）"
	e.media.onDuration = func() {
		e.media.onDuration = nil
		if j := e.job(flatShow.Name); j.State != domain.StateRunning {
			t.Errorf("名前を変えるときのジョブ = %+v", j)
		}
		if err := e.store.ReplaceShow(e.ctx, flatShow.Name, renamed); err != nil {
			t.Error(err)
		}
	}
	if !e.runDue() {
		t.Fatal("ジョブを実行しなかった")
	}
	if j := e.job(renamed.Name); j.State != domain.StateDone {
		t.Fatalf("ジョブ = %+v", j)
	}
	if got := e.onceKeys(renamed.Name); len(got) != 1 || got[0] != laterOnce.Key() {
		t.Errorf("新しい名前の番組の 1 回の予約 = %v, want [%s]", got, laterOnce.Key())
	}
}
