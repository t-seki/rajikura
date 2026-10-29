//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"golang.org/x/sys/windows/svc"

	"github.com/t-seki/rajikura/internal/config"
)

// serviceStopWaitHint は、停止の要求を受けてから止まるまでの見込み。止まるまでの間、SCM に停止中と、この見込みを返し続ける。
const serviceStopWaitHint = 30 * time.Second

// serviceCheckpointEvery は、止まるまでの間に SCM に停止中を知らせ直す間隔（進んでいることを示す）。
const serviceCheckpointEvery = 2 * time.Second

// serveHost は serve を動かす。Windows のサービスとして起動されたとき（SCM が起こしたとき）は、サービスとして動かす
// （停止と PC の終了の要求で ctx を取り消し、ログはデータのフォルダの logs\rajikura.log に出す。
// docs/adr/0020-windows-service-with-virtual-account.md）。そうでなければ、今までどおりコンソールで動かす（ログは標準エラー）。
func serveHost(ctx context.Context, boot config.Boot, opts serveOptions, stdout, stderr io.Writer) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("Windows のサービスとして起動されたかを見分けられない: %w", err)
	}
	if !isService {
		return serve(ctx, boot, opts, stdout, stderr)
	}
	return runAsService(ctx, boot.LogPath(), logMaxBytes, logFiles, svc.Run, func(ctx context.Context, log io.Writer) error {
		return serve(ctx, boot, opts, log, log)
	})
}

// runAsService は、ログのファイル（logPath。maxBytes を超えたら回し、files 本まで残す）を開き、runner（svc.Run。テストで差し替える）に
// サービスの処理を渡す。serveFn は serve の本体で、ログの出し先と、停止の要求で取り消される ctx を受け取る。
func runAsService(ctx context.Context, logPath string, maxBytes int64, files int, runner func(string, svc.Handler) error, serveFn func(context.Context, io.Writer) error) error {
	lf, err := openRotatingFile(logPath, maxBytes, files)
	if err != nil {
		// ログを書けなくても SCM には応え、失敗として止まる（SCM が System のイベントログに残す）。
		_ = runner(serviceName, failedService{})
		return err
	}
	defer lf.Close()
	h := &serviceHandler{ctx: ctx, log: slog.New(slog.NewTextHandler(lf, nil)), serve: func(ctx context.Context) error { return serveFn(ctx, lf) }}
	if err := runner(serviceName, h); err != nil {
		h.log.Error("Windows のサービスとして動けない", "err", err)
		return fmt.Errorf("Windows のサービスとして動けない: %w", err)
	}
	return h.err
}

// failedService は、始める前に失敗したことを SCM に返す。
type failedService struct{}

func (failedService) Execute([]string, <-chan svc.ChangeRequest, chan<- svc.Status) (bool, uint32) {
	return false, 1
}

// serviceHandler は SCM の要求を serve に伝える。Stop と Shutdown（PC の終了）で ctx を取り消し（SIGTERM と同じ扱い）、
// serve が戻るまで停止中を返す。
type serviceHandler struct {
	ctx   context.Context
	serve func(context.Context) error
	log   *slog.Logger
	// checkpointEvery は、止まるまでの間に停止中を知らせ直す間隔。0 なら serviceCheckpointEvery。
	checkpointEvery time.Duration
	// err は serve が返したエラー（停止の要求で止まったときの取り消しは含めない）。
	err error
}

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.serve(ctx) }()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	h.log.Info("Windows のサービスとして動く")

	every := h.checkpointEvery
	if every <= 0 {
		every = serviceCheckpointEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	stopping := false
	var checkpoint uint32
	stopPending := func() svc.Status {
		checkpoint++
		return svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: uint32(serviceStopWaitHint / time.Millisecond)}
	}
	for {
		select {
		case err := <-done:
			if stopping && errors.Is(err, context.Canceled) {
				err = nil
			}
			h.err = err
			if err != nil {
				h.log.Error("serve が止まった", "err", err)
				return false, 1
			}
			h.log.Info("止まった")
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				if stopping {
					continue
				}
				stopping = true
				what := "停止"
				if c.Cmd == svc.Shutdown {
					what = "PC の終了"
				}
				h.log.Info("サービスの" + what + "の要求を受けた。実行中の格納と HTTP を止めて終わる")
				cancel()
				s <- stopPending()
			}
		case <-tick.C:
			if stopping {
				s <- stopPending()
			}
		}
	}
}
