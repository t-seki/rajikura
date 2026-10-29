package app

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
)

// 録れるジョブが 2 件あるとき、1 回の見回り（pollRound）で 2 件とも実行され、その間に通知の判定が挟まる
// （1 件目の録り終えた知らせが、2 件目の入手が始まる前に届く）。
func TestPollRoundRunsAllDueJobsWithNotificationsBetween(t *testing.T) {
	e := newFlowEnv(t)
	d := e.useDiscord()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	var sentAtStart []int // ジョブごとに最初のセグメントを求められた時点で、届いていた通知の数
	e.fake.set(func(f *fakeRadiko) {
		f.onSegment = func(r *http.Request) {
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			key := name[:10] // 放送日 + 時
			if key == "2026100710" {
				return // ふらっとの 2 つ目の放送回（ジョブは 8 時台の放送回で数える）
			}
			d.mu.Lock()
			n := len(d.got)
			d.mu.Unlock()
			mu.Lock()
			defer mu.Unlock()
			if !seen[key] {
				seen[key] = true
				sentAtStart = append(sentAtStart, n)
			}
		}
	})
	e.app.pollRound(e.ctx)
	for _, j := range e.jobs() {
		if j.State != domain.StateDone {
			t.Fatalf("1 回の見回りで done にならないジョブがある: %+v", j)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sentAtStart) != 2 || sentAtStart[0] != 0 || sentAtStart[1] < 1 {
		t.Errorf("ジョブの入手の開始時点で届いていた通知の数 = %v, want [0 1 以上]", sentAtStart)
	}
}

// 録音のループは、PollInterval を待たずに、展開役が展開し終えた後に起こされて、できたジョブを全部録る。
func TestServeWakesRecorderAfterExpand(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	e.fake.setPrograms("TBS", day, flat1, news, flat2, suppin)
	e.app.PollInterval = time.Hour
	e.stopExpander()
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- e.app.Serve(ctx) }()
	// 最初の見回り（ジョブが無い）が済むのを待つ。
	time.Sleep(300 * time.Millisecond)

	e.putShows(flatShow, suppinShow)
	e.app.RequestExpand(flatShow, suppinShow)
	deadline := time.Now().Add(10 * time.Second)
	for {
		jobs, err := e.store.ListJobs(e.ctx, domain.StateDone)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PollInterval を待たずに 2 件とも done にならない: %+v", e.jobs())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// WakeRecorder は、何度呼んでも 1 回分にまとまり、塞がっていても止まらない。
func TestWakeRecorderCoalesces(t *testing.T) {
	a := &App{}
	for range 5 {
		a.WakeRecorder()
	}
	if n := len(a.recordWake); n != 1 {
		t.Errorf("溜まった起こす合図 = %d, want 1", n)
	}
}
