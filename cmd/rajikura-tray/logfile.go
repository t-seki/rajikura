package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// logMaxBytes は、トレイのログのファイルを替える大きさ。超えそうになったら今のファイルを tray.log.1 に替え（前の tray.log.1 は消す）、
// 新しいファイルに書く。windowsgui の exe は標準出力・標準エラーに書けないので、ログはファイルにだけ出す。
const logMaxBytes = 1 << 20

// trayLogPath は、トレイのログのパス（cacheDir の下の rajikura\tray.log。Windows では %LocalAppData%\rajikura\tray.log）。
func trayLogPath(cacheDir string) string {
	return filepath.Join(cacheDir, "rajikura", "tray.log")
}

// logFile は、maxBytes を超えそうになったら path を path.1 に替えるログのファイル（残すのは path.1 の 1 本だけ）。並行に書いてよい。
// 1 回の書き込みが maxBytes より大きいときは、替えた後の空のファイルにそのまま書く（行を割らない）。
type logFile struct {
	path     string
	maxBytes int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// openLogFile は path を書き足すために開く（フォルダが無ければ作る）。
func openLogFile(path string, maxBytes int64) (*logFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ログのフォルダを作れない: %w", err)
	}
	l := &logFile{path: path, maxBytes: maxBytes}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *logFile) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("ログのファイルを開けない: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("ログのファイルの大きさを読めない: %w", err)
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Write は p を書き足す。必要なら先に替える。替えられなかったときは、今のファイルに書き続ける（ログを落とさない）。
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	if l.size > 0 && l.size+int64(len(p)) > l.maxBytes {
		if err := l.rotate(); err != nil && l.f == nil {
			if oerr := l.open(); oerr != nil {
				return 0, errors.Join(err, oerr)
			}
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// rotate は今のファイルを閉じて path.1 に替え、新しいファイルを開く（Windows は開いたままのファイルを rename できないので、先に閉じる）。
func (l *logFile) rotate() error {
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("ログのファイルを閉じられない: %w", err)
	}
	old := l.path + ".1"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("古いログのファイルを消せない: %w", err)
	}
	if err := os.Rename(l.path, old); err != nil {
		return fmt.Errorf("ログのファイルを替えられない: %w", err)
	}
	return l.open()
}

// Close はファイルを閉じる。
func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
