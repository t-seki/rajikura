package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/store"
)

// Jobs はジョブの API が使う保存先（store.Store が満たす）。
type Jobs interface {
	ListJobsFromDay(ctx context.Context, day domain.BroadcastDay, states ...domain.State) ([]domain.Job, error)
	GetJob(ctx context.Context, id int64) (domain.Job, error)
	Attempts(ctx context.Context, jobID int64) ([]domain.Attempt, error)
	LatestAttemptsFromDay(ctx context.Context, day domain.BroadcastDay) (map[int64]domain.Attempt, error)
	ManualRetry(ctx context.Context, id int64, now time.Time) error
	SkipByPerson(ctx context.Context, id int64, now time.Time) error
	RecordByPerson(ctx context.Context, id int64, now time.Time) error
	Rerecord(ctx context.Context, id int64, now time.Time) error
}

var _ Jobs = (*store.Store)(nil)

// jobVisibleDays は、ジョブの一覧と番組表が読むジョブの範囲（放送日が今日の放送日の何日前以降か）。
// 最長の録音期限（30 日）＋ 90 日。どのジョブも放送日からこの日数だけ見える。
// ID で引く詳細と手動再試行は、範囲の外のジョブも扱う。
// 同じ値（120）を web/src/jobs/JobsPage.tsx の文言と docs/cli.md（`jobs` の説明）と docs/web-ui.md（ジョブの画面）と docs/api.md（`GET /api/jobs` の説明）にも書いている。値を変えるときはそろえる。
const jobVisibleDays = 120

// visibleFromDay は、一覧と番組表が読むジョブの最も古い放送日（今日の放送日の jobVisibleDays 日前。その日を含む）。
func (u *UI) visibleFromDay() domain.BroadcastDay {
	return domain.BroadcastDayOf(u.now()).AddDays(-jobVisibleDays)
}

// registerJobs はジョブの API を mux に載せる。
func (u *UI) registerJobs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", u.guard(u.authed(u.listJobs)))
	mux.HandleFunc("GET /api/jobs/{id}", u.guard(u.authed(u.getJob)))
	mux.HandleFunc("GET /api/jobs/{id}/audio", u.guard(u.authed(u.jobAudio)))
	mux.HandleFunc("POST /api/jobs/{id}/retry", u.guard(u.authed(u.retryJob)))
	mux.HandleFunc("POST /api/jobs/{id}/skip", u.guard(u.authed(u.skipJob)))
	mux.HandleFunc("POST /api/jobs/{id}/record", u.guard(u.authed(u.recordJob)))
	mux.HandleFunc("POST /api/jobs/{id}/rerecord", u.guard(u.authed(u.rerecordJob)))
}

type jobSummary struct {
	ID       int64  `json:"id"`
	ShowName string `json:"showName"`
	// Day は放送日（YYYY-MM-DD）。
	Day  string `json:"day"`
	Part int    `json:"part"`
	// State は状態（waiting, running, retrying, done, expired, skipped）。
	// 足したら直す: ジョブの状態の移り方
	State string `json:"state"`
	// Settled は、skipped のジョブが確定したか（false なら録らない予定、true なら録らなかった。ADR 0019）。skipped でなければ false。
	Settled bool `json:"settled"`
	// ByPerson は、人が録る・録らないを決めたジョブか（番組名では状態が変わらない）。
	ByPerson bool `json:"byPerson"`
	// Live は、ライブで録るジョブか（domain.Job.Live。放送回の 1 つでもタイムフリーの対象外なら true）。録る前のジョブでは、
	// その時点の番組表での見込みで、番組表の同期で変わりうる（展開が放送回を写し直すため）。拾った後は、その方式で録ったことを表す。
	Live bool `json:"live"`
	// Skippable は、いま「録らない」（POST /api/jobs/{id}/skip）を受け付けるか（試行がまだ 1 度も無い waiting。store.SkipByPerson と同じ）。
	Skippable bool `json:"skippable"`
	// Recordable は、いま「録る」（POST /api/jobs/{id}/record）を受け付けるか（録らない予定、または録音期限を過ぎていない
	// 録らなかった。store.RecordByPerson と同じ）。
	Recordable    bool      `json:"recordable"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	Deadline      time.Time `json:"deadline"`
	// Retryable は、いま手動再試行を受け付けるか（retrying・expired・確定した skipped で、録音期限を過ぎていないこと。
	// ただし、録れた分を残している（KeepUntil の前の）ライブで録るジョブは、録音期限を過ぎていても受け付ける。
	// store.ManualRetry と同じく domain.ManualRetryInTime で決める。録らない予定は受け付けない）。
	// 足したら直す: ジョブの状態の移り方
	Retryable bool `json:"retryable"`
	// Rerecordable は、いま録り直し（POST /api/jobs/{id}/rerecord）を受け付けるか（done のタイムフリーから録るジョブで、録音期限を
	// 過ぎていないこと。store.Rerecord と同じく domain.CheckRerecord で決める。ADR 0025）。
	// 足したら直す: ジョブの状態の移り方
	Rerecordable bool `json:"rerecordable"`
	// SaveRetryUntil は、ライブで録るジョブの録れた分の保存を自動でやり直している間（retrying で、最後の試行が保存か検証の段階で
	// 失敗し、打ち切りの前）だけ、その打ち切り（放送の終了から 24 時間。domain.Job.LiveSaveCutoff）。ほかのときは出さない。
	// 足したら直す: ジョブの状態の移り方
	SaveRetryUntil *time.Time `json:"saveRetryUntil,omitempty"`
	// KeepUntil は、保存できずに expired にしたライブで録るジョブの録れた分（作業用フォルダ）を残している間だけ、残す期限
	// （最後の試行の終わりから 30 日。domain.LiveKeepUntil）。ほかのときは出さない。
	// 足したら直す: ジョブの状態の移り方
	KeepUntil *time.Time `json:"keepUntil,omitempty"`
	// OutputPath は保存先（まだ保存していなければ空）。MissingCount は欠落の放送回の数。
	OutputPath   string `json:"outputPath"`
	MissingCount int    `json:"missingCount"`
	// LastFailedStage と LastError は、直近の試行が失敗していれば、その失敗した段階（fetch, verify, save）とエラー。
	// 試行が無い・直近の試行が成功していれば空。
	LastFailedStage string `json:"lastFailedStage"`
	LastError       string `json:"lastError"`
	// Stations は局と時刻の組。局ごとに 1 組で、その局の最初の放送回の開始から最後の放送回の終わりまで。開始の順。
	Stations []stationSpan `json:"stations"`
	// Progress は録音中のジョブの進捗（用語集）。DB の状態が running で、serve のメモリに進捗があるときだけ出す
	// （withProgress）。ほかのときはキーごと出さない。
	Progress *progressView `json:"progress,omitempty"`
	// Chapters は、保存したファイルにチャプターが入ったか（added: 入った、waiting: 待っている、not_added: 入らなかった。
	// domain.Job.Chapters。ADR 0027）。取りに行っていないジョブ（値なし）ではキーごと出さない。一覧にも詳細にも付く。
	Chapters string `json:"chapters,omitempty"`
}

// progressView は進捗の JSON。Stage は今の試行の段階（fetch, verify, save）。FetchedSeconds（録れた音の長さ）と
// TotalSeconds（ジョブの放送回の長さの合計）は入手の間だけ出し、検証・保存の段階と、保存だけのやり直しでは出さない。
type progressView struct {
	Stage          string   `json:"stage"`
	FetchedSeconds *float64 `json:"fetchedSeconds,omitempty"`
	TotalSeconds   *float64 `json:"totalSeconds,omitempty"`
}

// withProgress は、ジョブ j が DB で running で、serve のメモリに進捗があれば、s に進捗を付ける（メモリと DB の間の一瞬の
// ずれを画面に出さないため、両方がそろったときだけ）。進捗を読む口（UI.Recorder が Progresses を満たすもの）が無ければ付けない。
func (u *UI) withProgress(s *jobSummary, j domain.Job) {
	if j.State != domain.StateRunning {
		return
	}
	pr, ok := u.Recorder.(Progresses)
	if !ok {
		return
	}
	p, ok := pr.JobProgress(j.ID)
	if !ok {
		return
	}
	v := &progressView{Stage: string(p.Stage)}
	if p.Counted {
		fetched, total := p.Fetched.Seconds(), p.Total.Seconds()
		v.FetchedSeconds, v.TotalSeconds = &fetched, &total
	}
	s.Progress = v
}

type stationSpan struct {
	StationID string    `json:"stationId"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
}

// stationSpans は、ジョブの放送回を局ごとにまとめた組にする（開始の順）。
func stationSpans(as []domain.Airing) []stationSpan {
	out := []stationSpan{}
	idx := map[string]int{}
	for _, a := range as {
		i, ok := idx[a.StationID]
		if !ok {
			idx[a.StationID] = len(out)
			out = append(out, stationSpan{StationID: a.StationID, Start: a.Start, End: a.End})
			continue
		}
		if a.Start.Before(out[i].Start) {
			out[i].Start = a.Start
		}
		if a.End.After(out[i].End) {
			out[i].End = a.End
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out
}

type airingView struct {
	StationID   string    `json:"stationId"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Title       string    `json:"title"`
	Performer   string    `json:"performer"`
	Description string    `json:"description"`
	Missing     bool      `json:"missing"`
}

type attemptView struct {
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	// FailedStage は失敗した段階（fetch, verify, save）。成功なら空。
	FailedStage string `json:"failedStage"`
	Error       string `json:"error"`
	// DurationSeconds は ffprobe で測った長さ（秒）。記録が無ければ 0。
	DurationSeconds float64 `json:"durationSeconds"`
}

type jobDetail struct {
	jobSummary
	Airings    []airingView  `json:"airings"`
	AttemptLog []attemptView `json:"attemptLog"`
	// DurationSeconds は、成功した試行で測った長さ（秒）。記録が無ければ 0。
	DurationSeconds float64 `json:"durationSeconds"`
}

// summarize はジョブの一覧の 1 行にする。last はそのジョブの直近の試行（無ければ nil）で、失敗の段階とエラーに使う。
func summarize(j domain.Job, last *domain.Attempt, now time.Time) jobSummary {
	settled := j.State == domain.StateSkipped && j.Settled
	s := jobSummary{
		ID: j.ID, ShowName: j.ShowName, Day: j.Day.String(), Part: j.Part, State: string(j.State),
		Settled: settled, ByPerson: j.ByPerson, Live: j.Live(),
		Skippable:  j.State == domain.StateWaiting && j.Attempts == 0,
		Recordable: j.State == domain.StateSkipped && (!j.Settled || j.Deadline.After(now)),
		Attempts:   j.Attempts, NextAttemptAt: j.NextAttemptAt, Deadline: j.Deadline,
		Retryable:    (j.State == domain.StateRetrying || j.State == domain.StateExpired || settled) && domain.ManualRetryInTime(j, last, now),
		Rerecordable: domain.CheckRerecord(j, now) == nil,
		OutputPath:   j.OutputPath,
		MissingCount: len(j.Missing),
		Stations:     stationSpans(j.Airings),
		Chapters:     string(j.Chapters),
	}
	if last != nil && last.Error != "" {
		s.LastFailedStage = string(last.FailedStage)
		s.LastError = last.Error
	}
	if keep := domain.LiveKeepUntil(j, last); !keep.IsZero() {
		switch cutoff := j.LiveSaveCutoff(); {
		case j.State == domain.StateRetrying && cutoff.After(now):
			s.SaveRetryUntil = &cutoff
		case j.State == domain.StateExpired && keep.After(now):
			s.KeepUntil = &keep
		}
	}
	return s
}

// lastOf は、試行の履歴（古い順）の最後の 1 件。無ければ nil。
func lastOf(as []domain.Attempt) *domain.Attempt {
	if len(as) == 0 {
		return nil
	}
	return &as[len(as)-1]
}

// summarizeJob は、ジョブの試行の履歴を読んで 1 行にする（1 件の応答用。一覧は LatestAttemptsFromDay でまとめて読む）。
func (u *UI) summarizeJob(ctx context.Context, j domain.Job) (jobSummary, error) {
	as, err := u.Jobs.Attempts(ctx, j.ID)
	if err != nil {
		return jobSummary{}, err
	}
	s := summarize(j, lastOf(as), u.now())
	u.withProgress(&s, j)
	return s, nil
}

func (u *UI) listJobs(w http.ResponseWriter, r *http.Request) {
	var states []domain.State
	if q := r.URL.Query().Get("state"); q != "" {
		for _, s := range strings.Split(q, ",") {
			st, err := domain.ParseState(strings.TrimSpace(s))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "状態が読めない: " + s})
				return
			}
			states = append(states, st)
		}
	}
	from := u.visibleFromDay()
	jobs, err := u.Jobs.ListJobsFromDay(r.Context(), from, states...)
	if err != nil {
		u.internalError(w, "ジョブを一覧できない", err)
		return
	}
	// 新しい放送日が上。同じ放送日は ListJobs の並び（番組名・パート）のまま。
	sort.SliceStable(jobs, func(a, b int) bool {
		return jobs[a].Day.String() > jobs[b].Day.String()
	})
	latest, err := u.Jobs.LatestAttemptsFromDay(r.Context(), from)
	if err != nil {
		u.internalError(w, "試行の履歴を読めない", err)
		return
	}
	out := make([]jobSummary, 0, len(jobs))
	for _, j := range jobs {
		var last *domain.Attempt
		if a, ok := latest[j.ID]; ok {
			last = &a
		}
		s := summarize(j, last, u.now())
		u.withProgress(&s, j)
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (u *UI) internalError(w http.ResponseWriter, msg string, err error) {
	u.log().Error(msg, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
}

func jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ID が整数ではない"})
		return 0, false
	}
	return id, true
}

func (u *UI) getJob(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	j, err := u.Jobs.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ジョブが無い"})
		return
	}
	if err != nil {
		u.internalError(w, "ジョブを読めない", err)
		return
	}
	as, err := u.Jobs.Attempts(r.Context(), id)
	if err != nil {
		u.internalError(w, "試行の履歴を読めない", err)
		return
	}
	missing := map[string]bool{}
	for _, a := range j.Missing {
		missing[a.Key()] = true
	}
	d := jobDetail{jobSummary: summarize(j, lastOf(as), u.now()), Airings: []airingView{}, AttemptLog: []attemptView{}}
	u.withProgress(&d.jobSummary, j)
	for _, a := range j.Airings {
		d.Airings = append(d.Airings, airingView{
			StationID: a.StationID, Start: a.Start, End: a.End, Title: a.Title,
			Performer: a.Performer, Description: a.Description, Missing: missing[a.Key()],
		})
	}
	for _, a := range as {
		d.AttemptLog = append(d.AttemptLog, attemptView{
			StartedAt: a.StartedAt, EndedAt: a.EndedAt, FailedStage: string(a.FailedStage), Error: a.Error,
			DurationSeconds: a.ProbeDuration.Seconds(),
		})
		// 長さは、成功した試行の ffprobe の値のうち、いちばん新しいもの。
		if a.FailedStage == "" && a.Error == "" && a.ProbeDuration > 0 {
			d.DurationSeconds = a.ProbeDuration.Seconds()
		}
	}
	writeJSON(w, http.StatusOK, d)
}

// act は人のジョブの操作（手動再試行・録らない・録る・録り直し）の処理。拒まれた状態と録音期限を過ぎたジョブは 409、無いジョブは 404。
// refusal は、拒まれたわけ（err）から 409 の文言を作る。空を返したら、act の既定の文言にする。nil なら既定の文言だけを使う。
func (u *UI) act(w http.ResponseWriter, r *http.Request, do func(ctx context.Context, id int64) error, refusal func(err error) string) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	err := do(r.Context(), id)
	switch {
	case err == nil:
		j, gerr := u.Jobs.GetJob(r.Context(), id)
		if gerr != nil {
			u.internalError(w, "ジョブを読めない", gerr)
			return
		}
		s, serr := u.summarizeJob(r.Context(), j)
		if serr != nil {
			u.internalError(w, "試行の履歴を読めない", serr)
			return
		}
		writeJSON(w, http.StatusOK, s)
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ジョブが無い"})
	case refusal != nil && refusal(err) != "":
		writeJSON(w, http.StatusConflict, map[string]string{"error": refusal(err)})
	case errors.Is(err, store.ErrPastDeadline):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "録音期限を過ぎているので再試行できない"})
	case errors.Is(err, domain.ErrInvalidTransition):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "このジョブの状態では受け付けられない"})
	default:
		u.internalError(w, "ジョブを操作できない", err)
	}
}

func (u *UI) retryJob(w http.ResponseWriter, r *http.Request) {
	u.act(w, r, func(ctx context.Context, id int64) error { return u.wakeAfter(u.Jobs.ManualRetry(ctx, id, u.now())) }, nil)
}

// skipJob は「録らない」（ADR 0019）。試行がまだ 1 度も無い waiting のジョブを skipped にし、人が決めた印を付ける。通知はしない。
func (u *UI) skipJob(w http.ResponseWriter, r *http.Request) {
	u.act(w, r, func(ctx context.Context, id int64) error { return u.Jobs.SkipByPerson(ctx, id, u.now()) }, nil)
}

// recordJob は「録る」（ADR 0019）。録らない予定のジョブは waiting に戻して人が決めた印を付け、録らなかったのジョブは手動再試行と
// 同じく retrying にする。通知はしない。
// 足したら直す: ジョブの状態の移り方
func (u *UI) recordJob(w http.ResponseWriter, r *http.Request) {
	u.act(w, r, func(ctx context.Context, id int64) error { return u.wakeAfter(u.Jobs.RecordByPerson(ctx, id, u.now())) }, nil)
}

// rerecordJob は録り直し（ADR 0025）。done のタイムフリーから録るジョブで録音期限の内側のものを、手動再試行と同じく retrying にして
// 録音のループを起こす。拒むときは、done でない・ライブ録音・録音期限を過ぎた、で文言を分けて 409 にする。受け付けたことは通知しない。
// 足したら直す: ジョブの状態の移り方
func (u *UI) rerecordJob(w http.ResponseWriter, r *http.Request) {
	u.act(w, r, func(ctx context.Context, id int64) error { return u.wakeAfter(u.Jobs.Rerecord(ctx, id, u.now())) }, rerecordRefusal)
}

// rerecordRefusal は、録り直しを拒んだわけ（domain.CheckRerecord の返すエラー）を 409 の文言にする。
func rerecordRefusal(err error) string {
	switch {
	case errors.Is(err, domain.ErrRerecordLive):
		return "ライブ録音は録り直せない"
	case errors.Is(err, domain.ErrRerecordPastDeadline):
		return "録音期限を過ぎているので録り直せない"
	case errors.Is(err, domain.ErrInvalidTransition):
		return "この状態では録り直せない（録り直せるのは録音済みのジョブだけ）"
	}
	return ""
}

// wakeAfter は、ジョブの操作が成功した（err が nil）なら録音のループを起こし、err をそのまま返す。
func (u *UI) wakeAfter(err error) error {
	if err == nil && u.Recorder != nil {
		u.Recorder.WakeRecorder()
	}
	return err
}

// jobAudio は GET /api/jobs/{id}/audio。ジョブの output_path のファイルを返す（Range に 206 で、HEAD にも答える）。
// パスは受け取らない。出す前に、保存先が今の設定の library の下の .m4a であることを確かめる（audioPathAllowed）。
func (u *UI) jobAudio(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	j, err := u.Jobs.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ジョブが無い"})
		return
	}
	if err != nil {
		u.internalError(w, "ジョブを読めない", err)
		return
	}
	if j.OutputPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "まだ保存していない"})
		return
	}
	var library string
	if u.Settings != nil {
		library = u.Settings.Current().Library
	}
	if !audioPathAllowed(library, j.OutputPath) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "今の保存先（library）の外にあるので出さない"})
		return
	}
	f, err := os.Open(j.OutputPath)
	var fi fs.FileInfo
	if err == nil {
		defer f.Close()
		fi, err = f.Stat()
		if err == nil && fi.IsDir() {
			err = fmt.Errorf("%s はフォルダ", j.OutputPath)
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ファイルが無い（移された・消された）"})
		return
	}
	if err != nil {
		u.log().Error("録音のファイルを読めない", "job", id, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ファイルを読めない: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "audio/mp4")
	http.ServeContent(w, r, filepath.Base(j.OutputPath), fi.ModTime(), f)
}

// audioPathAllowed は、保存先 path が library（絶対パス）の下にあり、拡張子が .m4a（大文字と小文字を区別しない）かを返す。
// 字面で比べ、symlink は辿らない。Windows では filepath.Rel が大文字と小文字を区別しないで比べる。library が空なら false。
func audioPathAllowed(library, path string) bool {
	if library == "" || !strings.EqualFold(filepath.Ext(path), ".m4a") {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(library), filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
