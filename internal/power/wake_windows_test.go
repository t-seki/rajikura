//go:build windows

package power

import (
	"testing"
	"time"
)

// OS のタイマーを掛けると、その時刻に鳴って掛けた時刻を知らせる。取り消したら鳴らない。Close の後の Set は誤りになる。
// 鳴らす時刻はすぐ先（起きている PC で鳴るだけで、スリープ中の PC を起こすことは確かめない）。
func TestWakerFiresCancelsAndCloses(t *testing.T) {
	w := NewWaker()
	defer w.Close()

	at := time.Now().Add(200 * time.Millisecond)
	problems, err := w.Set(at)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Logf("起こせない見込みの理由（この PC の設定）: %v", p)
	}
	select {
	case got := <-w.Fired():
		if !got.Equal(at) {
			t.Errorf("鳴った時刻 = %v, want %v", got, at)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("タイマーが鳴らない")
	}

	if _, err := w.Set(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := w.Cancel(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-w.Fired():
		t.Fatalf("取り消したタイマーが鳴った: %v", got)
	case <-time.After(time.Second):
	}

	w.Close()
	w.Close() // 2 回呼んでも何も起きない
	if _, err := w.Set(time.Now().Add(time.Hour)); err == nil {
		t.Error("Close の後の Set が誤りにならない")
	}
}
