package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/artwork"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/media"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/power"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/store"
)

// stageError は失敗した段階を持つエラー。
type stageError struct {
	stage domain.Stage
	err   error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

func failAt(stage domain.Stage, err error) error {
	return &stageError{stage: stage, err: err}
}

// errStationUnknown は、起動してから局一覧が一度も取れておらず、保存先の局名を決められないことを表す。
var errStationUnknown = errors.New("局一覧が取れておらず、保存先の局名を決められない")

func stageOf(err error) domain.Stage {
	var se *stageError
	if errors.As(err, &se) {
		return se.stage
	}
	return domain.StageFetch
}

// saved は保存の結果。
type saved struct {
	path   string
	extinf time.Duration
	probe  time.Duration
	// missing と gaps はライブ録音（liveAttempt）だけが入れる。missing は欠けた区間を持つ放送回（欠落として記録する）、
	// gaps はその欠けた区間。
	missing []domain.Airing
	gaps    []airingGap
	// lengthOff は、ライブ録音で長さ（probe）が #EXTINF の合計（extinf）と lengthTolerance を超えてずれたまま保存したこと
	// （タイムフリーからの録音では検証の失敗にするので立たない）。
	lengthOff bool
	// chapterAirings と chapterFetch は、保存した放送回と、その並びでチャプターを取った結果（fetchChapters）。取りに行って
	// いなければ chapterFetch は nil（domain.ChaptersAfterSave で値なしになる）。
	chapterAirings []domain.Airing
	chapterFetch   []domain.ChapterFetch
}

// chapterStatus は、保存の結果 res からジョブ job のチャプターの値を決める（保存のたびに決め直す。ADR 0027）。
func (a *App) chapterStatus(log *slog.Logger, job domain.Job, res saved) domain.ChapterStatus {
	ch := domain.ChaptersAfterSave(job, res.chapterAirings, res.chapterFetch, a.now())
	if ch.Chapters == domain.ChaptersWaiting {
		log.Info("チャプターがまだ無い放送回があるので、できるのを待つ（チャプターの取り直し）", "airings", len(ch.Waits), "check_at", ch.CheckAt)
	}
	return ch
}

// runJob は ClaimDue で running にしたジョブ（タイムフリーから録るもの）を 1 回試行する。ライブで録るジョブは runLiveJob（live.go）。cur は試行を始めたときの設定の写しで、試行の最後（保存・再スキャン）まで
// これを使う（録音の途中で設定が保存されても、保存先などは始めたときのまま）。
func (a *App) runJob(ctx context.Context, cur live, job domain.Job) error {
	log := a.log().With("job", job.ID, "show", job.ShowName, "day", job.Day, "part", job.Part, "attempt", job.Attempts)
	started := a.now()
	// 進捗は試行の間だけ持つ（成功でも失敗でも、DB の状態を変えた後に消す）。
	defer a.clearProgress(job.ID)
	if !started.Before(job.Deadline) {
		return a.finishAtDeadline(ctx, cur, log, job, started)
	}
	log.Info("録音を始める")

	res, err := a.attemptAwake(ctx, cur, log, job)
	// 再スキャンには、取り消せる元の ctx を渡す（done までは取り消されても進めるが、再スキャンは進めない）。
	cancelable := ctx
	if err == nil {
		// 保存先に確定した後は、ctx が取り消されていても done まで進める。running のまま残すと、
		// 次の起動時の回復で再試行され、同じ回が別名でもう 1 本保存される。
		ctx = context.WithoutCancel(ctx)
		if err := a.record(ctx, job, started, res, nil); err != nil {
			return err
		}
		if err := a.Store.Complete(ctx, job.ID, res.path, nil, a.chapterStatus(log, job, res)); err != nil {
			return err
		}
		a.markSaved(ctx, log)
		if job.ChapterRefetch {
			// チャプターの取り直しは通知しない（録音の結果は 1 回目の通知で伝わっている。ADR 0027）。
			log.Info("チャプターの取り直しで保存した", "path", res.path, "length", res.probe)
		} else {
			log.Info("保存した", "path", res.path, "length", res.probe)
			a.notifyRecorded(ctx, log, job, res.probe, "")
		}
		a.removeDoneOnces(ctx, log, job)
		a.removeJobDir(log, job.ID)
		a.rescan(cancelable, cur, log)
		return nil
	}
	if ctx.Err() != nil {
		// 中断した。ジョブは running のまま残し、次の起動時の回復で retrying に戻す。
		// 足したら直す: ジョブの状態の移り方
		log.Info("中断した")
		return nil
	}
	if err := a.record(ctx, job, started, res, err); err != nil {
		return err
	}

	log.Warn("試行が失敗した", "stage", stageOf(err), "err", err)
	if stageOf(err) != domain.StageFetch && !errors.Is(err, errStationUnknown) {
		// 格納・検証で落ちたのは、入手した中身が欠けていたためかもしれない。使った放送回を捨て、
		// 次の試行で取り直す（入手の段階の失敗と、局名を決められなかった失敗なら、取り終えた放送回はそのまま再利用する）。
		a.discardFetched(log, job)
	}
	if errors.Is(err, radiko.ErrNotAvailable) {
		log.Warn("恒久的に取れないので期限切れにする")
		return a.expire(ctx, log, job, failureText(err))
	}
	policy := domain.RetryPolicy{Membership: a.Membership, MaxAttemptsFree: cur.maxAttemptsFree}
	if next, ok := policy.NextAttemptAt(job.Attempts, started, job.Deadline, a.now()); ok {
		log.Info("再試行する", "next", next)
		return a.Store.Retry(ctx, job.ID, next)
	}
	if fetched, _ := a.fetchedAirings(job); fetched > 0 {
		// 取れた分を捨てずに、録音期限を過ぎてから扱いを決める（finishAtDeadline。取れた分を保存するが、録り直しの試行で
		// 一部しか取れていなければ保存せずに期限切れにする。ADR 0025。チャプターの取り直しの試行なら、期限切れにせずに done に戻す
		// （expire。ADR 0027））。
		log.Info("次の試行が無いので、録音期限を過ぎてから取れた分の扱いを決める", "next", job.Deadline)
		return a.Store.Retry(ctx, job.ID, job.Deadline)
	}
	log.Warn("次の試行が無いので期限切れにする")
	return a.expire(ctx, log, job, failureText(err))
}

// removeDoneOnces は、done にしたジョブ job の放送回の 1 回の予約（その番組の、局と開始時刻が同じもの）を消す。
// 番組の名前は、DB から読み直したジョブの今の名前を使う（録音中に Web UI・API で名前を変えると、store.ReplaceShow が
// ジョブを新しい名前に付け替え、1 回の予約も新しい名前の番組に引き継ぐので、録音を始めたときの job.ShowName では
// 見つからない）。部分保存（finishAtDeadline）で done にしたときも、欠落した放送回を含めてジョブの放送回を全部見る。消せなくても
// ログに出すだけ（残った 1 回の予約は、録音期限を過ぎてから同期で消える。done のジョブがあるので、その放送回の
// ジョブを作り直しもしない）。
func (a *App) removeDoneOnces(ctx context.Context, log *slog.Logger, job domain.Job) {
	name := job.ShowName
	if cur, err := a.Store.GetJob(ctx, job.ID); err != nil {
		log.Warn("ジョブを読み直せないので、録音を始めたときの番組の名前で 1 回の予約を消す", "err", err)
	} else {
		name = cur.ShowName
	}
	n, err := a.Store.DeleteOncesOfAirings(ctx, name, job.Airings)
	if err != nil {
		log.Warn("録音を終えた 1 回の予約を消せない（録音期限を過ぎてから同期で消す）", "err", err)
		return
	}
	if n > 0 {
		log.Info("録音を終えた 1 回の予約を消した", "count", n, "show", name)
	}
}

// discardFetched は、ジョブの放送回の取り終えたファイル（.aac と、取り終えた印の .extinf）を消す。
func (a *App) discardFetched(log *slog.Logger, job domain.Job) {
	dir := a.jobDir(job.ID)
	for _, ar := range job.Airings {
		base := airingBase(dir, ar)
		// 印を先に消す。.aac だけ残っても、印が無ければ取り直す。
		for _, p := range []string{base + ".extinf", base + ".aac"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Warn("取った放送回を消せない", "path", p, "err", err)
			}
		}
	}
}

// expire はジョブを expired にし、作業用フォルダを消して通知する。cause は直近のエラー（通知の本文に入れる）。
// ただし、チャプターの取り直しで起こした試行（domain.Job.ChapterRefetch）は、expired にせず done に戻し（前のファイル・保存先・
// 欠落の印は前のまま）、通知しない（ADR 0027）。
// 足したら直す: ジョブの状態の移り方
func (a *App) expire(ctx context.Context, log *slog.Logger, job domain.Job, cause string) error {
	if job.ChapterRefetch {
		if err := a.Store.RevertChapterRefetch(ctx, job.ID); err != nil {
			return err
		}
		a.removeJobDir(log, job.ID)
		log.Warn("チャプターの取り直しで録れなかったので、前のファイルを残して録音済みに戻す", "path", job.OutputPath, "cause", cause)
		return nil
	}
	if err := a.Store.Expire(ctx, job.ID); err != nil {
		return err
	}
	a.removeJobDir(log, job.ID)
	a.notifyExpired(ctx, log, job, cause, "")
	return nil
}

// expireKeeping は expire の、作業用フォルダを残す側。ライブで録るジョブの録れた分を、保存のやり直しの打ち切りまでに
// 保存できなかったときに使う。録れた分は、最後の試行の終わりから domain.LiveKeepFor（30 日）の間残し（消すのは
// sweepKeptLive）、録音できなかったの通知に、残した場所と消す日時と保存からやり直す手順を書く。最後の試行は呼ぶ前に記録しておく。
func (a *App) expireKeeping(ctx context.Context, log *slog.Logger, job domain.Job, cause string) error {
	if err := a.Store.Expire(ctx, job.ID); err != nil {
		return err
	}
	var keep time.Time
	if as, err := a.Store.Attempts(ctx, job.ID); err != nil {
		log.Warn("試行の履歴を読めないので、録れた分を消す日時を通知に書けない", "err", err)
	} else if len(as) > 0 {
		keep = domain.LiveKeepUntil(job, &as[len(as)-1])
	}
	dir := a.jobDir(job.ID)
	log.Warn("録れた分を作業用フォルダに残す", "dir", dir, "until", keep)
	a.notifyExpired(ctx, log, job, cause, keptLines(job, dir, keep))
	return nil
}

// record は試行を記録する。
func (a *App) record(ctx context.Context, job domain.Job, started time.Time, res saved, err error) error {
	at := domain.Attempt{
		JobID:         job.ID,
		StartedAt:     started,
		EndedAt:       a.now(),
		ExtinfTotal:   res.extinf,
		ProbeDuration: res.probe,
	}
	if err != nil {
		at.FailedStage = stageOf(err)
		at.Error = err.Error()
	}
	_, rerr := a.Store.RecordAttempt(ctx, at)
	return rerr
}

// attempt は全放送回を入手し、格納・検証・保存までを行う。
// 入手の間は進捗の録れた長さを、前の試行で入手し終えた放送回の分から始め、チャンクを書くごとに進める。
func (a *App) attempt(ctx context.Context, cur live, log *slog.Logger, job domain.Job) (saved, error) {
	if len(job.Airings) == 0 {
		return saved{}, failAt(domain.StageFetch, errors.New("ジョブに放送回が無い"))
	}
	dir := a.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return saved{}, failAt(domain.StageFetch, err)
	}
	// 録れた長さは、入手し終えた放送回の分（前の試行で取ったものを含む）。入手している放送回の分は、チャンクごとに足して見せる。
	var fetched time.Duration
	for _, ar := range job.Airings {
		if _, d, ok := doneAiring(dir, ar); ok {
			fetched += d
		}
	}
	a.startFetchProgress(job, fetched)
	var token *radiko.Token
	files := make([]string, len(job.Airings))
	var extinf time.Duration
	for i, ar := range job.Airings {
		file, d, ok := doneAiring(dir, ar)
		if ok {
			log.Info("前の試行で取った放送回を使う", "start", ar.Start)
		} else {
			if token == nil {
				t, err := a.Fetcher.Authenticate(ctx)
				a.authResult(ctx, log, err)
				if err != nil {
					return saved{}, failAt(domain.StageFetch, fmt.Errorf("認証できない: %w", err))
				}
				token = &t
			}
			var err error
			base := fetched
			if file, d, err = a.fetchAiring(ctx, *token, dir, ar, func(seconds float64) {
				a.setFetched(job.ID, base+time.Duration(seconds*float64(time.Second)))
			}); err != nil {
				return saved{}, failAt(domain.StageFetch, fmt.Errorf("%s の放送回を取れない: %w", broadcastClock(ar.Day(), ar.Start), err))
			}
			fetched += d
			a.setFetched(job.ID, fetched)
		}
		files[i] = file
		extinf += d
	}
	return a.save(ctx, cur, log, job, job.Airings, files, extinf, true)
}

// airingBase は放送回の作業ファイルの、拡張子を除いたパス。
func airingBase(dir string, ar domain.Airing) string {
	return filepath.Join(dir, ar.Start.In(domain.JST).Format("20060102150405"))
}

// doneAiring は、前の試行で取り終えた放送回のファイルと #EXTINF の合計を返す。
// 取り終えた印（.extinf）が無ければ false。
func doneAiring(dir string, ar domain.Airing) (string, time.Duration, bool) {
	base := airingBase(dir, ar)
	b, err := os.ReadFile(base + ".extinf")
	if err != nil {
		return "", 0, false
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return "", 0, false
	}
	if _, err := os.Stat(base + ".aac"); err != nil {
		return "", 0, false
	}
	return base + ".aac", time.Duration(sec * float64(time.Second)), true
}

// fetchAiring は放送回を <開始時刻>.aac に入手し、取り終えた印として #EXTINF の合計を <開始時刻>.extinf に書く。
// 途中のファイルは消して取り直す。onChunk は、チャンクを書くたびに、この放送回でそれまでに書いた #EXTINF の合計秒数で呼ぶ
// （radiko.Client.FetchTimefree の onChunk）。
func (a *App) fetchAiring(ctx context.Context, token radiko.Token, dir string, ar domain.Airing, onChunk func(seconds float64)) (string, time.Duration, error) {
	base := airingBase(dir, ar)
	aac, mark, part := base+".aac", base+".extinf", base+".aac.part"
	for _, p := range []string{mark, aac} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", 0, err
		}
	}
	f, err := os.Create(part)
	if err != nil {
		return "", 0, err
	}
	res, err := a.Fetcher.FetchTimefree(ctx, token, ar.StationID, ar.Start, ar.End, f, onChunk)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return "", 0, err
	}
	if err := os.Rename(part, aac); err != nil {
		os.Remove(part)
		return "", 0, err
	}
	if err := os.WriteFile(mark, []byte(strconv.FormatFloat(res.DurationSeconds, 'f', -1, 64)), 0o644); err != nil {
		return "", 0, err
	}
	return aac, time.Duration(res.DurationSeconds * float64(time.Second)), nil
}

// fetchedAirings は、ジョブの放送回のうち取り終えたものの数と、放送回の並びでの取れたかどうかを返す。
func (a *App) fetchedAirings(job domain.Job) (int, []bool) {
	dir := a.jobDir(job.ID)
	n := 0
	got := make([]bool, len(job.Airings))
	for i, ar := range job.Airings {
		if _, _, ok := doneAiring(dir, ar); ok {
			got[i] = true
			n++
		}
	}
	return n, got
}

// joinDistinct は items の前後の空白を落とし、空のものを除き、同じものは最初の 1 つだけを残して sep でつなぐ。
// 中身は解釈しない（出演者の欄は局ごとに区切りが違い、1 人ずつには分けられない）。
func joinDistinct(items []string, sep string) string {
	return strings.Join(distinct(items), sep)
}

// distinct は items の前後の空白を落とし、空のものを除き、同じものは最初の 1 つだけを残す。
func distinct(items []string) []string {
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it != "" && !slices.Contains(out, it) {
			out = append(out, it)
		}
	}
	return out
}

// joinPerformers は出演者の文字列 items を artist 用につなぐ。distinct で空白・空・同じものを整えた後、
// ほかの文字列の先頭部分（真の前方一致）になっているものを、並びの向きを問わず落として " / " でつなぐ。
// 帯番組で前半か後半の一方にだけゲストがいる日に、パーソナリティの名前が 2 回並ぶのを避けるため。
// 中身は解釈しない（比べるのは空白を落とした文字列そのもの）。
func joinPerformers(items []string) string {
	kept := distinct(items)
	var out []string
	for _, it := range kept {
		if !slices.ContainsFunc(kept, func(o string) bool { return o != it && strings.HasPrefix(o, it) }) {
			out = append(out, it)
		}
	}
	return strings.Join(out, " / ")
}

// save は files（airings の並び）を m4a に格納し、長さを検証して保存先に確定する。
// waitStation が true で局一覧が一度も取れていないときは、errStationUnknown で失敗する（次の試行を待つ）。
// false（録音期限を過ぎたとき）なら、局 ID で保存する。
// 進捗の段階は、失敗したときに記録する段階（stage）と同じ所で変える（格納は保存の段階に数える）。
func (a *App) save(ctx context.Context, cur live, log *slog.Logger, job domain.Job, airings []domain.Airing, files []string, extinf time.Duration, waitStation bool) (saved, error) {
	a.setStage(job.ID, domain.StageSave)
	res := saved{extinf: extinf}
	split := domain.SplitNone
	switch show, err := a.Store.GetShow(ctx, job.ShowName); {
	case err == nil:
		split = show.Split
	case errors.Is(err, store.ErrNotFound):
		log.Warn("番組が消えているので、分割番組ではないものとして保存する")
	default:
		return res, failAt(domain.StageSave, err)
	}
	first := airings[0]
	station, known := a.stationName(ctx, first.StationID)
	if !known {
		if waitStation {
			return res, failAt(domain.StageSave, errStationUnknown)
		}
		log.Warn("局一覧が取れないまま録音期限を過ぎたので、局 ID で保存する", "station", station)
	}
	// title に足す放送回の番組名は、保存する（取れた）放送回のものを開始時刻の順に並べる（ADR 0007）。
	byStart := slices.Clone(airings)
	slices.SortStableFunc(byStart, func(x, y domain.Airing) int { return x.Start.Compare(y.Start) })
	titles := make([]string, len(byStart))
	performers := make([]string, len(byStart))
	descriptions := make([]string, len(byStart))
	for i, ar := range byStart {
		titles[i] = ar.Title
		performers[i] = ar.Performer
		descriptions[i] = ar.Description
	}
	// artist と comment も同じく、保存する放送回のものを開始時刻の順につなぐ（放送回が 1 つならそのまま）。
	// artist は、ほかの回の先頭部分になっている出演者も落とす（joinPerformers）。comment は同じものだけをまとめる。
	performer := joinPerformers(performers)
	description := joinDistinct(descriptions, "\n\n")
	layout := cur.preset.Layout(library.Input{
		ShowName:     job.ShowName,
		Station:      station,
		BroadcastDay: time.Date(job.Day.Year, job.Day.Month, job.Day.Day, 0, 0, 0, 0, domain.JST),
		Part:         job.Part,
		Split:        split,
		Performers:   performer,
		Description:  description,
		AiringTitles: titles,
	})

	if layout.ArtistTruncated {
		log.Info("出演者が長いので、artist タグを切り詰めて保存する", "bytes", len(performer), "max", library.MaxArtistBytes)
	}

	var guide time.Duration
	for _, ar := range airings {
		guide += ar.End.Sub(ar.Start)
	}
	// artist・comment は保存する放送回からつないだが、放送回の画像はつながないので、先頭の放送回のもの。取れなければ画像なしで保存する（録音の保存は失敗にしない）。
	cover, coverData := a.fetchCover(ctx, log, job, first)
	// チャプターは、タイムフリーから録るジョブで設定 chapters がオンのときだけ取る（ライブ録音には入れない。ADR 0027）。
	// 取った結果は、保存の後にチャプターを待つか（チャプターの取り直し）を決めるのに使う（ADR 0027）。
	var chapters []media.Chapter
	if !job.Live() && cur.chapters {
		chapters, res.chapterFetch = a.fetchChapters(ctx, log, airings)
		res.chapterAirings = airings
	}
	remux := func(partial string) error {
		if cm, ok := cur.media.(ChapterRemuxer); ok && len(chapters) > 0 {
			return cm.RemuxWithChapters(ctx, files, partial, layout.Tags, cover, chapters)
		}
		return cur.media.Remux(ctx, files, partial, layout.Tags, cover)
	}

	stage := domain.StageSave
	// 前に保存したジョブ（保存先が空でない）の試行は録り直しで、選んだ名前が前の保存先と同じなら上書きする（ADR 0025）。
	// 保存先を書くのは done にする Store.Complete だけで、done から出るのは録り直しだけなので、保存先が空でない試行は
	// 録り直しの試行（録り直しが録れずに expired になった後の手動再試行を含む）。
	r, err := library.StoreOver(cur.library, layout, first.Start.In(domain.JST), job.OutputPath, func(partial string) error {
		if err := remux(partial); err != nil {
			return err
		}
		stage = domain.StageVerify
		a.setStage(job.ID, stage)
		d, err := cur.media.Duration(ctx, partial)
		if err != nil {
			return err
		}
		res.probe = d
		if diff := (d - extinf).Abs(); diff > lengthTolerance {
			if !job.Live() {
				return fmt.Errorf("長さ %v が #EXTINF の合計 %v と %v ずれている", d, extinf, diff)
			}
			// ライブ録音は取り直せないので、止めても得るものが無い。ずれたまま保存し、通知に書く（liveLengthLine）。
			log.Warn("ライブ録音の長さが #EXTINF の合計とずれているが、取り直せないのでそのまま保存する", "length", d, "extinf", extinf, "diff", diff)
			res.lengthOff = true
		}
		if diff := (d - guide).Abs(); diff >= guideTolerance {
			log.Warn("長さが番組表の放送時間と大きくずれている", "length", d, "guide", guide, "diff", diff)
		}
		stage = domain.StageSave
		a.setStage(job.ID, stage)
		return nil
	})
	if err != nil {
		return res, failAt(stage, err)
	}
	switch {
	case r.Replaced:
		log.Info("前の保存先を上書きした（録り直し）", "path", r.Path)
	case r.Renamed:
		log.Warn("同じ名前のファイルがあったので別名で保存した", "path", r.Path)
	}
	res.path = r.Path
	if coverData != nil {
		// 番組のフォルダの画像は、保存のたびに最新の回の画像にする。
		if err := library.StoreFolderImage(cur.library, layout, coverData); err != nil {
			log.Warn("番組のフォルダの画像を置けない", "err", err)
		}
	}
	return res, nil
}

// ChapterSource は radiko のチャプター（用語集の「チャプター」）の取得元。*radiko.Client が満たす。App は Fetcher がこれを
// 満たすときだけチャプターを取る（cmd は Fetcher に *radiko.Client を渡す）。
type ChapterSource interface {
	// Chapters は、局 stationID の放送日 day の、start に始まる放送回のチャプターを返す（radiko.Client.Chapters）。
	Chapters(ctx context.Context, stationID string, day, start time.Time) ([]radiko.Chapter, error)
}

// ChapterRemuxer は、チャプターも書く m4a への格納。*media.Tools と media.Builtin が満たす（Media がこれを満たさなければ、
// チャプター無しで Remux する）。
type ChapterRemuxer interface {
	RemuxWithChapters(ctx context.Context, inputs []string, out string, tags media.Tags, cover string, chapters []media.Chapter) error
}

// fetchChapters は、保存する放送回 airings（Remux の inputs と同じ並び）ごとにチャプターを取り、ファイルに入れる形にする（ADR 0027）。
// 時刻は放送回ごとの開始から数えたまま渡し、ファイルの中の位置（前の放送回の長さの合計だけずらす）とファイルの長さでの
// 切り詰めは media が入力の長さから決める。保存の試行のたびに取り直す。あわせて、放送回ごとの取った結果（airings の並び）を
// 返す（保存の後にチャプターを待つかを決める。ADR 0027）。Fetcher がチャプターの取得元でなければ、どちらも nil（取りに行っていない）。
//
// 取れない放送回（404 で、まだ無いか局に無い・空・eventId が無い・通信の失敗や読めない応答。chapterFetchOf）は、その放送回の分だけ
// チャプター無しにし、Info で局・放送回の開始時刻・理由を出す（ジョブは log が持つ）。試行は失敗にせず、再試行も通知もしない
// （404 と通信の失敗・読めない応答なら、保存の後にチャプターの取り直しを待つ）。
func (a *App) fetchChapters(ctx context.Context, log *slog.Logger, airings []domain.Airing) ([]media.Chapter, []domain.ChapterFetch) {
	src, ok := a.Fetcher.(ChapterSource)
	if !ok {
		return nil, nil
	}
	var out []media.Chapter
	fetched := make([]domain.ChapterFetch, len(airings))
	for i, ar := range airings {
		chs, err := chaptersOf(ctx, src, ar)
		f, reason := chapterFetchOf(chs, err)
		fetched[i] = f
		if f != domain.ChapterGot {
			attrs := []any{"station", ar.StationID, "start", ar.Start, "reason", reason}
			if err != nil {
				attrs = append(attrs, "err", err)
			}
			log.Info("チャプターを取れないので、この放送回はチャプター無しで保存する", attrs...)
			continue
		}
		for _, ch := range chs {
			out = append(out, media.Chapter{Input: i, Start: ch.From, Title: chapterTitle(ch)})
		}
		log.Info("チャプターを取った", "station", ar.StationID, "start", ar.Start, "count", len(chs))
	}
	return out, fetched
}

// checkChapters は、チャプターを待っている done のジョブ（domain.ChaptersWaiting）のうち、次にチャプターの API を引く時刻を
// 過ぎたものについて、待っている放送回のチャプターの API だけを引く（音声は取らない。ADR 0027）。タイムフリーの録音のループ
// （RunDue）が、ジョブを拾う前に呼ぶ。引いた結果で、待ち続けるか、チャプターの取り直しの試行を起こすか（domain.CheckChapters）、
// 待つのをやめて入らなかったにするかを決める。設定 chapters がオフなら、引かずに待ちを消す（入らなかったにする）。
// どれも通知しない（打ち切りはログに 1 行）。1 件のジョブで DB に書けなければ、エラーを返す（残りは次の見回りで引く）。
// 足したら直す: ジョブの状態の移り方
func (a *App) checkChapters(ctx context.Context, cur live) error {
	jobs, err := a.Store.DueChapterChecks(ctx, a.now())
	if err != nil || len(jobs) == 0 {
		return err
	}
	src, ok := a.Fetcher.(ChapterSource)
	for _, job := range jobs {
		if ctx.Err() != nil {
			return nil
		}
		log := a.log().With("job", job.ID, "show", job.ShowName, "day", job.Day, "part", job.Part)
		if !cur.chapters || !ok {
			if _, err := a.Store.UpdateChapterWait(ctx, job.ID, domain.ChapterStatus{Chapters: domain.ChaptersNotAdded}); err != nil {
				return err
			}
			log.Info("設定 chapters がオフなので、チャプターを待つのをやめる")
			continue
		}
		results := map[string]domain.ChapterFetch{}
		for _, ar := range job.Airings {
			if job.ChapterWaits[ar.Key()] != domain.AiringChapterPending {
				continue
			}
			chs, err := chaptersOf(ctx, src, ar)
			f, reason := chapterFetchOf(chs, err)
			results[ar.Key()] = f
			if f == domain.ChapterGot {
				log.Info("チャプターができた", "station", ar.StationID, "start", ar.Start, "count", len(chs))
			} else {
				log.Debug("チャプターはまだ取れない", "station", ar.StationID, "start", ar.Start, "reason", reason, "err", err)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		now := a.now()
		switch next, ch := domain.CheckChapters(job, results, now); next {
		case domain.ChapterRefetchNow:
			started, err := a.Store.StartChapterRefetch(ctx, job.ID, now)
			if err != nil {
				return err
			}
			if started {
				log.Info("チャプターができたので、チャプターの取り直しで録り直す")
			}
		case domain.ChapterGiveUp:
			if _, err := a.Store.UpdateChapterWait(ctx, job.ID, ch); err != nil {
				return err
			}
			log.Info("チャプターができないまま打ち切ったので、チャプター無しのままにする", "cutoff", job.ChapterCutoff())
		default:
			if _, err := a.Store.UpdateChapterWait(ctx, job.ID, ch); err != nil {
				return err
			}
		}
	}
	return nil
}

// chaptersOf は、放送回 ar のチャプターを src から取る。
func chaptersOf(ctx context.Context, src ChapterSource, ar domain.Airing) ([]radiko.Chapter, error) {
	day := ar.Day()
	return src.Chapters(ctx, ar.StationID, time.Date(day.Year, day.Month, day.Day, 0, 0, 0, 0, domain.JST), ar.Start)
}

// chapterFetchOf は、チャプターを取った結果（chs と err）を分け、取れなければログに書く理由も返す。404 と、通信の失敗・読めない
// 応答は、まだできていないかもしれない（待つ）。空（200 で 0 件）と eventId が無いのは、待っても入らない（待たない。ADR 0027）。
func chapterFetchOf(chs []radiko.Chapter, err error) (domain.ChapterFetch, string) {
	switch {
	case errors.Is(err, radiko.ErrNoChapters):
		return domain.ChapterPending, "404（まだ無いか、局に無い）"
	case errors.Is(err, radiko.ErrNoEvent):
		return domain.ChapterAbsent, "eventId が無い"
	case err != nil:
		return domain.ChapterPending, "通信の失敗か、読めない応答"
	case len(chs) == 0:
		return domain.ChapterAbsent, "空"
	}
	return domain.ChapterGot, ""
}

// chapterTitle は、radiko のチャプターからファイルに書く題を作る（ADR 0027）。
// 曲の場面は「♪ 曲名 / アーティスト」（アーティストが空なら「♪ 曲名」、曲名が空なら titleLong）。それ以外は titleLong
// （空なら title）。
func chapterTitle(ch radiko.Chapter) string {
	long := cmp.Or(strings.TrimSpace(ch.TitleLong), strings.TrimSpace(ch.Title))
	if !ch.IsMusic {
		return long
	}
	title, artist := strings.TrimSpace(ch.MusicTitle), strings.TrimSpace(ch.MusicArtist)
	switch {
	case title == "":
		return long
	case artist == "":
		return "♪ " + title
	}
	return "♪ " + title + " / " + artist
}

// fetchCover は ar の画像を取得し、remux に渡す作業ファイルのパスと画像の中身を返す。
// 取れないとき（URL が空、取得の失敗、JPEG・PNG・GIF でない、読み込めない、幅か高さが 4096 を超える、大きすぎる）は Warn を出して、空の値を返す。
func (a *App) fetchCover(ctx context.Context, log *slog.Logger, job domain.Job, ar domain.Airing) (string, []byte) {
	data, err := artwork.Fetch(ctx, a.ArtworkClient, ar.ImageURL)
	if err != nil {
		log.Warn("放送回の画像を取れないので、画像なしで保存する", "url", ar.ImageURL, "err", err)
		return "", nil
	}
	dir := a.jobDir(job.ID)
	path := filepath.Join(dir, "cover.jpg")
	if err = os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		log.Warn("放送回の画像を作業ディレクトリに書けないので、画像なしで保存する", "err", err)
		return "", nil
	}
	return path, data
}

// finishAtDeadline は録音期限を過ぎたジョブを、入手せずに片付ける。
// 一部でも取れていれば取れた分を保存して done にし、欠落を記録する。何も取れていなければ expired にする。
// ただし録り直しの試行（前の保存先がある）は、一部しか取れていなければ取れた分を保存せずに expired にし、前のファイル・保存先・
// 欠落の印を前のまま残す（全部取れていれば保存する。ADR 0025）。チャプターの取り直しの試行は、expired にするところで done に
// 戻す（expire。ADR 0027）。
// 局一覧が一度も取れていなくても、局 ID を局名にして保存する（局名が決められないことでは expired にしない）。
// 足したら直す: ジョブの状態の移り方
func (a *App) finishAtDeadline(ctx context.Context, cur live, log *slog.Logger, job domain.Job, started time.Time) error {
	_, got := a.fetchedAirings(job)
	verdict, missing := domain.JudgeAtDeadline(job.Airings, got)
	if verdict == domain.VerdictExpire {
		log.Warn("録音期限を過ぎ、何も取れていないので期限切れにする")
		// 通知には、録音期限を過ぎる前の直近の試行のエラーを書く。
		cause := "録音期限を過ぎ、何も取れていない"
		if prev := a.lastError(ctx, job.ID); prev != "" {
			cause += "。その前の試行のエラー: " + prev
		}
		if err := a.record(ctx, job, started, saved{}, failAt(domain.StageFetch, errors.New("録音期限を過ぎた"))); err != nil {
			return err
		}
		return a.expire(ctx, log, job, cause)
	}
	if verdict == domain.VerdictSavePartial && job.OutputPath != "" {
		// 録り直しの試行（前の保存先がある）は、一部しか取れていなければ取れた分を保存せずに期限切れにし、前のファイル・保存先・
		// 欠落の印を前のまま残す（前のファイルを欠落のあるファイルで置き換えない。ADR 0025）。チャプターの取り直しの試行なら、
		// 期限切れにせずに done に戻す（expire。ADR 0027）。
		log.Warn("録り直しで録音期限までに一部しか取れなかったので、前のファイルを残して期限切れにする", "fetched", len(job.Airings)-len(missing), "missing", len(missing), "path", job.OutputPath)
		cause := fmt.Sprintf("録り直しで録音期限までに %d 個の放送回のうち %d 個しか取れなかったので、取れた分を保存せず、前に保存したファイル（%s）を残した", len(job.Airings), len(job.Airings)-len(missing), job.OutputPath)
		if prev := a.lastError(ctx, job.ID); prev != "" {
			cause += "。その前の試行のエラー: " + prev
		}
		if err := a.record(ctx, job, started, saved{}, failAt(domain.StageFetch, errors.New("録音期限を過ぎ、録り直しで一部しか取れていない"))); err != nil {
			return err
		}
		return a.expire(ctx, log, job, cause)
	}

	dir := a.jobDir(job.ID)
	var airings []domain.Airing
	var files []string
	var extinf time.Duration
	for i, ar := range job.Airings {
		if !got[i] {
			continue
		}
		file, d, _ := doneAiring(dir, ar)
		airings = append(airings, ar)
		files = append(files, file)
		extinf += d
	}
	log.Info("録音期限を過ぎたので、取れた分を保存する", "fetched", len(airings), "missing", len(missing))
	res, err := a.save(ctx, cur, log, job, airings, files, extinf, false)
	cancelable := ctx
	if err == nil {
		// 保存先に確定した後は、ctx が取り消されていても done まで進める（runJob と同じ）。
		ctx = context.WithoutCancel(ctx)
	} else if ctx.Err() != nil {
		log.Info("中断した")
		return nil
	}
	if rerr := a.record(ctx, job, started, res, err); rerr != nil {
		return rerr
	}
	if err != nil {
		log.Warn("取れた分を保存できないので期限切れにする", "stage", stageOf(err), "err", err)
		return a.expire(ctx, log, job, failureText(err))
	}
	if err := a.Store.Complete(ctx, job.ID, res.path, missing, a.chapterStatus(log, job, res)); err != nil {
		return err
	}
	a.markSaved(ctx, log)
	a.removeDoneOnces(ctx, log, job)
	switch {
	case job.ChapterRefetch:
		// チャプターの取り直しは通知しない（ADR 0027）。一部しか取れていなければ上で録音済みに戻しているので、ここは全部取れたとき。
		log.Info("チャプターの取り直しで保存した", "path", res.path)
	case len(missing) > 0:
		log.Warn("欠落のまま保存した", "path", res.path, "missing", len(missing))
		a.notifyMissing(ctx, log, job, res.path, missing)
	default:
		log.Info("保存した", "path", res.path)
		a.notifyRecorded(ctx, log, job, res.probe, "")
	}
	a.removeJobDir(log, job.ID)
	a.rescan(cancelable, cur, log)
	return nil
}

// DefaultRescanTimeout は、再スキャンを頼む処理全体（要求 2 回の合計）の待ち時間の上限。
// ジョブの実行は周の中で同期的に走るので、Jellyfin が応答しなくてもループを長く止めない。
const DefaultRescanTimeout = 10 * time.Second

// rescan は保存した後に、ライブラリの再スキャンを 1 回頼む。ctx は取り消せるもの（SIGTERM で止める）。
// 全体に RescanTimeout の上限を付ける。失敗・取り消し・上限超過は Warn でログに出す（保存は成功のまま）。
// 失敗と上限超過は、あわせて再スキャンの失敗のサービスの異常（notify.KindRescan）の始まりを通知し（続いている間は書かない）、
// 頼めたら、続いていれば復旧を通知する。ctx が取り消されたとき（SIGTERM）は通知しない（addAnomaly）。
// 頼む先が無い（jellyfin.url が空か、API キーを復号できない）ときは頼まず、失敗の異常が続いていれば、止めたことを復旧として
// 通知する（closeRescanIfGone。保存したときと、定期的な見回りで書く）。
// 足したら直す: サービスの異常
func (a *App) rescan(ctx context.Context, cur live, log *slog.Logger) {
	if a.closeRescanIfGone(ctx, cur, log) {
		return
	}
	timeout := a.RescanTimeout
	if timeout <= 0 {
		timeout = DefaultRescanTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := cur.rescanner.Rescan(rctx); err != nil {
		log.Warn("ライブラリの再スキャンを頼めない", "err", err)
		a.addAnomaly(ctx, log, notify.KindRescan, notify.KindRescan,
			"【再スキャンの失敗】ライブラリの再スキャンを頼めなかった。録音したものが Jellyfin に出るのが、Jellyfin の定期スキャンまで遅れる。"+
				"対処は docs/settings.md の「ライブラリの再スキャン（jellyfin）」。\nエラー: "+oneLine(err.Error()))
		return
	}
	log.Info("ライブラリの再スキャンを頼んだ")
	a.addAnomaly(ctx, log, notify.KindRecovered, notify.KindRescan, "【復旧】ライブラリの再スキャンを頼めた。")
}

// closeRescanIfGone は、頼む先が無い（cur.rescanner == nil）とき、再スキャンの失敗の異常が続いていれば、止めたことを復旧として
// 閉じて true を返す（続いていなければ何も書かない）。頼む先があれば false を返し、何もしない。
// 再スキャンの失敗が続いている間に頼む先が無くなったら、頼める日は来ないので閉じる（バックアップの取る先を空にしたときと
// 同じ。BackupIfDue）。保存した後（rescan）のほか、定期的な見回り（runBackups）からも呼ぶので、録音の保存を待たずに閉じる。
// 足したら直す: サービスの異常
func (a *App) closeRescanIfGone(ctx context.Context, cur live, log *slog.Logger) bool {
	if cur.rescanner != nil {
		return false
	}
	a.addAnomaly(ctx, log, notify.KindRecovered, notify.KindRescan,
		"【復旧】ライブラリの再スキャンを頼む先が無くなった（jellyfin.url を空にしたか、API キーを復号できない）ので、再スキャンを頼むのを止めた。頼めなかった異常は終わりにするが、ライブラリの再スキャンは頼めていない。")
	return true
}

// attemptAwake は試行（タイムフリーからの録音は attempt、ライブで録るジョブ（domain.Job.Live）は liveAttempt）の間だけ
// OS にスリープしないよう伝える。伝えられなくても Warn を出して試行を続ける。
// 試行が誤りで終わっても、ctx が取り消されても、戻るときに外す。ライブ録音は並行して走るので、掛けるのも外すのも試行ごと。
func (a *App) attemptAwake(ctx context.Context, cur live, log *slog.Logger, job domain.Job) (saved, error) {
	keep := a.KeepAwake
	if keep == nil {
		keep = power.Acquire
	}
	release, err := keep(power.Reason)
	if err != nil {
		log.Warn("スリープを止められない。録音は続ける", "err", err)
	}
	if release != nil {
		defer release()
	}
	if job.Live() {
		return a.liveAttempt(ctx, cur, log, job)
	}
	return a.attempt(ctx, cur, log, job)
}
