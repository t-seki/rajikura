package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAwake は KeepAwake の代わり。掛けた回数と外した回数を数える。
type fakeAwake struct {
	mu       sync.Mutex
	acquired int
	released int
	err      error
}

func (f *fakeAwake) acquire(reason string) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired++
	if f.err != nil {
		return nil, f.err
	}
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.released++
	}, nil
}

func (f *fakeAwake) held() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired - f.released
}

func (f *fakeAwake) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired, f.released
}

// 試行の間だけ掛かり、試行が終われば外れる。
func TestKeepAwakeDuringAttemptOnly(t *testing.T) {
	e := newFlowEnv(t)
	fa := &fakeAwake{}
	e.app.KeepAwake = fa.acquire
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ := fa.counts(); a != 0 {
		t.Fatalf("試行の前に掛けている: %d", a)
	}
	var mu sync.Mutex
	maxHeld := 0
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(*http.Request) {
			mu.Lock()
			maxHeld = max(maxHeld, fa.held())
			mu.Unlock()
		}
	})
	if !e.runDue() {
		t.Fatal("ジョブが実行されない")
	}
	mu.Lock()
	defer mu.Unlock()
	if maxHeld != 1 {
		t.Errorf("試行の間の掛かり数 = %d, want 1", maxHeld)
	}
	if a, r := fa.counts(); a != 1 || r != 1 {
		t.Errorf("acquired=%d released=%d, want 1 と 1", a, r)
	}
}

// 試行が誤りで終わっても外れる。
func TestKeepAwakeReleasedOnFailure(t *testing.T) {
	e := newFlowEnv(t)
	fa := &fakeAwake{}
	e.app.KeepAwake = fa.acquire
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.media.mu.Lock()
	e.media.offset = time.Hour // 検証で落とす
	e.media.mu.Unlock()
	e.runDue()
	j := e.job(flatShow.Name)
	if len(e.attempts(j.ID)) == 0 || j.State == "done" {
		t.Fatalf("試行が誤りで終わっていない: %+v", j)
	}
	if a, r := fa.counts(); a != 1 || r != 1 {
		t.Errorf("acquired=%d released=%d, want 1 と 1", a, r)
	}
}

// ctx が取り消されても外れる。
func TestKeepAwakeReleasedOnCancel(t *testing.T) {
	e := newFlowEnv(t)
	fa := &fakeAwake{}
	e.app.KeepAwake = fa.acquire
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			cancel()
			<-r.Context().Done()
		}
	})
	if _, err := e.app.RunDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if a, r := fa.counts(); a != 1 || r != 1 {
		t.Errorf("acquired=%d released=%d, want 1 と 1", a, r)
	}
}

// 掛けられなかったときは警告を出し、試行を続ける。
func TestKeepAwakeFailureWarnsAndContinues(t *testing.T) {
	e := newFlowEnv(t)
	fa := &fakeAwake{err: errors.New("boom")}
	e.app.KeepAwake = fa.acquire
	buf := e.useLogBuffer()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	for e.runDue() {
	}
	if a, _ := fa.counts(); a == 0 {
		t.Fatal("掛けようとしていない")
	}
	if !strings.Contains(buf.String(), "スリープを止められない") || !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("警告が出ていない: %s", buf.String())
	}
	if j := e.job(flatShow.Name); j.State != "done" {
		t.Errorf("試行が続いていない: %+v", j)
	}
}
