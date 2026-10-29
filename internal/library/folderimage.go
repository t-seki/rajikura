package library

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// FolderImage は番組のフォルダに置く画像のファイル名。Jellyfin がアルバムの画像に使う。
	FolderImage = "folder.jpg"
	// partialImageExt は folder.jpg を書く途中の一時ファイルの接尾辞。
	partialImageExt = ".partial.jpg"
)

// StoreFolderImage は l の番組のフォルダに folder.jpg を置く。既にあれば上書きする。
// 一時ファイルに書いてから名前を変えるので、読む側が書きかけを見ない。
func StoreFolderImage(root string, l Layout, data []byte) error {
	dir := filepath.Join(root, l.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("library: %w", err)
	}
	f, err := os.CreateTemp(dir, "folder.*"+partialImageExt)
	if err != nil {
		return fmt.Errorf("library: %w", err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return fmt.Errorf("library: %w", werr)
	}
	// CreateTemp は 0600 で作る。別ユーザーやコンテナの Jellyfin が読めるよう 0644 にする。
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("library: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, FolderImage)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("library: %w", err)
	}
	return nil
}
