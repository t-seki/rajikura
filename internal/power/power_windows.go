//go:build windows

package power

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows に PowerCreateRequest のラッパーが無いので、kernel32.dll から引く。
var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procSetRequest    = kernel32.NewProc("PowerSetRequest")
	procClearRequest  = kernel32.NewProc("PowerClearRequest")
)

const (
	// POWER_REQUEST_CONTEXT_VERSION と POWER_REQUEST_CONTEXT_SIMPLE_STRING（winnt.h）。
	powerRequestContextVersion      = 0
	powerRequestContextSimpleString = 0x1
	// POWER_REQUEST_TYPE の PowerRequestSystemRequired。
	powerRequestSystemRequired = 1
)

// reasonContext は REASON_CONTEXT。Reason は共用体で、SimpleReasonString（LPWSTR）のほかに最大の Detailed
// （HMODULE・ULONG・ULONG・LPWSTR*）があるので、その大きさまで空けておく。
type reasonContext struct {
	Version uint32
	Flags   uint32
	Simple  *uint16
	_       [3]uintptr
}

func acquire(reason string) (func(), error) {
	text, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return func() {}, err
	}
	ctx := reasonContext{Version: powerRequestContextVersion, Flags: powerRequestContextSimpleString, Simple: text}
	h, _, callErr := procCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	if windows.Handle(h) == windows.InvalidHandle || h == 0 {
		return func() {}, fmt.Errorf("PowerCreateRequest: %w", callErr)
	}
	handle := windows.Handle(h)
	if r, _, callErr := procSetRequest.Call(h, powerRequestSystemRequired); r == 0 {
		windows.CloseHandle(handle)
		return func() {}, fmt.Errorf("PowerSetRequest: %w", callErr)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			procClearRequest.Call(h, powerRequestSystemRequired)
			windows.CloseHandle(handle)
		})
	}, nil
}
