package main

import (
	"net/url"

	"github.com/t-seki/rajikura/internal/trayconf"
)

// defaultURL は、-url にも tray.toml にも接続先が無いときに使う接続先（service install の既定の -listen）。
const defaultURL = "http://127.0.0.1:8080"

// trayConfig は tray.toml の中身（接続先と API トークン）。CLI の client.toml とは分け、そちらは読みも書きもしない（ADR 0023）。
// 読み書きは internal/trayconf にあり、rajikura の service install・tray install も同じものを使う。
type trayConfig = trayconf.Config

// trayConfigPath は tray.toml のパス（configDir の下の rajikura\tray.toml。Windows では %AppData%\rajikura\tray.toml）。
func trayConfigPath(configDir string) string { return trayconf.Path(configDir) }

// readTrayConfig は tray.toml を読む。無ければ空の値と nil を返す（未設定として扱う）。
func readTrayConfig(path string) (trayConfig, error) { return trayconf.Read(path) }

// writeTrayConfig は tray.toml を書く（一時のファイルに書いてから置き換える）。
func writeTrayConfig(path string, c trayConfig) error { return trayconf.Write(path, c) }

// connection は、トレイが今つなぐ先とトークン。Token が空なら未設定。
type connection struct {
	URL   string
	Token string
}

// resolveConnection は接続先を、-url の値（flagURL）、tray.toml の url、既定（defaultURL）の順に決める（CLI と同じく引数が先）。
// トークンは tray.toml の token だけ。末尾の / は落とす。
func resolveConnection(c trayConfig, flagURL string) (connection, error) {
	raw := flagURL
	if raw == "" {
		raw = c.URL
	}
	if raw == "" {
		raw = defaultURL
	}
	u, err := parseBaseURL(raw)
	if err != nil {
		return connection{}, err
	}
	return connection{URL: trayconf.NormalizeURL(u.String()), Token: c.Token}, nil
}

// parseBaseURL は接続先の URL を読む。http か https で、ホストが要る（rajikura の tray install -url も同じ決まり）。
func parseBaseURL(raw string) (*url.URL, error) { return trayconf.ParseURL(raw) }

// hostOf は、接続先の URL のホスト（ポートは付けない）。トースト・ツールチップの文言に出す。読めなければ URL のまま。
func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return baseURL
	}
	return u.Hostname()
}
