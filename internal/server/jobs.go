package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// ChapterWrites は、ジョブの保存したファイルへのチャプターの書き込みの履歴（ADR 0033）。
	ChapterWrites(ctx context.Context, jobID int64) ([]domain.ChapterWrite, error)
	LatestAttemptsFromDay(ctx context.Context, day domain.BroadcastDay) (map[int64]domain.Attempt, error)
	ManualRetry(ctx context.Context, id int64, now time.Time) error
	SkipByPerson(ctx context.Context, id int64, now time.Time) error
	RecordByPerson(ctx context.Context, id int64, now time.Time) error
	Rerecord(ctx context.Context, id int64, m domain.Membership, own map[string]bool, now time.Time) error
	// RequestChapterRedo と RequestChapterRedoAll は、チャプターの入れ直しの頼みを DB に書く（ADR 0033。書き込みは録音のループ）。
	RequestChapterRedo(ctx context.Context, id int64, now time.Time) error
	RequestChapterRedoAll(ctx context.Context, now time.Time) (int, error)
	// RequestOutputMove は保存先の移しの頼みを DB に書き、OutputMoves は保存先の移しの履歴を読む（ADR 0039。移すのは録音のループ）。
	RequestOutputMove(ctx context.Context, id int64, show string, now time.Time) error
	OutputMoves(ctx context.Context, jobID int64) ([]domain.OutputMove, error)
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
	mux.HandleFunc("POST /api/jobs/{id}/chapters", u.guard(u.authed(u.redoJobChapters)))
	mux.HandleFunc("POST /api/jobs/chapters", u.guard(u.authed(u.redoAllChapters)))
	mux.HandleFunc("POST /api/jobs/{id}/move", u.guard(u.authed(u.moveJob)))
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
	// FinishedAt は、ジョブが済んだ時刻（RFC 3339。ほかの日時の欄と同じ形）。画面の既定の並びが「済んだもの」を並べるのに使う。
	// 録音済み（done）は最後の試行の終わり、期限切れ（expired）は最後の試行の終わり（試行が無ければ録音期限）、
	// 確定した録らなかった（skipped）は次の試行の時刻（確定の時刻を持つ列は無いので、これで代える）。
	// 「これから」のジョブ（waiting・running・retrying・確定の前の skipped）と、試行が無い done は空文字。
	FinishedAt string `json:"finishedAt"`
	// Retryable は、いま手動再試行を受け付けるか（retrying・expired・確定した skipped で、録音期限を過ぎていないこと。
	// ただし、録れた分を残している（KeepUntil の前の）ライブで録るジョブは、録音期限を過ぎていても受け付ける。
	// store.ManualRetry と同じく domain.ManualRetryInTime で決める。録らない予定は受け付けない）。
	// 足したら直す: ジョブの状態の移り方
	Retryable bool `json:"retryable"`
	// Rerecordable は、いま録り直し（POST /api/jobs/{id}/rerecord）を受け付けるか（done のタイムフリーから録るジョブで、録音期限を
	// 過ぎていないこと。録音期限は今の会員種別と今の自分のエリアの局一覧で求める。store.Rerecord と同じく domain.CheckRerecord で決める。
	// エリアフリー・ダブルプランで自分のエリアの局一覧を取れないときは、done のタイムフリーから録るジョブを false にする。ADR 0036）。
	// 足したら直す: ジョブの状態の移り方
	Rerecordable bool `json:"rerecordable"`
	// ChaptersRedoable は、いまチャプターの入れ直し（POST /api/jobs/{id}/chapters）を受け付けるか（タイムフリーから録った done の
	// ジョブ。store.RequestChapterRedo と同じく domain.CheckChapterRedo で決める。ADR 0033）。
	ChaptersRedoable bool `json:"chaptersRedoable"`
	// ChaptersRedoRequested は、チャプターの入れ直しを頼んで、録音のループがまだ書き込んでいない間だけ true。
	ChaptersRedoRequested bool `json:"chaptersRedoRequested"`
	// Movable は、いま保存先の移し（POST /api/jobs/{id}/move）を受け付けるか（保存先があり、録音中でないジョブ。
	// store.RequestOutputMove と同じく domain.CheckOutputMove で決める。ADR 0039）。
	Movable bool `json:"movable"`
	// MoveRequested は、保存先の移しを頼んで（番組の名前の変更か、人の頼み）、録音のループがまだ移していない間だけ true。
	MoveRequested bool `json:"moveRequested"`
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

// chapterWriteView は、チャプターの書き込みの履歴の 1 行（domain.ChapterWrite。ADR 0033）。
type chapterWriteView struct {
	At time.Time `json:"at"`
	// By は誰が（refetch: チャプターの取り直し、redo: チャプターの入れ直し）。
	By string `json:"by"`
	// Result は結果（written: 書き込んだ、not_fetched: 取れなかったので書かなかった、failed: 書き込めなかった）。Count は
	// 書き込んだチャプターの件数（written のときだけ。ほかは 0）、Error は書き込めなかった理由（failed のときだけ。ほかは空）。
	Result string `json:"result"`
	Count  int    `json:"count"`
	Error  string `json:"error"`
}

// outputMoveView は、保存先の移しの履歴の 1 行（domain.OutputMove。ADR 0039）。
type outputMoveView struct {
	At time.Time `json:"at"`
	// From は移す前の保存先、To は移し先（保存先のパスが番組の名前の形でなく、移し先を組めなかったときは空。album_rewritten の
	// ときは From と同じ）。
	From string `json:"from"`
	To   string `json:"to"`
	// Result は結果（moved: 移した、album_rewritten: 保存先は同じなので、アルバムの名前だけ書き換えた（From と To は同じ）、
	// target_exists: 移し先に同じ名前のファイルがあるので移さなかった、no_file: 保存先にファイルが無いので移さなかった、
	// not_show_path: 保存先のパスが番組の名前の形でないので移さなかった、failed: 移せなかった）。Error は移せなかった理由（failed の
	// ときだけ。ほかは空）。
	Result string `json:"result"`
	Error  string `json:"error"`
}

type jobDetail struct {
	jobSummary
	Airings    []airingView  `json:"airings"`
	AttemptLog []attemptView `json:"attemptLog"`
	// ChapterWrites は、保存したファイルへのチャプターの書き込みの履歴（書き込んだ順。試行の履歴とは別。無ければ空の配列）。
	ChapterWrites []chapterWriteView `json:"chapterWrites"`
	// OutputMoves は、保存先の移しの履歴（移した順。無ければ空の配列）。
	OutputMoves []outputMoveView `json:"outputMoves"`
	// DurationSeconds は、成功した試行で測った長さ（秒）。記録が無ければ 0。
	DurationSeconds float64 `json:"durationSeconds"`
}

// rerecordArea は、録り直しを受け付けるかを決めるための、今の自分のエリアの局一覧（ADR 0036）。
// Own は局 ID の集合（nil なら放送回の印のまま。局一覧を使わないときと、取れないとき）、Unknown は局一覧が要るのに取れなかったこと。
type rerecordArea struct {
	Own     map[string]bool
	Unknown bool
}

// rerecordOwn は、録り直しの録音期限を求める、今の自分のエリアの局一覧（今日の放送日の、AreaGuide.AreaAirings の局の集合）を返す。
// 会員種別がエリアフリー・ダブルプランでないときと、AreaGuide が無いとき（テストの組み立て）は、局一覧を使わないので取らずに nil。
// 取れない・空ならエラー。
func (u *UI) rerecordOwn(ctx context.Context) (map[string]bool, error) {
	if !u.membership().AreaFree() || u.AreaGuide == nil {
		return nil, nil
	}
	ss, err := u.AreaGuide.AreaAirings(ctx, domain.BroadcastDayOf(u.now()))
	if err != nil {
		return nil, fmt.Errorf("自分のエリアの局一覧を取れない: %w", err)
	}
	if len(ss) == 0 {
		return nil, errors.New("自分のエリアの局一覧が空")
	}
	own := make(map[string]bool, len(ss))
	for _, s := range ss {
		own[s.ID] = true
	}
	return own, nil
}

// rerecordAreaFor は、応答に載せるジョブ jobs の録り直しの可否に使う局一覧を、リクエストごとに 1 回だけ取る。取るのは、会員種別が
// エリアフリー・ダブルプランで、jobs に done のタイムフリーから録るジョブがあるときだけ。取れなければ Unknown にして、Warn を 1 行出す。
func (u *UI) rerecordAreaFor(ctx context.Context, jobs ...domain.Job) rerecordArea {
	need := false
	for _, j := range jobs {
		if j.State == domain.StateDone && !j.Live() {
			need = true
			break
		}
	}
	if !need {
		return rerecordArea{}
	}
	own, err := u.rerecordOwn(ctx)
	if err != nil {
		u.log().Warn("自分のエリアの局一覧を取れないので、録音済みのジョブを録り直せないことにする", "err", err)
		return rerecordArea{Unknown: true}
	}
	return rerecordArea{Own: own}
}

// summarize はジョブの一覧の 1 行にする。area は録り直しの可否に使う局一覧（rerecordAreaFor）。last はそのジョブの直近の試行（無ければ nil）で、失敗の段階とエラー（LastFailedStage・LastError）、済んだ時刻（finishedAt）、
// 手動再試行の可否（domain.ManualRetryInTime）、録れた分を残す期限（domain.LiveKeepUntil）に使う。
func summarize(j domain.Job, last *domain.Attempt, m domain.Membership, area rerecordArea, now time.Time) jobSummary {
	settled := j.State == domain.StateSkipped && j.Settled
	s := jobSummary{
		ID: j.ID, ShowName: j.ShowName, Day: j.Day.String(), Part: j.Part, State: string(j.State),
		Settled: settled, ByPerson: j.ByPerson, Live: j.Live(),
		Skippable:  j.State == domain.StateWaiting && j.Attempts == 0,
		Recordable: j.State == domain.StateSkipped && (!j.Settled || j.Deadline.After(now)),
		Attempts:   j.Attempts, NextAttemptAt: j.NextAttemptAt, Deadline: j.Deadline,
		FinishedAt:            finishedAt(j, last),
		Retryable:             (j.State == domain.StateRetrying || j.State == domain.StateExpired || settled) && domain.ManualRetryInTime(j, last, now),
		Rerecordable:          !area.Unknown && domain.CheckRerecord(j, m, area.Own, now) == nil,
		ChaptersRedoable:      domain.CheckChapterRedo(j) == nil,
		ChaptersRedoRequested: !j.ChapterRedoAt.IsZero() && domain.CheckChapterRedo(j) == nil,
		Movable:               domain.CheckOutputMove(j) == nil,
		MoveRequested:         !j.MoveRequestedAt.IsZero() && j.OutputPath != "",
		OutputPath:            j.OutputPath,
		MissingCount:          len(j.Missing),
		Stations:              stationSpans(j.Airings),
		Chapters:              string(j.Chapters),
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

// finishedAt は、ジョブが済んだ時刻（jobSummary.FinishedAt）。済んでいない・決められないときは空文字。
func finishedAt(j domain.Job, last *domain.Attempt) string {
	var t time.Time
	switch {
	case j.State == domain.StateDone:
		if last != nil {
			t = last.EndedAt
		}
	case j.State == domain.StateExpired:
		if last != nil {
			t = last.EndedAt
		} else {
			t = j.Deadline
		}
	case j.State == domain.StateSkipped && j.Settled:
		t = j.NextAttemptAt
	}
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
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
	s := summarize(j, lastOf(as), u.membership(), u.rerecordAreaFor(ctx, j), u.now())
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
	area := u.rerecordAreaFor(r.Context(), jobs...)
	out := make([]jobSummary, 0, len(jobs))
	for _, j := range jobs {
		var last *domain.Attempt
		if a, ok := latest[j.ID]; ok {
			last = &a
		}
		s := summarize(j, last, u.membership(), area, u.now())
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
	cws, err := u.Jobs.ChapterWrites(r.Context(), id)
	if err != nil {
		u.internalError(w, "チャプターの書き込みの履歴を読めない", err)
		return
	}
	mvs, err := u.Jobs.OutputMoves(r.Context(), id)
	if err != nil {
		u.internalError(w, "保存先の移しの履歴を読めない", err)
		return
	}
	d := jobDetail{jobSummary: summarize(j, lastOf(as), u.membership(), u.rerecordAreaFor(r.Context(), j), u.now()), Airings: []airingView{}, AttemptLog: []attemptView{},
		ChapterWrites: []chapterWriteView{}, OutputMoves: []outputMoveView{}}
	for _, c := range cws {
		d.ChapterWrites = append(d.ChapterWrites, chapterWriteView{At: c.At, By: string(c.By), Result: string(c.Result), Count: c.Count, Error: c.Error})
	}
	for _, m := range mvs {
		d.OutputMoves = append(d.OutputMoves, outputMoveView{At: m.At, From: m.From, To: m.To, Result: string(m.Result), Error: m.Error})
	}
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

// act は人のジョブの操作（手動再試行・録らない・録る・録り直し・チャプターの入れ直し・保存先の移し）の処理。拒まれた状態と録音期限を過ぎたジョブは 409、
// 無いジョブと、保存先の移しの移し先に指定した無い番組（store.ErrNoShow）は 404。
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
	case errors.Is(err, store.ErrNoShow):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "番組が無い"})
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

// rerecordJob は録り直し（ADR 0036）。done のタイムフリーから録るジョブで録音期限の内側のものを、手動再試行と同じく retrying にして
// 録音のループを起こす。録音期限は今の会員種別と今の自分のエリアの局一覧で求め、局一覧は DB のトランザクションの前に取る
// （エリアフリー・ダブルプランで取れなければ、store を呼ばずに ErrRerecordAreaUnknown で断る）。
// 拒むときは、done でない・ライブ録音・録音期限を過ぎた・局一覧を取れない、で文言を分けて 409 にする。受け付けたことは通知しない。
// 足したら直す: ジョブの状態の移り方
func (u *UI) rerecordJob(w http.ResponseWriter, r *http.Request) {
	u.act(w, r, func(ctx context.Context, id int64) error {
		own, err := u.rerecordOwn(ctx)
		if err != nil {
			u.log().Warn("自分のエリアの局一覧を取れないので、録り直しを断る", "job", id, "err", err)
			return domain.ErrRerecordAreaUnknown
		}
		return u.wakeAfter(u.Jobs.Rerecord(ctx, id, u.membership(), own, u.now()))
	}, rerecordRefusal)
}

// rerecordRefusal は、録り直しを拒んだわけ（domain.CheckRerecord の返すエラー）を 409 の文言にする。
func rerecordRefusal(err error) string {
	switch {
	case errors.Is(err, domain.ErrRerecordLive):
		return "ライブ録音は録り直せない"
	case errors.Is(err, domain.ErrRerecordPastDeadline):
		return "録音期限を過ぎているので録り直せない"
	case errors.Is(err, domain.ErrRerecordAreaUnknown):
		return "自分のエリアの局一覧を取れないので、いまは録り直せない（後でやり直す）"
	case errors.Is(err, domain.ErrInvalidTransition):
		return "この状態では録り直せない（録り直せるのは録音済みのジョブだけ）"
	}
	return ""
}

// redoJobChapters は、ジョブ 1 件のチャプターの入れ直し（ADR 0033）。タイムフリーから録った done のジョブに頼みを DB に書き、
// 録音のループを起こして、受け付けたジョブの要約を返す（書き込みはまだ。結果はジョブのチャプターの値と、チャプターの書き込みの
// 履歴に出る）。チャプターの値は問わない（入ったのジョブも置き換える）。拒むときは、done でない・ライブ録音で文言を分けて 409 に
// する。通知はしない。
func (u *UI) redoJobChapters(w http.ResponseWriter, r *http.Request) {
	u.act(w, r, func(ctx context.Context, id int64) error {
		return u.wakeAfter(u.Jobs.RequestChapterRedo(ctx, id, u.now()))
	}, chapterRedoRefusal)
}

// chapterRedoRefusal は、チャプターの入れ直しを拒んだわけ（domain.CheckChapterRedo の返すエラー）を 409 の文言にする。
func chapterRedoRefusal(err error) string {
	switch {
	case errors.Is(err, domain.ErrChapterRedoLive):
		return "ライブ録音のジョブにはチャプターを入れ直せない"
	case errors.Is(err, domain.ErrInvalidTransition):
		return "この状態ではチャプターを入れ直せない（入れ直せるのは録音済みのジョブだけ）"
	}
	return ""
}

// chapterRedoAllView は、まとめてのチャプターの入れ直し（POST /api/jobs/chapters）の答え。Accepted は受け付けたジョブの数。
type chapterRedoAllView struct {
	Accepted int `json:"accepted"`
}

// redoAllChapters は、まとめてのチャプターの入れ直し（rajikura chapters --all。ADR 0033）。タイムフリーから録った done のジョブの
// うち、チャプターの値が値なしか入らなかったのものに頼みを DB に書き、録音のループを起こして、受け付けたジョブの数を返す。
// 入った・待っているのジョブは飛ばす。放送日では絞らない。当たるジョブが無くても 200（0 件）。
func (u *UI) redoAllChapters(w http.ResponseWriter, r *http.Request) {
	n, err := u.Jobs.RequestChapterRedoAll(r.Context(), u.now())
	if err != nil {
		u.internalError(w, "チャプターの入れ直しを頼めない", err)
		return
	}
	if n > 0 {
		u.wakeAfter(nil)
	}
	writeJSON(w, http.StatusOK, chapterRedoAllView{Accepted: n})
}

// moveRequest は、保存先の移し（POST /api/jobs/{id}/move）の body。Show は移し先の番組（今あるもの）の名前で、空（body が無い
// ときも）ならジョブの今の番組。
type moveRequest struct {
	Show string `json:"show"`
}

// moveJob は、ジョブ 1 件の保存先の移し（ADR 0039）。保存先のあるジョブに頼みを DB に書き（body の show が今の番組と違えば、ジョブの
// 番組の名前をその番組に付け替えてから）、録音のループを起こして、受け付けたジョブの要約を返す（移すのはまだ。結果はジョブの保存先と、
// 保存先の移しの履歴に出る）。拒むときは、保存先が無い・録音中で文言を分けて 409 にする。無い番組は 404。通知はしない。
func (u *UI) moveJob(w http.ResponseWriter, r *http.Request) {
	var req moveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body が JSON として読めない"})
		return
	}
	u.act(w, r, func(ctx context.Context, id int64) error {
		return u.wakeAfter(u.Jobs.RequestOutputMove(ctx, id, strings.TrimSpace(req.Show), u.now()))
	}, moveRefusal)
}

// moveRefusal は、保存先の移しを拒んだわけ（domain.CheckOutputMove の返すエラー）を 409 の文言にする。
func moveRefusal(err error) string {
	switch {
	case errors.Is(err, domain.ErrOutputMoveNoOutput):
		return "まだ保存していないジョブは、保存先を移せない"
	case errors.Is(err, domain.ErrOutputMoveRunning):
		return "録音中のジョブは、保存先を移せない（録音が終わってから頼む）"
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
