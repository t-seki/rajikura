package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/t-seki/rajikura/internal/domain"
)

// apply は PATCH /api/settings の本文と同じ JSON data を Patch として読み（知らないキーは誤り）、cur に当てる。
func apply(cur Config, data string) (Config, error) {
	var p Patch
	dec := json.NewDecoder(bytes.NewReader([]byte(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Config{}, err
	}
	return Apply(cur, p)
}

// js は s を JSON の文字列にする（Windows のパスの \ もそのまま渡るように）。
func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// withLibrary は保存先を決めた既定の設定。
func withLibrary(t *testing.T) Config {
	t.Helper()
	c := Default()
	c.Library = t.TempDir()
	return c
}

// lib は JSON に書く保存先（絶対パス）。後ろに続けるキーのために , で終わる。
func lib(t *testing.T) string {
	t.Helper()
	return `"library": ` + js(t.TempDir()) + `,`
}

func TestDefault(t *testing.T) {
	want := Config{
		BackupKeep:           DefaultBackupKeep,
		Preset:               "jellyfin",
		TruncateArtist:       true,
		Membership:           domain.MembershipFree,
		MaxAttemptsFree:      domain.DefaultMaxAttemptsFree,
		KeywordMaxPending:    domain.DefaultKeywordMaxPending,
		Muxer:                MuxerBuiltin,
		Chapters:             true,
		RadikoProgramCacheMB: DefaultProgramCacheMB,
		NotifyChannel:        ChannelNone,
		NotifySuccess:        true,
	}
	if got := Default(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// 保存先が決まっていない既定の設定も、設定の全体の確かめは通る（保存先は録音を待たせるだけ）。
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyAll(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := dir + "/bin/ffmpeg"
	c, err := apply(Default(), `{
		"library": `+js(dir)+`,
		"preset": "jellyfin",
		"truncate_artist": false,
		"membership": "free",
		"max_attempts_free": 3,
		"keyword_max_pending": 5,
		"muxer": "builtin",
		"ffmpeg": `+js(ffmpeg)+`,
		"ffprobe": "ffprobe",
		"chapters": false,
		"radiko": {"program_cache_mb": 8},
		"notify": {"channel": "discord", "success": false, "discord_webhook": "https://discord.invalid/api/webhooks/1/x"},
		"jellyfin": {"url": "http://j:8096", "library": "ラジオ", "api_key": "key"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Library != dir || c.TruncateArtist || c.MaxAttemptsFree != 3 || c.KeywordMaxPending != 5 || c.Muxer != MuxerBuiltin || c.Chapters ||
		c.FFmpeg != ffmpeg || c.FFprobe != "ffprobe" || c.RadikoProgramCacheMB != 8 ||
		c.DiscordWebhook != "https://discord.invalid/api/webhooks/1/x" || c.NotifyChannel != ChannelDiscord || c.NotifySuccess ||
		c.JellyfinURL != "http://j:8096" || c.JellyfinLibrary != "ラジオ" || c.JellyfinAPIKey != "key" {
		t.Fatalf("got %+v", c)
	}
}

// PATCH は、書いたキーだけを変え、書いていないキーは今の値のまま残す。
func TestApplyKeepsUnwritten(t *testing.T) {
	cur := withLibrary(t)
	cur.NotifyChannel, cur.DiscordWebhook, cur.MaxAttemptsFree = ChannelDiscord, "https://discord.invalid/old", 4
	c, err := apply(cur, `{"keyword_max_pending": 7, "notify": {"success": false}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeywordMaxPending != 7 || c.NotifySuccess || c.Library != cur.Library || c.MaxAttemptsFree != 4 ||
		c.NotifyChannel != ChannelDiscord || c.DiscordWebhook != "https://discord.invalid/old" {
		t.Fatalf("got %+v", c)
	}
	// Webhook だけを入れ替えても、選んである通知先のまま（channel の書き忘れにはしない）。
	c, err = apply(c, `{"notify": {"discord_webhook": "https://discord.invalid/new"}}`)
	if err != nil || c.DiscordWebhook != "https://discord.invalid/new" {
		t.Fatalf("Webhook の入れ替え: %+v, %v", c, err)
	}
}

// 秘密の空の文字列は「消す」。ほかの秘密は残る。
func TestApplyDeletesSecret(t *testing.T) {
	cur := withLibrary(t)
	cur.DiscordWebhook, cur.SlackWebhook = "https://discord.invalid/x", "https://slack.invalid/x"
	empty := ""
	p := Patch{Notify: &NotifyPatch{DiscordWebhook: &empty}}
	if got := p.Secrets(); !reflect.DeepEqual(got, map[string]string{SecretDiscordWebhook: ""}) {
		t.Fatalf("Secrets = %v", got)
	}
	c, err := Apply(cur, p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DiscordWebhook != "" || c.SlackWebhook != "https://slack.invalid/x" {
		t.Fatalf("got %+v", c)
	}
	// 選んでいる通知先の秘密を消すと、通知先の設定が欠けるのでエラーにする。
	cur.NotifyChannel = ChannelDiscord
	if _, err := Apply(cur, p); err == nil || !strings.Contains(err.Error(), "discord_webhook が無い") {
		t.Fatalf("選んでいる通知先の秘密を消したとき err = %v", err)
	}
}

// 誤りが 1 つでもあれば、何も当てない（今の設定 cur も変えない）。
func TestApplyAtomic(t *testing.T) {
	cur := withLibrary(t)
	before := cur
	if _, err := apply(cur, `{"max_attempts_free": 3, "muxer": "go"}`); err == nil {
		t.Fatal("誤りがあるのにエラーにならない")
	}
	if !reflect.DeepEqual(cur, before) {
		t.Fatalf("cur が変わった: %+v", cur)
	}
}

func TestApplyErrors(t *testing.T) {
	email := func(smtp string) string {
		return `{"notify": {"channel": "email", "smtp": {` + smtp + `}}}`
	}
	const smtpBase = `"host": "smtp.gmail.com", "port": 587, "username": "me@gmail.com", "from": "me@gmail.com", "to": ["me@gmail.com", "you@example.com"], "password": "TOPSECRET-PASS"`
	tests := []struct {
		name, data, want string
	}{
		{"保存先を空にする", `{"library": ""}`, "空にできない"},
		{"保存先が相対パス", `{"library": "lib"}`, "絶対パス"},
		{"バックアップの取る先が相対パス", `{"backup_dir": "bk"}`, "backup_dir"},
		{"バックアップの残す数が 0", `{"backup_keep": 0}`, "backup_keep は 1 以上"},
		{"未対応の会員種別", `{"membership": "areafree"}`, "未対応"},
		{"不明な会員種別", `{"membership": "gold"}`, "不明な会員種別"},
		{"未対応のプリセット", `{"preset": "plex"}`, "未対応"},
		{"知らない muxer", `{"muxer": "go"}`, "muxer \"go\" は知らない"},
		{"ffmpeg が相対パス", `{"ffmpeg": "bin/ffmpeg"}`, "絶対パス"},
		{"試行回数の上限が 0", `{"max_attempts_free": 0}`, "max_attempts_free"},
		{"キーワードの上限が 0", `{"keyword_max_pending": 0}`, "keyword_max_pending は 1 以上"},
		{"番組表のキャッシュが 0", `{"radiko": {"program_cache_mb": 0}}`, "program_cache_mb は 1 以上"},
		{"channel の書き忘れ", `{"notify": {"discord_webhook": "https://discord.invalid/x"}}`, "notify.channel を書いて"},
		{"channel の書き忘れ（slack）", `{"notify": {"slack_webhook": "https://slack.invalid/x"}}`, "notify.channel を書いて"},
		{"channel の書き忘れ（smtp）", `{"notify": {"smtp": {"host": "smtp.example.com"}}}`, "notify.channel を書いて"},
		{"channel の書き忘れ（パスワードだけ）", `{"notify": {"smtp": {"password": "TOPSECRET-PASS"}}}`, "notify.channel を書いて"},
		{"discord なのに Webhook が無い", `{"notify": {"channel": "discord"}}`, "discord_webhook"},
		{"slack なのに Webhook が無い", `{"notify": {"channel": "slack"}}`, "slack_webhook"},
		{"email なのに smtp が無い", `{"notify": {"channel": "email"}}`, "host・port・username・from・to"},
		{"email なのに to が無い", email(`"host": "h", "port": 587, "username": "u", "from": "a@example.com"`), "to が無い"},
		{"email なのにパスワードが無い", email(strings.Replace(smtpBase, `, "password": "TOPSECRET-PASS"`, "", 1)), SecretSMTPPassword},
		{"from が読めない", email(strings.Replace(smtpBase, `"from": "me@gmail.com"`, `"from": "me"`, 1)), "notify.smtp.from"},
		{"to が読めない", email(strings.Replace(smtpBase, `"you@example.com"`, `"you"`, 1)), "notify.smtp.to"},
		{"表示名つきは受け付けない", email(strings.Replace(smtpBase, `"from": "me@gmail.com"`, `"from": "Me <me@gmail.com>"`, 1)), "notify.smtp.from"},
		{"port が範囲外", email(strings.Replace(smtpBase, `"port": 587`, `"port": 70000`, 1)), "範囲にない"},
		{"知らない通知先", `{"notify": {"channel": "line"}}`, "知らない"},
		{"通知の知らないキー", `{"notify": {"webhook": "x"}}`, "webhook"},
		{"jellyfin の url だけ", `{"jellyfin": {"url": "http://j", "api_key": "k"}}`, "jellyfin.url"},
		{"jellyfin の library だけ", `{"jellyfin": {"library": "ラジオ"}}`, "jellyfin.url"},
		{"jellyfin の API キーが無い", `{"jellyfin": {"url": "http://j", "library": "ラジオ"}}`, SecretJellyfinAPIKey},
		{"知らないキー", `{"libary": "x"}`, "libary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := apply(withLibrary(t), tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q を含む", err, tt.want)
			}
			// 秘密の値はエラーに出さない。
			if strings.Contains(err.Error(), "TOPSECRET") {
				t.Fatalf("エラーに秘密の値が出た: %v", err)
			}
		})
	}
}

// メールの設定は notify.smtp から読み、パスワードは notify.smtp.password から読む。
func TestApplyEmail(t *testing.T) {
	c, err := apply(withLibrary(t), `{"notify": {"channel": "email", "smtp": {"host": "smtp.gmail.com", "port": 587, "username": "me@gmail.com", "from": "me@gmail.com", "to": ["me@gmail.com", "you@example.com"], "password": "pw"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.NotifyChannel != ChannelEmail || c.SMTPHost != "smtp.gmail.com" || c.SMTPPort != 587 || c.SMTPUsername != "me@gmail.com" ||
		c.SMTPFrom != "me@gmail.com" || len(c.SMTPTo) != 2 || c.SMTPTo[1] != "you@example.com" || c.SMTPPassword != "pw" {
		t.Fatalf("got %+v", c)
	}
}

// none を選べば、通知先の設定が残っていてもよい。
func TestApplyNotifyNone(t *testing.T) {
	c, err := apply(withLibrary(t), `{"notify": {"channel": "none", "discord_webhook": "https://discord.invalid/x"}}`)
	if err != nil || c.NotifyChannel != ChannelNone {
		t.Fatalf("%+v, %v", c, err)
	}
}

// DB に置いた値（Values の形）から作り直すと同じ設定になる。保存先が空でもよい（まだ決まっていない）。
func TestFromStored(t *testing.T) {
	c, err := apply(Default(), `{`+lib(t)+` "max_attempts_free": 3, "notify": {"channel": "slack", "slack_webhook": "https://slack.invalid/x"}}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromStored(Values(c))
	if err != nil {
		t.Fatal(err)
	}
	got.SlackWebhook = c.SlackWebhook // 秘密は呼び出し側が入れる
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("got %+v\nwant %+v", got, c)
	}
	if got, err := FromStored(Values(Default())); err != nil || !reflect.DeepEqual(got, Default()) {
		t.Fatalf("既定: %+v, %v", got, err)
	}
	if got, err := FromStored(Patch{}); err != nil || !reflect.DeepEqual(got, Default()) {
		t.Fatalf("空: %+v, %v", got, err)
	}
	// muxer を書いていない DB の設定は builtin で格納する。書いてあれば、その値のまま（ADR 0013）
	if got, err := FromStored(Patch{}); err != nil || got.Muxer != MuxerBuiltin {
		t.Fatalf("muxer 無し: %q, %v", got.Muxer, err)
	}
	if got, err := FromStored(Patch{Muxer: ptr(MuxerFFmpeg)}); err != nil || got.Muxer != MuxerFFmpeg {
		t.Fatalf("muxer ffmpeg: %q, %v", got.Muxer, err)
	}
}
