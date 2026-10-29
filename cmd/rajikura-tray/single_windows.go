//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// singleMutexName は、トレイの二重起動を防ぐ名前付き mutex の名前。Local\ の名前空間なので、ログオンのセッション（ユーザー）ごとに 1 つになる。
const singleMutexName = `Local\rajikura-tray`

// acquireSingle は名前付き mutex を取る。すでに誰かが持っていれば ok は false（2 つ目のトレイ）。
// release は、プロセスの終わりまで持つなら呼ばなくてよい（終わると Windows が放す）。
func acquireSingle(name string) (release func(), ok bool, err error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			// 既存の mutex を開けたときも、ハンドルは返る。
			if h != 0 {
				windows.CloseHandle(h)
			}
			return func() {}, false, nil
		}
		return nil, false, err
	}
	return func() { windows.CloseHandle(h) }, true, nil
}
