package app

import (
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// beforeBroadcast は 10-07 の放送の前（ジョブは最初の試行を待つ）。
var beforeBroadcast = jst(2026, 10, 7, 6, 0)

func (e *env) sync() {
	e.t.Helper()
	if err := e.app.Sync(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) setEnabled(name string, enabled bool) {
	e.t.Helper()
	if err := e.store.SetShowEnabled(e.ctx, name, enabled); err != nil {
		e.t.Fatal(err)
	}
}

// 無効な番組には、番組表の同期でジョブができない。無効にした後の番組表の取り直しで waiting のジョブが消え、
// retrying のジョブは残る。有効に戻すと、次の同期でジョブができる。
func TestSyncDisabledShow(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	disabled := suppinShow
	disabled.Disabled = true
	e.putShows(flatShow, disabled)
	e.sync()
	if jobs := e.jobs(); len(jobs) != 1 || jobs[0].ShowName != flatShow.Name {
		t.Fatalf("無効な番組にジョブができた: %+v", jobs)
	}

	e.setEnabled(suppinShow.Name, true)
	e.sync()
	if jobs := e.jobs(); len(jobs) != 2 {
		t.Fatalf("有効に戻した後: %+v", jobs)
	}
	// ふらっとを retrying にする（1 回目の試行が失敗した）。
	fj, ok, err := e.store.ClaimDue(e.ctx, afterBroadcast)
	if err != nil || !ok || fj.ShowName != flatShow.Name {
		t.Fatalf("ClaimDue = %+v, %v, %v", fj, ok, err)
	}
	if err := e.store.Retry(e.ctx, fj.ID, afterBroadcast.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// TBS を同期し続けるように、有効な番組を TBS にもう 1 つ置く（無効な番組の毎週の予約にしか出てこない局は
	// 同期しない。TestRefreshDropsDisabledShowJob）。
	e.putShows(domain.NewShow("ニュース", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 8, Minute: 31})))
	e.setEnabled(flatShow.Name, false)
	e.setEnabled(suppinShow.Name, false)
	e.sync()
	var jobs []domain.Job
	for _, j := range e.jobs() {
		if j.ShowName != "ニュース" {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) != 1 || jobs[0].ID != fj.ID || jobs[0].State != domain.StateRetrying {
		t.Fatalf("無効にした後（waiting は消え、retrying は残る）: %+v", jobs)
	}

	// 有効に戻すと、次の同期で今日から先の放送回のジョブができる。
	e.setEnabled(suppinShow.Name, true)
	e.sync()
	if sj := e.job(suppinShow.Name); sj.State != domain.StateWaiting {
		t.Errorf("有効に戻した後: %+v", sj)
	}
}

// 無効な番組の毎週の予約にしか出てこない局は同期しない。その局の waiting のジョブは、番組を
// 消したときと同じく、最初の試行の前の番組表の取り直しで消える。
func TestRefreshDropsDisabledShowJob(t *testing.T) {
	e := newFlowEnv(t)
	e.clock.Set(beforeBroadcast)
	e.sync()
	e.setEnabled(flatShow.Name, false)
	e.setEnabled(suppinShow.Name, false)
	e.sync() // 局が無いので同期しない
	if n := len(e.jobs()); n != 2 {
		t.Fatalf("jobs = %d", n)
	}
	e.clock.Set(afterBroadcast)
	if e.runDue() {
		t.Fatal("無効な番組のジョブを実行した")
	}
	if jobs := e.jobs(); len(jobs) != 0 {
		t.Errorf("取り直しで消えていない: %+v", jobs)
	}
}

// Web UI・API で名前を変えると（store.ReplaceShow）、古い名前の waiting のジョブは新しい名前に付け替わり、
// 次の番組表の同期の後も同じジョブのまま残る（消して作り直さない）。
func TestSyncRenamedShow(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	e.sync()
	old := e.job(suppinShow.Name)

	renamed := suppinShow
	renamed.Name = "すっぴんしゃん（新）"
	renamed.CheckTitle = false // 番組表の番組名と食い違っても録らない予定にしない（名前の付け替えだけを見る）
	if err := e.store.ReplaceShow(e.ctx, suppinShow.Name, renamed); err != nil {
		t.Fatal(err)
	}
	e.sync()
	jobs := e.jobs()
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v", jobs)
	}
	for _, j := range jobs {
		if j.ShowName == suppinShow.Name {
			t.Errorf("古い名前のジョブが残っている: %+v", j)
		}
	}
	nj := e.job(renamed.Name)
	if nj.ID != old.ID || nj.State != domain.StateWaiting || len(nj.Airings) != 1 || !nj.Airings[0].Start.Equal(suppin.start) {
		t.Errorf("新しい名前のジョブ: %+v", nj)
	}
}
