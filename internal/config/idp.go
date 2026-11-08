package config

import (
	"fmt"
	"net/url"
	"strings"
)

// IdP のログインの設定（docs/adr/0042-idp-binding-shows-preferred-username.md）。
//
// IdP の設定は PATCH /api/settings の Config・Patch には入れず、専用の口（internal/server の idp.go）で読み書きする
// （API トークンでは読めも変えもしないため）。DB には、秘密を除く値を app_state に、client secret を secrets の表に置く
// （internal/store の idp.go）。

// SecretIdPClientSecret は IdP の client secret の秘密の名前（暗号化の追加データ。secrets の表の name）。
// SecretNames には入れない（GET /api/settings に出さない）。
const SecretIdPClientSecret = "idp.client_secret"

// IdPCallbackPath は、Web UI の URL の下の、IdP からの戻り先のパス。
const IdPCallbackPath = "/api/idp/callback"

// IdP は IdP のログインの設定（client secret を除く）。3 つとも空なら、IdP を設定していない。
type IdP struct {
	// Issuer は IdP の issuer の URL（discovery の起点。ID トークンの iss と同じ文字列）。
	Issuer string `json:"issuer"`
	// ClientID は IdP に登録した client の id。
	ClientID string `json:"client_id"`
	// WebURL は Web UI の URL（scheme://host[:port]。末尾の / とパスは持たない）。戻り先の URL はこの下に作る。
	WebURL string `json:"web_url"`
}

// Configured は、IdP を設定しているか。
func (p IdP) Configured() bool { return p.Issuer != "" }

// RedirectURL は IdP に登録する戻り先の URL。設定していなければ空。
func (p IdP) RedirectURL() string {
	if p.WebURL == "" {
		return ""
	}
	return p.WebURL + IdPCallbackPath
}

// WebHost は Web UI の URL の host（ポートがあれば付く）。設定していなければ空。
func (p IdP) WebHost() string {
	u, err := url.Parse(p.WebURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// IssuerHostname は issuer の URL の host の名前（ログインの画面のボタンに出す）。設定していなければ空。
func (p IdP) IssuerHostname() string {
	u, err := url.Parse(p.Issuer)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// NormalizeIdP は p の値を確かめ、前後の空白を除き、Web UI の URL の末尾の / を除いた設定を返す。
// 3 つとも空なら IdP を設定しないこと（エラーにしない）。1 つでも書いたら 3 つとも要る。
func NormalizeIdP(p IdP) (IdP, error) {
	p.Issuer = strings.TrimSpace(p.Issuer)
	p.ClientID = strings.TrimSpace(p.ClientID)
	p.WebURL = strings.TrimSpace(p.WebURL)
	if p.Issuer == "" && p.ClientID == "" && p.WebURL == "" {
		return p, nil
	}
	var missing []string
	if p.Issuer == "" {
		missing = append(missing, "issuer")
	}
	if p.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if p.WebURL == "" {
		missing = append(missing, "web_url")
	}
	if len(missing) > 0 {
		return IdP{}, fmt.Errorf("IdP の設定は issuer・client_id・web_url を全部書く（%s が無い）", strings.Join(missing, "・"))
	}
	if _, err := checkHTTPURL("issuer", p.Issuer); err != nil {
		return IdP{}, err
	}
	u, err := checkHTTPURL("web_url", p.WebURL)
	if err != nil {
		return IdP{}, err
	}
	if u.Path != "" && u.Path != "/" {
		return IdP{}, fmt.Errorf("web_url %q はパスを持てない（https://rajikura.example のように、Web UI を開く scheme と host だけを書く）", p.WebURL)
	}
	p.WebURL = u.Scheme + "://" + u.Host
	return p, nil
}

// checkHTTPURL は s が http か https の、host のある URL（userinfo・query・fragment なし）かを確かめる。
func checkHTTPURL(key, s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("%s %q は URL として読めない", key, s)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%s %q は http:// か https:// で始める", key, s)
	case u.Host == "" || u.Hostname() == "":
		return nil, fmt.Errorf("%s %q に host が無い", key, s)
	case u.User != nil:
		return nil, fmt.Errorf("%s %q にユーザー名・パスワードを書けない", key, s)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(s, "#"):
		return nil, fmt.Errorf("%s %q に ? や # を書けない", key, s)
	}
	return u, nil
}
