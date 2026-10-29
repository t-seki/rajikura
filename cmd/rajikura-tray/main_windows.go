//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run はトレイを動かす。windowsgui の exe は標準出力・標準エラーに書けないので、引数の誤りもログのファイルに出す。
func run(args []string) int {
	// 二重起動は、ログのファイルを開く前に断る。2 つ目は、アイコン・トースト・ポーリング・tray.log のどれも始めずに終わる。
	// mutex を作れなかったとき（ほぼ無い）は、二重起動の防ぎなしで続ける。
	if release, ok, err := acquireSingle(singleMutexName); err == nil {
		if !ok {
			return 0
		}
		defer release()
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return 1
	}
	lf, err := openLogFile(trayLogPath(cacheDir), logMaxBytes)
	if err != nil {
		return 1
	}
	defer lf.Close()
	logger := slog.New(slog.NewTextHandler(lf, nil))
	// systray は log のパッケージに書くので、同じファイルに向ける。
	log.SetOutput(lf)

	fs := flag.NewFlagSet("rajikura-tray", flag.ContinueOnError)
	fs.SetOutput(lf)
	flagURL := fs.String("url", "", "rajikura の Web UI の URL（tray.toml の url より先に使う。どちらも無ければ既定 "+defaultURL+"）")
	if err := fs.Parse(args); err != nil {
		logger.Error("引数を読めない", "err", err)
		return 2
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		logger.Error("設定のフォルダが分からない", "err", err)
		return 1
	}
	t := &tray{
		log:     logger,
		flagURL: *flagURL,
		cfgPath: trayConfigPath(configDir),
		api:     apiClient{http: &http.Client{}},
		mon:     newMonitor(time.Now()),
		toasts:  make(chan outgoingToast, 32),
		refresh: make(chan struct{}, 1),
	}
	logger.Info("トレイを起動した", "url", *flagURL, "config", t.cfgPath)
	if p, err := writeAppIcon(cacheDir); err != nil {
		logger.Warn("トーストのアイコンを書けない", "err", err)
	} else if err := registerAUMID(p); err != nil {
		logger.Warn("トーストの AUMID を書けない", "err", err)
	}
	// systray.Run は main のスレッド（systray が init で留めている）で回す。トーストは別のスレッドから出す。
	systray.Run(t.onReady, t.onExit)
	logger.Info("トレイを終えた")
	return 0
}

// tray は、通知領域のアイコンとメニュー、ポーリング、トースト、連携をつなぐ。
type tray struct {
	log     *slog.Logger
	flagURL string
	cfgPath string
	api     apiClient
	// mon はポーリングの goroutine だけが触る。
	mon *monitor

	toasts  chan outgoingToast
	refresh chan struct{}
	cancel  context.CancelFunc

	mu   sync.Mutex
	link *linkSession
	// shown は今出しているアイコンの状態（変わったときだけ絵を差し替え、ログに出す）。
	shown     iconState
	shownSet  bool
	lastError string
}

func (t *tray) onReady() {
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel

	systray.SetTitle("rajikura")
	t.setStatus(trayStatus{State: stateStopped, Tooltip: "rajikura: 確かめている"})

	mOpen := systray.AddMenuItem("Web UI を開く", "")
	mNotifications := systray.AddMenuItem("通知を開く", "")
	mTest := systray.AddMenuItem("テスト通知を送る", "")
	mLink := systray.AddMenuItem("Web UI と連携する", "")
	systray.AddSeparator()
	auto, err := autostartEnabled()
	if err != nil {
		t.log.Warn("ログイン時に起動するかを読めない", "err", err)
	}
	mAuto := systray.AddMenuItemCheckbox("ログイン時に起動する", "", auto)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("終了", "")

	go runToasts(t.toasts, func(o outgoingToast, err error) {
		if err != nil {
			t.log.Warn("トーストを出せない", "title", o.Title, "err", err)
			return
		}
		t.log.Info("トーストを出した", "title", o.Title, "url", o.URL)
	})
	go t.pollLoop(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-mOpen.ClickedCh:
				t.openBrowser(t.connection().URL + "/")
			case <-mNotifications.ClickedCh:
				t.openBrowser(t.connection().URL + "/notifications")
			case <-mTest.ClickedCh:
				// 送る間もメニューの処理を止めない（最大 15 秒待つ）。
				go t.sendTest(ctx)
			case <-mLink.ClickedCh:
				t.startLink()
			case <-mAuto.ClickedCh:
				t.toggleAutostart(mAuto)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func (t *tray) onExit() {
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Lock()
	if t.link != nil {
		t.link.Close()
	}
	t.mu.Unlock()
}

// readConnection は tray.toml を読み、今つなぐ先を決める（-url、tray.toml の url、既定の順。resolveConnection）。
func (t *tray) readConnection() (connection, error) {
	c, err := readTrayConfig(t.cfgPath)
	if err != nil {
		return connection{}, err
	}
	return resolveConnection(c, t.flagURL)
}

// connection は、メニューで開く先（tray.toml が読めなければ -url か既定）。
func (t *tray) connection() connection {
	conn, err := t.readConnection()
	if err != nil {
		if c, err := resolveConnection(trayConfig{}, t.flagURL); err == nil {
			return c
		}
		return connection{URL: defaultURL}
	}
	return conn
}

func (t *tray) pollLoop(ctx context.Context) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	var prev connection
	for {
		conn, err := t.readConnection()
		if conn != prev {
			t.mon.reset()
			prev = conn
		}
		var st trayStatus
		var toasts []toastMsg
		switch {
		case err != nil:
			t.noteError(err)
			st, toasts = t.mon.unconfigured("tray.toml か接続先の URL を読めない")
		case conn.Token == "":
			st, toasts = t.mon.unconfigured("API トークンが無い")
		default:
			s, err := t.api.poll(ctx, conn)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				t.noteError(err)
			} else {
				t.noteError(nil)
			}
			// 届かないときの文言に、接続先のホストを出す。
			t.mon.setURL(conn.URL)
			st, toasts = t.mon.observe(time.Now(), s, err)
		}
		t.setStatus(st)
		for _, m := range toasts {
			u := ""
			if conn.URL != "" {
				open := m.Open
				if open == "" {
					open = "/"
				}
				u = conn.URL + open
			}
			select {
			case t.toasts <- outgoingToast{Title: m.Title, Body: m.Body, URL: u}:
			default:
				t.log.Warn("トーストが溜まりすぎたので出さない", "title", m.Title)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-t.refresh:
		}
	}
}

// noteError は、ポーリングの誤りを、前と違うときだけログに出す（1 分ごとに同じ行を書かない）。
func (t *tray) noteError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	t.mu.Lock()
	changed := msg != t.lastError
	t.lastError = msg
	t.mu.Unlock()
	if !changed {
		return
	}
	if err != nil {
		t.log.Warn("rajikura を読めない", "err", err)
	} else {
		t.log.Info("rajikura を読めるようになった")
	}
}

func (t *tray) setStatus(st trayStatus) {
	t.mu.Lock()
	changed := !t.shownSet || st.State != t.shown
	t.shown, t.shownSet = st.State, true
	t.mu.Unlock()
	if changed {
		systray.SetIcon(iconICO(st.State))
		t.log.Info("アイコンの状態が変わった", "state", st.State.String())
	}
	systray.SetTooltip(clipTooltip(st.Tooltip))
}

// startLink は連携の受け口を開き、ブラウザで Web UI の連携の画面を開く。前の受け口が開いていれば閉じる。
func (t *tray) startLink() {
	conn := t.connection()
	t.mu.Lock()
	if t.link != nil {
		t.link.Close()
		t.link = nil
	}
	sess, err := startLink(conn.URL, linkTimeout, func(c trayConfig) error {
		return writeTrayConfig(t.cfgPath, c)
	}, t.log)
	if err == nil {
		t.link = sess
	}
	t.mu.Unlock()
	if err != nil {
		t.log.Error("連携を始められない", "err", err)
		t.toast(outgoingToast{Title: "rajikura: 連携を始められない", Body: err.Error()})
		return
	}
	host, _ := os.Hostname()
	t.log.Info("連携の受け口を開いた", "port", sess.Port, "url", conn.URL)
	t.openBrowser(linkPageURL(conn.URL, sess.Port, sess.State, linkName(host, os.Getenv("USERNAME"))))
	go func() {
		err := sess.Wait()
		t.mu.Lock()
		if t.link == sess {
			t.link = nil
		}
		t.mu.Unlock()
		switch err {
		case nil:
			t.log.Info("連携した", "config", t.cfgPath)
			select {
			case t.refresh <- struct{}{}:
			default:
			}
		case errLinkCanceled:
		default:
			t.log.Info("連携の受け口を閉じた", "reason", err)
		}
	}()
}

// sendTest は POST /api/notifications/test を呼び、200 ならすぐ 1 回ポーリングする（トーストはふだんの新しい通知の道で出る）。
// 送れないとき（届かない・401・未設定・200 以外）は「テスト通知を送れない」のトーストを出す。
func (t *tray) sendTest(ctx context.Context) {
	conn, err := t.readConnection()
	if err == nil && conn.Token == "" {
		err = errors.New("API トークンが無い。「Web UI と連携する」で連携する")
	}
	if err == nil {
		err = t.api.sendTest(ctx, conn)
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		t.log.Warn("テスト通知を送れない", "err", err)
		t.toast(outgoingToast{Title: testFailTitle, Body: err.Error()})
		return
	}
	t.log.Info("テスト通知を送った")
	select {
	case t.refresh <- struct{}{}:
	default:
	}
}

func (t *tray) toast(o outgoingToast) {
	select {
	case t.toasts <- o:
	default:
	}
}

func (t *tray) toggleAutostart(item *systray.MenuItem) {
	on := !item.Checked()
	exe, err := os.Executable()
	if err != nil {
		t.log.Error("トレイの exe の場所が分からない", "err", err)
		return
	}
	if err := setAutostart(on, autostartCommand(exe, t.flagURL)); err != nil {
		t.log.Error("ログイン時に起動するかを変えられない", "err", err)
		t.toast(outgoingToast{Title: "rajikura: ログイン時に起動するかを変えられない", Body: err.Error()})
		return
	}
	if on {
		item.Check()
		t.log.Info("ログイン時に起動するようにした", "exe", exe)
	} else {
		item.Uncheck()
		t.log.Info("ログイン時に起動しないようにした")
	}
}

// openBrowser は、既定のブラウザで u を開く。
func (t *tray) openBrowser(u string) {
	if err := windows.ShellExecute(0, ptr("open"), ptr(u), nil, nil, windows.SW_SHOWNORMAL); err != nil {
		t.log.Error("ブラウザを開けない", "url", u, "err", err)
	}
}

func ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}
