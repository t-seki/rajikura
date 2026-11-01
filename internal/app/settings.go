package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/secret"
	"github.com/t-seki/rajikura/internal/store"
)

// 設定（docs/adr/0015-settings-and-secrets-in-db.md）。
//
// 設定の正本は DB（store の settings と secrets）にだけ置き、Settings がそれを読み書きする。Settings は、設定から作った
// 部品と値の写し（Runtime）を丸ごと 1 つ持ち、保存したら新しい写しに atomic に差し替える。使う側（録音のループ・展開役・
// HTTP の goroutine）は、使うときに最新の写しを読むので、保存した設定は再起動しなくてもすぐに効く
// （番組表のキャッシュの大きさ（radiko.program_cache_mb）は除く。キャッシュは cmd が起動したときの値で作るので、再起動が要る）。
// 会員種別もすぐに効く（再試行・番組表の範囲は、使うときの会員種別から読む。録音期限は、下のとおり展開役が付け直す）。会員種別かログイン情報を保存したら、
// OnApply（cmd が App.SettingsApplied をつなぐ）が、持っている radiko のログインのセッションを捨て、会員種別が変わったなら
// まだ終わっていないジョブの録音期限の付け直しを展開役に頼む（待たない。付け直すのは展開役。ADR 0030・0032、own_area.go）。
// ジョブの実行は、試行を始めたときに写しを 1 つ取り、その試行の最後（保存・再スキャン）までその写しを使う（録音の途中で
// 保存先が変わって、1 つのジョブのファイルが 2 か所に分かれないように）。
//
// 秘密は DB の中では暗号化する（internal/secret。鍵はデータのフォルダの rajikura.key）。鍵のファイルが無い・合わないときも
// 起動は止めず、復号できない秘密は未設定として扱い、Error でログに出し、API（View）で分かるようにする。

// Runtime は、設定から作った、録音と通知が使う部品と値の写し。作った後は変えない。
type Runtime struct {
	// Config は設定（秘密を含む。ログに出さない）。
	Config config.Config
	// Media は m4a への格納と長さの測定（muxer・ffmpeg・ffprobe から作る）。
	Media Media
	// Preset は保存先の命名とタグのプリセット。
	Preset library.Preset
	// Notifier は通知先。nil なら通知先なし（選んだ通知先の秘密を復号できないときも nil）。
	Notifier notify.Notifier
	// Rescanner は再スキャンを頼む先（Jellyfin）。nil なら頼まない。
	Rescanner Rescanner
}

// Build は設定から Runtime を作る（cmd が部品の作り方を渡す。テストでは偽の部品を返す）。
type Build func(c config.Config) Runtime

// InvalidSettingsError は、設定の値の誤り（保存先に書けないことを含む）。API は 400 にする。
type InvalidSettingsError struct{ Err error }

func (e *InvalidSettingsError) Error() string { return e.Err.Error() }
func (e *InvalidSettingsError) Unwrap() error { return e.Err }

// Settings は設定の正本（DB）と、使う側が読む写し（Runtime）をつなぐ。Load してから使う。並んで使ってよい。
type Settings struct {
	Store *store.Store
	// Secrets は秘密の暗号化と鍵のファイル。
	Secrets *secret.Box
	// Build は設定から Runtime を作る。
	Build Build
	// Log は nil なら slog.Default。
	Log *slog.Logger
	// OnApply は、Apply が保存して写しを差し替えた後に、当てた p と前後の設定を渡して呼ぶ（nil なら呼ばない）。Apply の中で
	// 呼ぶので、次の Apply は OnApply が終わるまで待つ。
	OnApply func(ctx context.Context, p config.Patch, prev, next config.Config)

	// mu は Apply を 1 つずつにする（読んでから書くまでに、ほかの保存が割り込まないように）。
	mu  sync.Mutex
	cur atomic.Pointer[settingsState]
}

// settingsState は、ある時点の設定の写し。
type settingsState struct {
	rt Runtime
	// unreadable は、DB にあるのに復号できない秘密の名前（名前の順）。
	unreadable []string
}

func (s *Settings) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Load は DB から設定を読み、秘密を復号して写しを作る。復号できない秘密は未設定として扱い、Error でログに出す（エラーにはしない）。
// 設定の値が読めない（DB が壊れているなど）ときはエラーを返す。
func (s *Settings) Load(ctx context.Context) error {
	body, sealed, err := s.Store.Settings(ctx)
	if err != nil {
		return fmt.Errorf("設定を DB から読めない: %w", err)
	}
	var stored config.Patch
	if body != "" {
		if err := json.Unmarshal([]byte(body), &stored); err != nil {
			return fmt.Errorf("DB の設定が読めない: %w", err)
		}
	}
	c, err := config.FromStored(stored)
	if err != nil {
		return fmt.Errorf("DB の設定が読めない: %w", err)
	}
	st := &settingsState{}
	for _, name := range config.SecretNames {
		b, ok := sealed[name]
		if !ok {
			continue
		}
		v, err := s.Secrets.Open(name, b)
		if err != nil {
			st.unreadable = append(st.unreadable, name)
			s.log().Error("秘密を復号できないので、未設定として扱う（Web UI で入れ直す）",
				"secret", name, "key_file", s.Secrets.Path(), "err", err)
			continue
		}
		c.SetSecret(name, v)
	}
	if err := c.Validate(); err != nil {
		// 復号できない秘密があると、選んだ通知先の設定が欠ける。起動は止めず、欠けた部品は作らない（Build）。
		s.log().Warn("設定が揃っていない（揃うまで、欠けた通知先・Jellyfin は使わない）", "err", err)
	}
	st.rt = s.Build(c)
	s.cur.Store(st)
	return nil
}

func (s *Settings) state() *settingsState {
	st := s.cur.Load()
	if st == nil {
		panic("app.Settings を Load する前に使った")
	}
	return st
}

// Runtime は今の写し。
func (s *Settings) Runtime() Runtime { return s.state().rt }

// Current は今の設定（秘密を含む。ログ・API に値を出さない）。
func (s *Settings) Current() config.Config { return s.state().rt.Config }

// View は今の設定の、秘密の値を除いた見え方（GET /api/settings）。
func (s *Settings) View() config.View {
	st := s.state()
	c := st.rt.Config
	v := config.View{
		Settings:          config.Values(c),
		Secrets:           map[string]config.SecretState{},
		LibrarySet:        c.Library != "",
		UnreadableSecrets: append([]string{}, st.unreadable...),
	}
	for _, name := range config.SecretNames {
		unreadable := slices.Contains(st.unreadable, name)
		v.Secrets[name] = config.SecretState{Set: c.Secret(name) != "" || unreadable, Unreadable: unreadable}
	}
	return v
}

// Apply は設定に p を当てて DB に保存し、写しを差し替える（再起動しなくてもすぐに効く）。p に書いた欄だけを変え、
// 秘密の空の文字列は消す。値の誤りが 1 つでもあれば、何も当てずに *InvalidSettingsError を返す。
// 保存先を変えるときは、フォルダを作り、書けることを確かめてから保存する（書けなければ *InvalidSettingsError）。
// 当てた秘密は、復号できない秘密の一覧から外す（入れ直した・消した）。
func (s *Settings) Apply(ctx context.Context, p config.Patch) (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state()
	cur := st.rt.Config
	next, err := config.Apply(cur, p)
	if err != nil {
		return config.Config{}, &InvalidSettingsError{err}
	}
	if p.Library != nil && next.Library != cur.Library {
		if err := checkDir("保存先", next.Library); err != nil {
			return config.Config{}, &InvalidSettingsError{err}
		}
	}
	backupDirChanged := p.BackupDir != nil && next.BackupDir != cur.BackupDir
	if backupDirChanged && next.BackupDir != "" {
		if err := checkDir("バックアップの取る先", next.BackupDir); err != nil {
			return config.Config{}, &InvalidSettingsError{err}
		}
	}
	changes := map[string][]byte{}
	unreadable := slices.Clone(st.unreadable)
	for name, v := range p.Secrets() {
		if v == "" {
			changes[name] = nil
		} else {
			b, err := s.Secrets.Seal(name, v)
			if err != nil {
				return config.Config{}, fmt.Errorf("秘密 %s を暗号化できない: %w", name, err)
			}
			changes[name] = b
		}
		unreadable = slices.DeleteFunc(unreadable, func(n string) bool { return n == name })
	}
	body, err := json.Marshal(config.Values(next))
	if err != nil {
		return config.Config{}, err
	}
	if err := s.Store.SaveSettings(ctx, string(body), changes); err != nil {
		return config.Config{}, fmt.Errorf("設定を DB に書けない: %w", err)
	}
	if backupDirChanged {
		// 取る先を変えたら、前に取った日時は前の取る先のものなので忘れる（次の見回りで、新しい取る先に取る）。
		if err := s.Store.SetLastBackupAt(ctx, time.Time{}); err != nil {
			s.log().Warn("取る先を変えたので前に取った日時を消そうとしたが、消せない（24 時間後まで新しい取る先に取らないことがある）", "err", err)
		}
	}
	s.cur.Store(&settingsState{rt: s.Build(next), unreadable: unreadable})
	changed := make([]string, 0, len(changes))
	for name := range changes {
		changed = append(changed, name)
	}
	slices.Sort(changed)
	// 秘密は値を出さず、変えた秘密の名前だけを出す。
	s.log().Info("設定を保存した", "library", next.Library, "notify", next.NotifyChannel, "notify_success", next.NotifySuccess,
		"backup_dir", next.BackupDir, "backup_keep", next.BackupKeep, "jellyfin", next.JellyfinURL, "muxer", next.Muxer, "chapters", next.Chapters,
		"membership", next.Membership, "secrets_changed", changed)
	if s.OnApply != nil {
		s.OnApply(context.WithoutCancel(ctx), p, cur, next)
	}
	return next, nil
}

// RadikoAccount は、今の設定から radiko のログインに使うログイン情報を返す（radiko.Client.Account にする）。会員種別が非会員のとき、
// ログイン情報がそろっていない（パスワードを復号できないときも）ときは false（非会員として認証する）。
func (s *Settings) RadikoAccount() (radiko.Account, bool) {
	c := s.Current()
	if !c.Membership.Member() || c.RadikoMail == "" || c.RadikoPassword == "" {
		return radiko.Account{}, false
	}
	return radiko.Account{Mail: c.RadikoMail, Password: c.RadikoPassword, AreaFree: c.Membership.AreaFree()}, true
}

// checkDir はフォルダ dir（what は「保存先」など、エラーの文に出す呼び名）を作り、書けることを確かめる（試しのファイルを書いて消す）。
func checkDir(what, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%s %s を作れない: %w", what, dir, err)
	}
	f, err := os.CreateTemp(dir, ".rajikura-write-test-*")
	if err != nil {
		return fmt.Errorf("%s %s に書けない: %w", what, dir, err)
	}
	name := f.Name()
	cerr := f.Close()
	rerr := os.Remove(name)
	if err := errors.Join(cerr, rerr); err != nil {
		return fmt.Errorf("%s %s に書けない: %w", what, dir, err)
	}
	return nil
}

// live は、使うときに読む設定の値（Live の写し、または App のフィールド）。
type live struct {
	library           string
	media             Media
	preset            library.Preset
	maxAttemptsFree   int
	keywordMaxPending int
	notifier          notify.Notifier
	muteSuccess       bool
	rescanner         Rescanner
	backupDir         string
	backupKeep        int
	// chapters は設定 chapters（タイムフリーから録るファイルにチャプターを入れるか。ADR 0027）。
	chapters bool
	// membership は会員種別（録音期限・再試行・キーワードの照合に使う。保存したらすぐ効く。ADR 0030）。
	membership domain.Membership
}

// current は今の設定の値。Live があれば最新の写しを読み、無ければ App のフィールドを使う。
func (a *App) current() live {
	if a.Live == nil {
		return live{
			library: a.Library, media: a.Media, preset: a.Preset, maxAttemptsFree: a.MaxAttemptsFree,
			keywordMaxPending: a.KeywordMaxPending, notifier: a.Notifier, muteSuccess: a.MuteSuccess, rescanner: a.Rescanner,
			backupDir: a.BackupDir, backupKeep: cmp.Or(a.BackupKeep, config.DefaultBackupKeep), chapters: a.Chapters,
			membership: cmp.Or(a.Membership, domain.MembershipFree),
		}
	}
	rt := a.Live()
	return live{
		library: rt.Config.Library, media: rt.Media, preset: rt.Preset, maxAttemptsFree: rt.Config.MaxAttemptsFree,
		keywordMaxPending: rt.Config.KeywordMaxPending, notifier: rt.Notifier, muteSuccess: !rt.Config.NotifySuccess, rescanner: rt.Rescanner,
		backupDir: rt.Config.BackupDir, backupKeep: cmp.Or(rt.Config.BackupKeep, config.DefaultBackupKeep), chapters: rt.Config.Chapters,
		membership: cmp.Or(rt.Config.Membership, domain.MembershipFree),
	}
}

// membership は今の会員種別（Live があれば設定の最新の写しから、無ければ App.Membership）。
func (a *App) membership() domain.Membership { return a.current().membership }

// LoginResetter は、持っている radiko のログインのセッションを捨てられるもの（*radiko.Client が満たす）。
type LoginResetter interface {
	ResetLogin()
}

// SettingsApplied は、設定を保存した後に呼ぶ（cmd が Settings.OnApply につなぐ）。会員種別かログイン情報（radiko.mail・
// radiko.password）を保存したら、持っている radiko のログインのセッションを捨てる（Fetcher が LoginResetter を満たすとき）。
// 会員種別を非会員にして保存したら、radiko のログインの失敗の異常が続いていれば、radiko のログインを止めたことを復旧として閉じる
// （もう radiko のログインをしないので、ログインが通る日は来ない。バックアップの取る先を空にしたときと同じ）。
// 会員種別が変わったら、まだ終わっていないジョブ（waiting・retrying・skipped・running）の録音期限の付け直しを展開役に頼み、
// 待たずに戻る（ADR 0030・0032。自分のエリアが変わったときと同じ reviseDeadlines を、展開役が reviseForMembership で呼ぶ）。
// エリアフリー・ダブルプランなら今の自分のエリアの局一覧を取って使い、取れなければジョブの放送回の印（エリアの外から録るか）の
// まま付け直す。
// 足したら直す: サービスの異常
func (a *App) SettingsApplied(ctx context.Context, p config.Patch, prev, next config.Config) {
	login := p.Membership != nil || (p.Radiko != nil && (p.Radiko.Mail != nil || p.Radiko.Password != nil))
	if r, ok := a.Fetcher.(LoginResetter); ok && login {
		r.ResetLogin()
		a.log().Info("会員種別かログイン情報を保存したので、radiko のログインのセッションを捨てた（次に認証が要るときに radiko のログインをし直す）",
			"membership", next.Membership)
	}
	if p.Membership != nil && !next.Membership.Member() {
		a.addAnomaly(ctx, a.log(), notify.KindRecovered, notify.KindLogin,
			"【復旧】会員種別を非会員にしたので、radiko のログインを止めた。radiko のログインの失敗は終わりにするが、会員としては録っていない。")
	}
	if next.Membership == prev.Membership {
		return
	}
	a.log().Info("会員種別を変えたので、まだ終わっていないジョブの録音期限の付け直しを展開役に頼む", "from", prev.Membership, "to", next.Membership)
	a.requestRevise()
}
