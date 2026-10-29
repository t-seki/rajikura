package client

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// 接続先とトークンを渡す環境変数。
const (
	EnvURL   = "RAJIKURA_URL"
	EnvToken = "RAJIKURA_TOKEN"
)

// Settings は CLI が serve につなぐための接続先とトークン。
type Settings struct {
	URL   string
	Token string
}

// Sources は Settings の読み元。値ごとに、フラグ → 環境変数 → 設定ファイルの順に、先にあるものを使う。
type Sources struct {
	// FlagURL と FlagToken は -url と -token の値（空なら無い）。
	FlagURL, FlagToken string
	// Getenv は環境変数を読む。nil なら os.Getenv。
	Getenv func(string) string
	// ConfigDir は設定ファイルを置くフォルダの親（os.UserConfigDir）。nil なら os.UserConfigDir。
	ConfigDir func() (string, error)
	// GOOS は権限を確かめるかを決める OS。空なら runtime.GOOS。
	GOOS string
}

// ConfigPath は設定ファイルのパス（ConfigDir の下の rajikura/client.toml）。
func (src Sources) ConfigPath() (string, error) {
	dir := src.ConfigDir
	if dir == nil {
		dir = os.UserConfigDir
	}
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "rajikura", "client.toml"), nil
}

type settingsFile struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// Resolve は接続先とトークンを読む。続けてよいが知らせたいこと（設定ファイルの権限が広い、トークンが平文で流れうる）は
// warnings に入れて返す。接続先かトークンがどこにも無い・正しくなければエラー。
func Resolve(src Sources) (s Settings, warnings []string, err error) {
	getenv := src.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	s = Settings{URL: src.FlagURL, Token: src.FlagToken}
	if s.URL == "" {
		s.URL = getenv(EnvURL)
	}
	if s.Token == "" {
		s.Token = getenv(EnvToken)
	}
	path, perr := src.ConfigPath()
	if s.URL == "" || s.Token == "" {
		if perr != nil {
			return Settings{}, nil, fmt.Errorf("設定ファイルの場所が分からない: %w", perr)
		}
		f, w, err := readSettingsFile(path, src.GOOS)
		if err != nil {
			return Settings{}, nil, err
		}
		warnings = append(warnings, w...)
		if s.URL == "" {
			s.URL = f.URL
		}
		if s.Token == "" {
			s.Token = f.Token
		}
	}
	where := "設定ファイル " + path
	if perr != nil {
		where = "設定ファイル"
	}
	if s.URL == "" {
		return Settings{}, nil, fmt.Errorf("接続先が無い（-url、環境変数 %s、%s の url のどれかに serve の URL を書く）", EnvURL, where)
	}
	if s.Token == "" {
		return Settings{}, nil, fmt.Errorf("API トークンが無い（-token、環境変数 %s、%s の token のどれかに書く。トークンは Web UI で発行する）", EnvToken, where)
	}
	u, err := parseBaseURL(s.URL)
	if err != nil {
		return Settings{}, nil, err
	}
	if u.Scheme == "http" && !isLocalhost(u.Hostname()) {
		warnings = append(warnings, fmt.Sprintf("接続先 %s は http:// なので、API トークンが平文で流れうる（https:// の URL を使うか、Tailscale などの暗号化された経路で使う）", s.URL))
	}
	return s, warnings, nil
}

// readSettingsFile は設定ファイルを読む。無ければ空の値を返す。
// Windows のほかでは、権限が 0600 より広ければ（グループかほかの利用者が読み書きできれば）警告を返す。
func readSettingsFile(path, goos string) (settingsFile, []string, error) {
	var f settingsFile
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil, nil
	}
	if err != nil {
		return f, nil, fmt.Errorf("設定ファイル %s を読めない: %w", path, err)
	}
	var warnings []string
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			return f, nil, fmt.Errorf("設定ファイル %s を読めない: %w", path, err)
		}
		if PermissionTooOpen(fi.Mode()) {
			warnings = append(warnings, fmt.Sprintf("設定ファイル %s の権限が %#o で、0600 より広い（API トークンがほかの利用者に読まれうる。chmod 600 %s で狭める）", path, fi.Mode().Perm(), path))
		}
	}
	md, err := toml.Decode(string(b), &f)
	if err != nil {
		return f, nil, fmt.Errorf("設定ファイル %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return f, nil, fmt.Errorf("設定ファイル %s: 知らないキー %s（url と token だけを書く）", path, strings.Join(names, ", "))
	}
	return f, warnings, nil
}

// PermissionTooOpen は、権限が 0600 より広い（グループかほかの利用者に何かの権限がある）こと。
func PermissionTooOpen(mode fs.FileMode) bool {
	return mode.Perm()&0o077 != 0
}

// parseBaseURL は接続先の URL を読む。http か https で、ホストが要る。
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("接続先 %q が URL として読めない（http:// か https:// で始める）", raw)
	}
	return u, nil
}

// isLocalhost は、ホストが localhost かループバックの IP アドレスであること。
func isLocalhost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
