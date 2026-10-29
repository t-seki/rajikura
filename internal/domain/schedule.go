package domain

import (
	"errors"
	"fmt"
	"time"
)

const (
	// minTFDelay は最初の試行までに待つ、局の tf_max_delay の下限。
	minTFDelay = 150 * time.Second
	// firstAttemptMargin は最初の試行の余裕。
	firstAttemptMargin = 2 * time.Minute
	// lastAttemptBeforeDeadline は、次の試行が録音期限を過ぎるときに最後の試行を置く、録音期限の前の時間。
	lastAttemptBeforeDeadline = 10 * time.Minute
	// DefaultMaxAttemptsFree は非会員の試行回数の上限の既定。
	//
	// 非会員のタイムフリーには「再生開始から 24 時間・合計 3 時間」の聴取制限がある。
	// ダウンロードがこれに数えられるかは事前検証で分からなかったので、控えめな仮の値にしている。
	DefaultMaxAttemptsFree = 10

	// LiveLead は、ライブ録音で放送の開始のどれだけ前に繋ぎ始めるか（ライブで録るジョブの最初の試行の時刻）。
	LiveLead = time.Minute
	// LiveDeadlineAfter は、ライブ録音の録音期限を放送の終了のどれだけ後にするか。
	LiveDeadlineAfter = 5 * time.Minute
)

// retryIntervals は失敗した後、前の試行から次の試行までの間隔。最後の値を以降繰り返す。
var retryIntervals = []time.Duration{15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour}

// FirstAttemptAt は、タイムフリーから録るジョブの最初の試行の時刻。
// ジョブの最後の放送回の終了時刻 + max(局の tf_max_delay, 150 秒) + 2 分（設定 chapters に依らない。チャプターが取れなければ、
// 保存の後にチャプターの取り直しで入れる。ADR 0027）。
func FirstAttemptAt(lastEnd time.Time, tfMaxDelay time.Duration) time.Time {
	return lastEnd.Add(max(tfMaxDelay, minTFDelay) + firstAttemptMargin)
}

// JobFirstAttemptAt は放送回 airings を持つジョブの最初の試行の時刻。
// ライブで録るジョブ（LiveAirings）は、最初の放送回の開始の LiveLead（1 分）前。それ以外（タイムフリーからの録音）は
// FirstAttemptAt（最後の放送回の終了から数える）。放送回が無ければ FirstAttemptAt にゼロ値を渡したもの。
func JobFirstAttemptAt(airings []Airing, tfMaxDelay time.Duration) time.Time {
	if LiveAirings(airings) {
		first := airings[0].Start
		for _, a := range airings[1:] {
			if a.Start.Before(first) {
				first = a.Start
			}
		}
		return first.Add(-LiveLead)
	}
	return FirstAttemptAt(lastEnd(airings), tfMaxDelay)
}

// RetryPolicy は再試行の決まり（タイムフリーからの録音のジョブだけ）。ライブで録るジョブは録音（入手）を再試行しない。
// 録れた分の保存だけをやり直す決まりは LiveSaveRetryAt（録音期限と非会員の試行回数の上限は当てない）。
type RetryPolicy struct {
	Membership Membership
	// MaxAttemptsFree は非会員の試行回数の上限。0 以下なら DefaultMaxAttemptsFree。
	MaxAttemptsFree int
}

func (p RetryPolicy) maxAttempts() int {
	if p.Membership != MembershipFree {
		return 0
	}
	if p.MaxAttemptsFree <= 0 {
		return DefaultMaxAttemptsFree
	}
	return p.MaxAttemptsFree
}

// NextAttemptAt は試行が失敗した後の次の試行の時刻を返す。次の試行が無ければ false。
//
//   - attempts はこれまでの試行回数（失敗した試行を含む）、lastStart はその最後の試行を始めた時刻、
//     now は失敗が分かった時刻。
//   - 間隔は前の試行から 15 分 → 1 時間 → 3 時間 → 以降 6 時間ごと。
//   - 次の試行が録音期限を過ぎるなら、録音期限の 10 分前に最後の試行を置く。
//     それが前の試行より後でなければ、次の試行は無い。
//   - 録音期限を過ぎていれば、次の試行は無い。
//   - 非会員は試行回数が上限に達したら、次の試行は無い。
func (p RetryPolicy) NextAttemptAt(attempts int, lastStart, deadline, now time.Time) (time.Time, bool) {
	if !now.Before(deadline) {
		return time.Time{}, false
	}
	if m := p.maxAttempts(); m > 0 && attempts >= m {
		return time.Time{}, false
	}
	i := max(attempts-1, 0)
	if i >= len(retryIntervals) {
		i = len(retryIntervals) - 1
	}
	next := lastStart.Add(retryIntervals[i])
	if next.After(deadline) {
		next = deadline.Add(-lastAttemptBeforeDeadline)
		if !next.After(lastStart) {
			return time.Time{}, false
		}
	}
	return next, true
}

const (
	// LiveSaveRetryFor は、ライブで録るジョブの録れた分の保存を、放送の終了（最後の放送回の終了）からどれだけの間、自動で
	// やり直すか（設定にしない決め打ちの値）。
	LiveSaveRetryFor = 24 * time.Hour
	// LiveKeepFor は、保存をやり直しても保存できずに expired にしたライブで録るジョブの作業用フォルダ（録れた分）を、最後の試行の
	// 終わりからどれだけの間残すか（設定にしない決め打ちの値）。
	LiveKeepFor = 30 * 24 * time.Hour
)

// LiveSaveCutoff は、ライブで録るジョブの保存のやり直しの打ち切り（最後の放送回の終了の LiveSaveRetryFor 後）。
func (j Job) LiveSaveCutoff() time.Time {
	return j.LastEnd().Add(LiveSaveRetryFor)
}

// LiveSaveRetryAt は、ライブで録るジョブの試行が保存か検証の段階で失敗した後の、保存だけをやり直す次の試行の時刻を返す。
// 次の試行が無ければ（打ち切りを過ぎた）false。
//
//   - attempts はこれまでの試行回数（失敗した試行を含む）、lastStart はその最後の試行を始めた時刻、cutoff は打ち切り
//     （Job.LiveSaveCutoff）、now は失敗が分かった時刻。
//   - 間隔はタイムフリーの再試行（RetryPolicy）と同じく、前の試行の開始から 15 分 → 1 時間 → 3 時間 → 以降 6 時間ごと。
//   - 次の試行が打ち切りより後になるなら、打ち切りの時刻に最後の試行を置く。
//   - 打ち切りの時刻以降に失敗したなら、次の試行は無い。
//   - 録音期限と非会員の試行回数の上限は当てない（radiko に繋がず、残したセグメントから保存するだけなので）。
//
// 足したら直す: ジョブの状態の移り方
func LiveSaveRetryAt(attempts int, lastStart, cutoff, now time.Time) (time.Time, bool) {
	if !now.Before(cutoff) {
		return time.Time{}, false
	}
	i := min(max(attempts-1, 0), len(retryIntervals)-1)
	next := lastStart.Add(retryIntervals[i])
	if next.After(cutoff) {
		next = cutoff
	}
	return next, true
}

// LiveKeepUntil は、ライブで録るジョブ j の録れた分（作業用フォルダ）を、保存できなかった後も残しておく期限を返す。
// last はジョブの最後の試行（試行の開始が最も新しいもの。無ければ nil）。ライブで録るジョブで、最後の試行が保存か検証の
// 段階で失敗していれば、その試行の終わりの LiveKeepFor 後。それ以外はゼロ値（残すものが無い）。
//
// この期限より前なら、録音期限を過ぎていても手動再試行を受け付け（internal/store の ManualRetry、internal/server の
// Retryable）、期限を過ぎた expired のジョブの作業用フォルダはライブの録音役が消す（internal/app）。
// 足したら直す: ジョブの状態の移り方
func LiveKeepUntil(j Job, last *Attempt) time.Time {
	if !j.Live() || last == nil || (last.FailedStage != StageSave && last.FailedStage != StageVerify) {
		return time.Time{}
	}
	return last.EndedAt.Add(LiveKeepFor)
}

// ManualRetryInTime は、手動再試行を時刻の上で受け付けるか（状態は呼び出し側が見る）。録音期限（Deadline）を過ぎていなければ
// true。過ぎていても、retrying か expired のライブで録るジョブで、録れた分を残しておく期限（LiveKeepUntil）の前なら true
// （保存からやり直す）。last はジョブの最後の試行（無ければ nil）。internal/store の ManualRetry と internal/server の
// Retryable が、食い違わないようにこれを使う。
// 足したら直す: ジョブの状態の移り方
func ManualRetryInTime(j Job, last *Attempt, now time.Time) bool {
	if j.Deadline.After(now) {
		return true
	}
	return (j.State == StateRetrying || j.State == StateExpired) && LiveKeepUntil(j, last).After(now)
}

// 録り直しを断る理由（CheckRerecord が返す）。errors.Is で見分ける。done でないジョブは ErrInvalidTransition を包んで返す。
var (
	// ErrRerecordLive は、ライブで録るジョブなので録り直せないこと（放送は一度きり）。
	ErrRerecordLive = errors.New("ライブ録音は録り直せない")
	// ErrRerecordPastDeadline は、録音期限を過ぎているので録り直せないこと。
	ErrRerecordPastDeadline = errors.New("録音期限を過ぎているので録り直せない")
)

// CheckRerecord は、録り直し（ADR 0025）を受け付けるかを確かめ、受け付けなければ理由のエラーを返す。受け付けるのは、done の
// タイムフリーから録るジョブ（Job.Live が false）で、録音期限の内側（Deadline が now より後）のものだけ。done でなければ
// ErrInvalidTransition、ライブで録るジョブなら ErrRerecordLive、録音期限を過ぎていれば ErrRerecordPastDeadline を包む。
// ライブ録音で録れた分を残している間の例外（ManualRetryInTime）は当てない。internal/store の Rerecord と internal/server の
// Rerecordable が、食い違わないようにこれを使う。
// 足したら直す: ジョブの状態の移り方
func CheckRerecord(j Job, now time.Time) error {
	switch {
	case j.State != StateDone:
		return fmt.Errorf("%w: %s は録り直せない", ErrInvalidTransition, j.State)
	case j.Live():
		return ErrRerecordLive
	case !j.Deadline.After(now):
		return ErrRerecordPastDeadline
	}
	return nil
}
