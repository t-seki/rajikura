// Package trayconf は、Windows のトレイ（cmd/rajikura-tray）の接続先と API トークンを置く tray.toml を読み書きする
// （docs/adr/0023-windows-tray-embedded-and-linked-by-loopback.md）。CLI の client.toml とは分け、そちらは読みも書きもしない。
//
// tray.toml を書くのは、トレイの連携（メニューの「Web UI と連携する」）と、rajikura の service install・tray install の 2 つ
// 。どちらもこのパッケージを使う。
package trayconf

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config は tray.toml の中身（接続先と API トークン）。
type Config struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// Path は tray.toml のパス（configDir の下の rajikura\tray.toml。Windows では %AppData%\rajikura\tray.toml）。
func Path(configDir string) string {
	return filepath.Join(configDir, "rajikura", "tray.toml")
}

// Read は tray.toml を読む。無ければ空の値と nil を返す（未設定として扱う）。知らないキーは誤りにする。
func Read(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("%s を読めない: %w", path, err)
	}
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return Config{}, fmt.Errorf("%s: 知らないキー %s（url と token だけを書く）", path, strings.Join(names, ", "))
	}
	return c, nil
}

// Write は tray.toml を書く（フォルダが無ければ作る）。書きかけのファイルが残らないよう、同じフォルダの一時のファイルに
// 書いてから置き換える。
func Write(path string, c Config) error {
	var buf bytes.Buffer
	buf.WriteString("# rajikura のトレイの接続先と API トークン。トレイのメニューの「Web UI と連携する」と、rajikura の service install・tray install が書く。\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("tray.toml を作れない: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%s を作れない: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "tray.toml.*")
	if err != nil {
		return fmt.Errorf("%s に書けない: %w", dir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("%s に書けない: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s に書けない: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s を置き換えられない: %w", path, err)
	}
	return nil
}

// ParseURL は接続先の URL を読む。http か https で、ホストが要る。
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("接続先 %q が URL として読めない（http:// か https:// で始める）", raw)
	}
	return u, nil
}

// NormalizeURL は、接続先の URL の末尾の / を落とす（トレイが接続先を読むときと、install が tray.toml の url と比べるときの形）。
func NormalizeURL(raw string) string {
	return strings.TrimRight(raw, "/")
}
