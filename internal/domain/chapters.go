package domain

import (
	"errors"
	"fmt"
	"time"
)

// Chapters は、ジョブの保存したファイルにチャプターが入ったか（用語集の「チャプター」「チャプターの取り直し」。ADR 0027・0033）。
type Chapters string

const (
	// ChaptersNone は値なし。取りに行っていない（ライブ録音、設定 chapters がオフ、まだ保存していない、チャプターの取り直しの
	// 前の版で保存した）。
	ChaptersNone Chapters = ""
	// ChaptersAdded は入った（取りに行った全部の放送回で取れた）。
	ChaptersAdded Chapters = "added"
	// ChaptersWaiting は待っている（チャプターがまだできていないかもしれない放送回があり、チャプターの取り直しを待っている）。
	ChaptersWaiting Chapters = "waiting"
	// ChaptersNotAdded は入らなかった（待つのを打ち切った、または待たない理由で一部か全部が取れなかった。一部だけ入ったものも含む。
	// チャプターの取り直しで書き込めなかったものも含む）。
	ChaptersNotAdded Chapters = "not_added"
)

// ChapterFetch は、放送回のチャプターを radiko から取った結果の分け方。
type ChapterFetch int

const (
	// ChapterGot は取れた（1 件以上）。
	ChapterGot ChapterFetch = iota
	// ChapterPending は、まだできていないかもしれない（404 か、通信の失敗・読めない応答）。待つ。
	ChapterPending
	// ChapterAbsent は、待っても入らない（空（200 で 0 件）か、放送回の一覧に eventId が無い）。待たない。
	ChapterAbsent
)

// AiringChapterWait は、待っている放送回の今の様子（Job.ChapterWaits の値）。
type AiringChapterWait int

const (
	// AiringChapterPending は、まだ取れていない（次にチャプターの API を引くときに引く）。
	AiringChapterPending AiringChapterWait = 1
	// AiringChapterReady は、チャプターの API で取れるようになった（チャプターの取り直しで保存したファイルに書き込む）。
	AiringChapterReady AiringChapterWait = 2
)

const (
	// ChapterCheckInterval は、待っているジョブがチャプターの API を引く間隔（保存から数える。設定にしない決め打ちの値。ADR 0027）。
	ChapterCheckInterval = 30 * time.Minute
	// ChapterCheckFor は、待つのを打ち切る時刻を、最後の放送回の終了からどれだけ後にするか（設定にしない決め打ちの値。ADR 0027）。
	ChapterCheckFor = 6 * time.Hour
)

// ChapterStatus は、ジョブのチャプターの値と、待っている間の次の時刻・待っている放送回（store が jobs と job_airings に書く）。
type ChapterStatus struct {
	Chapters Chapters
	CheckAt  time.Time
	Waits    map[string]AiringChapterWait
}

// ChapterCutoff は、ジョブ j がチャプターを待つのを打ち切る時刻（最後の放送回の終了の ChapterCheckFor 後）。
func (j Job) ChapterCutoff() time.Time {
	return j.LastEnd().Add(ChapterCheckFor)
}

// ChaptersAfterSave は、保存したジョブ j のチャプターの値を決める（保存のたびに決め直す。ADR 0027）。airings は保存した
// 放送回、fetched はその並びでチャプターを取った結果。fetched が nil なら取りに行っていない（値なし）。now は保存した時刻。
//
//   - 全部取れたら入った。
//   - まだできていないかもしれない放送回（ChapterPending）があり、打ち切り（ChapterCutoff）と録音期限の前なら待っている。
//     次に API を引く時刻は now の ChapterCheckInterval 後（打ち切りより後なら打ち切りの時刻）。
//   - それ以外（待たない理由で取れなかった放送回がある、打ち切りか録音期限を過ぎた）は入らなかった。
func ChaptersAfterSave(j Job, airings []Airing, fetched []ChapterFetch, now time.Time) ChapterStatus {
	if fetched == nil {
		return ChapterStatus{}
	}
	waits := map[string]AiringChapterWait{}
	all := true
	for i, f := range fetched {
		if f != ChapterGot {
			all = false
		}
		if f == ChapterPending && i < len(airings) {
			waits[airings[i].Key()] = AiringChapterPending
		}
	}
	switch {
	case all:
		return ChapterStatus{Chapters: ChaptersAdded}
	case len(waits) > 0 && now.Before(j.ChapterCutoff()) && now.Before(j.Deadline):
		return ChapterStatus{Chapters: ChaptersWaiting, CheckAt: nextChapterCheck(j, now), Waits: waits}
	}
	return ChapterStatus{Chapters: ChaptersNotAdded}
}

// nextChapterCheck は、次にチャプターの API を引く時刻（now の ChapterCheckInterval 後。打ち切りより後なら打ち切りの時刻）。
func nextChapterCheck(j Job, now time.Time) time.Time {
	next, cutoff := now.Add(ChapterCheckInterval), j.ChapterCutoff()
	if next.After(cutoff) {
		return cutoff
	}
	return next
}

// ChapterCheckResult は、待っているジョブのチャプターの API を引いた後にどうするか（CheckChapters が返す）。
type ChapterCheckResult int

const (
	// ChapterKeepWaiting は待ち続ける（ChapterStatus の時刻に次を引く）。
	ChapterKeepWaiting ChapterCheckResult = iota
	// ChapterWriteNow は、チャプターの取り直しで、保存したファイルにチャプターを書き込む（試行は起こさない。ADR 0033）。
	ChapterWriteNow
	// ChapterGiveUp は、待つのをやめて入らなかったにする。
	ChapterGiveUp
)

// CheckChapters は、待っているジョブ j（Job.ChapterWaits）のうち、まだ取れていない放送回のチャプターを引いた結果 results
// （Airing.Key → 結果。引かなかった放送回は持たない）から、次にどうするかと、待ち続けるときの値を決める（ADR 0027）。
//
//   - 録音期限を過ぎていれば、待つのをやめる（ADR 0027 の待つかの決め方のまま。ADR 0033）。
//   - 取れた放送回は AiringChapterReady にし、待っても入らない（ChapterAbsent）ものは待つのをやめる。
//   - まだ取れていない放送回が無くなれば、取れるようになった放送回があれば書き込み、無ければ待つのをやめる。
//   - 打ち切り（ChapterCutoff）を過ぎていれば、取れるようになった放送回が 1 つでもあれば書き込み、無ければ待つのをやめる。
//     書き込みに入るときの ChapterStatus は、書き込みの前の取得で「取れていたはずの放送回」を決める待ちの様子
//     （Waits。取れた放送回は AiringChapterReady、待っても入らない放送回は持たない。DecideChapterWrite。ADR 0038）を返す。
//   - 取れるようになった放送回が無いまま打ち切りを過ぎていても、書き込みのやり直しの間（Job.inChapterRetry。打ち切りの後、
//     ChapterRetryLimit の前）は、まだ取れていない放送回があれば待ち続け、次に引く時刻を NextChapterRetry にする（ADR 0038）。
//     打ち切りより前に始まったやり直しは、次に引く時刻が打ち切りを超えないので、待っていた放送回が打ち切りまで取れないままなら
//     打ち切りの時点で諦める（ADR 0038 の帰結）。
//   - それ以外は待ち続け、次に引く時刻を now の ChapterCheckInterval 後（打ち切りより後なら打ち切りの時刻）にする。
func CheckChapters(j Job, results map[string]ChapterFetch, now time.Time) (ChapterCheckResult, ChapterStatus) {
	if !now.Before(j.Deadline) {
		return ChapterGiveUp, ChapterStatus{Chapters: ChaptersNotAdded}
	}
	waits := map[string]AiringChapterWait{}
	pending, ready := false, false
	for key, w := range j.ChapterWaits {
		if w == AiringChapterPending {
			if r, ok := results[key]; ok {
				switch r {
				case ChapterGot:
					w = AiringChapterReady
				case ChapterAbsent:
					continue
				}
			}
		}
		waits[key] = w
		pending = pending || w == AiringChapterPending
		ready = ready || w == AiringChapterReady
	}
	if !pending || !now.Before(j.ChapterCutoff()) {
		if ready {
			return ChapterWriteNow, ChapterStatus{Chapters: ChaptersWaiting, Waits: waits}
		}
		if pending && j.inChapterRetry(now) {
			// 書き込みのやり直しの間（打ち切りの後、上限の前）。待ちの放送回が書き込みの前に一時的に取れなかったまま
			// （待ちの様子は書き換えない）なので、上限まで諦めずに引き直す（ADR 0038）。
			return ChapterKeepWaiting, ChapterStatus{Chapters: ChaptersWaiting, CheckAt: NextChapterRetry(j, now), Waits: waits}
		}
		return ChapterGiveUp, ChapterStatus{Chapters: ChaptersNotAdded}
	}
	return ChapterKeepWaiting, ChapterStatus{Chapters: ChaptersWaiting, CheckAt: nextChapterCheck(j, now), Waits: waits}
}

// inChapterRetry は、待っているジョブ j が、打ち切り（ChapterCutoff）の後の書き込みのやり直しの間か（now が打ち切り以後で
// やり直しの上限（ChapterRetryLimit）と録音期限の前、かつ次に引く時刻が打ち切りより後）。打ち切りを待つだけの間は次に引く時刻が
// 打ち切りを超えない（nextChapterCheck）ので、次に引く時刻が打ち切りより後なら、書き込みのやり直しで置いた時刻（NextChapterRetry）
// と分かる。新しい印や列は足さない。打ち切りより前に始まったやり直しは、次に引く時刻が打ち切りを超えないので、ここでは真に
// ならない（打ち切りまで取れないままなら打ち切りの時点で諦める。ADR 0038 の帰結）。
func (j Job) inChapterRetry(now time.Time) bool {
	return !now.Before(j.ChapterCutoff()) && now.Before(j.ChapterRetryLimit()) && now.Before(j.Deadline) && j.ChapterCheckAt.After(j.ChapterCutoff())
}

// ChapterRetryFor は、書き込みのやり直し（取れていたはずの放送回が書き込みの前に一時的に取れなかったときに、書かずに待ちを残して
// やり直すこと）をやめる時刻を、待つのを打ち切る時刻（ChapterCutoff）からどれだけ後にするか。待っている間の打ち切りと同じ長さ
// （最後の放送回の終了から 12 時間。設定にしない決め打ちの値。ADR 0038）。
const ChapterRetryFor = ChapterCheckFor

// ChapterRetryLimit は、ジョブ j の書き込みのやり直しの上限の時刻（ChapterCutoff の ChapterRetryFor 後）。
func (j Job) ChapterRetryLimit() time.Time {
	return j.ChapterCutoff().Add(ChapterRetryFor)
}

// NextChapterRetry は、書き込みのやり直しで次にチャプターの API を引く時刻（now の ChapterCheckInterval 後。やり直しの上限より
// 後なら上限の時刻。ADR 0038）。
func NextChapterRetry(j Job, now time.Time) time.Time {
	next, limit := now.Add(ChapterCheckInterval), j.ChapterRetryLimit()
	if next.After(limit) {
		return limit
	}
	return next
}

// ChapterWriteVerdict は、書き込みの前にチャプターを取り直した結果から決めた、書き込みの扱い（DecideChapterWrite が返す）。
type ChapterWriteVerdict int

const (
	// ChapterWriteProceed は、取れた分を書く（1 つも取れなければ書かない。ChaptersAfterWrite・ChapterNotFetched の決まり）。
	ChapterWriteProceed ChapterWriteVerdict = iota
	// ChapterWriteRetry は、書かずに待ちを残し、次の見回りでもう一度書き込みに入る（書き込みのやり直し。履歴には残さない）。
	ChapterWriteRetry
	// ChapterWriteSkip は、書かない（ファイルは前のまま）。履歴は取れなかったので書かなかった。やり直さない。
	ChapterWriteSkip
)

// DecideChapterWrite は、保存したファイルへの書き込みの前に、保存した放送回 j.SavedAirings() の並びでチャプターを取り直した
// 結果 fetched から、書くか・やり直すか・書かずに終えるかを決める（ADR 0038）。
//
// 取れていたはずの放送回が 1 つでも一時的に取れなかった（ChapterPending）と、書くとファイルに入っていたその放送回のチャプターが
// 消えるか、取れるようになったチャプターが入らないので、書かない。取れていたはずの放送回は by で決まる。
//   - ChapterWriteByRefetch: 待ち waits（CheckChapters が返した Waits。nil なら j.ChapterWaits）に無い放送回（保存のときに待たなかった
//     放送回、待っても入らなかった放送回）と、AiringChapterReady の放送回。まだ取れていない（AiringChapterPending のまま）
//     放送回は含めない。
//   - ChapterWriteByRedo: j.Chapters が入ったときだけ、保存した全部の放送回。それ以外は無し（取れた分を書く）。
//
// 書かないときは、チャプターの取り直しなら now がやり直しの上限（Job.ChapterRetryLimit）より前の間はやり直し
// （ChapterWriteRetry）、上限以後は書かずに諦める（ChapterWriteSkip）。チャプターの入れ直しはやり直さない
// （ChapterWriteSkip。人が頼み直せる）。
func DecideChapterWrite(j Job, by ChapterWriteBy, waits map[string]AiringChapterWait, fetched []ChapterFetch, now time.Time) ChapterWriteVerdict {
	if waits == nil {
		waits = j.ChapterWaits
	}
	airings := j.SavedAirings()
	blocked := false
	for i, f := range fetched {
		if f != ChapterPending || i >= len(airings) {
			continue
		}
		switch by {
		case ChapterWriteByRedo:
			blocked = blocked || j.Chapters == ChaptersAdded
		default:
			w, waited := waits[airings[i].Key()]
			blocked = blocked || !waited || w == AiringChapterReady
		}
	}
	switch {
	case !blocked:
		return ChapterWriteProceed
	case by == ChapterWriteByRefetch && now.Before(j.ChapterRetryLimit()):
		return ChapterWriteRetry
	}
	return ChapterWriteSkip
}

// ChaptersAfterWrite は、保存したファイルにチャプターを書き込む前に、保存した放送回ごとにチャプターを取った結果 fetched から、
// ジョブのチャプターの値を決める（ADR 0027 の決まり。待ちは起こさない。ADR 0033）。全部取れたら入った、それ以外は入らなかった。
func ChaptersAfterWrite(fetched []ChapterFetch) Chapters {
	if len(fetched) == 0 {
		return ChaptersNotAdded
	}
	for _, f := range fetched {
		if f != ChapterGot {
			return ChaptersNotAdded
		}
	}
	return ChaptersAdded
}

// ChapterParts は、保存したファイルの放送回 airings（Job.SavedAirings）のパートの長さを返す（連結したファイルで、2 つ目以降の
// パートのチャプターをずらす量。ADR 0033）。保存のときに DB に残した長さ lengths（Airing.Key → 長さ）が全部の放送回にあれば
// それを、1 つでも無ければ（この決定より前に保存したファイル）、全部を番組表の放送時間（終了 − 開始）で代える。
func ChapterParts(airings []Airing, lengths map[string]time.Duration) []time.Duration {
	out := make([]time.Duration, len(airings))
	stored := len(lengths) > 0
	for i, a := range airings {
		d, ok := lengths[a.Key()]
		if !ok {
			stored = false
		}
		out[i] = d
	}
	if !stored {
		for i, a := range airings {
			out[i] = a.End.Sub(a.Start)
		}
	}
	return out
}

// ChapterWriteBy は、チャプターの書き込みを誰が起こしたか（チャプターの書き込みの履歴の「誰が」）。
type ChapterWriteBy string

const (
	// ChapterWriteByRefetch は、rajikura が自分で行うチャプターの取り直し。
	ChapterWriteByRefetch ChapterWriteBy = "refetch"
	// ChapterWriteByRedo は、人が指示するチャプターの入れ直し（API の POST /api/jobs/{id}/chapters・/api/jobs/chapters から）。
	ChapterWriteByRedo ChapterWriteBy = "redo"
)

// ChapterWriteResult は、チャプターの書き込みの結果。
type ChapterWriteResult string

const (
	// ChapterWritten は、保存したファイルにチャプターを書き込んだ。
	ChapterWritten ChapterWriteResult = "written"
	// ChapterNotFetched は、チャプターを取れなかったので書かなかった。
	ChapterNotFetched ChapterWriteResult = "not_fetched"
	// ChapterWriteFailed は、書き込めなかった（保存先にファイルが無い、読めない箱の並び、音声のサンプルが元と一致しない、
	// 書けないなど。元のファイルは残る）。
	ChapterWriteFailed ChapterWriteResult = "failed"
)

// ChapterWrite は、チャプターの書き込みの履歴の 1 行（書き込みのたびに 1 行残す。試行の履歴とは別。ADR 0033）。
type ChapterWrite struct {
	ID    int64
	JobID int64
	At    time.Time
	By    ChapterWriteBy
	// Result は結果。Count は書き込んだチャプターの件数（ChapterWritten のときだけ）、Error は書き込めなかった理由
	// （ChapterWriteFailed のときだけ）。
	Result ChapterWriteResult
	Count  int
	Error  string
}

// ErrChapterRedoLive は、ライブで録るジョブなのでチャプターを入れ直せないこと（CheckChapterRedo が返す。ライブ録音のファイルには
// チャプターを入れない。ADR 0027）。errors.Is で見分ける。done でないジョブは ErrInvalidTransition を包んで返す。
var ErrChapterRedoLive = errors.New("ライブ録音のジョブにはチャプターを入れ直せない")

// CheckChapterRedo は、ジョブ 1 件の指定のチャプターの入れ直し（用語集。ADR 0033）を受け付けるかを確かめ、受け付けなければ理由の
// エラーを返す。受け付けるのは、タイムフリーから録った done のジョブ（Job.Live が false）。チャプターの値は問わない（入ったの
// ジョブも、ファイルのチャプターを置き換える）。録音期限も問わない（音声は取らず、チャプターの API だけを引くため）。
// done でなければ ErrInvalidTransition、ライブで録るジョブなら ErrChapterRedoLive を包む。internal/store の RequestChapterRedo・
// DueChapterRedos と internal/server の chaptersRedoable が、食い違わないようにこれを使う。
func CheckChapterRedo(j Job) error {
	switch {
	case j.State != StateDone:
		return fmt.Errorf("%w: %s にはチャプターを入れ直せない", ErrInvalidTransition, j.State)
	case j.Live():
		return ErrChapterRedoLive
	}
	return nil
}

// ChapterRedoInAll は、まとめてのチャプターの入れ直し（rajikura chapters --all）が当たるジョブか。CheckChapterRedo で受け付ける
// ジョブのうち、チャプターの値が取りに行っていない（値なし）か入らなかったのもの。入った・待っているのジョブは飛ばす。放送日では
// 絞らない（ADR 0033）。internal/store の RequestChapterRedoAll は、これで当たるジョブを選ぶ。
func ChapterRedoInAll(j Job) bool {
	return CheckChapterRedo(j) == nil && (j.Chapters == ChaptersNone || j.Chapters == ChaptersNotAdded)
}

// ChaptersAfterFailedWrite は、保存したファイルにチャプターを書き込めなかった（ファイルは前のまま）ときの、ジョブのチャプターの値
// （ADR 0027 の決まり）。前が入ったなら、ファイルのチャプターは前のままなので入ったのまま。それ以外（待っている・入らなかった・
// 値なし）は入らなかった。
func ChaptersAfterFailedWrite(prev Chapters) Chapters {
	if prev == ChaptersAdded {
		return ChaptersAdded
	}
	return ChaptersNotAdded
}
