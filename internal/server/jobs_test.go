package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

func newJobsUI(t *testing.T) (http.Handler, *store.Store, *http.Cookie, time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rajikura.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, domain.JST)
	ui := &UI{
		Sessions: st, Jobs: st, Passwords: passwords(string(hash)),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	return h, st, login(t, h), now
}

func mustDay(t *testing.T, s string) domain.BroadcastDay {
	t.Helper()
	d, err := domain.ParseBroadcastDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// addJob はジョブを作る。skipped は確定した録らなかったで作る（録らない予定は addJobOf で Settled を false にして作る）。
func addJob(t *testing.T, st *store.Store, name, day string, part int, state domain.State, now time.Time, airings ...domain.Airing) int64 {
	t.Helper()
	return addJobOf(t, st, domain.Job{
		ShowName: name, Day: mustDay(t, day), Part: part, State: state, Airings: airings,
		NextAttemptAt: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour), Settled: state == domain.StateSkipped,
	})
}

// addJobOf はジョブ j をそのまま作る。
func addJobOf(t *testing.T, st *store.Store, j domain.Job) int64 {
	t.Helper()
	id, err := st.InsertJob(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("JSON が読めない: %v\n%s", err, body)
	}
}

func TestJobsRequireLogin(t *testing.T) {
	h, _, _, _ := newJobsUI(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/jobs"}, {"GET", "/api/jobs/1"}, {"POST", "/api/jobs/1/retry"},
	} {
		if rec := do(h, c.method, c.path, ""); rec.Code != 401 {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestListJobs(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	a := addJob(t, st, "番組A", "2026-10-10", 0, domain.StateWaiting, now)
	b := addJob(t, st, "番組B", "2026-10-11", 2, domain.StateRetrying, now)
	c := addJob(t, st, "番組C", "2026-10-11", 0, domain.StateDone, now)
	type resp struct {
		Jobs []jobSummary `json:"jobs"`
	}
	ids := func(r resp) []int64 {
		var out []int64
		for _, j := range r.Jobs {
			out = append(out, j.ID)
		}
		return out
	}

	rec := do(h, "GET", "/api/jobs", "", ck)
	var all resp
	decode(t, rec.Body.Bytes(), &all)
	// 新しい放送日が上。同じ放送日は番組名の順。
	if got := ids(all); len(got) != 3 || got[0] != b || got[1] != c || got[2] != a {
		t.Errorf("並び = %v, want [%d %d %d]", got, b, c, a)
	}
	if j := all.Jobs[0]; j.ShowName != "番組B" || j.Day != "2026-10-11" || j.Part != 2 || j.State != "retrying" || !j.Retryable {
		t.Errorf("先頭 = %+v", j)
	}
	if j := all.Jobs[1]; j.Retryable {
		t.Errorf("done に操作の印: %+v", j)
	}

	var filtered resp
	decode(t, do(h, "GET", "/api/jobs?state=waiting,retrying", "", ck).Body.Bytes(), &filtered)
	if got := ids(filtered); len(got) != 2 || got[0] != b || got[1] != a {
		t.Errorf("絞り込み = %v", got)
	}
	var none resp
	rec = do(h, "GET", "/api/jobs?state=expired", "", ck)
	decode(t, rec.Body.Bytes(), &none)
	if rec.Code != 200 || none.Jobs == nil || len(none.Jobs) != 0 {
		t.Errorf("0 件 = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/jobs?state=bogus", "", ck); rec.Code != 400 {
		t.Errorf("不明な状態 = %d", rec.Code)
	}
}

// 一覧の各ジョブに、保存先・欠落の数・直近の試行の失敗の段階とエラーを付ける（rajikura jobs が表示する）。
func TestListJobsSummaryFields(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	ctx := context.Background()
	a1 := domain.Airing{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: "番組A #1"}
	a2 := domain.Airing{StationID: "TBS", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: "番組A #2"}
	saved, err := st.InsertJob(ctx, domain.Job{
		ShowName: "番組A", Day: mustDay(t, "2026-10-12"), State: domain.StateDone, OutputPath: "/library/番組A/a.m4a",
		Airings: []domain.Airing{a1, a2}, Missing: []domain.Airing{a2}, Deadline: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	st.RecordAttempt(ctx, domain.Attempt{JobID: saved, StartedAt: now, EndedAt: now.Add(time.Minute), FailedStage: domain.StageFetch, Error: "boom"})
	st.RecordAttempt(ctx, domain.Attempt{JobID: saved, StartedAt: now.Add(time.Hour), EndedAt: now.Add(2 * time.Hour)})
	failed := addJob(t, st, "番組B", "2026-10-11", 0, domain.StateRetrying, now)
	st.RecordAttempt(ctx, domain.Attempt{JobID: failed, StartedAt: now, EndedAt: now.Add(time.Minute), FailedStage: domain.StageVerify, Error: "短い"})
	none := addJob(t, st, "番組C", "2026-10-10", 0, domain.StateWaiting, now)

	var res struct {
		Jobs []jobSummary `json:"jobs"`
	}
	decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &res)
	byID := map[int64]jobSummary{}
	for _, j := range res.Jobs {
		byID[j.ID] = j
	}
	// 直近の試行が成功なら、前の試行のエラーは出さない。
	if j := byID[saved]; j.OutputPath != "/library/番組A/a.m4a" || j.MissingCount != 1 || j.LastFailedStage != "" || j.LastError != "" {
		t.Errorf("保存したジョブ = %+v", j)
	}
	if j := byID[failed]; j.OutputPath != "" || j.MissingCount != 0 || j.LastFailedStage != "verify" || j.LastError != "短い" {
		t.Errorf("失敗したジョブ = %+v", j)
	}
	if j := byID[none]; j.LastFailedStage != "" || j.LastError != "" {
		t.Errorf("試行の無いジョブ = %+v", j)
	}
}

// 一覧と詳細の各ジョブに live を常に出す。タイムフリーの対象外の放送回を 1 つでも含むジョブで true、含まないジョブで false。
func TestJobsLive(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	tf := domain.Airing{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: "タイムフリー"}
	ng := domain.Airing{StationID: "TBS", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: "対象外"}
	ng.TimeFree.InNG = 2
	live := addJob(t, st, "番組A", "2026-10-12", 0, domain.StateWaiting, now, tf, ng)
	notLive := addJob(t, st, "番組B", "2026-10-12", 0, domain.StateDone, now, tf)

	var res struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &res)
	got := map[string]string{}
	for _, j := range res.Jobs {
		got[string(j["id"])] = string(j["live"])
	}
	want := map[string]string{strconv.FormatInt(live, 10): "true", strconv.FormatInt(notLive, 10): "false"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("一覧の live（id %s） = %q, want %q", id, got[id], w)
		}
	}

	for id, w := range want {
		var d map[string]json.RawMessage
		decode(t, do(h, "GET", "/api/jobs/"+id, "", ck).Body.Bytes(), &d)
		if string(d["live"]) != w {
			t.Errorf("詳細の live（id %s） = %s, want %s", id, d["live"], w)
		}
	}
}

func TestJobDetail(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	a1 := domain.Airing{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: "番組A #1", Performer: "出演者"}
	a2 := domain.Airing{StationID: "TBS", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: "番組A #2"}
	id, err := st.InsertJob(context.Background(), domain.Job{
		ShowName: "番組A", Day: mustDay(t, "2026-10-12"), State: domain.StateRunning,
		Airings: []domain.Airing{a1, a2}, Missing: []domain.Airing{a2}, Deadline: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: now, EndedAt: now.Add(time.Minute), FailedStage: domain.StageFetch, Error: "boom"})
	st.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: now.Add(time.Hour), EndedAt: now.Add(2 * time.Hour), ProbeDuration: 90 * time.Minute})

	rec := do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck)
	if rec.Code != 200 {
		t.Fatalf("詳細 = %d %s", rec.Code, rec.Body)
	}
	var d jobDetail
	decode(t, rec.Body.Bytes(), &d)
	if len(d.Airings) != 2 || d.Airings[0].Missing || !d.Airings[1].Missing || d.Airings[0].Title != "番組A #1" || d.Airings[0].StationID != "TBS" {
		t.Errorf("放送回 = %+v", d.Airings)
	}
	if len(d.AttemptLog) != 2 || d.AttemptLog[0].FailedStage != "fetch" || d.AttemptLog[0].Error != "boom" {
		t.Errorf("試行の履歴 = %+v", d.AttemptLog)
	}
	if d.DurationSeconds != 5400 {
		t.Errorf("長さ = %v, want 5400", d.DurationSeconds)
	}

	// 試行の記録が無ければ、長さは 0 で、履歴は空の配列。
	id2 := addJob(t, st, "番組B", "2026-10-12", 0, domain.StateWaiting, now)
	rec = do(h, "GET", "/api/jobs/"+strconv.FormatInt(id2, 10), "", ck)
	var raw map[string]json.RawMessage
	decode(t, rec.Body.Bytes(), &raw)
	if string(raw["attemptLog"]) != "[]" || string(raw["durationSeconds"]) != "0" {
		t.Errorf("記録が無いとき: %s", rec.Body)
	}

	if rec := do(h, "GET", "/api/jobs/9999", "", ck); rec.Code != 404 {
		t.Errorf("無いジョブ = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/jobs/abc", "", ck); rec.Code != 400 {
		t.Errorf("整数でない ID = %d", rec.Code)
	}
}

// ジョブの詳細と一覧は、チャプターの値（入った・待っている・入らなかった）を chapters に出し、値なしならキーを出さない
// （ADR 0027。一覧にも出す）。
func TestJobDetailChapters(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	for _, tt := range []struct {
		chapters domain.Chapters
		want     string
	}{
		{domain.ChaptersAdded, `"added"`},
		{domain.ChaptersWaiting, `"waiting"`},
		{domain.ChaptersNotAdded, `"not_added"`},
		{domain.ChaptersNone, ""},
	} {
		id, err := st.InsertJob(context.Background(), domain.Job{
			ShowName: "番組" + string(tt.chapters), Day: mustDay(t, "2026-10-12"), State: domain.StateDone, OutputPath: "/lib/x.m4a",
			Airings:  []domain.Airing{{StationID: "TBS", Start: now, End: now.Add(time.Hour), Title: "番組"}},
			Deadline: now.Add(time.Hour), Chapters: tt.chapters,
		})
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &raw)
		got, ok := raw["chapters"]
		if tt.want == "" {
			if ok {
				t.Errorf("値なしなのに chapters = %s", got)
			}
		} else if string(got) != tt.want {
			t.Errorf("chapters = %s, want %s", got, tt.want)
		}
	}
	var list struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &list)
	if len(list.Jobs) != 4 {
		t.Fatalf("一覧 = %d 件", len(list.Jobs))
	}
	want := map[string]string{
		"番組" + string(domain.ChaptersAdded):    `"added"`,
		"番組" + string(domain.ChaptersWaiting):  `"waiting"`,
		"番組" + string(domain.ChaptersNotAdded): `"not_added"`,
		"番組": "",
	}
	for _, j := range list.Jobs {
		var name string
		decode(t, j["showName"], &name)
		got, ok := j["chapters"]
		switch w := want[name]; {
		case w == "" && ok:
			t.Errorf("一覧: %s は値なしなのに chapters = %s", name, got)
		case w != "" && string(got) != w:
			t.Errorf("一覧: %s の chapters = %s, want %s", name, got, w)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("一覧に無いジョブ: %v", want)
	}
}

func TestRetry(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	ctx := context.Background()
	path := func(id int64, op string) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/" + op }
	state := func(id int64) domain.State {
		j, err := st.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j.State
	}

	expired := addJob(t, st, "E", "2026-10-11", 0, domain.StateExpired, now)
	rec := do(h, "POST", path(expired, "retry"), "", ck)
	var sum jobSummary
	decode(t, rec.Body.Bytes(), &sum)
	if rec.Code != 200 || sum.State != "retrying" || !sum.NextAttemptAt.Equal(now) || sum.Attempts != 0 {
		t.Errorf("expired の再試行 = %d %s", rec.Code, rec.Body)
	}
	if state(expired) != domain.StateRetrying {
		t.Errorf("再試行の後の状態 = %s", state(expired))
	}
	// 録らなかった（確定した skipped）のジョブも再試行にできる。録らない予定（確定の前の skipped）は 409（「録る」を使う）。
	skipped := addJob(t, st, "S", "2026-10-11", 0, domain.StateSkipped, now)
	if rec := do(h, "POST", path(skipped, "retry"), "", ck); rec.Code != 200 || state(skipped) != domain.StateRetrying {
		t.Errorf("skipped の再試行 = %d %s", rec.Code, rec.Body)
	}
	planned := addJobOf(t, st, domain.Job{ShowName: "P", Day: mustDay(t, "2026-10-11"), State: domain.StateSkipped,
		NextAttemptAt: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour)})
	if rec := do(h, "POST", path(planned, "retry"), "", ck); rec.Code != 409 || state(planned) != domain.StateSkipped {
		t.Errorf("録らない予定の再試行 = %d %s", rec.Code, rec.Body)
	}

	// 対象外の状態は 409 で、状態を変えない。
	for _, st0 := range []domain.State{domain.StateWaiting, domain.StateRunning, domain.StateDone} {
		id := addJob(t, st, "R"+string(st0), "2026-10-11", 0, st0, now)
		if rec := do(h, "POST", path(id, "retry"), "", ck); rec.Code != 409 {
			t.Errorf("%s の再試行 = %d, want 409", st0, rec.Code)
		}
		if state(id) != st0 {
			t.Errorf("%s の再試行を拒んだのに状態が %s", st0, state(id))
		}
	}
	// 録音期限を過ぎたジョブの再試行は 409。
	for _, s0 := range []domain.State{domain.StateExpired, domain.StateSkipped} {
		late, err := st.InsertJob(ctx, domain.Job{ShowName: "L" + string(s0), Day: mustDay(t, "2026-10-01"), State: s0,
			Settled: s0 == domain.StateSkipped, Deadline: now.Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if rec := do(h, "POST", path(late, "retry"), "", ck); rec.Code != 409 || state(late) != s0 {
			t.Errorf("%s の録音期限切れの再試行 = %d", s0, rec.Code)
		}
	}

	if rec := do(h, "POST", path(9999, "retry"), "", ck); rec.Code != 404 {
		t.Errorf("無いジョブの retry = %d", rec.Code)
	}
	if rec := do(h, "GET", path(expired, "retry"), "", ck); rec.Code == 200 {
		t.Errorf("GET で再試行できた")
	}
}

func TestRetryableRespectsDeadline(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	for _, s0 := range []domain.State{domain.StateExpired, domain.StateRetrying, domain.StateSkipped} {
		// 録音期限がちょうど now のジョブは ManualRetry が拒むので、retryable は false。
		id, err := st.InsertJob(context.Background(), domain.Job{ShowName: "D" + string(s0), Day: mustDay(t, "2026-10-01"), State: s0,
			Settled: s0 == domain.StateSkipped, Deadline: now})
		if err != nil {
			t.Fatal(err)
		}
		var d jobDetail
		decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &d)
		if d.Retryable {
			t.Errorf("%s の期限切れが retryable", s0)
		}
	}
	live := addJob(t, st, "live", "2026-10-12", 0, domain.StateExpired, now)
	var d jobDetail
	decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(live, 10), "", ck).Body.Bytes(), &d)
	if !d.Retryable {
		t.Errorf("期限内の expired が retryable でない")
	}
	liveSkipped := addJob(t, st, "live-skipped", "2026-10-12", 0, domain.StateSkipped, now)
	decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(liveSkipped, 10), "", ck).Body.Bytes(), &d)
	if !d.Retryable {
		t.Errorf("期限内の skipped が retryable でない")
	}
}

// ライブで録るジョブの録れた分の保存が失敗したときは、録音期限を過ぎていても、残している間（保存か検証の段階の失敗から 30 日）は
// retryable で、詳細・一覧・操作の応答に、保存をやり直している間は打ち切り（saveRetryUntil）、残している間は残す期限
// （keepUntil）が付く。30 日を過ぎたものと入手の段階の失敗は、今のまま retryable でなく、時刻も付かない。
func TestLiveKeptRetryableAndTimes(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	ctx := context.Background()
	end := now.Add(-2 * time.Hour) // 放送の終了。録音期限（5 分後）は過ぎている
	ar := domain.Airing{StationID: "TBS", Start: end.Add(-time.Hour), End: end, Title: "ライブ"}
	ar.TimeFree.InNG = 2
	add := func(name string, state domain.State, stage domain.Stage, ended time.Time) int64 {
		t.Helper()
		id := addJobOf(t, st, domain.Job{ShowName: name, Day: mustDay(t, "2026-10-12"), State: state, Airings: []domain.Airing{ar},
			NextAttemptAt: now.Add(time.Hour), Deadline: end.Add(domain.LiveDeadlineAfter), Attempts: 1})
		if _, err := st.RecordAttempt(ctx, domain.Attempt{JobID: id, StartedAt: ended.Add(-time.Minute), EndedAt: ended, FailedStage: stage, Error: "boom"}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kept := add("kept", domain.StateExpired, domain.StageSave, now.Add(-time.Hour))
	saving := add("saving", domain.StateRetrying, domain.StageVerify, now.Add(-time.Hour))
	old := add("old", domain.StateExpired, domain.StageSave, now.Add(-31*24*time.Hour))
	fetch := add("fetch", domain.StateExpired, domain.StageFetch, now.Add(-time.Hour))
	keep := now.Add(-time.Hour).Add(domain.LiveKeepFor)
	cutoff := end.Add(24 * time.Hour)

	check := func(where string, s jobSummary, retryable bool, saveUntil, keepUntil time.Time) {
		t.Helper()
		if s.Retryable != retryable {
			t.Errorf("%s %s: retryable = %v, want %v", where, s.ShowName, s.Retryable, retryable)
		}
		for _, f := range []struct {
			name string
			got  *time.Time
			want time.Time
		}{{"saveRetryUntil", s.SaveRetryUntil, saveUntil}, {"keepUntil", s.KeepUntil, keepUntil}} {
			switch {
			case f.want.IsZero() && f.got != nil:
				t.Errorf("%s %s: %s = %v, want 無し", where, s.ShowName, f.name, *f.got)
			case !f.want.IsZero() && (f.got == nil || !f.got.Equal(f.want)):
				t.Errorf("%s %s: %s = %v, want %v", where, s.ShowName, f.name, f.got, f.want)
			}
		}
	}
	want := map[int64]struct {
		retryable       bool
		saveUntil, keep time.Time
	}{
		kept:   {true, time.Time{}, keep},
		saving: {true, cutoff, time.Time{}},
		old:    {false, time.Time{}, time.Time{}},
		fetch:  {false, time.Time{}, time.Time{}},
	}
	for id, w := range want {
		var d jobDetail
		decode(t, do(h, "GET", "/api/jobs/"+strconv.FormatInt(id, 10), "", ck).Body.Bytes(), &d)
		check("詳細", d.jobSummary, w.retryable, w.saveUntil, w.keep)
	}
	var all struct{ Jobs []jobSummary }
	decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &all)
	if len(all.Jobs) != len(want) {
		t.Fatalf("一覧 = %+v", all.Jobs)
	}
	for _, s := range all.Jobs {
		w := want[s.ID]
		check("一覧", s, w.retryable, w.saveUntil, w.keep)
	}
	// raw JSON でも、無いときはフィールドを出さない。
	if body := do(h, "GET", "/api/jobs/"+strconv.FormatInt(fetch, 10), "", ck).Body.String(); strings.Contains(body, "keepUntil") || strings.Contains(body, "saveRetryUntil") {
		t.Errorf("入手の段階の失敗の詳細に時刻が付いた: %s", body)
	}

	path := func(id int64) string { return "/api/jobs/" + strconv.FormatInt(id, 10) + "/retry" }
	if rec := do(h, "POST", path(old), "", ck); rec.Code != 409 {
		t.Errorf("30 日を過ぎたものの再試行 = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", path(fetch), "", ck); rec.Code != 409 {
		t.Errorf("入手の段階の失敗の再試行 = %d %s", rec.Code, rec.Body)
	}
	rec := do(h, "POST", path(kept), "", ck)
	var sum jobSummary
	decode(t, rec.Body.Bytes(), &sum)
	if rec.Code != 200 || sum.State != "retrying" || !sum.NextAttemptAt.Equal(now) {
		t.Fatalf("残している間の再試行 = %d %s", rec.Code, rec.Body)
	}
	// 手動再試行の後（retrying）は、打ち切りの前なので保存をやり直している間の時刻が付く。
	check("操作の応答", sum, true, cutoff, time.Time{})
}

// countingJobs は、試行の読み出しの呼び出し回数を数える。
type countingJobs struct {
	Jobs
	attempts, latest int
}

func (c *countingJobs) Attempts(ctx context.Context, id int64) ([]domain.Attempt, error) {
	c.attempts++
	return c.Jobs.Attempts(ctx, id)
}

func (c *countingJobs) LatestAttemptsFromDay(ctx context.Context, day domain.BroadcastDay) (map[int64]domain.Attempt, error) {
	c.latest++
	return c.Jobs.LatestAttemptsFromDay(ctx, day)
}

func TestListJobsReadsAttemptsOnceForAnyJobCount(t *testing.T) {
	_, st, _, now := newJobsUI(t)
	for i := 0; i < 5; i++ {
		id := addJob(t, st, "番組"+strconv.Itoa(i), "2026-10-11", i, domain.StateRetrying, now)
		st.RecordAttempt(context.Background(), domain.Attempt{JobID: id, StartedAt: now, EndedAt: now.Add(time.Minute), FailedStage: domain.StageFetch, Error: "boom"})
	}
	c := &countingJobs{Jobs: st}
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ui := &UI{
		Sessions: st, Jobs: c, Passwords: passwords(string(hash)),
		Files: fstest.MapFS{"index.html": {Data: []byte("x")}},
		Now:   func() time.Time { return now },
	}
	h := Handler(Health{LastPoll: time.Now}, ui)
	ck := login(t, h)
	var res struct {
		Jobs []jobSummary `json:"jobs"`
	}
	decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &res)
	if len(res.Jobs) != 5 || res.Jobs[0].LastError != "boom" {
		t.Fatalf("jobs = %+v", res.Jobs)
	}
	if c.attempts != 0 || c.latest != 1 {
		t.Errorf("Attempts %d 回, LatestAttemptsFromDay %d 回; want 0, 1", c.attempts, c.latest)
	}
}

// 一覧は局と時刻の組を返す。同じ局の放送回は 1 組にまとめ、2 局のジョブは開始の順に 2 組になる。
func TestListJobsStations(t *testing.T) {
	h, st, ck, now := newJobsUI(t)
	at := func(station string, from, to int) domain.Airing {
		return domain.Airing{StationID: station, Start: now.Add(time.Duration(from) * time.Hour), End: now.Add(time.Duration(to) * time.Hour)}
	}
	one := addJob(t, st, "一局", "2026-10-12", 0, domain.StateWaiting, now, at("TBS", 1, 3))
	same := addJob(t, st, "同じ局", "2026-10-12", 0, domain.StateWaiting, now, at("TBS", 3, 4), at("TBS", 1, 3))
	two := addJob(t, st, "二局", "2026-10-12", 0, domain.StateWaiting, now, at("QRR", 3, 5), at("TBS", 1, 3))
	var res struct {
		Jobs []jobSummary `json:"jobs"`
	}
	decode(t, do(h, "GET", "/api/jobs", "", ck).Body.Bytes(), &res)
	byID := map[int64]jobSummary{}
	for _, j := range res.Jobs {
		byID[j.ID] = j
	}
	want := map[int64][]stationSpan{
		one:  {{"TBS", now.Add(1 * time.Hour), now.Add(3 * time.Hour)}},
		same: {{"TBS", now.Add(1 * time.Hour), now.Add(4 * time.Hour)}},
		two:  {{"TBS", now.Add(1 * time.Hour), now.Add(3 * time.Hour)}, {"QRR", now.Add(3 * time.Hour), now.Add(5 * time.Hour)}},
	}
	for id, w := range want {
		g := byID[id].Stations
		if len(g) != len(w) {
			t.Fatalf("job %d の stations = %+v, want %+v", id, g, w)
		}
		for i := range w {
			if g[i].StationID != w[i].StationID || !g[i].Start.Equal(w[i].Start) || !g[i].End.Equal(w[i].End) {
				t.Errorf("job %d の stations[%d] = %+v, want %+v", id, i, g[i], w[i])
			}
		}
	}
}

// 一覧は放送日が今日の放送日の 120 日前以降のジョブだけを返し、ID で引く詳細は範囲の外のジョブも返す。
func TestListJobsVisibleRange(t *testing.T) {
	h, st, ck, now := newJobsUI(t)                                       // 今日は 2026-10-12
	edge := addJob(t, st, "境", "2026-06-14", 0, domain.StateDone, now)   // 120 日前
	old := addJob(t, st, "古", "2026-06-13", 0, domain.StateExpired, now) // 121 日前
	fut := addJob(t, st, "先", "2026-11-01", 0, domain.StateWaiting, now)
	st.RecordAttempt(context.Background(), domain.Attempt{JobID: old, StartedAt: now, EndedAt: now.Add(time.Minute), FailedStage: domain.StageFetch, Error: "boom"})

	for _, path := range []string{"/api/jobs", "/api/jobs?state=done,expired,waiting"} {
		var res struct {
			Jobs []jobSummary `json:"jobs"`
		}
		decode(t, do(h, "GET", path, "", ck).Body.Bytes(), &res)
		if len(res.Jobs) != 2 || res.Jobs[0].ID != fut || res.Jobs[1].ID != edge {
			t.Errorf("%s = %+v, want [%d %d]", path, res.Jobs, fut, edge)
		}
	}
	rec := do(h, "GET", "/api/jobs/"+strconv.FormatInt(old, 10), "", ck)
	var d jobSummary
	decode(t, rec.Body.Bytes(), &d)
	if rec.Code != 200 || d.ID != old || d.LastError != "boom" {
		t.Errorf("範囲の外の詳細 = %d %s", rec.Code, rec.Body)
	}
}
