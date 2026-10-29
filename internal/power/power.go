// Package power は、録音の試行の間だけ OS にスリープしないよう伝え、ライブで録るジョブの前にスリープ中の PC を
// 起こすタイマーを掛ける。
//
// # スリープしないよう伝える（Acquire）
//
// Windows では PowerCreateRequest / PowerSetRequest(PowerRequestSystemRequired) で電源要求を掛ける。画面は点けたままにしない
// （PowerRequestDisplayRequired は使わない）。人がフタを閉じたときや、スリープを選んだときは止めない。
// Windows 以外では何もしない。
//
// # スリープ中の PC を起こす（Waker）
//
// Windows では、SetWaitableTimer を fResume=TRUE・UTC の絶対時刻で掛ける。タスク スケジューラ（WakeToRun）は使わない
// （スリープ中も serve のプロセスは残るので、プロセスの外に状態を持たない）。タイマーを掛けるのは OS スレッドに固定した
// （runtime.LockOSThread）専用の goroutine で、Close するまでそのスレッドを終わらせない。SetWaitableTimer（Ex でない方）も、
// 呼んだスレッドが終わるとタイマーが取り消されるかは未確認で、スレッドの固定は取り消される前提で備えるもの。
// 起こせないと分かったとき（SetWaitableTimer が成功して GetLastError が ERROR_NOT_SUPPORTED、または有効な電源プランの
// 「スリープ解除タイマーの許可」の AC か DC の値が 1（有効）でない）は、Set が理由を返す。
// Windows 以外では何もしない（タイマーは鳴らない）。
//
// いつ掛けるか（どのジョブのために、何分前に）と、起きた後に「スリープしない」を掛けるのは internal/app のライブの録音役が決める。
package power

import (
	"errors"
	"time"
)

// Reason は電源要求に付ける理由の文字列（管理者のターミナルの powercfg /requests に出るとされる。未確認）。
const Reason = "rajikura: 録音中"

// WakeReason は、タイマーで起きた後、ライブ録音の試行が始まるまでの間に掛ける電源要求の理由の文字列。
const WakeReason = "rajikura: ライブ録音の前に起こした"

// Acquire はスリープしないよう伝え、外す関数を返す。release は何回呼んでもよい。
// 伝えられなかったときは誤りを返す（release は nil でなく、呼んでも何も起きない）。
func Acquire(reason string) (release func(), err error) {
	return acquire(reason)
}

// 起こせないと分かった理由。Waker.Set が返す problems は、どれかを errors.Is で見分けられる（値を添えて包むことがある）。
var (
	// ErrWakeNotSupported は、SetWaitableTimer が成功を返し、GetLastError が ERROR_NOT_SUPPORTED だったこと
	// （この PC はタイマーでスリープから起きられない）。
	ErrWakeNotSupported = errors.New("この PC はタイマーでスリープから起きられない（SetWaitableTimer が ERROR_NOT_SUPPORTED を返した）")
	// ErrWakeTimersDisallowed は、有効な電源プランの「スリープ解除タイマーの許可」の AC か DC の値が 1（有効）でないこと。
	ErrWakeTimersDisallowed = errors.New("電源プランの「スリープ解除タイマーの許可」が有効でない")
)

// Waker は、スリープ中の PC を決めた時刻に起こすタイマー（1 本）。メソッドはどの goroutine から呼んでもよい。
type Waker interface {
	// Set は at（絶対時刻）に起こすタイマーを掛ける。掛けてあれば掛け直す（タイマーは 1 本だけ）。
	// 掛けたが起こせないと分かった理由を problems に返す。掛けられなかったときは err を返す。
	Set(at time.Time) (problems []error, err error)
	// Cancel は掛けたタイマーを取り消す。掛けていなければ何もしない。
	Cancel() error
	// Fired は、タイマーが鳴ったときに、鳴ったタイマーの起こす時刻（Set の at）を送る。鳴らない Waker は nil を返してよい。
	Fired() <-chan time.Time
	// Close はタイマーを取り消して片付ける。Close の後の Set は誤りを返す。
	Close()
}

// NewWaker は OS のタイマーを使う Waker を作る。Windows 以外では何もしない Waker を返す。
func NewWaker() Waker {
	return newWaker()
}
