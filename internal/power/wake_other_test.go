//go:build !windows

package power

import (
	"testing"
	"time"
)

// Windows 以外の Waker は何もしない（鳴らず、誤りも理由も返さない）。
func TestWakerDoesNothingOutsideWindows(t *testing.T) {
	w := NewWaker()
	problems, err := w.Set(time.Now().Add(time.Hour))
	if err != nil || len(problems) != 0 {
		t.Fatalf("Set = %v, %v", problems, err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatal(err)
	}
	if w.Fired() != nil {
		t.Error("Fired が nil でない")
	}
	w.Close()
}
