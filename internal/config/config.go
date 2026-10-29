// Package config は rajikura の設定の型と確かめ、起動の設定の型を扱う。
//
// 設定（保存先・通知先・会員種別など）の正本は DB にだけ置き、人は Web UI で変える（docs/adr/0015-settings-and-secrets-in-db.md）。
// このパッケージは DB を知らず、設定の値と、その変え方（Patch）と確かめ（Apply）だけを持つ。DB への読み書きと、保存したら
// すぐに効かせる差し替えは internal/app の Settings が行う。
//
// 設定のキーは PATCH /api/settings の本文（JSON）のキー。書き方は docs/settings.md にある。
package config

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/radiko"
)

// 既定値。
const (
	DefaultPreset     = "jellyfin"
	DefaultMembership = domain.MembershipFree
	DefaultMuxer      = MuxerBuiltin
	// DefaultBackupKeep は、自動のバックアップを取る先に残す数の既定。
	DefaultBackupKeep = 7
	// DefaultListen は /healthz・/metrics・Web UI・API を待ち受けるアドレスの既定（serve の -listen）。
	// 足したら直す: 待ち受ける口
	DefaultListen = ":8080"
	// DefaultProgramCacheMB は番組表のキャッシュの合計の大きさの上限（MB）の既定。
	DefaultProgramCacheMB = int(radiko.DefaultProgramCacheBytes >> 20)
)

// データのフォルダに置くもの。
// 足したら直す: データのフォルダ
const (
	// DBFile は DB のファイルの名前。
	DBFile = "rajikura.db"
	// KeyFile は秘密を暗号化する鍵のファイルの名前。
	KeyFile = "rajikura.key"
	// WorkDirName は作業用フォルダの名前。入手途中の ADTS（work/<ジョブ ID>/）と、GET /api/backup が返すまでのバックアップ（work/backup-*/）を置く。
	WorkDirName = "work"
	// LogDirName はログのフォルダの名前。serve が Windows のサービスとして動くときだけ、ここに LogFile を書く
	// （docs/adr/0020-windows-service-with-virtual-account.md）。
	LogDirName = "logs"
	// LogFile はログのファイルの名前（大きさで回し、古いものは rajikura.log.1・rajikura.log.2… にする）。
	LogFile = "rajikura.log"
)

// muxer の値（m4a への格納を何で行うか。ADR 0013）。
const (
	MuxerFFmpeg  = "ffmpeg"
	MuxerBuiltin = "builtin"
)

// notify.channel の値（通知先）。
const (
	ChannelDiscord = "discord"
	ChannelSlack   = "slack"
	ChannelEmail   = "email"
	ChannelNone    = "none"
)

// 秘密の名前（PATCH /api/settings の本文のキーを . でつないだもの）。秘密は DB の中では暗号化し、API では値を返さない。
const (
	SecretDiscordWebhook = "notify.discord_webhook"
	SecretSlackWebhook   = "notify.slack_webhook"
	SecretSMTPPassword   = "notify.smtp.password"
	SecretJellyfinAPIKey = "jellyfin.api_key"
)

// SecretNames は秘密の名前の一覧（並びは API の並び）。
var SecretNames = []string{SecretDiscordWebhook, SecretSlackWebhook, SecretSMTPPassword, SecretJellyfinAPIKey}

// Boot は起動の設定（serve のフラグで渡す。設定と違って Web UI からは変えられず、変えるには再起動が要る）。
type Boot struct {
	// DataDir はデータのフォルダ（DB・鍵のファイル・作業用フォルダと、Web UI のパスワードが決まるまでの間だけセットアップコードのファイル setup-code.txt を置く。
	// Windows のサービスとして動くときは、ログのフォルダ logs も置く。serve の -data）。
	// 中身（鍵のファイルを含む）は置き場所の権限で守る。Windows で service install が作るデータのフォルダは、ACL をサービスのアカウント・
	// SYSTEM・Administrators だけに絞る（docs/adr/0020-windows-service-with-virtual-account.md）。
	// 足したら直す: データのフォルダ
	DataDir string
	// Listen は /healthz・/metrics・Web UI・API を待ち受けるアドレス（host:port。serve の -listen）。
	// 足したら直す: 待ち受ける口
	Listen string
	// AllowedHosts は、IP のアドレスと localhost のほかに、Web UI・API が受ける Host の名前（serve の -allowed-hosts。
	// docs/adr/0021-web-ui-origin-host-and-login-limit.md）。空なら IP のアドレスと localhost だけを受ける。
	AllowedHosts []string
	// RadikoAuthHost と RadikoProgramHost は radiko のホスト。空なら radiko の既定。人が使う口（フラグ）は無く、
	// テストが偽の radiko を差すためだけにある。
	RadikoAuthHost    string
	RadikoProgramHost string
}

// DBPath は DB のファイルのパス。
func (b Boot) DBPath() string { return filepath.Join(b.DataDir, DBFile) }

// KeyPath は秘密の鍵のファイルのパス。
func (b Boot) KeyPath() string { return filepath.Join(b.DataDir, KeyFile) }

// WorkDir は作業用フォルダのパス。
func (b Boot) WorkDir() string { return filepath.Join(b.DataDir, WorkDirName) }

// LogPath は、Windows のサービスとして動くときのログのファイルのパス。
func (b Boot) LogPath() string { return filepath.Join(b.DataDir, LogDirName, LogFile) }

// DefaultDataDir は既定のデータのフォルダ（実行ファイルと同じフォルダ）。
func DefaultDataDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("実行ファイルの場所が分からない: %w", err)
	}
	return filepath.Dir(exe), nil
}

// Config は設定。秘密（DiscordWebhook・SlackWebhook・SMTPPassword・JellyfinAPIKey）も持つが、ログ・API には値を出さない。
type Config struct {
	// Library は保存先のルート（絶対パス）。空なら保存先はまだ決まっていない（録音を待たせる）。
	Library string
	// BackupDir は自動のバックアップを取る先のフォルダ（絶対パス）。空なら自動では取らない。
	BackupDir string
	// BackupKeep は、BackupDir に残すバックアップの数（新しいものから数える）。既定は DefaultBackupKeep、1 以上。
	BackupKeep int
	// Preset は保存先の命名とタグのプリセット。MVP は "jellyfin" だけ。
	Preset string
	// TruncateArtist は、artist タグに書く出演者を 250 バイトに切り詰めるか。既定は true。
	TruncateArtist bool
	// Membership は会員種別。MVP で動かせるのは free だけ。
	Membership domain.Membership
	// MaxAttemptsFree は非会員の試行回数の上限。
	MaxAttemptsFree int
	// KeywordMaxPending は、キーワードごとの、足した放送回のうちまだ録音を終えていない回の数の上限。既定は 10、1 以上。
	KeywordMaxPending int
	// Muxer は m4a への格納を何で行うか（MuxerFFmpeg・MuxerBuiltin）。既定は MuxerBuiltin（ADR 0013）。
	Muxer string
	// FFmpeg と FFprobe は ffmpeg / ffprobe のパス。空なら PATH 上のもの。Muxer が MuxerBuiltin なら使わない。
	FFmpeg  string
	FFprobe string
	// Chapters は、タイムフリーから録るファイルに radiko のチャプターを入れるか。オンなら保存のときにチャプターを取り、
	// まだできていないかもしれない放送回があれば、できるのを待ってチャプターの取り直しで入れる（ADR 0027）。最初の試行の時刻は
	// この設定に依らない。既定は true。
	Chapters bool
	// RadikoProgramCacheMB は番組表のキャッシュの合計の大きさの上限（MB）。
	RadikoProgramCacheMB int
	// NotifyChannel は通知先（ChannelDiscord・ChannelSlack・ChannelEmail・ChannelNone）。
	NotifyChannel string
	// NotifySuccess は、録音済みの通知を通知先に送るか（false でも DB には書く）。既定は true。
	NotifySuccess bool
	// DiscordWebhook は通知を送る Discord の Webhook の URL（秘密）。
	DiscordWebhook string
	// SlackWebhook は通知を送る Slack の Incoming Webhook の URL（秘密）。
	SlackWebhook string
	// SMTPHost・SMTPPort・SMTPUsername・SMTPFrom・SMTPTo は、メールで送る SMTP の設定（[notify.smtp]）。
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPFrom     string
	SMTPTo       []string
	// SMTPPassword は SMTP のパスワード（秘密）。
	SMTPPassword string
	// JellyfinURL と JellyfinLibrary は、保存のたびにライブラリの再スキャンを頼む Jellyfin の URL と、
	// 再スキャンするライブラリの名前。JellyfinURL が空なら頼まない。
	JellyfinURL     string
	JellyfinLibrary string
	// JellyfinAPIKey は Jellyfin の API キー（秘密）。
	JellyfinAPIKey string
}

// Default は、まだ何も保存していないときの設定。保存先は決まっていない。
func Default() Config {
	return Config{
		BackupKeep:           DefaultBackupKeep,
		Preset:               DefaultPreset,
		TruncateArtist:       true,
		Membership:           DefaultMembership,
		MaxAttemptsFree:      domain.DefaultMaxAttemptsFree,
		KeywordMaxPending:    domain.DefaultKeywordMaxPending,
		Muxer:                DefaultMuxer,
		Chapters:             true,
		RadikoProgramCacheMB: DefaultProgramCacheMB,
		NotifyChannel:        ChannelNone,
		NotifySuccess:        true,
	}
}

// secretField は秘密の名前に当たる欄。
func (c *Config) secretField(name string) *string {
	switch name {
	case SecretDiscordWebhook:
		return &c.DiscordWebhook
	case SecretSlackWebhook:
		return &c.SlackWebhook
	case SecretSMTPPassword:
		return &c.SMTPPassword
	case SecretJellyfinAPIKey:
		return &c.JellyfinAPIKey
	}
	panic("知らない秘密の名前: " + name)
}

// Secret は秘密 name の値（無ければ空）。
func (c Config) Secret(name string) string { return *c.secretField(name) }

// SetSecret は秘密 name の値を v にする（空なら消す）。
func (c *Config) SetSecret(name, v string) { *c.secretField(name) = v }

// Patch は設定の変え方。nil の欄は変えない（PATCH /api/settings の本文）。
// 秘密の欄（DiscordWebhook・SlackWebhook・SMTP.Password・Jellyfin.APIKey）の空の文字列は「消す」。
// Values で作った Patch は、秘密を除く全部の欄を持つ（DB に置く形、GET /api/settings）。
type Patch struct {
	Library           *string        `json:"library,omitempty"`
	BackupDir         *string        `json:"backup_dir,omitempty"`
	BackupKeep        *int           `json:"backup_keep,omitempty"`
	Preset            *string        `json:"preset,omitempty"`
	TruncateArtist    *bool          `json:"truncate_artist,omitempty"`
	Membership        *string        `json:"membership,omitempty"`
	MaxAttemptsFree   *int           `json:"max_attempts_free,omitempty"`
	KeywordMaxPending *int           `json:"keyword_max_pending,omitempty"`
	Muxer             *string        `json:"muxer,omitempty"`
	FFmpeg            *string        `json:"ffmpeg,omitempty"`
	FFprobe           *string        `json:"ffprobe,omitempty"`
	Chapters          *bool          `json:"chapters,omitempty"`
	Radiko            *RadikoPatch   `json:"radiko,omitempty"`
	Notify            *NotifyPatch   `json:"notify,omitempty"`
	Jellyfin          *JellyfinPatch `json:"jellyfin,omitempty"`
}

// RadikoPatch は [radiko]。
type RadikoPatch struct {
	ProgramCacheMB *int `json:"program_cache_mb,omitempty"`
}

// NotifyPatch は [notify]。
type NotifyPatch struct {
	Channel        *string    `json:"channel,omitempty"`
	Success        *bool      `json:"success,omitempty"`
	DiscordWebhook *string    `json:"discord_webhook,omitempty"`
	SlackWebhook   *string    `json:"slack_webhook,omitempty"`
	SMTP           *SMTPPatch `json:"smtp,omitempty"`
}

// SMTPPatch は [notify.smtp]。
type SMTPPatch struct {
	Host     *string   `json:"host,omitempty"`
	Port     *int      `json:"port,omitempty"`
	Username *string   `json:"username,omitempty"`
	From     *string   `json:"from,omitempty"`
	To       *[]string `json:"to,omitempty"`
	Password *string   `json:"password,omitempty"`
}

// JellyfinPatch は [jellyfin]。
type JellyfinPatch struct {
	URL     *string `json:"url,omitempty"`
	Library *string `json:"library,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
}

// Secrets は、p が変える秘密（名前 → 値。空の文字列は「消す」）。
func (p Patch) Secrets() map[string]string {
	out := map[string]string{}
	set := func(name string, v *string) {
		if v != nil {
			out[name] = *v
		}
	}
	if p.Notify != nil {
		set(SecretDiscordWebhook, p.Notify.DiscordWebhook)
		set(SecretSlackWebhook, p.Notify.SlackWebhook)
		if p.Notify.SMTP != nil {
			set(SecretSMTPPassword, p.Notify.SMTP.Password)
		}
	}
	if p.Jellyfin != nil {
		set(SecretJellyfinAPIKey, p.Jellyfin.APIKey)
	}
	return out
}

// WithoutSecrets は、p から秘密の欄を落とした写し。
func (p Patch) WithoutSecrets() Patch {
	if p.Notify != nil {
		n := *p.Notify
		n.DiscordWebhook, n.SlackWebhook = nil, nil
		if n.SMTP != nil {
			s := *n.SMTP
			s.Password = nil
			n.SMTP = &s
		}
		p.Notify = &n
	}
	if p.Jellyfin != nil {
		j := *p.Jellyfin
		j.APIKey = nil
		p.Jellyfin = &j
	}
	return p
}

func ptr[T any](v T) *T { return &v }

// Values は c の秘密を除く全部の値を持つ Patch。
func Values(c Config) Patch {
	return Patch{
		Library:           ptr(c.Library),
		BackupDir:         ptr(c.BackupDir),
		BackupKeep:        ptr(c.BackupKeep),
		Preset:            ptr(c.Preset),
		TruncateArtist:    ptr(c.TruncateArtist),
		Membership:        ptr(string(c.Membership)),
		MaxAttemptsFree:   ptr(c.MaxAttemptsFree),
		KeywordMaxPending: ptr(c.KeywordMaxPending),
		Muxer:             ptr(c.Muxer),
		FFmpeg:            ptr(c.FFmpeg),
		FFprobe:           ptr(c.FFprobe),
		Chapters:          ptr(c.Chapters),
		Radiko:            &RadikoPatch{ProgramCacheMB: ptr(c.RadikoProgramCacheMB)},
		Notify: &NotifyPatch{
			Channel: ptr(c.NotifyChannel),
			Success: ptr(c.NotifySuccess),
			SMTP: &SMTPPatch{
				Host: ptr(c.SMTPHost), Port: ptr(c.SMTPPort), Username: ptr(c.SMTPUsername), From: ptr(c.SMTPFrom),
				To: ptr(slices.Clone(c.SMTPTo)),
			},
		},
		Jellyfin: &JellyfinPatch{URL: ptr(c.JellyfinURL), Library: ptr(c.JellyfinLibrary)},
	}
}

// FromStored は DB に置いた値（Values の形）から設定を作る。DB に無い欄は既定値にする。値の 1 つずつは確かめるが、
// 設定の全体の確かめ（通知先の設定が揃っているかなど）はしない（復号できない秘密を未設定として扱っても、起動を止めないため）。
// 秘密は呼び出し側が SetSecret で入れる。
func FromStored(stored Patch) (Config, error) {
	c := Default()
	if err := merge(&c, stored.WithoutSecrets(), true); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Apply は今の設定 cur に p を当てた設定を返す。値の誤りが 1 つでもあれば、エラーを返す（何も当てない）。
// 当てた後の設定の全体を確かめる（Validate）。保存先のフォルダを作れるか・書けるかは確かめない（internal/app の Settings が確かめる）。
func Apply(cur Config, p Patch) (Config, error) {
	c := cur
	c.SMTPTo = slices.Clone(cur.SMTPTo)
	if err := merge(&c, p, false); err != nil {
		return Config{}, err
	}
	if p.Notify != nil && p.Notify.Channel == nil && c.NotifyChannel == ChannelNone && patchSetsNotifyTarget(p) {
		return Config{}, fmt.Errorf("notify.channel を書いて（discord_webhook・slack_webhook・[notify.smtp] を書いたのに、通知先が選ばれていない。"+
			"Discord に送るなら channel = %q、Slack なら %q、メールなら %q、送らないなら %q）",
			ChannelDiscord, ChannelSlack, ChannelEmail, ChannelNone)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// patchSetsNotifyTarget は、p が通知先の設定（discord_webhook・slack_webhook・[notify.smtp]）に空でない値を書いているか。
func patchSetsNotifyTarget(p Patch) bool {
	n := p.Notify
	nonEmpty := func(s *string) bool { return s != nil && *s != "" }
	if nonEmpty(n.DiscordWebhook) || nonEmpty(n.SlackWebhook) {
		return true
	}
	if s := n.SMTP; s != nil {
		return nonEmpty(s.Host) || (s.Port != nil && *s.Port != 0) || nonEmpty(s.Username) || nonEmpty(s.From) ||
			(s.To != nil && len(*s.To) > 0) || nonEmpty(s.Password)
	}
	return false
}

// merge は p の欄を c に当てる。値の 1 つずつを確かめる。stored なら DB から読んだ値（保存先の空も受け付ける）。
func merge(c *Config, p Patch, stored bool) error {
	if p.Library != nil {
		switch {
		case *p.Library == "" && !stored:
			return errors.New("library（保存先のルート）は空にできない")
		case *p.Library != "" && !filepath.IsAbs(*p.Library):
			return fmt.Errorf("library %q は絶対パスで書く", *p.Library)
		}
		c.Library = *p.Library
	}
	// backup_dir は空にできる（空なら自動では取らない）。
	if p.BackupDir != nil {
		if *p.BackupDir != "" && !filepath.IsAbs(*p.BackupDir) {
			return fmt.Errorf("backup_dir %q は絶対パスで書く", *p.BackupDir)
		}
		c.BackupDir = *p.BackupDir
	}
	if p.BackupKeep != nil {
		if *p.BackupKeep <= 0 {
			return fmt.Errorf("backup_keep は 1 以上（%d）", *p.BackupKeep)
		}
		c.BackupKeep = *p.BackupKeep
	}
	if p.Preset != nil {
		if *p.Preset != "jellyfin" {
			return fmt.Errorf("preset %q は未対応（jellyfin だけ）", *p.Preset)
		}
		c.Preset = *p.Preset
	}
	if p.TruncateArtist != nil {
		c.TruncateArtist = *p.TruncateArtist
	}
	if p.Membership != nil {
		m, err := domain.ParseMembership(*p.Membership)
		if err != nil {
			return fmt.Errorf("membership: %w", err)
		}
		if m != domain.MembershipFree {
			return fmt.Errorf("membership %q は未対応（いま動かせるのは free だけ）", m)
		}
		c.Membership = m
	}
	if p.MaxAttemptsFree != nil {
		if *p.MaxAttemptsFree <= 0 {
			return fmt.Errorf("max_attempts_free は 1 以上（%d）", *p.MaxAttemptsFree)
		}
		c.MaxAttemptsFree = *p.MaxAttemptsFree
	}
	if p.KeywordMaxPending != nil {
		if *p.KeywordMaxPending <= 0 {
			return fmt.Errorf("keyword_max_pending は 1 以上（%d）", *p.KeywordMaxPending)
		}
		c.KeywordMaxPending = *p.KeywordMaxPending
	}
	if p.Muxer != nil {
		switch *p.Muxer {
		case MuxerFFmpeg, MuxerBuiltin:
		default:
			return fmt.Errorf("muxer %q は知らない（%q・%q）", *p.Muxer, MuxerFFmpeg, MuxerBuiltin)
		}
		c.Muxer = *p.Muxer
	}
	// ffmpeg / ffprobe は、名前だけ（"ffmpeg"）なら PATH から探す。パスの区切りを含むなら絶対パスで書く。
	for _, f := range []struct {
		key string
		v   *string
		dst *string
	}{{"ffmpeg", p.FFmpeg, &c.FFmpeg}, {"ffprobe", p.FFprobe, &c.FFprobe}} {
		if f.v == nil {
			continue
		}
		if strings.ContainsAny(*f.v, `/\`) && !filepath.IsAbs(*f.v) {
			return fmt.Errorf("%s %q は絶対パスか、PATH から探す名前だけで書く", f.key, *f.v)
		}
		*f.dst = *f.v
	}
	if p.Chapters != nil {
		c.Chapters = *p.Chapters
	}
	if p.Radiko != nil && p.Radiko.ProgramCacheMB != nil {
		if *p.Radiko.ProgramCacheMB <= 0 {
			return fmt.Errorf("radiko.program_cache_mb は 1 以上（%d）", *p.Radiko.ProgramCacheMB)
		}
		c.RadikoProgramCacheMB = *p.Radiko.ProgramCacheMB
	}
	if n := p.Notify; n != nil {
		if n.Channel != nil {
			switch *n.Channel {
			case ChannelDiscord, ChannelSlack, ChannelEmail, ChannelNone:
			default:
				return fmt.Errorf("notify.channel %q は知らない（%q・%q・%q・%q）", *n.Channel, ChannelDiscord, ChannelSlack, ChannelEmail, ChannelNone)
			}
			c.NotifyChannel = *n.Channel
		}
		if n.Success != nil {
			c.NotifySuccess = *n.Success
		}
		setStr(&c.DiscordWebhook, n.DiscordWebhook)
		setStr(&c.SlackWebhook, n.SlackWebhook)
		if s := n.SMTP; s != nil {
			setStr(&c.SMTPHost, s.Host)
			if s.Port != nil {
				c.SMTPPort = *s.Port
			}
			setStr(&c.SMTPUsername, s.Username)
			setStr(&c.SMTPFrom, s.From)
			if s.To != nil {
				c.SMTPTo = slices.Clone(*s.To)
			}
			setStr(&c.SMTPPassword, s.Password)
		}
	}
	if j := p.Jellyfin; j != nil {
		setStr(&c.JellyfinURL, j.URL)
		setStr(&c.JellyfinLibrary, j.Library)
		setStr(&c.JellyfinAPIKey, j.APIKey)
	}
	return nil
}

func setStr(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

// Validate は設定の全体を確かめる（Apply が当てた後に呼ぶ）。
//
//   - jellyfin.url と jellyfin.library は、両方書くか両方省く。url があれば API キー（jellyfin.api_key）が要る
//   - notify.channel に選んだ通知先の設定（Webhook の URL・[notify.smtp] とパスワード）が無ければ、エラーにする
func (c Config) Validate() error {
	if (c.JellyfinURL == "") != (c.JellyfinLibrary == "") {
		return errors.New("jellyfin.url と jellyfin.library は、両方書くか両方省く")
	}
	if c.JellyfinURL != "" && c.JellyfinAPIKey == "" {
		return fmt.Errorf("jellyfin を使うには、API キー（%s）が要る", SecretJellyfinAPIKey)
	}
	switch c.NotifyChannel {
	case ChannelDiscord:
		if c.DiscordWebhook == "" {
			return fmt.Errorf("notify.channel = %q なのに、discord_webhook が無い", ChannelDiscord)
		}
	case ChannelSlack:
		if c.SlackWebhook == "" {
			return fmt.Errorf("notify.channel = %q なのに、slack_webhook が無い", ChannelSlack)
		}
	case ChannelEmail:
		return c.checkSMTP()
	}
	return nil
}

// checkSMTP は channel = "email" のとき、SMTP の設定が揃っていてメールアドレスが読めることを確かめる。
// パスワードの値はエラーに出さない。
func (c Config) checkSMTP() error {
	var missing []string
	if c.SMTPHost == "" {
		missing = append(missing, "host")
	}
	if c.SMTPPort == 0 {
		missing = append(missing, "port")
	}
	if c.SMTPUsername == "" {
		missing = append(missing, "username")
	}
	if c.SMTPFrom == "" {
		missing = append(missing, "from")
	}
	if len(c.SMTPTo) == 0 {
		missing = append(missing, "to")
	}
	if len(missing) > 0 {
		return fmt.Errorf("notify.channel = %q なのに、[notify.smtp] の %s が無い", ChannelEmail, strings.Join(missing, "・"))
	}
	if c.SMTPPassword == "" {
		return fmt.Errorf("notify.channel = %q なのに、SMTP のパスワード（%s）が無い", ChannelEmail, SecretSMTPPassword)
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return fmt.Errorf("notify.smtp.port %d は 1〜65535 の範囲にない", c.SMTPPort)
	}
	if err := checkAddress("notify.smtp.from", c.SMTPFrom); err != nil {
		return err
	}
	for _, to := range c.SMTPTo {
		if err := checkAddress("notify.smtp.to", to); err != nil {
			return err
		}
	}
	return nil
}

// checkAddress は s が「name@host」だけのメールアドレス（表示名なし）として読めることを確かめる。
func checkAddress(key, s string) error {
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return fmt.Errorf("%s %q はメールアドレス（name@example.com の形）として読めない", key, s)
	}
	return nil
}

// SecretState は秘密 1 つの状態（値は持たない）。
type SecretState struct {
	// Set は、値が設定されているか（復号できないものも数える）。
	Set bool `json:"set"`
	// Unreadable は、DB にあるのに復号できない（鍵のファイルが無い・合わない）か。復号できない秘密は未設定として扱う。
	Unreadable bool `json:"unreadable"`
}

// View は GET /api/settings の答え。秘密の値は持たない。
type View struct {
	// Settings は秘密を除く設定の値（Values の形）。
	Settings Patch `json:"settings"`
	// Secrets は秘密ごとの状態（キーは SecretNames）。
	Secrets map[string]SecretState `json:"secrets"`
	// LibrarySet は保存先が決まっているか（決まるまで録音を待たせる）。
	LibrarySet bool `json:"library_set"`
	// UnreadableSecrets は復号できない秘密の名前（無ければ空の配列）。
	UnreadableSecrets []string `json:"unreadable_secrets"`
}
