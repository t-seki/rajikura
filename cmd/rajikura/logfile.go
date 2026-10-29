package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// 既定のログの回し方（Windows のサービスとして動くとき。docs/adr/0020-windows-service-with-virtual-account.md）。
// ファイルが logMaxBytes を超えそうになったら回し、今のファイルを含めて logFiles 本まで残す。
const (
	logMaxBytes = 10 << 20
	logFiles    = 5
)

// rotatingFile は、大きさで回すログのファイル。書き足す前に maxBytes を超えるなら、path を path.1 に、
// path.1 を path.2 に…と送り（path.<files-1> より古いものは消す）、path を作り直してから書く。
// 1 回の書き込みが maxBytes より大きいときは、回した後の空のファイルにそのまま書く（行を割らない）。
// 並行に書いてよい。
type rotatingFile struct {
	path     string
	maxBytes int64
	files    int

	mu sync.Mutex
	// f は今のファイル。nil なら、回す途中で閉じた・開けなかったので、次の Write で開き直す。
	f    *os.File
	size int64
	// closed は Close を呼んだか（呼んだ後の Write は os.ErrClosed）。f が nil なのとは別にする
	// （開き直しに失敗しても、ログを書けないままにしない）。
	closed bool
}

// openRotatingFile は path を書き足すために開く（フォルダが無ければ作る）。
func openRotatingFile(path string, maxBytes int64, files int) (*rotatingFile, error) {
	if maxBytes <= 0 || files < 1 {
		return nil, fmt.Errorf("ログの回し方が正しくない（大きさ %d、本数 %d）", maxBytes, files)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ログのフォルダを作れない: %w", err)
	}
	r := &rotatingFile{path: path, maxBytes: maxBytes, files: files}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("ログのファイルを開けない: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("ログのファイルの大きさを読めない: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write は p を書き足す。必要なら先に回す。前の Write で開き直せなかったときは、ここで開き直しを試す。
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		// 回せなかったら（今のファイルを閉じられない、ほかのプロセスが古いファイルを開いているなど）、今のファイルを
		// 開き直して書き続ける（ログを落とさない。閉じかけのハンドルには書かない）。
		if err := r.rotate(); err != nil && r.f == nil {
			if oerr := r.open(); oerr != nil {
				return 0, errors.Join(err, oerr)
			}
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate は今のファイルを閉じて送り、新しいファイルを開く（Windows は開いたままのファイルを rename できないので、先に閉じる）。
func (r *rotatingFile) rotate() error {
	// 閉じられなくても、そのハンドルには書かない（nil にして、Write に開き直させる）。
	err := r.f.Close()
	r.f = nil
	if err != nil {
		return fmt.Errorf("ログのファイルを閉じられない: %w", err)
	}
	if r.files == 1 {
		if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ログのファイルを消せない: %w", err)
		}
		return r.open()
	}
	oldest := fmt.Sprintf("%s.%d", r.path, r.files-1)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("古いログのファイルを消せない: %w", err)
	}
	for i := r.files - 2; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", r.path, i)
		if err := os.Rename(from, fmt.Sprintf("%s.%d", r.path, i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("古いログのファイルを送れない: %w", err)
		}
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		return fmt.Errorf("ログのファイルを送れない: %w", err)
	}
	return r.open()
}

// Close はファイルを閉じる。
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
