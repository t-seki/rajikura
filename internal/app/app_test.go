package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/media"
)

// 放送日 2026-10-07（水）の TBS の番組表。ふらっとは 2 つの放送回に分かれ、間にニュースが挟まる。
var (
	day      = domain.NewBroadcastDay(2026, time.October, 7)
	flat1    = prog{id: "P1", start: jst(2026, 10, 7, 8, 30), end: jst(2026, 10, 7, 8, 31), title: "パンサー向井のふらっと (1)", pfm: "パンサー向井"}
	news     = prog{id: "N1", start: jst(2026, 10, 7, 8, 31), end: jst(2026, 10, 7, 10, 0), title: "ニュース"}
	flat2    = prog{id: "P1", start: jst(2026, 10, 7, 10, 0), end: jst(2026, 10, 7, 10, 1), title: "パンサー向井のふらっと (2)", pfm: "パンサー向井"}
	suppin   = prog{id: "S1", start: jst(2026, 10, 7, 11, 0), end: jst(2026, 10, 7, 11, 1), title: "イモトアヤコのすっぴんしゃん", pfm: "イモトアヤコ"}
	flatShow = domain.Show{Name: "パンサー向井のふらっと", Split: domain.SplitJoin, CheckTitle: true, Weekly: []domain.Weekly{
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 8, Minute: 30}),
		domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 10}),
	}}
	suppinShow = domain.NewShow("イモトアヤコのすっぴんしゃん", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 11}))
	// afterBroadcast は放送が終わってしばらく後（どのジョブも最初の試行の時刻を過ぎている）。
	afterBroadcast = jst(2026, 10, 7, 12, 0)
)

func newFlowEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.putShows(flatShow, suppinShow)
	return e
}

func TestFlow(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	jobs := e.jobs()
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v", jobs)
	}
	fj := e.job(flatShow.Name)
	if len(fj.Airings) != 2 || fj.State != domain.StateWaiting {
		t.Fatalf("ふらっと = %+v", fj)
	}
	// 最初の試行の時刻は、最後の放送回の終了 + max(tf_max_delay 90 秒, 150 秒) + 2 分。
	if want := flat2.end.Add(150*time.Second + 2*time.Minute); !fj.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v", fj.NextAttemptAt, want)
	}

	if !e.runDue() || !e.runDue() {
		t.Fatal("2 件とも実行されない")
	}
	if e.runDue() {
		t.Fatal("3 件目が実行された")
	}

	fj = e.job(flatShow.Name)
	wantPath := filepath.Join(e.app.Library, "TBSラジオ", flatShow.Name, flatShow.Name+" 2026-10-07.m4a")
	if fj.State != domain.StateDone || fj.OutputPath != wantPath || len(fj.Missing) != 0 {
		t.Fatalf("ふらっと = %+v", fj)
	}
	b, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	// 連結した中身は、2 つの放送回のセグメント（各 12 個）を開始時刻の順につないだもの。間のニュースは含まない。
	if got := strings.Count(string(b), "["); got != 24 || !strings.HasPrefix(string(b), "[20261007083000.aac]") ||
		!strings.HasSuffix(string(b), "[20261007100055.aac]") {
		t.Errorf("中身 = %q", b)
	}
	as := e.attempts(fj.ID)
	if len(as) != 1 || as[0].FailedStage != "" || as[0].ExtinfTotal != 2*time.Minute || as[0].ProbeDuration != 2*time.Minute {
		t.Errorf("attempts = %+v", as)
	}
	if _, err := os.Stat(e.app.jobDir(fj.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("作業フォルダが残っている: %v", err)
	}

	sj := e.job(suppinShow.Name)
	if sj.State != domain.StateDone || filepath.Base(sj.OutputPath) != suppinShow.Name+" 2026-10-07.m4a" {
		t.Errorf("すっぴんしゃん = %+v", sj)
	}

	// タグ。ふらっとは分割番組なので track は MMDD×10、すっぴんしゃんは MMDD。
	// 放送回の番組名はどちらも name と同じ（ふらっとは末尾の印を落とすと同じ）なので、title は放送日だけ。
	if len(e.media.tags) != 2 {
		t.Fatalf("tags = %+v", e.media.tags)
	}
	ft, st := e.media.tags[0], e.media.tags[1]
	if ft.Track != 10070 || ft.AlbumArtist != "TBSラジオ" || ft.Album != flatShow.Name || ft.Artist != "パンサー向井" || ft.Disc != 2026 || ft.Title != "2026-10-07" {
		t.Errorf("ふらっとのタグ = %+v", ft)
	}
	if st.Track != 1007 || st.Title != "2026-10-07" {
		t.Errorf("すっぴんしゃんのタグ = %+v", st)
	}
}

// 連結しない番組は、同じ放送日の放送回をパートごとのファイル（-1・-2）にする。track は、分割番組でつながない番組なら
// MMDD×10+N、分割番組でない番組ならどのパートも MMDD（ADR 0003）。
func TestFlowPartsTrack(t *testing.T) {
	for _, tt := range []struct {
		split  domain.SplitMode
		tracks []int
	}{
		{domain.SplitSeparate, []int{10071, 10072}},
		{domain.SplitNone, []int{1007, 1007}},
	} {
		t.Run(tt.split.String(), func(t *testing.T) {
			e := newFlowEnv(t)
			r := flatShow
			r.Split = tt.split
			e.putShows(r)
			e.store.DeleteShow(e.ctx, suppinShow.Name)
			if err := e.app.Startup(e.ctx); err != nil {
				t.Fatal(err)
			}
			if !e.runDue() || !e.runDue() || e.runDue() {
				t.Fatal("2 件だけ実行されない")
			}
			var bases []string
			for _, j := range e.jobs() {
				if j.State != domain.StateDone {
					t.Errorf("job = %+v", j)
				}
				bases = append(bases, filepath.Base(j.OutputPath))
			}
			slices.Sort(bases)
			if want := []string{r.Name + " 2026-10-07-1.m4a", r.Name + " 2026-10-07-2.m4a"}; !slices.Equal(bases, want) {
				t.Errorf("ファイル = %v, want %v", bases, want)
			}
			var tracks []int
			for _, tg := range e.media.tags {
				tracks = append(tracks, tg.Track)
			}
			slices.Sort(tracks)
			if !slices.Equal(tracks, tt.tracks) {
				t.Errorf("track = %v, want %v", tracks, tt.tracks)
			}
		})
	}
}

func TestRetryThenSuccess(t *testing.T) {
	e := newFlowEnv(t)
	e.putShows(flatShow)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.streamFail = true })
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying || !j.NextAttemptAt.Equal(afterBroadcast.Add(15*time.Minute)) {
		t.Fatalf("job = %+v", j)
	}
	as := e.attempts(j.ID)
	if len(as) != 1 || as[0].FailedStage != domain.StageFetch || as[0].Error == "" {
		t.Fatalf("attempts = %+v", as)
	}
	// 次の試行の時刻の前には実行しない。
	if e.runDue() {
		t.Fatal("次の試行の時刻の前に実行した")
	}

	e.fake.set(func(f *fakeRadiko) { f.streamFail = false })
	e.clock.Set(afterBroadcast.Add(15 * time.Minute))
	if !e.runDue() {
		t.Fatal("再試行されない")
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone || j.Attempts != 2 {
		t.Fatalf("job = %+v", j)
	}
}

func TestNotAvailableExpires(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateExpired {
		t.Fatalf("job = %+v", j)
	}
	if _, err := os.Stat(e.app.jobDir(j.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("作業フォルダが残っている: %v", err)
	}
}

// 前の試行で取り終えた放送回は取り直さない。
func TestReuseFetchedAiring(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying || e.fake.playlistCount(flat1.start) != 1 {
		t.Fatalf("job = %+v, playlist = %d", j, e.fake.playlistCount(flat1.start))
	}
	if _, _, ok := doneAiring(e.app.jobDir(j.ID), j.Airings[0]); !ok {
		t.Fatal("1 つ目の放送回が取り終えた扱いになっていない")
	}
	// 途中のファイルは残さない。
	if _, err := os.Stat(airingBase(e.app.jobDir(j.ID), j.Airings[1]) + ".aac.part"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("途中のファイルが残っている: %v", err)
	}

	e.fake.set(func(f *fakeRadiko) { delete(f.failSeek, flat2.start.Format(radikoTime)) })
	e.clock.Set(j.NextAttemptAt)
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if n := e.fake.playlistCount(flat1.start); n != 1 {
		t.Errorf("1 つ目の放送回を取り直した（%d 回）", n)
	}
}

// 局一覧が一度も取れていないときは、保存の段階で失敗して取った放送回を残し、次の試行で局名のフォルダに保存する。
func TestStationUnknownWaitsThenSaves(t *testing.T) {
	e := newFlowEnv(t)
	e.fake.set(func(f *fakeRadiko) { f.stationsFail = true })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	if as := e.attempts(j.ID); len(as) != 1 || as[0].FailedStage != domain.StageSave {
		t.Errorf("attempts = %+v", as)
	}
	if _, err := os.Stat(filepath.Join(e.app.Library, "TBS")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("局 ID のフォルダができた: %v", err)
	}
	if _, _, ok := doneAiring(e.app.jobDir(j.ID), j.Airings[0]); !ok {
		t.Error("取った放送回が残っていない")
	}

	e.fake.set(func(f *fakeRadiko) { f.stationsFail = false })
	e.clock.Set(j.NextAttemptAt)
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if n := e.fake.playlistCount(flat1.start); n != 1 {
		t.Errorf("取った放送回を取り直した（%d 回）", n)
	}
	want := filepath.Join(e.app.Library, "TBSラジオ", flatShow.Name, flatShow.Name+" 2026-10-07.m4a")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("局名のフォルダに保存されていない: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.app.Library, "TBS")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("局 ID のフォルダができた: %v", err)
	}
}

// 録音期限を過ぎても局一覧が取れないときは、局 ID のフォルダに保存して done にする。
func TestStationUnknownSavesByIDAtDeadline(t *testing.T) {
	e := newFlowEnv(t)
	e.fake.set(func(f *fakeRadiko) { f.stationsFail = true })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	want := filepath.Join(e.app.Library, "TBS", flatShow.Name, flatShow.Name+" 2026-10-07.m4a")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("局 ID のフォルダに保存されていない: %v", err)
	}
}

// 局一覧は取れたが局が載っていないときは、待たずに 1 回目の試行で局 ID のフォルダに保存する。
func TestStationNotListedSavesByID(t *testing.T) {
	e := newFlowEnv(t)
	e.fake.set(func(f *fakeRadiko) { f.stationsOmitTBS = true })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if as := e.attempts(j.ID); len(as) != 1 || as[0].FailedStage != "" {
		t.Errorf("attempts = %+v", as)
	}
	want := filepath.Join(e.app.Library, "TBS", flatShow.Name, flatShow.Name+" 2026-10-07.m4a")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("局 ID のフォルダに保存されていない: %v", err)
	}
}

func TestVerifyFailure(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.media.offset = 11 * time.Second
	e.runDue()
	j := e.job(flatShow.Name)
	as := e.attempts(j.ID)
	if j.State != domain.StateRetrying || len(as) != 1 || as[0].FailedStage != domain.StageVerify || as[0].ProbeDuration != 2*time.Minute+11*time.Second {
		t.Fatalf("job = %+v, attempts = %+v", j, as)
	}
	// 保存先には何も残さない（partial も消す）。
	entries, _ := os.ReadDir(filepath.Join(e.app.Library, "TBSラジオ", flatShow.Name))
	if len(entries) != 0 {
		t.Errorf("保存先に残っている: %v", entries)
	}
}

// 録音期限を過ぎたジョブは、取れた分を保存して done にし、欠落を記録する。
func TestDeadlinePartial(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	e.clock.Set(j.Deadline)
	before := e.fake.playlistCount(flat2.start)
	for e.runDue() {
	}
	j = e.job(flatShow.Name)
	if j.State != domain.StateDone || len(j.Missing) != 1 || !j.Missing[0].Start.Equal(flat2.start) {
		t.Fatalf("job = %+v", j)
	}
	if e.fake.playlistCount(flat2.start) != before {
		t.Error("録音期限を過ぎてから入手しにいった")
	}
	b, err := os.ReadFile(j.OutputPath)
	if err != nil || strings.Count(string(b), "[") != 12 {
		t.Errorf("中身 = %q, %v", b, err)
	}
	// すっぴんしゃんは一度も試行しないまま録音期限を迎えたので、何も取れておらず期限切れ。
	if s := e.job(suppinShow.Name); s.State != domain.StateExpired {
		t.Errorf("すっぴんしゃん = %+v", s)
	}
}

// 試行回数の上限に達しても、取れた分があれば録音期限まで待ってから保存する。
func TestNoNextAttemptKeepsFetched(t *testing.T) {
	e := newFlowEnv(t)
	e.app.MaxAttemptsFree = 1
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying || !j.NextAttemptAt.Equal(j.Deadline) {
		t.Fatalf("job = %+v", j)
	}
	// 何も取れていないジョブは期限切れ。
	e.fake.set(func(f *fakeRadiko) { f.streamFail = true })
	e.runDue()
	if s := e.job(suppinShow.Name); s.State != domain.StateExpired {
		t.Fatalf("すっぴんしゃん = %+v", s)
	}
	e.clock.Set(j.Deadline)
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone || len(j.Missing) != 1 {
		t.Fatalf("job = %+v", j)
	}
}

func TestDeadlineNothingExpires(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	j := e.job(flatShow.Name)
	e.clock.Set(j.Deadline.Add(time.Hour))
	for e.runDue() {
	}
	for _, j := range e.jobs() {
		if j.State != domain.StateExpired {
			t.Errorf("job = %+v", j)
		}
		if as := e.attempts(j.ID); len(as) != 1 || as[0].FailedStage != domain.StageFetch {
			t.Errorf("attempts = %+v", as)
		}
	}
}

// 最初の試行の直前に番組表を取り直し、休止したジョブは実行しない。
func TestRefreshBeforeFirstAttempt(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	// ふらっとは休止、すっぴんしゃんは 1 分延長。
	longer := suppin
	longer.end = suppin.end.Add(time.Minute)
	e.fake.setPrograms("TBS", day, news, longer)
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	jobs := e.jobs()
	if len(jobs) != 1 || jobs[0].ShowName != suppinShow.Name || jobs[0].State != domain.StateDone {
		t.Fatalf("jobs = %+v", jobs)
	}
	if !jobs[0].Airings[0].End.Equal(longer.end) {
		t.Errorf("放送回の終了時刻が更新されていない: %+v", jobs[0].Airings[0])
	}
	if e.fake.playlistCount(flat1.start) != 0 {
		t.Error("休止したふらっとを入手した")
	}
}

// 番組表を取れなかったときは展開せず、既存のジョブを消さない。
func TestSyncKeepsJobsWhenGuideFails(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Sync(e.ctx); err != nil {
		t.Fatal(err)
	}
	// どの局・放送日の番組表も取れない（空も含む）ときは、同期の失敗としてエラーを返す。
	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	if err := e.app.Sync(e.ctx); err == nil {
		t.Fatal("番組表を 1 つも取れないのにエラーにならない")
	}
	e.fake.setPrograms("TBS", day)
	e.fake.set(func(f *fakeRadiko) { f.guideFail = false })
	if err := e.app.Sync(e.ctx); err == nil {
		t.Fatal("番組表が空でも取れなくてもエラーにならない")
	}
	if n := len(e.jobs()); n != 2 {
		t.Fatalf("jobs = %d", n)
	}
	// 取り直しにも失敗したら、今の放送回のまま進む。
	e.fake.set(func(f *fakeRadiko) { f.guideFail = true })
	if !e.runDue() || e.job(flatShow.Name).State != domain.StateDone {
		t.Fatalf("jobs = %+v", e.jobs())
	}
}

// 過去の放送日は、録音期限の内側にある waiting のジョブの放送日だけ同期する。
func TestSyncDays(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Sync(e.ctx); err != nil {
		t.Fatal(err)
	}
	// 3 日後に止まっていたサーバーが戻った。番組表の取り直しで、10-07 の放送回が更新される。
	e.clock.Set(afterBroadcast.AddDate(0, 0, 3))
	longer := suppin
	longer.end = suppin.end.Add(time.Minute)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, longer)
	if err := e.app.Sync(e.ctx); err != nil {
		t.Fatal(err)
	}
	if j := e.job(suppinShow.Name); !j.Airings[0].End.Equal(longer.end) {
		t.Errorf("過去の放送日を同期していない: %+v", j.Airings[0])
	}
}

func TestStartupRecoversAndCleans(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Sync(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.store.ClaimDue(e.ctx, afterBroadcast); err != nil || !ok {
		t.Fatal(ok, err)
	}
	partial := filepath.Join(e.app.Library, "TBSラジオ", "x", "x 2026-10-07.123.partial.m4a")
	os.MkdirAll(filepath.Dir(partial), 0o755)
	os.WriteFile(partial, nil, 0o644)

	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if j := e.job(flatShow.Name); j.State != domain.StateRetrying {
		t.Errorf("回復していない: %+v", j)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial が残っている: %v", err)
	}
}

// 起動の回復の最中に ctx が切られても、エラーにしない（serve の終了コードが 0 になる）。
func TestStartupRecoverCanceled(t *testing.T) {
	e := newFlowEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	if err := e.app.startupRecover(ctx); err != nil {
		t.Fatalf("止めたことによる失敗がエラーになった: %v", err)
	}
}

// ctx が切られていないときの Store.Recover の失敗は、エラーで止まる。
func TestStartupRecoverFailure(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.app.startupRecover(e.ctx); err == nil {
		t.Fatal("Recover の失敗がエラーにならない")
	}
}

// ctx を取り消したら、実行中のジョブは running のまま残す（次の起動時の回復で retrying に戻る）。
func TestCancelLeavesRunning(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			cancel()
			<-r.Context().Done()
		}
	})
	if _, err := e.app.RunDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateRunning || len(e.attempts(j.ID)) != 0 {
		t.Fatalf("job = %+v", j)
	}
}

// LastPoll は、Serve を始める前は零値で、始めたら周の始まりの時刻になる。
func TestLastPoll(t *testing.T) {
	e := newFlowEnv(t)
	if !e.app.LastPoll().IsZero() {
		t.Fatalf("Serve の前の LastPoll = %v", e.app.LastPoll())
	}
	e.app.PollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	e.stopExpander()
	go func() { done <- e.app.Serve(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for e.app.LastPoll().IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("LastPoll が記録されない")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestServeStopsOnCancel(t *testing.T) {
	e := newFlowEnv(t)
	e.app.PollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	e.stopExpander()
	go func() { done <- e.app.Serve(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		jobs, err := e.store.ListJobs(e.ctx, domain.StateDone)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("2 件とも done にならない: %+v", e.jobs())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve が終わらない")
	}
}

// 本物の ffmpeg / ffprobe で、同期 → 展開 → 実行 → 保存を 1 本通す。見つからなければ飛ばす。
func TestFlowWithFFmpeg(t *testing.T) {
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s が無い", n)
		}
	}
	tools := media.New("", "")
	seg := filepath.Join(t.TempDir(), "seg.aac")
	out, err := exec.Command(tools.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=d=5", "-c:a", "aac", "-f", "adts", seg).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}

	e := newFlowEnv(t)
	e.app.Media = tools
	e.fake.set(func(f *fakeRadiko) { f.segment = data })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	for e.runDue() {
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone {
		t.Fatalf("job = %+v, attempts = %+v", j, e.attempts(j.ID))
	}
	d, err := tools.Duration(e.ctx, j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if (d - 2*time.Minute).Abs() > 10*time.Second {
		t.Errorf("長さ = %v", d)
	}
	tag, err := exec.Command(tools.FFprobe, "-v", "error", "-show_entries", "format_tags=track,album_artist", "-of", "default=nw=1", j.OutputPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	if s := string(tag); !strings.Contains(s, "track=10070") || !strings.Contains(s, "album_artist=TBSラジオ") {
		t.Errorf("tags = %q", s)
	}
	if s := e.job(suppinShow.Name); s.State != domain.StateDone {
		t.Errorf("すっぴんしゃん = %+v", s)
	}
}

// muxer = "builtin"（media.Builtin）で、同期 → 展開 → 実行 → 保存を 1 本通す。ffmpeg / ffprobe を使わない
// ことを確かめるため、PATH を空にする。builtin は音声をデコードしないので、入力は中身の無い合成の ADTS で足りる。
func TestFlowWithBuiltin(t *testing.T) {
	t.Setenv("PATH", "")
	e := newFlowEnv(t)
	e.app.Media = media.Builtin{}
	// 5 秒ぶんのセグメント（48 kHz の AAC-LC、1 フレーム 1024 サンプル。偽の radiko の #EXTINF と同じ長さ）。
	// 本物の radiko と同じく、セグメントの先頭に ID3v2.4 のタグを付ける（入手でつながると、ストリームの途中にも来る）。
	id3 := append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0x3f}, make([]byte, 0x3f)...)
	e.fake.set(func(f *fakeRadiko) { f.segment = append(id3, synthADTS(234, 3)...) })
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	for e.runDue() {
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone {
		t.Fatalf("job = %+v, attempts = %+v", j, e.attempts(j.ID))
	}
	d, err := (media.Builtin{}).Duration(e.ctx, j.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if (d - 2*time.Minute).Abs() > 10*time.Second {
		t.Errorf("長さ = %v", d)
	}
	if s := e.job(suppinShow.Name); s.State != domain.StateDone {
		t.Errorf("すっぴんしゃん = %+v", s)
	}
}

// synthADTS は、中身が 0 の AAC-LC・ステレオの ADTS のフレームを n 個作る（freqIndex は sampling_frequency_index）。
func synthADTS(n int, freqIndex byte) []byte {
	const payload = 16
	frameLen := 7 + payload
	var b []byte
	for range n {
		b = append(b,
			0xFF, 0xF1, // sync word、MPEG-4、CRC なし
			1<<6|freqIndex<<2|0, // profile = AAC-LC（1）、周波数、チャンネル構成の上位ビット（2ch なので 0）
			2<<6|byte(frameLen>>11),
			byte(frameLen>>3),
			byte(frameLen&7)<<5|0x1F,
			0xFC, // buffer fullness の残りと、raw_data_block が 1 つ
		)
		b = append(b, make([]byte, payload)...)
	}
	return b
}

// 検証に落ちたら、その試行で使った放送回を捨て、次の試行で取り直す。
func TestVerifyFailureRefetches(t *testing.T) {
	e := newFlowEnv(t)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.media.offset = 11 * time.Second
	e.runDue()
	j := e.job(flatShow.Name)
	if j.State != domain.StateRetrying {
		t.Fatalf("job = %+v", j)
	}
	for _, ar := range j.Airings {
		if _, _, ok := doneAiring(e.app.jobDir(j.ID), ar); ok {
			t.Errorf("検証に落ちた放送回 %v が取り終えた扱いのまま", ar.Start)
		}
	}

	e.media.offset = 0
	e.clock.Set(j.NextAttemptAt)
	for e.runDue() {
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	for _, p := range []prog{flat1, flat2} {
		if n := e.fake.playlistCount(p.start); n != 2 {
			t.Errorf("%v の入手 = %d 回、want 2（取り直す）", p.start, n)
		}
	}
}

// 保存の直後に ctx が取り消されても done にし、再起動しても二重に保存しない。
func TestCancelAfterSave(t *testing.T) {
	countFiles := func(t *testing.T, e *env) int {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(e.app.Library, "TBSラジオ", flatShow.Name))
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	check := func(t *testing.T, e *env, ctx context.Context, cancel context.CancelFunc, wantMissing int) {
		t.Helper()
		// Duration は library.Store の書き込みの中で呼ばれ、その後に確定のリネームが続く。
		e.media.onDuration = cancel
		if _, err := e.app.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() == nil {
			t.Fatal("取り消されていない")
		}
		j := e.job(flatShow.Name)
		if j.State != domain.StateDone || j.OutputPath == "" || len(j.Missing) != wantMissing {
			t.Fatalf("job = %+v", j)
		}
		if as := e.attempts(j.ID); len(as) == 0 || as[len(as)-1].FailedStage != "" {
			t.Errorf("attempts = %+v", as)
		}
		if _, err := os.Stat(e.app.jobDir(j.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("作業フォルダが残っている: %v", err)
		}

		// 再起動しても、もう 1 本保存しない。
		e.media.onDuration = nil
		if err := e.app.Startup(e.ctx); err != nil {
			t.Fatal(err)
		}
		for e.runDue() {
		}
		if n := countFiles(t, e); n != 1 {
			t.Errorf("保存先のファイル = %d 本", n)
		}
	}

	t.Run("通常の試行", func(t *testing.T) {
		e := newFlowEnv(t)
		e.store.DeleteShow(e.ctx, suppinShow.Name)
		if err := e.app.Startup(e.ctx); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(e.ctx)
		defer cancel()
		check(t, e, ctx, cancel, 0)
	})

	t.Run("録音期限を過ぎたジョブ", func(t *testing.T) {
		e := newFlowEnv(t)
		e.store.DeleteShow(e.ctx, suppinShow.Name)
		if err := e.app.Startup(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.fake.set(func(f *fakeRadiko) { f.failSeek[flat2.start.Format(radikoTime)] = true })
		e.runDue()
		j := e.job(flatShow.Name)
		if j.State != domain.StateRetrying {
			t.Fatalf("job = %+v", j)
		}
		e.clock.Set(j.Deadline)
		ctx, cancel := context.WithCancel(e.ctx)
		defer cancel()
		check(t, e, ctx, cancel, 1)
	})
}
