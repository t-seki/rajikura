package app

import (
	"slices"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
)

// 今日より前の放送日での、開始日を持つ毎週の予約の当たり方（ADR 0018）。

// pastNow は放送日 10-07 を今日より前にする時刻（10-09 の昼。非会員の録音期限の内側）。
var pastNow = afterBroadcast.AddDate(0, 0, 2)

// withFrom は番組 r の毎週の予約に、開始日 from を入れたもの。
func withFrom(r domain.Show, from domain.BroadcastDay) domain.Show {
	r.Weekly = slices.Clone(r.Weekly)
	for i := range r.Weekly {
		r.Weekly[i] = r.Weekly[i].WithFrom(from)
	}
	return r
}

// syncIgnoringFuture は番組表を同期する。今日から先の番組表は無い（404）ので、一部の局・放送日は展開できない（エラーは見ない）。
func (e *env) syncIgnoringFuture() {
	e.t.Helper()
	_ = e.app.Sync(e.ctx)
}

func (e *env) skippedNotifications() int {
	e.t.Helper()
	n := 0
	for _, x := range e.notifications() {
		if x.Kind == notify.KindSkipped {
			n++
		}
	}
	return n
}

// 開始日を持つ毎週の予約は、開始日から今日の前日までの、録音期限の内側の放送回にジョブを作る（同期でも、
// 番組を変えた直後の展開でも）。開始日を持たない毎週の予約は作らない。
func TestWeeklyFromPastDayJobs(t *testing.T) {
	flat := withFrom(flatShow, day.AddDays(-7))

	t.Run("同期", func(t *testing.T) {
		e := newEnv(t, pastNow)
		e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
		e.putShows(flat, suppinShow)
		e.syncIgnoringFuture()
		j := e.job(flat.Name)
		if j.State != domain.StateWaiting || j.Day != day || len(j.Airings) != 2 {
			t.Errorf("job = %+v", j)
		}
		// 開始日を持たない毎週の予約には、今日より前の放送日のジョブができない。
		if e.hasJob(suppinShow.Name) {
			t.Errorf("開始日なしの毎週の予約にジョブができた: %+v", e.jobs())
		}
		// 繰り返しても作り直さない。
		e.syncIgnoringFuture()
		if jobs := e.jobs(); len(jobs) != 1 || jobs[0].ID != j.ID {
			t.Errorf("繰り返し: %+v", jobs)
		}
	})

	t.Run("番組を変えた直後の展開", func(t *testing.T) {
		e := newEnv(t, pastNow)
		e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
		e.putShows(flat, suppinShow)
		e.app.expandShows(e.ctx, flat, suppinShow) // 今日から先の番組表は 404 なのでエラーになる
		if j := e.job(flat.Name); j.State != domain.StateWaiting || j.Day != day {
			t.Errorf("job = %+v", j)
		}
		if e.hasJob(suppinShow.Name) {
			t.Errorf("開始日なしの毎週の予約にジョブができた: %+v", e.jobs())
		}
	})

	t.Run("無効から有効に戻す", func(t *testing.T) {
		e := newEnv(t, pastNow)
		e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
		disabled := flat
		disabled.Disabled = true
		e.putShows(disabled)
		e.syncIgnoringFuture()
		if jobs := e.jobs(); len(jobs) != 0 {
			t.Fatalf("無効な番組にジョブができた: %+v", jobs)
		}
		e.setEnabled(flat.Name, true)
		enabled, err := e.store.GetShow(e.ctx, flat.Name)
		if err != nil {
			t.Fatal(err)
		}
		e.app.expandShows(e.ctx, disabled, enabled) // 今日から先の番組表は 404 なのでエラーになる
		if j := e.job(flat.Name); j.State != domain.StateWaiting || j.Day != day {
			t.Errorf("有効に戻した後: job = %+v", j)
		}
	})

	t.Run("録音期限を過ぎた", func(t *testing.T) {
		// 10-07 の放送回の録音期限（非会員）は 10-15 05:00。
		e := newEnv(t, jst(2026, 10, 15, 6, 0))
		e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
		e.putShows(flat)
		e.syncIgnoringFuture()
		e.app.expandShows(e.ctx, flat)
		if jobs := e.jobs(); len(jobs) != 0 {
			t.Errorf("録音期限を過ぎた放送回にジョブができた: %+v", jobs)
		}
	})
}

// 今日より前の放送日で、番組名が番組の名前で始まらない放送回は録らなかった（skipped）のジョブになり、録りにいかず、
// 同期を繰り返しても作り直されず、通知は 1 回だけ出る。
func TestWeeklyFromPastDaySkipped(t *testing.T) {
	once := func(p prog) domain.Once { return domain.Once{StationID: "TBS", Start: p.start} }
	// 番組の名前「ふらっと（別名）」は、番組表の番組名「パンサー向井のふらっと (N)」と食い違う。
	other := withFrom(flatShow, day)
	other.Name = "ふらっと（別名）"
	// ニュースの枠（8:31）にも当たる番組。ふらっとの放送回は合い、ニュースは食い違う。
	partial := withFrom(flatShow, day)
	partial.Weekly = append(partial.Weekly, domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 8, Minute: 31}).WithFrom(day))
	noJoin := partial
	noJoin.Split = domain.SplitSeparate
	noCheck := other
	noCheck.CheckTitle = false

	type want struct {
		state   domain.State
		airings int
	}
	tests := []struct {
		name   string
		r      domain.Show
		onces  []domain.Once
		want   []want // ジョブ（パート番号の順）
		notify int
	}{
		{"連結する・全部食い違う", other, nil, []want{{domain.StateSkipped, 2}}, 1},
		{"連結する・一部食い違う", partial, nil, []want{{domain.StateWaiting, 2}}, 0},
		{"連結しない・一部食い違う", noJoin, nil, []want{{domain.StateWaiting, 1}, {domain.StateSkipped, 1}, {domain.StateWaiting, 1}}, 1},
		{"check_title が false", noCheck, nil, []want{{domain.StateWaiting, 2}}, 0},
		{"1 回の予約の放送回", other, []domain.Once{once(flat2)}, []want{{domain.StateWaiting, 1}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, pastNow)
			e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
			e.putShows(tt.r)
			for _, o := range tt.onces {
				if _, err := e.store.CreateOnce(e.ctx, tt.r.Name, o); err != nil {
					t.Fatal(err)
				}
			}
			check := func(label string) []domain.Job {
				t.Helper()
				jobs := e.jobs()
				slices.SortFunc(jobs, func(x, y domain.Job) int { return x.Part - y.Part })
				if len(jobs) != len(tt.want) {
					t.Fatalf("%s: jobs = %+v", label, jobs)
				}
				for i, w := range tt.want {
					if jobs[i].State != w.state || len(jobs[i].Airings) != w.airings {
						t.Errorf("%s: job %d = %s・放送回 %d, want %s・%d", label, i, jobs[i].State, len(jobs[i].Airings), w.state, w.airings)
					}
				}
				if n := e.skippedNotifications(); n != tt.notify {
					t.Errorf("%s: 録らなかったの通知 = %d, want %d", label, n, tt.notify)
				}
				return jobs
			}
			e.syncIgnoringFuture()
			first := check("1 回目の同期")
			e.syncIgnoringFuture()
			again := check("2 回目の同期")
			for i := range first {
				if first[i].ID != again[i].ID {
					t.Errorf("作り直した: %+v → %+v", first[i], again[i])
				}
			}
			// 録らなかったのジョブは録りにいかない（録音のループは録る放送回だけを入手する）。
			for e.runDue() {
			}
			for _, j := range e.jobs() {
				if j.State != domain.StateSkipped {
					continue
				}
				for _, a := range j.Airings {
					if n := e.fake.playlistCount(a.Start); n != 0 {
						t.Errorf("録らなかったの放送回 %s を入手しにいった（%d 回）", a.Title, n)
					}
				}
			}
		})
	}
}

// 録らなかったの通知の本文は、番組名の食い違いを書く。
func TestNotifySkippedMessage(t *testing.T) {
	e := newEnv(t, pastNow)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	r := domain.NewShow("すっぴん（別名）", domain.NewWeekly("TBS", time.Wednesday, domain.Clock{Hour: 11}).WithFrom(day))
	e.putShows(r)
	e.syncIgnoringFuture()
	var msg string
	for _, n := range e.notifications() {
		if n.Kind == notify.KindSkipped {
			msg = n.Message
		}
	}
	want := "【録らなかった】すっぴん（別名）・放送日 2026-10-07 は、放送済みの回で番組名が食い違うので録らなかった（録音期限 2026-10-15 05:00）。録るなら「録る」（手動で再試行）を選ぶ。" +
		"\n番組名の食い違い: 番組表の番組名 「イモトアヤコのすっぴんしゃん」 が番組の name で始まらない"
	if msg != want {
		t.Errorf("本文 = %q\nwant %q", msg, want)
	}
}
