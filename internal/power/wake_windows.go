//go:build windows

package power

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows に waitable timer と電源プランの設定を読むラッパーが無いので、kernel32.dll と powrprof.dll から引く。
var (
	procCreateWaitableTimer = kernel32.NewProc("CreateWaitableTimerW")
	procSetWaitableTimer    = kernel32.NewProc("SetWaitableTimer")
	procCancelWaitableTimer = kernel32.NewProc("CancelWaitableTimer")

	powrprof                  = windows.NewLazySystemDLL("powrprof.dll")
	procPowerGetActiveScheme  = powrprof.NewProc("PowerGetActiveScheme")
	procPowerReadACValueIndex = powrprof.NewProc("PowerReadACValueIndex")
	procPowerReadDCValueIndex = powrprof.NewProc("PowerReadDCValueIndex")
)

var (
	// guidSleepSubgroup は GUID_SLEEP_SUBGROUP（238C9FA8-0AAD-41ED-83F4-97BE242C8F20）。
	guidSleepSubgroup = windows.GUID{Data1: 0x238C9FA8, Data2: 0x0AAD, Data3: 0x41ED, Data4: [8]byte{0x83, 0xF4, 0x97, 0xBE, 0x24, 0x2C, 0x8F, 0x20}}
	// guidAllowRTCWake は GUID_ALLOW_RTC_WAKE（「スリープ解除タイマーの許可」。BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D）。
	// 値は 0=無効、1=有効、2=重要なものだけ。
	guidAllowRTCWake = windows.GUID{Data1: 0xBD3B718A, Data2: 0x0680, Data3: 0x4D9D, Data4: [8]byte{0x8A, 0xB2, 0xE1, 0xD2, 0xB4, 0xAC, 0x80, 0x6D}}
)

var errWakerClosed = errors.New("スリープから起こすタイマーはもう片付けた")

type wakeOp int

const (
	wakeSet wakeOp = iota
	wakeCancel
	wakeClose
)

type wakeReq struct {
	op    wakeOp
	at    time.Time
	reply chan wakeReply
}

type wakeReply struct {
	problems []error
	err      error
}

// winWaker は SetWaitableTimer の Waker。タイマーの作成・掛け外し・待ちは、OS スレッドに固定した loop の goroutine だけが行い、
// ほかの goroutine は reqs に頼みを積んで event で知らせる。
type winWaker struct {
	mu      sync.Mutex // 頼みを 1 つずつにする（reqs は 1 つ分だけ積める）
	closed  bool
	initErr error
	event   windows.Handle
	reqs    chan wakeReq
	fired   chan time.Time
}

func newWaker() Waker {
	w := &winWaker{reqs: make(chan wakeReq, 1), fired: make(chan time.Time, 1)}
	ready := make(chan struct{})
	go w.loop(ready)
	<-ready
	return w
}

func (w *winWaker) Set(at time.Time) ([]error, error) {
	r := w.do(wakeReq{op: wakeSet, at: at})
	return r.problems, r.err
}

func (w *winWaker) Cancel() error { return w.do(wakeReq{op: wakeCancel}).err }

func (w *winWaker) Fired() <-chan time.Time { return w.fired }

func (w *winWaker) Close() { w.do(wakeReq{op: wakeClose}) }

func (w *winWaker) do(req wakeReq) wakeReply {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.initErr != nil {
		return wakeReply{err: w.initErr}
	}
	if w.closed {
		if req.op == wakeClose {
			return wakeReply{}
		}
		return wakeReply{err: errWakerClosed}
	}
	req.reply = make(chan wakeReply, 1)
	w.reqs <- req
	if err := windows.SetEvent(w.event); err != nil {
		<-w.reqs // loop は気づかないので、積んだ頼みを下ろす
		return wakeReply{err: fmt.Errorf("SetEvent: %w", err)}
	}
	r := <-req.reply
	if req.op == wakeClose {
		w.closed = true
	}
	return r
}

// loop はタイマーを持つ goroutine の中身。OS スレッドに固定し、Close されるまで戻らない（固定を外さずに戻るので、
// スレッドごと終わる）。
func (w *winWaker) loop(ready chan<- struct{}) {
	runtime.LockOSThread()
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		w.initErr = fmt.Errorf("CreateEvent: %w", err)
		close(ready)
		return
	}
	w.event = ev
	// 自動リセット（待ちが返ると非シグナルに戻る）・名前なしのタイマー。
	h, _, callErr := procCreateWaitableTimer.Call(0, 0, 0)
	timer := windows.Handle(h)
	var timerErr error
	if h == 0 {
		timerErr = fmt.Errorf("CreateWaitableTimerW: %w", callErr)
	}
	close(ready)

	var armed time.Time // 掛けている起こす時刻（零値なら掛けていない）
	for {
		handles := []windows.Handle{ev}
		if timer != 0 {
			handles = append(handles, timer)
		}
		r, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil {
			time.Sleep(time.Second) // ふつうは起きない。回り続けないように待つ
			continue
		}
		switch r {
		case windows.WAIT_OBJECT_0 + 1:
			if !armed.IsZero() {
				at := armed
				armed = time.Time{}
				select {
				case <-w.fired: // 読まれていない古い知らせは捨てる
				default:
				}
				w.fired <- at
			}
		case windows.WAIT_OBJECT_0:
			var req wakeReq
			select {
			case req = <-w.reqs:
			default:
				continue
			}
			var rep wakeReply
			switch req.op {
			case wakeSet:
				if timerErr != nil {
					rep.err = timerErr
					break
				}
				rep.problems, rep.err = setTimer(timer, req.at)
				if rep.err == nil {
					armed = req.at
				}
			case wakeCancel:
				if timer != 0 {
					if r, _, callErr := procCancelWaitableTimer.Call(uintptr(timer)); r == 0 {
						rep.err = fmt.Errorf("CancelWaitableTimer: %w", callErr)
					}
				}
				armed = time.Time{}
			case wakeClose:
				if timer != 0 {
					procCancelWaitableTimer.Call(uintptr(timer))
					windows.CloseHandle(timer)
				}
				windows.CloseHandle(ev)
				req.reply <- rep
				return
			}
			req.reply <- rep
		}
	}
}

// setTimer は timer を at（UTC の絶対時刻）に、スリープから起こす指定（fResume=TRUE）で掛ける。
func setTimer(timer windows.Handle, at time.Time) ([]error, error) {
	ft := windows.NsecToFiletime(at.UnixNano())
	// 正の値は絶対時刻（FILETIME。1601 年からの 100 ナノ秒、UTC）。
	due := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	r, _, callErr := procSetWaitableTimer.Call(uintptr(timer), uintptr(unsafe.Pointer(&due)), 0, 0, 0, 1)
	if r == 0 {
		return nil, fmt.Errorf("SetWaitableTimer: %w", callErr)
	}
	var problems []error
	// 成功しても、復帰に対応しないシステムでは GetLastError が ERROR_NOT_SUPPORTED を返す（Go の syscall は呼ぶ前に
	// 最後のエラーを 0 にするので、前の呼び出しの値は残らない）。
	if errors.Is(callErr, windows.ERROR_NOT_SUPPORTED) {
		problems = append(problems, ErrWakeNotSupported)
	}
	if err := checkAllowRTCWake(); err != nil {
		problems = append(problems, err)
	}
	return problems, nil
}

// checkAllowRTCWake は、有効な電源プランの「スリープ解除タイマーの許可」の AC と DC の値を読み、どちらかが 1（有効）でなければ
// ErrWakeTimersDisallowed を包んで返す。読めなければその誤りを返す。
func checkAllowRTCWake() error {
	var scheme *windows.GUID
	if r, _, _ := procPowerGetActiveScheme.Call(0, uintptr(unsafe.Pointer(&scheme))); r != 0 {
		return fmt.Errorf("有効な電源プランを読めないので、「スリープ解除タイマーの許可」を確かめられない（PowerGetActiveScheme: %w）", syscall.Errno(r))
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(scheme))))
	read := func(proc *windows.LazyProc) (uint32, error) {
		var v uint32
		r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(scheme)), uintptr(unsafe.Pointer(&guidSleepSubgroup)),
			uintptr(unsafe.Pointer(&guidAllowRTCWake)), uintptr(unsafe.Pointer(&v)))
		if r != 0 {
			return 0, fmt.Errorf("「スリープ解除タイマーの許可」を読めない（%s: %w）", proc.Name, syscall.Errno(r))
		}
		return v, nil
	}
	ac, err := read(procPowerReadACValueIndex)
	if err != nil {
		return err
	}
	dc, err := read(procPowerReadDCValueIndex)
	if err != nil {
		return err
	}
	if ac != 1 || dc != 1 {
		return fmt.Errorf("%w（電源に接続: %d、バッテリー: %d。1 が有効）", ErrWakeTimersDisallowed, ac, dc)
	}
	return nil
}
