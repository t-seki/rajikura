package domain

import (
	"errors"
	"fmt"
)

// State はジョブの状態。
type State string

const (
	// StateWaiting は放送待ち（最初の試行を待っている）。
	StateWaiting State = "waiting"
	// StateRunning は録音中。
	StateRunning State = "running"
	// StateRetrying は再試行待ち（失敗した）。
	StateRetrying State = "retrying"
	// StateDone は完了。
	StateDone State = "done"
	// StateExpired は期限切れ。
	StateExpired State = "expired"
	// StateSkipped は録らない（用語集の「録らない予定」と「録らなかった」）。最初の試行の時刻の前は録らない予定、後は
	// 確定して録らなかったと呼ぶ（Job.Settled。ADR 0019）。rajikura が番組名の食い違いで決める（Show.Hit が HitSkip。
	// ADR 0018）か、人が「録らない」で決める（Job.ByPerson）。値の skipped は、前の版で消した人が操作するジョブのスキップと
	// 同じ文字列を使い直している（jobs.state の CHECK に残っている。migrations/0012_drop_job_skip.sql）。
	StateSkipped State = "skipped"
)

// States はすべての状態。
var States = []State{StateWaiting, StateRunning, StateRetrying, StateDone, StateExpired, StateSkipped}

// ParseState は状態の文字列を読む。
func ParseState(s string) (State, error) {
	for _, st := range States {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("不明な状態 %q", s)
}

// transitions は許される遷移。
//
//	waiting → running            実行を始めた
//	running → done               保存まで完了した。またはチャプターの取り直しで起こした試行が録れなかった（次の試行が無い・
//	                             録音期限を過ぎた。expired にせず、前の保存先のまま done に戻す。ADR 0027）
//	running → retrying           試行が失敗し、録音期限の内側に次の試行がある／起動時の回復（タイムフリーから録るジョブ）。
//	                             ライブで録るジョブ（Job.Live）は、入手（録音）の失敗では retrying にせず、保存か検証の段階で
//	                             失敗したときだけ、保存のやり直しの打ち切り（放送の終了から 24 時間。録音期限を過ぎてもよい）の
//	                             前なら retrying にする。起動時の回復では running のまま残す
//	running → expired            試行が失敗し、次の試行が無い。または録音期限を過ぎた（ライブで録るジョブは、1 つも録れなかった
//	                             とき（入手の段階の失敗）と、録れた分の保存のやり直しを打ち切った後に保存できなかったとき）
//	retrying → running           次の試行を始めた
//	expired → retrying           人が手動で再試行した（試行回数の上限は数え直す。録音期限の内側に限るが、ライブで録るジョブで
//	                             録れた分を作業用フォルダに残している間（LiveKeepUntil の前）は、録音期限を過ぎていてもよい）
//	skipped → retrying           人が手動で再試行した・「録る」を選んだ（確定した録らなかったのジョブを録らせる）
//	done → retrying              人が録り直しを選んだ（タイムフリーから録るジョブで、録音期限の内側に限る。CheckRerecord。
//	                             ADR 0025）。または、チャプターを待っていたジョブのチャプターができたので、録音のループが
//	                             チャプターの取り直しの試行を起こした（受け付ける条件は録り直しと同じ。ADR 0027）
//	waiting → skipped            確定の前の展開が番組名の食い違いで録らないと決めた・最初の試行の時刻を過ぎた展開で
//	                             録らないと決めた・人が「録らない」を選んだ（試行がまだ 1 度も無いもの）
//	skipped → waiting            確定の前の展開が、番組名が直ったので録ると決めた・人が確定の前に「録る」を選んだ
//
// 展開はジョブを skipped で作ることもある（今日より前の放送日では確定した録らなかった、今日と今日から先の放送日では
// 最初の試行の時刻の前なら録らない予定）。確定した skipped からは、retrying へだけ遷移する。
//
// retrying のジョブの手動再試行は状態を変えず、次の試行の時刻と試行回数だけを書き換えるので、遷移表には無い。
// 足したら直す: ジョブの状態の移り方
var transitions = map[State][]State{
	StateWaiting:  {StateRunning, StateSkipped},
	StateRunning:  {StateDone, StateRetrying, StateExpired},
	StateRetrying: {StateRunning},
	StateDone:     {StateRetrying},
	StateExpired:  {StateRetrying},
	StateSkipped:  {StateRetrying, StateWaiting},
}

// CanTransition は from から to への遷移が許されるか。
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ErrInvalidTransition は許されない遷移。errors.Is で見分ける。
var ErrInvalidTransition = errors.New("許されない状態の遷移")

// Transition は from から to への遷移を確かめ、許されなければ ErrInvalidTransition を包んだエラーを返す。
func Transition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s から %s", ErrInvalidTransition, from, to)
	}
	return nil
}
