package app

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/secret"
)

func sp(s string) *string { return &s }

// lockedBuffer はログを並んで書けるバッファ。
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newSettings は e の DB と鍵のファイル keyPath を使う Settings を作って読み込む。部品は偽のもの（格納は e.media、
// 通知先は Discord の Webhook に送る notify.Discord）で作る。ログは log に書く（nil なら捨てない。t.Log に流す）。
func (e *env) newSettings(keyPath string, log *lockedBuffer) *Settings {
	e.t.Helper()
	s := &Settings{
		Store:   e.store,
		Secrets: secret.Load(keyPath),
		Log:     e.app.Log,
		Build: func(c config.Config) Runtime {
			rt := Runtime{Config: c, Media: e.media, Preset: library.Jellyfin{}}
			if c.NotifyChannel == config.ChannelDiscord && c.DiscordWebhook != "" {
				rt.Notifier = &notify.Discord{WebhookURL: c.DiscordWebhook}
			}
			return rt
		},
	}
	if log != nil {
		s.Log = slog.New(slog.NewTextHandler(log, nil))
	}
	if err := s.Load(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// useSettings は App が設定の写し（Settings.Runtime）を使うようにする。App のフィールドの保存先・格納・通知先は使わない。
func (e *env) useSettings() *Settings {
	e.t.Helper()
	s := e.newSettings(filepath.Join(e.t.TempDir(), config.KeyFile), nil)
	e.app.Live = s.Runtime
	e.app.Library, e.app.Media, e.app.Notifier = "", nil, nil
	// 設定 chapters の既定はオン（保存のときにチャプターを取り、取れなければチャプターの取り直しを待つ。ADR 0027）。App の
	// フィールド（Chapters の既定は false）と同じく、チャプターを取りに行かないように、オフにしておく。
	off := false
	e.apply(s, config.Patch{Chapters: &off})
	return s
}

func (e *env) apply(s *Settings, p config.Patch) {
	e.t.Helper()
	if _, err := s.Apply(e.ctx, p); err != nil {
		e.t.Fatal(err)
	}
}

// 保存先が決まるまでは録音だけを待たせる。番組表の同期と展開は動き、ジョブは waiting のまま残る。保存先を決めたら、
// 再起動しなくても録音する。
func TestNoLibraryWaitsRecording(t *testing.T) {
	e := newFlowEnv(t)
	s := e.useSettings()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	jobs := e.jobs()
	if len(jobs) != 2 {
		t.Fatalf("保存先が無くても番組表の同期と展開は動く: jobs = %+v", jobs)
	}
	if e.runDue() {
		t.Fatal("保存先が無いのに録音した")
	}
	for _, j := range e.jobs() {
		if j.State != domain.StateWaiting || j.Attempts != 0 {
			t.Fatalf("保存先が無いのにジョブが進んだ: %+v", j)
		}
	}
	// 番組を変えた直後の展開も動く。
	e.app.RequestExpand(flatShow)
	if err := e.app.ExpandPending(e.ctx); err != nil {
		t.Fatal(err)
	}

	lib := filepath.Join(t.TempDir(), "library")
	e.apply(s, config.Patch{Library: &lib})
	if !e.runDue() {
		t.Fatal("保存先を決めたのに録音しない")
	}
	if j := e.job(flatShow.Name); j.State != domain.StateDone || !strings.HasPrefix(j.OutputPath, lib+string(filepath.Separator)) {
		t.Fatalf("job = %+v", j)
	}
}

// 保存した設定は再起動なしで効く（保存先と通知先）。
func TestSettingsTakeEffectWithoutRestart(t *testing.T) {
	e := newFlowEnv(t)
	s := e.useSettings()
	d := e.useDiscord()
	webhook := e.app.Notifier.(*notify.Discord).WebhookURL
	e.app.Notifier = nil
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}

	libA := filepath.Join(t.TempDir(), "A")
	e.apply(s, config.Patch{Library: &libA, Notify: &config.NotifyPatch{Channel: sp(config.ChannelDiscord), DiscordWebhook: &webhook}})
	if !e.runDue() {
		t.Fatal("1 件目が実行されない")
	}
	first := e.job(flatShow.Name)
	if first.State != domain.StateDone || !strings.HasPrefix(first.OutputPath, libA+string(filepath.Separator)) {
		t.Fatalf("1 件目 = %+v", first)
	}
	e.checkNotifications()
	d.expectOne(t, "【録音済み】"+flatShow.Name)

	// 保存先と通知先を変える（通知先なし）。次の録音は新しい保存先に保存し、通知先には送らない。
	libB := filepath.Join(t.TempDir(), "B")
	e.apply(s, config.Patch{Library: &libB, Notify: &config.NotifyPatch{Channel: sp(config.ChannelNone)}})
	if !e.runDue() {
		t.Fatal("2 件目が実行されない")
	}
	second := e.job(suppinShow.Name)
	if second.State != domain.StateDone || !strings.HasPrefix(second.OutputPath, libB+string(filepath.Separator)) {
		t.Fatalf("2 件目 = %+v", second)
	}
	e.checkNotifications()
	d.expectNone(t)
	// 通知先なしのテスト通知は、送らずに行だけ書く（通知先に送る印を付けない）。
	res, err := e.app.SendTestNotification(e.ctx)
	if err != nil || !res.NoChannel || res.SendErr != nil || res.Notification.Deliver ||
		!res.Notification.SentAt.IsZero() || !res.Notification.FailedAt.IsZero() {
		t.Fatalf("通知先なしのテスト通知 = %+v, %v", res, err)
	}
	d.expectNone(t)
}

// 走っている録音は、始めたときの設定（保存先）のまま最後まで進める。
func TestRunningJobKeepsStartSettings(t *testing.T) {
	e := newFlowEnv(t)
	e.store.DeleteShow(e.ctx, suppinShow.Name)
	s := e.useSettings()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	libA := filepath.Join(t.TempDir(), "A")
	libB := filepath.Join(t.TempDir(), "B")
	e.apply(s, config.Patch{Library: &libA})
	// 格納の後、保存先に確定する前（長さを測っている間）に保存先を変える。
	e.media.onDuration = func() {
		e.media.onDuration = nil
		if _, err := s.Apply(e.ctx, config.Patch{Library: &libB}); err != nil {
			t.Error(err)
		}
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	j := e.job(flatShow.Name)
	if j.State != domain.StateDone || !strings.HasPrefix(j.OutputPath, libA+string(filepath.Separator)) {
		t.Fatalf("録音の途中で保存先を変えたら、始めたときの保存先に保存する: %+v", j)
	}
	if s.Current().Library != libB {
		t.Fatalf("保存先 = %q", s.Current().Library)
	}
	if entries, err := os.ReadDir(libB); err != nil || len(entries) != 0 {
		t.Fatalf("新しい保存先にファイルがある: %v, %v", entries, err)
	}
}

// 設定は DB に残り、読み直すと同じ設定（復号した秘密を含む）になる。
func TestSettingsPersist(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	key := filepath.Join(t.TempDir(), config.KeyFile)
	s := e.newSettings(key, nil)
	lib := filepath.Join(t.TempDir(), "lib")
	_, err := s.Apply(e.ctx, config.Patch{Library: &lib, MaxAttemptsFree: new(int),
		Notify: &config.NotifyPatch{Channel: sp(config.ChannelSlack), SlackWebhook: sp("https://slack.invalid/x")}})
	if err == nil {
		t.Fatal("max_attempts_free = 0 がエラーにならない")
	}
	three := 3
	want, err := s.Apply(e.ctx, config.Patch{Library: &lib, MaxAttemptsFree: &three,
		Notify: &config.NotifyPatch{Channel: sp(config.ChannelSlack), SlackWebhook: sp("https://slack.invalid/x")}})
	if err != nil {
		t.Fatal(err)
	}
	got := e.newSettings(key, nil).Current()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("読み直した設定 = %+v\nwant %+v", got, want)
	}
	// 秘密は暗号化して置く（DB の中に値がそのまま無い）。
	_, sealed, err := e.store.Settings(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := sealed[config.SecretSlackWebhook]; !ok || bytes.Contains(b, []byte("slack.invalid")) {
		t.Fatalf("秘密の暗号文 = %q, %v", b, ok)
	}
	if fi, err := os.Stat(key); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Errorf("鍵のファイルの権限 = %v", fi.Mode().Perm())
	}
	v := e.newSettings(key, nil).View()
	if !v.Secrets[config.SecretSlackWebhook].Set || v.Secrets[config.SecretDiscordWebhook].Set || !v.LibrarySet || len(v.UnreadableSecrets) != 0 {
		t.Fatalf("View = %+v", v)
	}
}

// 鍵のファイルが無い・合わないときは、読み込みを止めず、復号できない秘密を未設定として扱い、ログ（Error）と View で知らせる。
// 入れ直すと、鍵のファイルを作り直して使える。
func TestSettingsUnreadableSecrets(t *testing.T) {
	for name, breakKey := range map[string]func(path string) error{
		"無い": os.Remove,
		"合わない": func(path string) error {
			return os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o600)
		},
		"鍵として読めない": func(path string) error { return os.WriteFile(path, []byte("not a key\n"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, afterBroadcast)
			key := filepath.Join(t.TempDir(), config.KeyFile)
			s := e.newSettings(key, nil)
			lib := t.TempDir()
			e.apply(s, config.Patch{Library: &lib,
				Notify: &config.NotifyPatch{Channel: sp(config.ChannelDiscord), DiscordWebhook: sp("https://discord.invalid/TOPSECRET"),
					SlackWebhook: sp("https://slack.invalid/TOPSECRET")},
				Jellyfin: &config.JellyfinPatch{URL: sp("http://j:8096"), Library: sp("ラジオ"), APIKey: sp("TOPSECRET-KEY")}})
			if err := breakKey(key); err != nil {
				t.Fatal(err)
			}

			var log lockedBuffer
			s = e.newSettings(key, &log)
			c := s.Current()
			if c.DiscordWebhook != "" || c.JellyfinAPIKey != "" || c.NotifyChannel != config.ChannelDiscord || c.Library != lib {
				t.Fatalf("復号できない秘密は未設定、ほかは読める: %+v", c)
			}
			if rt := s.Runtime(); rt.Notifier != nil {
				t.Errorf("Webhook が無いのに通知先を作った: %T", rt.Notifier)
			}
			v := s.View()
			want := []string{config.SecretDiscordWebhook, config.SecretSlackWebhook, config.SecretJellyfinAPIKey}
			if !reflect.DeepEqual(v.UnreadableSecrets, want) || !v.Secrets[config.SecretDiscordWebhook].Unreadable ||
				!v.Secrets[config.SecretDiscordWebhook].Set || v.Secrets[config.SecretSMTPPassword].Set {
				t.Fatalf("View = %+v", v)
			}
			out := log.String()
			if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "秘密を復号できない") ||
				!strings.Contains(out, config.SecretDiscordWebhook) || strings.Contains(out, "TOPSECRET") {
				t.Errorf("ログ: %s", out)
			}

			// 当てた後の設定の全体を確かめるので、使っている Jellyfin の API キーが復号できないままなら、ほかの秘密だけを
			// 入れ直すことはできない（一緒に入れ直す）。
			var invalid *InvalidSettingsError
			if _, err := s.Apply(e.ctx, config.Patch{Notify: &config.NotifyPatch{DiscordWebhook: sp("https://discord.invalid/new")}}); !errors.As(err, &invalid) ||
				!strings.Contains(err.Error(), config.SecretJellyfinAPIKey) {
				t.Fatalf("Jellyfin の API キーが復号できないまま: %v", err)
			}
			// 入れ直した秘密は使え、入れ直していない秘密は復号できないまま。
			e.apply(s, config.Patch{Notify: &config.NotifyPatch{DiscordWebhook: sp("https://discord.invalid/new")},
				Jellyfin: &config.JellyfinPatch{APIKey: sp("new-key")}})
			if v := s.View(); !reflect.DeepEqual(v.UnreadableSecrets, []string{config.SecretSlackWebhook}) {
				t.Fatalf("入れ直した後の View = %+v", v)
			}
			if _, err := os.Stat(key); err != nil {
				t.Fatalf("入れ直したのに鍵のファイルが無い: %v", err)
			}
			reloaded := e.newSettings(key, nil)
			if c := reloaded.Current(); c.DiscordWebhook != "https://discord.invalid/new" || c.JellyfinAPIKey != "new-key" {
				t.Fatalf("入れ直した秘密を読み直せない: %+v", c)
			}
			if rt := reloaded.Runtime(); rt.Notifier == nil {
				t.Error("入れ直したのに通知先が無い")
			}
			// 消した秘密も、復号できない秘密の一覧から外れる。
			e.apply(reloaded, config.Patch{Notify: &config.NotifyPatch{SlackWebhook: sp("")}})
			if v := e.newSettings(key, nil).View(); len(v.UnreadableSecrets) != 0 || v.Secrets[config.SecretSlackWebhook].Set {
				t.Fatalf("消した後の View = %+v", v)
			}
		})
	}
}

// 保存先を変えるときは、フォルダを作り、書けることを確かめてから保存する。書けなければ値の誤りにして、何も保存しない。
// ほかの値の誤りがあるときも、保存先のフォルダを作らない。
func TestSettingsApplyLibrary(t *testing.T) {
	e := newEnv(t, afterBroadcast)
	s := e.newSettings(filepath.Join(t.TempDir(), config.KeyFile), nil)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(file, "lib")
	var invalid *InvalidSettingsError
	if _, err := s.Apply(e.ctx, config.Patch{Library: &bad, KeywordMaxPending: &[]int{3}[0]}); !errors.As(err, &invalid) || !strings.Contains(err.Error(), "保存先") {
		t.Fatalf("書けない保存先: %v", err)
	}
	if c := e.newSettings(filepath.Join(t.TempDir(), config.KeyFile), nil).Current(); c.Library != "" || c.KeywordMaxPending != domain.DefaultKeywordMaxPending {
		t.Fatalf("書けない保存先なのに保存した: %+v", c)
	}

	nested := filepath.Join(t.TempDir(), "a", "b")
	if _, err := s.Apply(e.ctx, config.Patch{Library: &nested, Muxer: sp("go")}); !errors.As(err, &invalid) {
		t.Fatalf("知らない muxer: %v", err)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("誤りがあるのに保存先を作った: %v", err)
	}
	e.apply(s, config.Patch{Library: &nested})
	if fi, err := os.Stat(nested); err != nil || !fi.IsDir() {
		t.Fatalf("保存先を作っていない: %v", err)
	}
	if entries, _ := os.ReadDir(nested); len(entries) != 0 {
		t.Fatalf("書けるかを確かめたファイルが残っている: %v", entries)
	}
}
