package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP はテスト用の SMTP サーバー。STARTTLS を出すか、最初から TLS で待つかを選べる。
type fakeSMTP struct {
	port     int
	pool     *x509.CertPool
	mu       sync.Mutex
	plain    []string // 平文のまま読んだコマンド（STARTTLS・TLS の前に来たもの）
	auth     []string // AUTH PLAIN の中身（デコード後）
	froms    []string
	rcpts    []string
	data     []string
	offerTLS bool
	implicit bool
	failQuit bool // QUIT にエラーを返して切る
}

// 本文を受け取ってもらえた後の QUIT の失敗は、送れなかった扱いにしない（送り直しで重複して届くため）。
func TestEmailIgnoresQuitFailure(t *testing.T) {
	f := newFakeSMTP(t, true, false)
	f.failQuit = true
	if err := f.email().Notify(context.Background(), KindExpired, "本文"); err != nil {
		t.Fatalf("QUIT の失敗で送れなかった扱いになった: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.data) != 1 {
		t.Fatalf("届いたメール = %d 通", len(f.data))
	}
}

func newFakeSMTP(t *testing.T, offerSTARTTLS, implicitTLS bool) *fakeSMTP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{port: ln.Addr().(*net.TCPAddr).Port, pool: pool, offerTLS: offerSTARTTLS, implicit: implicitTLS}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, tlsCfg)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn, tlsCfg *tls.Config) {
	defer func() { conn.Close() }()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	secure := false
	if f.implicit {
		conn = tls.Server(conn, tlsCfg)
		secure = true
	}
	r := bufio.NewReader(conn)
	say := func(s string) { conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !secure {
			f.mu.Lock()
			f.plain = append(f.plain, line)
			f.mu.Unlock()
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake")
			if f.offerTLS && !secure {
				say("250-STARTTLS")
			}
			say("250 AUTH PLAIN")
		case cmd == "STARTTLS":
			say("220 go ahead")
			conn = tls.Server(conn, tlsCfg)
			r = bufio.NewReader(conn)
			secure = true
		case strings.HasPrefix(cmd, "AUTH PLAIN "):
			b, _ := base64.StdEncoding.DecodeString(line[len("AUTH PLAIN "):])
			f.mu.Lock()
			f.auth = append(f.auth, string(b))
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.froms = append(f.froms, line[len("MAIL FROM:"):])
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.rcpts = append(f.rcpts, line[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = append(f.data, b.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			if f.failQuit {
				say("421 going away")
				return
			}
			say("221 bye")
			return
		default:
			say("502 no")
		}
	}
}

func (f *fakeSMTP) email() *Email {
	return &Email{
		Host: "127.0.0.1", Port: f.port,
		Username: "user@example.com", Password: "TOPSECRET-PASS",
		From: "rajikura@example.com", To: []string{"a@example.com", "b@example.com"},
		TLSConfig: &tls.Config{RootCAs: f.pool},
	}
}

func parseSent(t *testing.T, raw string) (*mail.Message, string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("メールを読めない: %v\n%s", err, raw)
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := m.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(sb.String(), "\r", ""), "\n", ""))
	if err != nil {
		t.Fatalf("本文が base64 でない: %v", err)
	}
	return m, string(body)
}

// STARTTLS の後に認証し、件名は種類から、本文は通知の本文のままで、全員の宛先に送る。
func TestEmailSendsOverSTARTTLS(t *testing.T) {
	f := newFakeSMTP(t, true, false)
	msg := "【録音できなかった】番組 A\n" + strings.Repeat("長い行 ", 60)
	if err := f.email().Notify(context.Background(), KindExpired, msg); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.data) != 1 {
		t.Fatalf("届いたメール = %d 通", len(f.data))
	}
	for _, l := range f.plain {
		u := strings.ToUpper(l)
		if strings.HasPrefix(u, "AUTH") || strings.HasPrefix(u, "MAIL") || strings.HasPrefix(u, "RCPT") || u == "DATA" {
			t.Fatalf("TLS の前に平文で %q を送った", l)
		}
	}
	if len(f.auth) != 1 || f.auth[0] != "\x00user@example.com\x00TOPSECRET-PASS" {
		t.Fatalf("認証 = %q", f.auth)
	}
	if len(f.rcpts) != 2 || !strings.Contains(f.rcpts[0], "a@example.com") || !strings.Contains(f.rcpts[1], "b@example.com") {
		t.Fatalf("宛先 = %q", f.rcpts)
	}
	m, body := parseSent(t, f.data[0])
	subj, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subj != "[rajikura] 録音できなかった" {
		t.Fatalf("件名 = %q (%v)", subj, err)
	}
	if m.Header.Get("From") != "rajikura@example.com" || m.Header.Get("To") != "a@example.com, b@example.com" {
		t.Fatalf("From/To = %q / %q", m.Header.Get("From"), m.Header.Get("To"))
	}
	if body != msg {
		t.Fatalf("本文 = %q", body)
	}
}

// ポート 465 なら最初から TLS で話す（STARTTLS は使わない）。
func TestEmailSendsOverImplicitTLS(t *testing.T) {
	f := newFakeSMTP(t, false, true)
	e := f.email()
	// 465 番は固定で使えないことがあるので、偽のサーバーは任意のポートで待たせ、同じ振る舞いをこの印で選ぶ。
	// ポートの判定そのものは下で確かめる。
	e.implicitTLS = true
	if !(&Email{Port: SMTPTLSPort}).useImplicitTLS() || SMTPTLSPort != 465 || (&Email{Port: 587}).useImplicitTLS() {
		t.Fatal("ポートの判定が違う")
	}
	if err := e.Notify(context.Background(), KindTest, "テスト本文"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.data) != 1 {
		t.Fatalf("届いたメール = %d 通", len(f.data))
	}
	if len(f.plain) != 0 {
		t.Fatalf("TLS の前に平文を送った: %q", f.plain)
	}
}

// サーバーが STARTTLS を出さなければ、認証も本文も送らずにエラーにする。
func TestEmailRefusesWithoutSTARTTLS(t *testing.T) {
	f := newFakeSMTP(t, false, false)
	err := f.email().Notify(context.Background(), KindExpired, "本文")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auth) != 0 || len(f.data) != 0 || len(f.froms) != 0 {
		t.Fatalf("平文のサーバーに送った: auth=%q data=%d", f.auth, len(f.data))
	}
	if strings.Contains(err.Error(), "TOPSECRET-PASS") {
		t.Fatalf("エラーにパスワードが出た: %v", err)
	}
}

// 証明書を信じられなければ送らない（認証も送らない）。
func TestEmailRefusesUntrustedCertificate(t *testing.T) {
	f := newFakeSMTP(t, true, false)
	e := f.email()
	e.TLSConfig = nil // 偽のサーバーの証明書を信じない
	err := e.Notify(context.Background(), KindExpired, "本文")
	if err == nil {
		t.Fatal("証明書を信じられないのに送れた")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auth) != 0 || len(f.data) != 0 {
		t.Fatalf("送った: auth=%q data=%d", f.auth, len(f.data))
	}
}

// エラーにパスワードを出さない（サーバーが認証の中身を返してきても）。
func TestEmailErrorOmitsPassword(t *testing.T) {
	e := &Email{Host: "127.0.0.1", Port: 1, Username: "u", Password: "TOPSECRET-PASS", From: "a@example.com", To: []string{"b@example.com"}}
	err := e.Notify(context.Background(), KindTest, "x")
	if err == nil || strings.Contains(err.Error(), "TOPSECRET-PASS") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmailSubject(t *testing.T) {
	for _, k := range Kinds {
		if s := EmailSubject(k); s == "[rajikura] "+k && k != "" {
			// 全部の種類に日本語の件名がある。
			t.Errorf("%s の件名が無い: %q", k, s)
		}
	}
	if got := EmailSubject(KindExpired); got != "[rajikura] 録音できなかった" {
		t.Fatalf("got %q", got)
	}
}
