package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/t-seki/rajikura/internal/app"
	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/jellyfin"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/media"
	"github.com/t-seki/rajikura/internal/metrics"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/secret"
	"github.com/t-seki/rajikura/internal/server"
	"github.com/t-seki/rajikura/internal/store"
	"github.com/t-seki/rajikura/web"
)

// SetupCodeFile は、データのフォルダに置く、セットアップコードを書いたファイルの名前（docs/adr/0016-first-password-by-setup-code.md）。
// 足したら直す: データのフォルダ
const SetupCodeFile = "setup-code.txt"

// serveOptions は、起動の設定のほかに serve に渡すフラグ。
type serveOptions struct {
	// resetPassword は、起動するときに Web UI のパスワード・ログインのセッション・API トークンを消す（-reset-password）。
	resetPassword bool
}

// serve は常駐する。ctx が取り消されたら終わる。boot は起動の設定（データのフォルダと待ち受けるアドレスと許すホスト名）。
// Web UI のパスワードが無ければ、起動のたびにセットアップコードを作り、データのフォルダの setup-code.txt とログに出す。
// 設定は DB から読み（app.Settings）、Web UI・API で保存したら、再起動しなくてもすぐに効く。
// ただし番組表のキャッシュの大きさ（radiko.program_cache_mb）と会員種別は、起動したときの値で番組表のキャッシュを作る。
func serve(ctx context.Context, boot config.Boot, opts serveOptions, stdout, stderr io.Writer) error {
	log := slog.New(slog.NewTextHandler(stderr, nil))

	// データのフォルダと作業用フォルダは、無ければ作る（DB は store.Open が、鍵のファイルは初めて秘密を保存するときに作る）。
	if err := os.MkdirAll(boot.WorkDir(), 0o755); err != nil {
		return fmt.Errorf("データのフォルダ %s に作業用フォルダを作れない: %w", boot.DataDir, err)
	}
	st, err := store.Open(ctx, boot.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	setup, err := prepareSetupCode(ctx, st, boot, opts, log)
	if err != nil {
		return err
	}

	settings := &app.Settings{Store: st, Secrets: secret.Load(boot.KeyPath()), Build: buildRuntime, Log: log}
	if err := settings.Load(ctx); err != nil {
		return err
	}
	cfg := settings.Current()

	client := radiko.NewClient()
	if boot.RadikoAuthHost != "" {
		client.AuthHost = boot.RadikoAuthHost
	}
	if boot.RadikoProgramHost != "" {
		client.ProgramHost = boot.RadikoProgramHost
	}
	client.EnableProgramCache(domain.MaxAvailableDays(cfg.Membership), int64(cfg.RadikoProgramCacheMB)<<20, log)
	mt := metrics.New()
	mt.InitNotifyKinds(app.NotifyKinds...)
	// エリアの日別番組表は、Web UI の番組表とキーワードの照合（展開役）で共有する（エリア ID と取得中のものを 1 つにまとめる）。
	areaGuide := app.NewRadikoAreaGuide(client)
	a := &app.App{
		Store:      st,
		Guide:      app.RadikoGuide{Client: client},
		Fetcher:    client,
		WorkDir:    boot.WorkDir(),
		Membership: cfg.Membership,
		AreaGuide:  areaGuide,
		Log:        log,
		Metrics:    mt,
		// 保存先・通知先などは、使うときに設定の最新の写しを読む。
		Live: settings.Runtime,
	}
	if err := a.LoadMetrics(ctx); err != nil {
		return fmt.Errorf("最後に保存した時刻を読めない: %w", err)
	}
	// 秘密は値を出さず、ログには通知先の種類だけを出す。
	log.Info("起動する", "version", version, "data", boot.DataDir, "library", cfg.Library, "membership", cfg.Membership,
		"notify", cfg.NotifyChannel, "notify_success", cfg.NotifySuccess, "jellyfin", cfg.JellyfinURL, "muxer", cfg.Muxer)

	// 待ち受けは起動の前に開き、使えない（ポートが埋まっているなど）ならここで止める。
	ln, err := net.Listen("tcp", boot.Listen)
	if err != nil {
		return fmt.Errorf("listen %s を開けない: %w", boot.Listen, err)
	}
	// 足したら直す: 待ち受ける口
	log.Info("HTTP（healthz・metrics・Web UI・API）を待ち受ける", "listen", ln.Addr().String())
	ui := &server.UI{
		Sessions:      st,
		Shows:         st,
		Onces:         st,
		Keywords:      st,
		Guide:         a.Guide,
		AreaGuide:     areaGuide,
		Jobs:          st,
		Tokens:        st,
		Notifications: st,
		TestNotifier:  a,
		Settings:      settings,
		Backups:       st,
		// バックアップを返すまでの写しは、書けることが分かっている作業用フォルダに置く（返したら消す）。
		BackupTempDir: boot.WorkDir(),
		Expander:      a,
		Recorder:      a,
		Version:       version,
		Passwords:     st,
		Setup:         setup,
		Files:         web.FS(),
		Log:           log,
		AllowedHosts:  boot.AllowedHosts,
	}
	// serve の終了（ctx の取り消し）で HTTP も止める。HTTP が先に落ちたら、serve も止める。
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpDone := make(chan error, 1)
	go func() {
		httpDone <- server.Serve(sctx, ln, server.WithMetrics(server.Handler(server.Health{LastPoll: a.LastPoll, Now: a.Now}, ui), mt.Handler()))
		cancel()
	}()
	err = a.Serve(sctx)
	cancel()
	if herr := <-httpDone; herr != nil && err == nil {
		err = fmt.Errorf("HTTP: %w", herr)
	}
	return err
}

// prepareSetupCode は、-reset-password ならパスワード・ログインのセッション・API トークンを消し、パスワードが無ければ
// セットアップコードを作って、データのフォルダの setup-code.txt とログ（Info）に出す。パスワードがあれば nil を返し、
// 前の起動で残ったセットアップコードのファイルを消す。
func prepareSetupCode(ctx context.Context, st *store.Store, boot config.Boot, opts serveOptions, log *slog.Logger) (*server.SetupCode, error) {
	file := filepath.Join(boot.DataDir, SetupCodeFile)
	if opts.resetPassword {
		if err := st.ResetPassword(ctx); err != nil {
			return nil, fmt.Errorf("パスワードを戻せない: %w", err)
		}
		log.Warn("-reset-password: Web UI のパスワード・ログインのセッション・API トークンを消した。セットアップコードからパスワードを決め直し、API トークンは発行し直す")
	}
	hash, err := st.PasswordHash(ctx)
	if err != nil {
		return nil, fmt.Errorf("パスワードを読めない: %w", err)
	}
	if hash != "" {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Error("前のセットアップコードのファイルを消せない（手で消してよい）", "file", file, "err", err)
		}
		return nil, nil
	}
	code, err := server.NewSetupCode()
	if err != nil {
		return nil, fmt.Errorf("セットアップコードを作れない: %w", err)
	}
	// 前の起動のファイルを消してから作る（os.WriteFile は、ファイルがあれば権限を変えないので、毎回 0600 で作るため）。
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("前のセットアップコードのファイル %s を消せない: %w", file, err)
	}
	if err := os.WriteFile(file, []byte(code+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("セットアップコードを %s に書けない: %w", file, err)
	}
	log.Info("Web UI のパスワードが決まっていない。Web UI を開き、セットアップコードを入れてパスワードを決める", "setup_code", code, "file", file)
	return &server.SetupCode{Code: code, File: file}, nil
}

// buildRuntime は設定から、録音と通知が使う部品の写しを作る（app.Settings が、読み込んだときと保存したときに呼ぶ）。
func buildRuntime(c config.Config) app.Runtime {
	rt := app.Runtime{
		Config:   c,
		Media:    newMedia(c),
		Preset:   library.Jellyfin{FullArtist: !c.TruncateArtist},
		Notifier: newNotifier(c),
	}
	// API キーを復号できないときは、頼まない（保存は続ける）。
	if c.JellyfinURL != "" && c.JellyfinAPIKey != "" {
		rt.Rescanner = &jellyfin.Client{URL: c.JellyfinURL, APIKey: c.JellyfinAPIKey, Library: c.JellyfinLibrary}
	}
	return rt
}

// newMedia は、設定 muxer に従って m4a への格納と長さの測定を行うものを作る（ADR 0013）。
// builtin なら ffmpeg・ffprobe の設定は使わない。
func newMedia(cfg config.Config) app.Media {
	if cfg.Muxer == config.MuxerBuiltin {
		return media.Builtin{}
	}
	return media.New(cfg.FFmpeg, cfg.FFprobe)
}

// newNotifier は通知先を作る。通知先なし（none）なら nil（通知は DB に書くだけで送らない）。
// 選んだ通知先の秘密が無い（復号できない）ときも nil にする（通知は Web UI の履歴にだけ残る）。
func newNotifier(cfg config.Config) notify.Notifier {
	switch cfg.NotifyChannel {
	case config.ChannelDiscord:
		if cfg.DiscordWebhook == "" {
			return nil
		}
		return &notify.Discord{WebhookURL: cfg.DiscordWebhook}
	case config.ChannelSlack:
		if cfg.SlackWebhook == "" {
			return nil
		}
		return &notify.Slack{WebhookURL: cfg.SlackWebhook}
	case config.ChannelEmail:
		if cfg.SMTPPassword == "" {
			return nil
		}
		return &notify.Email{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, To: cfg.SMTPTo,
		}
	default:
		return nil
	}
}
