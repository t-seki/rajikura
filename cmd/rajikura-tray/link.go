package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// linkTimeout は、連携の受け口を開いておく長さ。過ぎたら閉じる。
const linkTimeout = 5 * time.Minute

// linkSession は、連携の受け口 1 つ（ADR 0023）。127.0.0.1 の空いているポートで開き、Web UI の連携の画面から
// フォームの POST（/callback に token と state）を受ける。state が合えば save に接続先とトークンを渡し、受け口を閉じる。
// state が合わなければ書かずに断り、開いたままにする。開いてから linkTimeout で閉じる。
type linkSession struct {
	// Port は受け口のポート、State は使い捨ての state。
	Port  int
	State string

	ln     net.Listener
	srv    *http.Server
	done   chan struct{}
	once   sync.Once
	result error
}

// errLinkTimeout は、受け口を開いてから linkTimeout の間に連携されなかったこと。errLinkCanceled は、受け口を閉じたこと（連携し直したなど）。
var (
	errLinkTimeout  = errors.New("連携の受け口を開いてから 5 分の間に連携されなかったので閉じた")
	errLinkCanceled = errors.New("連携の受け口を閉じた")
)

// startLink は受け口を開く。serveURL は連携の後に tray.toml に書く接続先で、save はトークンを受け取ったときに呼ぶ
// （エラーを返したら、ブラウザにその旨を返し、受け口は開いたままにする）。
func startLink(serveURL string, timeout time.Duration, save func(trayConfig) error, log *slog.Logger) (*linkSession, error) {
	state, err := newLinkState()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("連携の受け口を開けない: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if port < 1024 {
		// Web UI は 1024〜65535 のポートにだけ送る。空いているポートとして 1024 より小さいものは返らないはず。
		ln.Close()
		return nil, fmt.Errorf("連携の受け口のポート %d が 1024 より小さい", port)
	}
	s := &linkSession{Port: port, State: state, ln: ln, done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /callback", func(w http.ResponseWriter, r *http.Request) {
		s.callback(w, r, serveURL, save, log)
	})
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("連携の受け口が止まった", "err", err)
			s.finish(err)
		}
	}()
	go func() {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-t.C:
			s.finish(errLinkTimeout)
		case <-s.done:
		}
	}()
	return s, nil
}

// newLinkState は、使い捨ての state（32 バイトの乱数を 16 進にしたもの）を作る。
func newLinkState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("連携の state を作れない: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// linkPageURL は、ブラウザで開く Web UI の連携の画面の URL（<serveURL>/tray-link?port=…&state=…&name=…）。
func linkPageURL(serveURL string, port int, state, name string) string {
	q := url.Values{}
	q.Set("port", fmt.Sprint(port))
	q.Set("state", state)
	q.Set("name", name)
	return serveURL + "/tray-link?" + q.Encode()
}

func (s *linkSession) callback(w http.ResponseWriter, r *http.Request, serveURL string, save func(trayConfig) error, log *slog.Logger) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeLinkPage(w, http.StatusBadRequest, "連携できなかった（送られた中身を読めない）。トレイのメニューから連携し直す。")
		return
	}
	state, token := r.PostForm.Get("state"), r.PostForm.Get("token")
	if subtle.ConstantTimeCompare([]byte(state), []byte(s.State)) != 1 {
		log.Warn("連携の state が合わないので書かない")
		writeLinkPage(w, http.StatusForbidden, "連携できなかった（state が合わない）。トレイのメニューから連携し直す。")
		return
	}
	if token == "" {
		writeLinkPage(w, http.StatusBadRequest, "連携できなかった（API トークンが送られていない）。トレイのメニューから連携し直す。")
		return
	}
	if err := save(trayConfig{URL: serveURL, Token: token}); err != nil {
		log.Error("連携の結果を書けない", "err", err)
		writeLinkPage(w, http.StatusInternalServerError, "連携できなかった（トレイが tray.toml を書けない）。トレイのログ（%LocalAppData%\\rajikura\\tray.log）を見る。")
		return
	}
	writeLinkPage(w, http.StatusOK, "連携した。この画面は閉じてよい。")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.finish(nil)
}

func writeLinkPage(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, "<!doctype html><html lang=\"ja\"><head><meta charset=\"utf-8\"><title>rajikura のトレイ</title></head><body><p>%s</p></body></html>\n", html.EscapeString(msg))
}

// finish は受け口を閉じる（最初の 1 回だけ効く）。err は Wait が返すもの（nil なら連携した）。
// 閉じるのは、返しかけの答えを書き終えてからにする（Shutdown が待つ）。
func (s *linkSession) finish(err error) {
	s.once.Do(func() {
		s.result = err
		close(s.done)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.srv.Shutdown(ctx)
		}()
	})
}

// Close は受け口を閉じる（連携し直すときに前の受け口を閉じる）。
func (s *linkSession) Close() {
	s.finish(errLinkCanceled)
}

// Wait は受け口が閉じるまで待ち、連携したなら nil を返す。
func (s *linkSession) Wait() error {
	<-s.done
	return s.result
}
