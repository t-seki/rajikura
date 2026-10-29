package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/jellyfin"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

type fakeRescanner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeRescanner) Rescan(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

// useLogBuffer は App のログを、読み出せるバッファに切り替える。
func (e *env) useLogBuffer() *bytes.Buffer {
	var buf bytes.Buffer
	e.app.Log = slog.New(slog.NewTextHandler(&buf, nil))
	return &buf
}

// 保存のたびに再スキャンを 1 回頼む。
func TestRescanAfterSave(t *testing.T) {
	e := coverEnv(t, "")
	r := &fakeRescanner{}
	e.app.Rescanner = r
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1", r.calls)
	}
}

// Rescanner が無ければ頼まず、保存は成功する。
func TestRescanNone(t *testing.T) {
	e := coverEnv(t, "")
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
}

// 再スキャンが失敗しても、保存は成功のまま。Warn に出し、送り直さない（異常の通知は TestRescanFailureAnomalyAndRecovery）。
func TestRescanFailureKeepsSaveDone(t *testing.T) {
	e := coverEnv(t, "")
	buf := e.useLogBuffer()
	r := &fakeRescanner{err: errors.New("boom")}
	e.app.Rescanner = r
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if r.calls != 1 {
		t.Errorf("再スキャンの回数 = %d, want 1（送り直さない）", r.calls)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "再スキャン") {
		t.Errorf("Warn が出ていない: %s", buf)
	}
}

// 再スキャンを頼めなければ、再スキャンの失敗のサービスの異常の始まりを通知し（対処の docs の節を添える）、続いている間は
// 書かない。次に頼めたら復旧を通知し、帯（続いている異常）から消える。
func TestRescanFailureAnomalyAndRecovery(t *testing.T) {
	e := coverEnv(t, "")
	d := e.useDiscord()
	r := &fakeRescanner{err: errors.New("boom")}
	e.app.Rescanner = r
	for e.runDue() {
	}
	e.checkNotifications()
	ns := e.notificationsOf(notify.KindRescan)
	if len(ns) != 1 {
		t.Fatalf("再スキャンの失敗の通知 = %q, want 1 通", ns)
	}
	sent := d.take()
	var got string
	for _, m := range sent {
		if strings.HasPrefix(m, "【再スキャンの失敗】") {
			got = m
		}
	}
	for _, w := range []string{"docs/settings.md の「ライブラリの再スキャン（jellyfin）」", "エラー: boom"} {
		if !strings.Contains(got, w) {
			t.Errorf("通知に %q が無い: %q（送った通知 %q）", w, got, sent)
		}
	}
	if as := ongoing(t, e); len(as) != 1 || as[0].Anomaly != notify.KindRescan || !strings.Contains(as[0].Message, "docs/settings.md") {
		t.Fatalf("続いている異常 = %+v", as)
	}

	// 続いている間は、また失敗しても始まりを書かない。
	e.app.rescan(e.ctx, e.app.current(), e.app.log())
	if ns := e.notificationsOf(notify.KindRescan); len(ns) != 1 {
		t.Fatalf("続いている間に始まりを書いた: %q", ns)
	}

	// 次に頼めたら復旧を書き、続いている異常から消える。もう一度頼めても、復旧を重ねない。
	r.mu.Lock()
	r.err = nil
	r.mu.Unlock()
	e.app.rescan(e.ctx, e.app.current(), e.app.log())
	e.app.rescan(e.ctx, e.app.current(), e.app.log())
	e.checkNotifications()
	d.expectOne(t, "【復旧】", "再スキャン")
	if as := ongoing(t, e); len(as) != 0 {
		t.Fatalf("復旧の後も続いている: %+v", as)
	}
}

// 再スキャンの失敗が続いている間に頼む先が無くなったら（Jellyfin の設定を外した）、次に保存したときに、止めたことを
// 復旧として通知する（バックアップの取る先を空にしたときと同じ）。続いていなければ何も書かない。
func TestRescanRemovedClosesAnomaly(t *testing.T) {
	e := coverEnv(t, "")
	d := e.useDiscord()
	e.app.Rescanner = &fakeRescanner{err: errors.New("boom")}
	e.app.rescan(e.ctx, e.app.current(), e.app.log())
	if as := ongoing(t, e); len(as) != 1 || as[0].Anomaly != notify.KindRescan {
		t.Fatalf("続いている異常 = %+v", as)
	}
	e.checkNotifications()
	d.take()

	e.app.Rescanner = nil
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if as := ongoing(t, e); len(as) != 0 {
		t.Fatalf("頼む先を外した後も続いている: %+v", as)
	}
	var recovered []string
	for _, n := range e.notifications() {
		if n.Kind == notify.KindRecovered && n.Anomaly == notify.KindRescan {
			recovered = append(recovered, n.Message)
		}
	}
	if len(recovered) != 1 || !strings.Contains(recovered[0], "【復旧】") || !strings.Contains(recovered[0], "頼むのを止めた") {
		t.Fatalf("復旧 = %q", recovered)
	}

	// もう一度頼む先が無いまま呼んでも、復旧を重ねない。
	e.app.rescan(e.ctx, e.app.current(), e.app.log())
	n := 0
	for _, x := range e.notifications() {
		if x.Anomaly == notify.KindRescan {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("再スキャンの異常の行 = %d, want 2（始まりと復旧）", n)
	}
}

// 再スキャンの失敗が続いている間に頼む先が無くなったら、録音の保存を待たず、定期的な見回り（runBackups）で復旧として閉じる。
// 頼む先があるうちは、見回りは閉じない。
func TestRescanRemovedClosesAnomalyOnPatrol(t *testing.T) {
	e := newEnv(t, time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC))
	e.app.BackupCheckInterval = 10 * time.Millisecond
	e.app.Rescanner = &fakeRescanner{err: errors.New("boom")}
	e.app.rescan(e.ctx, e.app.current(), e.app.log())
	if as := ongoing(t, e); len(as) != 1 || as[0].Anomaly != notify.KindRescan {
		t.Fatalf("続いている異常 = %+v", as)
	}

	// 頼む先があるうちは、見回りが何周しても閉じない。
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.app.runBackups(ctx)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if as := ongoing(t, e); len(as) != 1 {
		t.Fatalf("頼む先があるのに閉じた: %+v", as)
	}

	e.app.Rescanner = nil
	ctx, cancel = context.WithCancel(e.ctx)
	defer cancel()
	done = make(chan struct{})
	go func() {
		defer close(done)
		e.app.runBackups(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(ongoing(t, e)) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if as := ongoing(t, e); len(as) != 0 {
		t.Fatalf("見回りで閉じていない: %+v", as)
	}
	n := 0
	for _, x := range e.notifications() {
		if x.Kind == notify.KindRecovered && x.Anomaly == notify.KindRescan && strings.Contains(x.Message, "頼むのを止めた") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("復旧の通知 = %d, want 1", n)
	}
}

// 再スキャンを頼めた（失敗が続いていない）ときは、何も通知しない。
func TestRescanSuccessNoAnomaly(t *testing.T) {
	e := coverEnv(t, "")
	e.app.Rescanner = &fakeRescanner{}
	for e.runDue() {
	}
	for _, n := range e.notifications() {
		if n.Anomaly != "" {
			t.Errorf("異常の通知を書いた: %+v", n)
		}
	}
}

// ongoing は続いているサービスの異常（帯の元）を返す。
func ongoing(t *testing.T, e *env) []store.Notification {
	t.Helper()
	as, err := e.store.OngoingAnomalies(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

// ライブラリが名前で見つからないとき、Warn に出し、保存は成功のまま（Jellyfin の実クライアントで確かめる）。
func TestRescanLibraryNotFoundWarns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Library/VirtualFolders" {
			w.Write([]byte(`[{"Name":"音楽","ItemId":"aaa"}]`))
			return
		}
		t.Errorf("想定外の呼び出し: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	e := coverEnv(t, "")
	buf := e.useLogBuffer()
	e.app.Rescanner = &jellyfin.Client{URL: srv.URL, APIKey: "k", Library: "ラジオ"}
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "見つからない") {
		t.Errorf("Warn が出ていない: %s", buf)
	}
}

// blockingRescanner は ctx が終わるまで戻らない。
type blockingRescanner struct{ started chan struct{} }

func (b *blockingRescanner) Rescan(ctx context.Context) error {
	close(b.started)
	<-ctx.Done()
	return ctx.Err()
}

// 応答しない Jellyfin でも、上限で戻る。保存は成功のまま、Warn だけ。
func TestRescanTimeoutReturns(t *testing.T) {
	e := coverEnv(t, "")
	buf := e.useLogBuffer()
	e.app.RescanTimeout = 50 * time.Millisecond
	e.app.Rescanner = &blockingRescanner{started: make(chan struct{})}
	for e.runDue() {
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "deadline exceeded") {
		t.Errorf("Warn が出ていない: %s", buf)
	}
	if ns := e.notificationsOf(notify.KindRescan); len(ns) != 1 {
		t.Errorf("上限を超えたのに、再スキャンの失敗の異常を書いていない: %q", ns)
	}
}

// 保存の確定後に取り消されたら、done までは進めるが、再スキャンは止まる。
func TestRescanStopsOnCancel(t *testing.T) {
	e := coverEnv(t, "")
	buf := e.useLogBuffer()
	b := &blockingRescanner{started: make(chan struct{})}
	e.app.Rescanner = b
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	go func() {
		<-b.started
		cancel()
	}()
	if _, err := e.app.RunDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if j := e.job(suppinShow.Name); j.State != domain.StateDone {
		t.Fatalf("job = %+v", j)
	}
	if !strings.Contains(buf.String(), "context canceled") {
		t.Errorf("取り消しで止まっていない: %s", buf)
	}
	if ns := e.notificationsOf(notify.KindRescan); len(ns) != 0 {
		t.Errorf("取り消し（SIGTERM）で、再スキャンの失敗の異常を書いた: %q", ns)
	}
}
