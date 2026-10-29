package domain

import "time"

// Chapters は、ジョブの保存したファイルにチャプターが入ったか（用語集の「チャプター」「チャプターの取り直し」。ADR 0027）。
type Chapters string

const (
	// ChaptersNone は値なし。取りに行っていない（ライブ録音、設定 chapters がオフ、まだ保存していない、チャプターの取り直しの
	// 前の版で保存した）。
	ChaptersNone Chapters = ""
	// ChaptersAdded は入った（取りに行った全部の放送回で取れた）。
	ChaptersAdded Chapters = "added"
	// ChaptersWaiting は待っている（チャプターがまだできていないかもしれない放送回があり、チャプターの取り直しを待っている。
	// 取り直しの試行の間も含む）。
	ChaptersWaiting Chapters = "waiting"
	// ChaptersNotAdded は入らなかった（待つのを打ち切った、または待たない理由で一部か全部が取れなかった。一部だけ入ったものも含む）。
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
	// AiringChapterReady は、チャプターの API で取れるようになった（チャプターの取り直しで入れる）。
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
	// ChapterRefetchNow は、チャプターの取り直しの試行を起こす。
	ChapterRefetchNow
	// ChapterGiveUp は、待つのをやめて入らなかったにする。
	ChapterGiveUp
)

// CheckChapters は、待っているジョブ j（Job.ChapterWaits）のうち、まだ取れていない放送回のチャプターを引いた結果 results
// （Airing.Key → 結果。引かなかった放送回は持たない）から、次にどうするかと、待ち続けるときの値を決める（ADR 0027）。
//
//   - 録音期限を過ぎていれば、待つのをやめる（録り直せない）。
//   - 取れた放送回は AiringChapterReady にし、待っても入らない（ChapterAbsent）ものは待つのをやめる。
//   - まだ取れていない放送回が無くなれば、取れるようになった放送回があれば取り直し、無ければ待つのをやめる。
//   - 打ち切り（ChapterCutoff）を過ぎていれば、取れるようになった放送回が 1 つでもあれば取り直し（1 回）、無ければ待つのをやめる。
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
			return ChapterRefetchNow, ChapterStatus{Chapters: ChaptersWaiting}
		}
		return ChapterGiveUp, ChapterStatus{Chapters: ChaptersNotAdded}
	}
	return ChapterKeepWaiting, ChapterStatus{Chapters: ChaptersWaiting, CheckAt: nextChapterCheck(j, now), Waits: waits}
}
