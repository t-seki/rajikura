// Package notify は通知先（用語集）と通知の種類。録音の結果や異常を、人が見る場所（Discord・Slack・メールなど）へ知らせる。
//
// 通知先は Notifier のインターフェースにし、差し替えられるようにする。いまの実装は Discord・Slack・メール
// （メールは email.go。SMTP で、TLS 必須（465 なら最初から TLS、それ以外は STARTTLS 必須）。件名は種類から作り、
// パスワードはエラーにもログにも出さない。以下は Discord と Slack。Discord の Webhook に、JSON の content を POST する。1 通の上限（2000 文字）を超える本文は切り詰め、
// 切り詰めたことを本文に書く。Slack の Incoming Webhook には JSON の text を POST し、本文の & < > を
// Slack の書式にエスケープする。上限は SlackMaxLen。Webhook の URL は秘密なので、どちらもエラーにもログにも出さない）。
// 通知先なし（notify.channel = "none"）のときは Notifier を作らない。
//
// 送るのに失敗しても送り直さない（送り直しは呼び出し側の app が、DB の通知の行で行う）。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// 通知の種類。DB の通知の行（notifications.kind）と、/metrics の rajikura_notify_failures_total の kind の値に使う
// （KindTest は /metrics では使わない）。
// 値は監視との約束でもあるので、変えるときは docs/api.md の「メトリクス」と一緒に直す。
// 足したら直す: 通知の種類
const (
	// ジョブの出来事
	KindRecorded     = "recorded"      // 録音済み
	KindMissing      = "missing"       // 欠落あり
	KindExpired      = "expired"       // 録音できなかった
	KindSkipped      = "skipped"       // 録らなかった
	KindSkipPlanned  = "skip_planned"  // 録らない予定にした
	KindUnskipped    = "unskipped"     // 録音予定に戻した
	KindDeadlineNear = "deadline_near" // 期限間近
	// キーワードの出来事
	KindKeywordAdded = "keyword_added" // キーワードで録音予定にした
	KindKeywordLimit = "keyword_limit" // 上限で足すのを止めた
	// 番組の出来事
	KindUnmatchedDay = "unmatched_day" // 毎週の予約の当たらない日を新しく見つけた
	// サービスの異常（始まり）
	KindSync      = "sync"        // 番組表の同期が 2 回続けて失敗した
	KindOutOfArea = "out_of_area" // radiko がエリア外と判定した
	KindBackup    = "backup"      // 自動のバックアップを取れなかった
	KindWake      = "wake"        // ライブ録音の前にスリープ中の PC を起こせない見込み
	KindRescan    = "rescan"      // ライブラリの再スキャンを頼めなかった
	// サービスの異常の復旧
	KindRecovered = "recovered"
	// テスト通知（人が Web UI かトレイのメニューから送る。録音のループを通さず、rajikura_notify_failures_total でも数えない）
	KindTest = "test"
)

// Kinds は通知の種類の全部。
// 足したら直す: 通知の種類
var Kinds = []string{KindRecorded, KindMissing, KindExpired, KindSkipped, KindSkipPlanned, KindUnskipped, KindDeadlineNear, KindKeywordAdded, KindKeywordLimit,
	KindUnmatchedDay, KindSync, KindOutOfArea, KindBackup, KindWake, KindRescan, KindRecovered, KindTest}

// Notifier は通知先。
type Notifier interface {
	// Notify は種類 kind（Kinds のどれか。メールの件名などに使う）の本文 msg を送る。送れなければエラーを返す。
	Notify(ctx context.Context, kind, msg string) error
}

// DiscordMaxLen は Discord の 1 通の本文の上限（文字数）。
const DiscordMaxLen = 2000

// truncatedNote は切り詰めたときに本文の末尾に書く文。
const truncatedNote = "\n…（長いので切り詰めた）"

// webhookTimeout は Webhook に送るときの待ち時間の上限。
const webhookTimeout = 30 * time.Second

// Discord は Discord の Webhook に送る。
type Discord struct {
	// WebhookURL は Webhook の URL。秘密なので、エラーにもログにも出さない。
	WebhookURL string
	// Client は HTTP のクライアント。nil なら、待ち時間の上限を付けた既定のもの。
	Client *http.Client
}

// Notify は Notifier を実装する。Discord は kind を使わない（本文の頭の印で分かる）。
func (d *Discord) Notify(ctx context.Context, kind, msg string) error {
	body, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Truncate(msg, DiscordMaxLen)})
	if err != nil {
		return err
	}
	return postJSON(ctx, "discord", d.WebhookURL, d.Client, body)
}

// postJSON は Webhook の URL に JSON を POST する。2xx 以外はエラー。エラーに URL を出さない。
func postJSON(ctx context.Context, name, webhookURL string, client *http.Client, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		// URL の誤りのエラーは URL を含むので、中身を出さない。
		return errors.New(name + ": Webhook の URL を読めない")
	}
	req.Header.Set("Content-Type", "application/json")
	c := client
	if c == nil {
		c = &http.Client{Timeout: webhookTimeout}
	}
	resp, err := c.Do(req)
	if err != nil {
		// *url.Error は URL を含むので、URL を除いた中身だけを返す。
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s: 送れない: %w", name, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: 状態 %d が返った", name, resp.StatusCode)
	}
	return nil
}

// SlackMaxLen は Slack の 1 通の本文の上限（文字数）。Slack の文書（chat.postMessage のページ
// https://docs.slack.dev/reference/methods/chat.postMessage ）は text を 4,000 文字までにするのが良いとし、
// 40,000 文字を超えると Slack が切り捨てると書く(2026-10-15 に確かめた)ので、勧める 4000 に置いた。
// 確かめたのは chat.postMessage のページで、Incoming Webhook 専用のページでは確かめていない。
const SlackMaxLen = 4000

// Slack は Slack の Incoming Webhook に送る。
type Slack struct {
	// WebhookURL は Webhook の URL。秘密なので、エラーにもログにも出さない。
	WebhookURL string
	// Client は HTTP のクライアント。nil なら、待ち時間の上限を付けた既定のもの。
	Client *http.Client
}

// Notify は Notifier を実装する。Slack は kind を使わない。
func (s *Slack) Notify(ctx context.Context, kind, msg string) error {
	// エスケープで本文が伸びるので、切り詰めは上限に収まるようエスケープの後に行う。
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{slackTruncate(SlackEscape(msg), SlackMaxLen)})
	if err != nil {
		return err
	}
	return postJSON(ctx, "slack", s.WebhookURL, s.Client, body)
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// slackTruncate は Truncate と同じ。ただし、切った末尾が未完の実体参照（&amp; などの途中）なら、
// その & の手前まで削ってから注記を付ける。
func slackTruncate(escaped string, max int) string {
	if utf8.RuneCountInString(escaped) <= max {
		return escaped
	}
	keep := max - utf8.RuneCountInString(truncatedNote)
	if keep < 0 {
		keep = 0
	}
	r := []rune(escaped)[:keep]
	for i := len(r) - 1; i >= 0 && len(r)-i < 5; i-- {
		if r[i] == ';' {
			break
		}
		if r[i] == '&' {
			r = r[:i]
			break
		}
	}
	return string(r) + truncatedNote
}

// SlackEscape は Slack の書式に合わせて & < > をエスケープする。
func SlackEscape(msg string) string { return slackEscaper.Replace(msg) }

// Truncate は msg が max 文字を超えるとき、切り詰めたことを末尾に書いて max 文字に収める。
func Truncate(msg string, max int) string {
	if utf8.RuneCountInString(msg) <= max {
		return msg
	}
	keep := max - utf8.RuneCountInString(truncatedNote)
	if keep < 0 {
		keep = 0
	}
	r := []rune(msg)
	return string(r[:keep]) + truncatedNote
}
