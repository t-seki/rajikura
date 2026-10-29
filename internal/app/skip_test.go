package app

import (
	"slices"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// 今日から先の放送回の録らない予定と、最初の試行の時刻での確定、人の「録る」「録らない」（ADR 0019）。

// special はすっぴんしゃんの枠に入った、番組名の食い違う特番。
var special = func() prog {
	p := suppin
	p.title = "特番"
	return p
}()

// skipKinds は、録るか録らないかの通知の種類を、書いた順に返す。
func (e *env) skipKinds() []string {
	e.t.Helper()
	var out []string
	for _, n := range e.notifications() {
		switch n.Kind {
		case notify.KindSkipPlanned, notify.KindUnskipped, notify.KindSkipped:
			out = append(out, n.Kind)
		}
	}
	slices.Reverse(out) // notifications は新しい順
	return out
}

func (e *env) notificationOf(kind string) string {
	e.t.Helper()
	for _, n := range e.notifications() {
		if n.Kind == kind {
			return n.Message
		}
	}
	return ""
}

// 番組名が食い違えば、放送の前でも録らない予定にし、直れば録音予定に戻す。そのたびに通知する。
func TestSkipPlannedBeforeAiring(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.putShows(suppinShow)
	e.sync()
	j := e.job(suppinShow.Name)
	if j.State != domain.StateSkipped || j.Settled || j.ByPerson {
		t.Fatalf("食い違う: %+v", j)
	}
	if got := e.notificationOf(notify.KindSkipPlanned); got != "【録らない予定】イモトアヤコのすっぴんしゃん・放送日 2026-10-07 は、番組名が食い違うので録らない予定にした（録りにいく時刻 2026-10-07 11:05）。録るなら「録る」を選ぶ。"+
		"\n番組名の食い違い: 番組表の番組名 「特番」 が番組の name で始まらない" {
		t.Errorf("録らない予定の通知 = %q", got)
	}
	// 同じ番組表で同期し直しても、通知は重ならない。
	e.sync()
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("繰り返し: %v", got)
	}

	// 番組名が直れば録音予定に戻す。
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.sync()
	if j := e.job(suppinShow.Name); j.State != domain.StateWaiting {
		t.Fatalf("直った: %+v", j)
	}
	if got := e.notificationOf(notify.KindUnskipped); got != "【録音予定に戻した】イモトアヤコのすっぴんしゃん・放送日 2026-10-07 は、番組名が直ったので録る（録りにいく時刻 2026-10-07 11:05）。" {
		t.Errorf("録音予定に戻したの通知 = %q", got)
	}
	// また食い違えば、もう 1 度知らせる。
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.sync()
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned, notify.KindUnskipped, notify.KindSkipPlanned}) {
		t.Errorf("行き来: %v", got)
	}

	// 最初の試行の時刻に番組表を取り直し、食い違ったままなら確定する（録らなかった。もう知らせたので通知しない）。録りにいかない。
	e.clock.Set(afterBroadcast)
	if e.runDue() {
		t.Error("録らない予定のジョブを実行した")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("確定: %+v", j)
	}
	if got := e.skipKinds(); len(got) != 3 {
		t.Errorf("確定で通知した: %v", got)
	}
	if e.fake.playlistCount(suppin.start) != 0 {
		t.Error("録らなかったの放送回を入手した")
	}
	// 確定した後は、番組名が直っても戻さない。
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.sync()
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("確定した後: %+v", j)
	}
}

// 最初の試行の時刻に取り直した番組表で番組名が直っていれば、録音予定に戻してそのまま録る。
func TestSkipPlannedFixedAtFirstAttempt(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.putShows(suppinShow)
	e.sync()
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.clock.Set(afterBroadcast)
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone || j.ByPerson {
		t.Errorf("job = %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned, notify.KindUnskipped}) {
		t.Errorf("通知 = %v", got)
	}
}

// 録音予定のジョブが、最初の試行の時刻に取り直した番組表で食い違えば、確定した録らなかったにして通知する。
func TestSkippedAtFirstAttempt(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(suppinShow)
	e.sync()
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.clock.Set(afterBroadcast)
	if e.runDue() {
		t.Error("食い違った放送回を録った")
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("job = %+v", j)
	}
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipped}) {
		t.Errorf("通知 = %v", got)
	}
}

// 人が「録る」「録らない」を決めたジョブは、番組名では状態を変えず、通知もしない。
func TestByPersonKeepsState(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, special)
	e.putShows(flatShow, suppinShow)
	e.sync()
	// 録らない予定の特番を「録る」にすると、番組名が食い違ったままでも録音予定のまま。
	sj := e.job(suppinShow.Name)
	if err := e.store.RecordByPerson(e.ctx, sj.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	// 録音予定のふらっとを「録らない」にすると、番組名が合っていても録らない予定のまま。
	fj := e.job(flatShow.Name)
	if err := e.store.SkipByPerson(e.ctx, fj.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if j := e.job(suppinShow.Name); j.State != domain.StateWaiting || !j.ByPerson {
		t.Errorf("録ると決めた: %+v", j)
	}
	if j := e.job(flatShow.Name); j.State != domain.StateSkipped || !j.ByPerson || j.Settled {
		t.Errorf("録らないと決めた: %+v", j)
	}
	// 人の操作では通知しない（rajikura が録らない予定にした 1 通だけ）。
	if got := e.skipKinds(); !slices.Equal(got, []string{notify.KindSkipPlanned}) {
		t.Errorf("通知 = %v", got)
	}

	// 最初の試行の時刻に、録ると決めたジョブは番組名が食い違っていても録り、録らないと決めたジョブは確定する。
	e.clock.Set(afterBroadcast)
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	e.runDue()
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Errorf("録ると決めた: %+v", j)
	}
	if j := e.job(flatShow.Name); j.State != domain.StateSkipped || !j.Settled {
		t.Errorf("録らないと決めた: %+v", j)
	}
	if e.fake.playlistCount(flat1.start) != 0 {
		t.Error("録らないと決めた放送回を入手した")
	}
	if got := e.skipKinds(); len(got) != 1 {
		t.Errorf("確定で通知した: %v", got)
	}
}

// 人が決めたジョブにも、休止と番組の無効化は効く。
func TestByPersonFollowsCancelAndDisable(t *testing.T) {
	e := newEnv(t, beforeBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	e.sync()
	if err := e.store.SkipByPerson(e.ctx, e.job(suppinShow.Name).ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	fj := e.job(flatShow.Name)
	if err := e.store.SkipByPerson(e.ctx, fj.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.store.RecordByPerson(e.ctx, fj.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	// すっぴんしゃんが休止すると、人が録らないと決めたジョブも消える。
	e.fake.setPrograms("TBS", day, flat1, news, flat2)
	e.sync()
	if e.hasJob(suppinShow.Name) {
		t.Errorf("休止したのに残った: %+v", e.jobs())
	}
	// ふらっとを無効にすると、人が録ると決めたジョブも消える。
	e.setEnabled(flatShow.Name, false)
	e.sync()
	if e.hasJob(flatShow.Name) {
		t.Errorf("無効にしたのに残った: %+v", e.jobs())
	}
}
