package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailTimeout は SMTP の 1 通を送る全体の待ち時間の上限（ctx にもっと短い期限があればそちらを使う）。
const emailTimeout = 30 * time.Second

// SMTPTLSPort は、最初から TLS で話す SMTP のポート（SMTPS）。それ以外のポートは STARTTLS を必須にする。
const SMTPTLSPort = 465

// emailSubjects は通知の種類ごとの件名（[rajikura] の後ろ）。画面の言葉に揃える。
// 足したら直す: 通知の種類
var emailSubjects = map[string]string{
	KindRecorded:     "録音済み",
	KindMissing:      "欠落あり",
	KindExpired:      "録音できなかった",
	KindSkipped:      "録らなかった",
	KindSkipPlanned:  "録らない予定にした",
	KindUnskipped:    "録音予定に戻した",
	KindDeadlineNear: "期限間近",
	KindKeywordAdded: "キーワードで録音予定",
	KindKeywordLimit: "キーワードの上限",
	KindUnmatchedDay: "当たらない日",
	KindSync:         "番組表の同期の失敗",
	KindOutOfArea:    "エリア外",
	KindBackup:       "バックアップの失敗",
	KindWake:         "スリープから起こせない",
	KindRescan:       "再スキャンの失敗",
	KindRecovered:    "復旧",
	KindTest:         "テスト",
}

// EmailSubject は種類 kind のメールの件名を返す（例: "[rajikura] 録音できなかった"）。知らない種類は kind をそのまま使う。
func EmailSubject(kind string) string {
	s, ok := emailSubjects[kind]
	if !ok {
		s = kind
	}
	return "[rajikura] " + s
}

// Email は SMTP でメールを送る。TLS は必須で、平文では送らない（ポートが SMTPTLSPort なら最初から TLS、
// それ以外は STARTTLS を必須にし、サーバーが STARTTLS を出さなければ送らずにエラーにする）。
// 件名は種類から作り、本文は通知の本文のまま（プレーンテキスト）。
type Email struct {
	Host string
	Port int
	// Username と Password は PLAIN 認証に使う。Password は秘密なので、エラーにもログにも出さない。
	Username string
	Password string
	// From は送り主、To は宛先（複数）。メールアドレスとして読めることは config が確かめてある。
	From string
	To   []string
	// TLSConfig は TLS の設定。nil なら Host で証明書を確かめる既定のもの（テストが偽のサーバーの証明書を信じるために使う）。
	TLSConfig *tls.Config
	// Now は Date ヘッダの時刻。nil なら time.Now。
	Now func() time.Time

	// implicitTLS はテスト用。true なら、ポートによらず最初から TLS で話す。
	implicitTLS bool
}

// useImplicitTLS は、最初から TLS で話す（SMTPS）かを返す。
func (e *Email) useImplicitTLS() bool { return e.implicitTLS || e.Port == SMTPTLSPort }

// Notify は Notifier を実装する。送れなければ、パスワードを含めないエラーを返す。
func (e *Email) Notify(ctx context.Context, kind, msg string) error {
	if err := e.send(ctx, kind, msg); err != nil {
		text := err.Error()
		if e.Password != "" {
			text = strings.ReplaceAll(text, e.Password, "***")
		}
		return errors.New("email: " + text)
	}
	return nil
}

func (e *Email) send(ctx context.Context, kind, msg string) error {
	if len(e.To) == 0 {
		return errors.New("宛先が無い")
	}
	ctx, cancel := context.WithTimeout(ctx, emailTimeout)
	defer cancel()

	cfg := &tls.Config{}
	if e.TLSConfig != nil {
		cfg = e.TLSConfig.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = e.Host
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}

	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("つながらない: %w", err)
	}
	defer conn.Close()
	// 読み書きの待ちも ctx の期限で打ち切り、取り消されたら接続を閉じる。
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var rw net.Conn = conn
	if e.useImplicitTLS() {
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS を始められない: %w", err)
		}
		rw = tc
	}
	c, err := smtp.NewClient(rw, e.Host)
	if err != nil {
		return fmt.Errorf("SMTP を始められない: %w", err)
	}
	defer c.Close()
	if !e.useImplicitTLS() {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("サーバーが STARTTLS に対応していない（平文では送らない）")
		}
		if err := c.StartTLS(cfg); err != nil {
			return fmt.Errorf("STARTTLS に失敗: %w", err)
		}
	}
	if e.Username != "" || e.Password != "" {
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return fmt.Errorf("認証に失敗: %w", err)
		}
	}
	if err := c.Mail(e.From); err != nil {
		return fmt.Errorf("送り主を断られた: %w", err)
	}
	for _, to := range e.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("宛先 %s を断られた: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("本文を送れない: %w", err)
	}
	if _, err := w.Write(e.message(kind, msg)); err != nil {
		w.Close()
		return fmt.Errorf("本文を送れない: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("本文を受け取ってもらえなかった: %w", err)
	}
	// 本文は受け取ってもらえた（届く）ので、QUIT の失敗は送れなかった扱いにしない（失敗で返すと、送り直しで重複して届く）。
	c.Quit()
	return nil
}

// message はメール 1 通（ヘッダと本文）を作る。件名は RFC 2047、本文は UTF-8 を base64 にする。
func (e *Email) message(kind, msg string) []byte {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	var b strings.Builder
	b.WriteString("From: " + e.From + "\r\n")
	b.WriteString("To: " + strings.Join(e.To, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.BEncoding.Encode("utf-8", EmailSubject(kind)) + "\r\n")
	b.WriteString("Date: " + now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(msg))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}
