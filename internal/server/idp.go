package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"

	"github.com/t-seki/rajikura/internal/config"
	"github.com/t-seki/rajikura/internal/store"
)

// IdP のログイン（docs/adr/0040-idp-login-by-generic-oidc.md）。rajikura は汎用の OpenID Connect の RP になり
// （Authorization Code + PKCE、issuer の discovery）、紐づけた 1 つの iss + sub の組だけを通す。パスワードのログインは残る。
//
//   - GET /api/idp: IdP の設定と紐づけ（Web UI のセッションだけで通す。API トークンは 403）
//   - PUT /api/idp: IdP の設定を変える（同じ）。issuer か client id を変えたら紐づけを消す
//   - POST /api/idp/link: 今のパスワードを確かめて紐づけを始め、IdP へ移る URL を返す（同じ。今のパスワードの間違いは、
//     ログインの総当たりの制限に数える。protect.go）。Web UI の URL の host で開いた画面からだけ受ける
//   - DELETE /api/idp/link: 紐づけを外す（同じ。パスワードは求めない）
//   - GET /api/idp/login-option: ログインの画面が IdP のログインを出すか（ログインの外）
//   - GET /api/idp/login: IdP のログインを始める（ログインの外。top-level の移動で開く）。Web UI の URL の host でなければ、
//     まずそこへ移る
//   - GET /api/idp/callback: IdP からの戻り先（config.IdPCallbackPath）。ログインならセッションを作って / へ、紐づけなら紐づけて
//     /settings へ移る。失敗したら idp_error を付けて同じ所へ移る
//
// ログインの途中の状態（state・nonce・PKCE の verifier）は serve のメモリに state をキーにして idpPendingTTL の間置き、ブラウザには
// state を入れた SameSite=Lax の cookie（idpStateCookie）を渡して、戻ってきた state と突き合わせる。IdP のログインの失敗は、
// ログインの失敗の数（protect.go）に入れない。

// idpStateCookie は、ログインの途中の state を入れる cookie の名前（SameSite=Lax。IdP から戻る移動はほかのサイトからの移動なので）。
const idpStateCookie = "rajikura_idp_state"

// idpStatePath は idpStateCookie の Path。
const idpStatePath = "/api/idp/"

// idpPendingTTL は、ログインの途中の状態を置いておく時間。
const idpPendingTTL = 10 * time.Minute

// idpMaxPending は、ログインの途中の状態を置く数の上限（ログインの外から始められるので、メモリを使い切らせない）。
// 超えたら古いものから捨てる。
const idpMaxPending = 64

// idpTimeout は、IdP（discovery・token の口）を待つ時間。
const idpTimeout = 30 * time.Second

// IdP のログインの失敗を、戻った先の画面に伝える idp_error の値。
const (
	// idpErrUnavailable は、IdP を設定していない・紐づけていない（ログイン）か、IdP の設定が途中で変わった。
	idpErrUnavailable = "unavailable"
	// idpErrDenied は、IdP がログインを拒んだ（戻り先に error が付いた）。
	idpErrDenied = "denied"
	// idpErrExpired は、ログインの途中の状態が無い（10 分を過ぎた、起動し直した、state の cookie と食い違う）。
	idpErrExpired = "expired"
	// idpErrFailed は、IdP とのやりとり（discovery・code の引き換え・ID トークンの検証・nonce）に失敗した。
	idpErrFailed = "failed"
	// idpErrMismatch は、IdP のログインの利用者が、紐づけた iss + sub と違う。
	idpErrMismatch = "mismatch"
)

// IdPStore は IdP の設定と紐づけの保存先（store.Store が満たす）。
type IdPStore interface {
	IdPSettings(ctx context.Context, secretName string) (body string, sealed []byte, err error)
	SaveIdPSettings(ctx context.Context, v store.IdPSave) error
	IdPBinding(ctx context.Context) (string, error)
	SetIdPBinding(ctx context.Context, body string) error
	DeleteIdPBinding(ctx context.Context) error
}

var _ IdPStore = (*store.Store)(nil)

// SecretBox は秘密の暗号化と復号（*secret.Box が満たす）。
type SecretBox interface {
	Seal(name, value string) ([]byte, error)
	Open(name string, sealed []byte) (string, error)
}

// idpBinding は紐づけ（app_state に JSON で置く）。
type idpBinding struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	// Email・PreferredUsername は表示用（通すかどうかには使わない）。ID トークンに無ければ空。
	// PreferredUsername の欄が無い前の紐づけの JSON は、空として読む（紐づけ直すと入る）。
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
}

// idpPending はログインの途中の状態 1 つ。
type idpPending struct {
	// link は紐づけを始めたものか（false ならログイン）。
	link     bool
	nonce    string
	verifier string
	// issuer・clientID は、始めたときの IdP の設定（戻ったときに変わっていたら断る）。
	issuer   string
	clientID string
	expires  time.Time
}

// idpFlows は IdP のログインの、serve のメモリにだけ置く状態。register が作る。
type idpFlows struct {
	mu      sync.Mutex
	pending map[string]idpPending
	order   []string
	// providers は issuer ごとの discovery の結果（鍵の取り直しも持つので使い回す）。
	providers map[string]*oidc.Provider
	// save は IdP の設定と紐づけを変えるのを 1 つずつにする。
	save sync.Mutex
}

// idpConfig は今の IdP の設定と client secret。
type idpConfig struct {
	config.IdP
	clientSecret string
	secretSet    bool
	// secretUnreadable は、client secret が DB にあるのに復号できないこと（未設定として扱う）。
	secretUnreadable bool
}

func (u *UI) registerIdP(mux *http.ServeMux) {
	if u.IdP == nil || u.IdPSecrets == nil {
		return
	}
	u.idp = &idpFlows{pending: map[string]idpPending{}, providers: map[string]*oidc.Provider{}}
	mux.HandleFunc("GET /api/idp", u.guard(u.sessionOnly(u.getIdP)))
	mux.HandleFunc("PUT /api/idp", u.guard(u.sessionOnly(u.putIdP)))
	mux.HandleFunc("POST /api/idp/link", u.guard(u.sessionOnly(u.startIdPLink)))
	mux.HandleFunc("DELETE /api/idp/link", u.guard(u.sessionOnly(u.deleteIdPLink)))
	mux.HandleFunc("GET /api/idp/login-option", u.guard(u.idpLoginOption))
	mux.HandleFunc("GET /api/idp/login", u.startIdPLogin)
	// 戻り先は config.IdPCallbackPath（docs_test.go がルートを拾えるように、文字列で書く。TestIdPCallbackPath が揃っていることを確かめる）。
	mux.HandleFunc("GET /api/idp/callback", u.idpCallback)
}

// sessionOnly は、Web UI のセッション（cookie）でだけ通す。API トークン（Authorization: Bearer）を付けた要求は、トークンが有効でも 403。
func (u *UI) sessionOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerToken(r); ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "IdP の設定と紐づけは、API トークンでは使えない（Web UI でログインして使う）"})
			return
		}
		u.authed(next)(w, r)
	}
}

func (u *UI) idpClient() *http.Client {
	if u.IdPClient != nil {
		return u.IdPClient
	}
	return &http.Client{Timeout: idpTimeout}
}

// loadIdP は今の IdP の設定を読む。
func (u *UI) loadIdP(ctx context.Context) (idpConfig, error) {
	body, sealed, err := u.IdP.IdPSettings(ctx, config.SecretIdPClientSecret)
	if err != nil {
		return idpConfig{}, err
	}
	var c idpConfig
	if body != "" {
		if err := json.Unmarshal([]byte(body), &c.IdP); err != nil {
			return idpConfig{}, fmt.Errorf("DB の IdP の設定が読めない: %w", err)
		}
	}
	if sealed != nil {
		c.secretSet = true
		v, err := u.IdPSecrets.Open(config.SecretIdPClientSecret, sealed)
		if err != nil {
			c.secretUnreadable = true
			u.log().Error("IdP の client secret を復号できないので、未設定として扱う（Web UI で入れ直す）", "err", err)
		} else {
			c.clientSecret = v
		}
	}
	return c, nil
}

func (u *UI) loadBinding(ctx context.Context) (*idpBinding, error) {
	body, err := u.IdP.IdPBinding(ctx)
	if err != nil || body == "" {
		return nil, err
	}
	var b idpBinding
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return nil, fmt.Errorf("DB の紐づけが読めない: %w", err)
	}
	return &b, nil
}

// idpResponse は GET /api/idp と PUT /api/idp の答え。
type idpResponse struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	WebURL   string `json:"web_url"`
	// ClientSecretSet は client secret があるか（復号できないものも数える）。値は返さない。
	ClientSecretSet bool `json:"client_secret_set"`
	// ClientSecretUnreadable は、client secret が DB にあるのに復号できないか。
	ClientSecretUnreadable bool `json:"client_secret_unreadable"`
	// RedirectURL は IdP に登録する戻り先の URL。設定していなければ空。
	RedirectURL string `json:"redirect_url"`
	// WebHost は Web UI の URL の host（ポートがあれば付く）。紐づけを始められるのは、この host で開いた画面からだけ。
	WebHost string `json:"web_host"`
	// WebHostAllowed は、Web UI の URL の host の名前を、IP のアドレス・localhost・-allowed-hosts のどれかとして受けるか
	// （false なら、戻り先の要求は 421 で断られる）。設定していなければ true。
	WebHostAllowed bool `json:"web_host_allowed"`
	// Binding は紐づけ。無ければ null。
	Binding *idpBinding `json:"binding"`
}

func (u *UI) idpResponse(ctx context.Context) (idpResponse, error) {
	c, err := u.loadIdP(ctx)
	if err != nil {
		return idpResponse{}, err
	}
	b, err := u.loadBinding(ctx)
	if err != nil {
		return idpResponse{}, err
	}
	resp := idpResponse{
		Issuer: c.Issuer, ClientID: c.ClientID, WebURL: c.WebURL,
		ClientSecretSet: c.secretSet, ClientSecretUnreadable: c.secretUnreadable,
		RedirectURL: c.RedirectURL(), WebHost: c.WebHost(), WebHostAllowed: true, Binding: b,
	}
	if c.WebURL != "" {
		resp.WebHostAllowed = hostAllowed(c.WebHost(), u.AllowedHosts)
	}
	return resp, nil
}

func (u *UI) writeIdP(w http.ResponseWriter, r *http.Request) {
	resp, err := u.idpResponse(r.Context())
	if err != nil {
		u.log().Error("IdP の設定を読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (u *UI) getIdP(w http.ResponseWriter, r *http.Request) { u.writeIdP(w, r) }

// putIdP は IdP の設定を置き換える。issuer・client_id・web_url は 3 つとも書き（全部空なら IdP の設定を消す）、
// client_secret は書けば変え（空なら消す）、省けば今のまま残す。issuer か client id を変えたら紐づけを消す。
func (u *UI) putIdP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer       *string `json:"issuer"`
		ClientID     *string `json:"client_id"`
		WebURL       *string `json:"web_url"`
		ClientSecret *string `json:"client_secret"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		msg := "JSON が読めない"
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			msg = "知らないキー " + strings.TrimPrefix(err.Error(), "json: unknown field ")
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if req.Issuer == nil || req.ClientID == nil || req.WebURL == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issuer・client_id・web_url を全部書く（IdP の設定を消すなら、全部空の文字列）"})
		return
	}
	next, err := config.NormalizeIdP(config.IdP{Issuer: *req.Issuer, ClientID: *req.ClientID, WebURL: *req.WebURL})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	u.idp.save.Lock()
	defer u.idp.save.Unlock()
	cur, err := u.loadIdP(r.Context())
	if err != nil {
		u.log().Error("IdP の設定を読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	save := store.IdPSave{SecretName: config.SecretIdPClientSecret}
	if next.Configured() {
		b, err := json.Marshal(next)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
			return
		}
		save.Body = string(b)
	}
	if req.ClientSecret != nil {
		save.SetSecret = true
		if *req.ClientSecret != "" {
			sealed, err := u.IdPSecrets.Seal(config.SecretIdPClientSecret, *req.ClientSecret)
			if err != nil {
				u.log().Error("IdP の client secret を暗号化できない", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "client secret を暗号化できない（鍵のファイル）"})
				return
			}
			save.Sealed = sealed
		}
	}
	save.ClearBinding = next.Issuer != cur.Issuer || next.ClientID != cur.ClientID
	if err := u.IdP.SaveIdPSettings(r.Context(), save); err != nil {
		u.log().Error("IdP の設定を保存できない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	u.log().Info("IdP の設定を変えた", "issuer", next.Issuer, "web_url", next.WebURL, "binding_cleared", save.ClearBinding)
	u.writeIdP(w, r)
}

// startIdPLink は今のパスワードを確かめて紐づけを始め、{"url": "<IdP へ移る URL>"} を返す（画面がそこへ top-level で移る）。
func (u *UI) startIdPLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON が読めない"})
		return
	}
	c, err := u.loadIdP(r.Context())
	if err != nil {
		u.log().Error("IdP の設定を読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	if !c.Configured() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "IdP を設定していない"})
		return
	}
	// ログインの途中の cookie はホストごとに分かれるので、戻り先と同じ host で開いた画面からだけ始める。
	if !strings.EqualFold(r.Host, c.WebHost()) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("紐づけは、Web UI の URL（%s）で開いた画面から始める", c.WebURL), "web_url": c.WebURL,
		})
		return
	}
	hash, err := u.Passwords.PasswordHash(r.Context())
	if err != nil {
		u.log().Error("パスワードを読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	// パスワードを変える口と同じく、今のパスワードの間違いはログインと同じ数に入れ、違えば 403。
	ok, handled := u.checkPassword(w, func() bool {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) == nil
	})
	if handled {
		return
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "今のパスワードが違う"})
		return
	}
	authURL, err := u.beginIdP(r.Context(), w, c, true)
	if err != nil {
		u.log().Warn("IdP の紐づけを始められない", "issuer", c.Issuer, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "IdP に届かない・IdP の答えが読めない: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

func (u *UI) deleteIdPLink(w http.ResponseWriter, r *http.Request) {
	u.idp.save.Lock()
	defer u.idp.save.Unlock()
	if err := u.IdP.DeleteIdPBinding(r.Context()); err != nil {
		u.log().Error("紐づけを外せない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	u.log().Info("IdP のログインの紐づけを外した")
	u.writeIdP(w, r)
}

// idpLoginOption は、ログインの画面が IdP のログインを出すか（{"linked": <IdP を設定して紐づけているか>, "issuer_host": "<issuer のホスト名>"}）を返す。
func (u *UI) idpLoginOption(w http.ResponseWriter, r *http.Request) {
	c, b, err := u.loginTarget(r.Context())
	if err != nil {
		u.log().Error("IdP の設定を読めない", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "内部エラー"})
		return
	}
	if b == nil {
		writeJSON(w, http.StatusOK, map[string]any{"linked": false, "issuer_host": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": true, "issuer_host": c.IssuerHostname()})
}

// loginTarget は、IdP のログインで通す設定と紐づけを返す。設定していない・紐づけていない・紐づけた issuer が今の設定と違うなら、紐づけは nil。
func (u *UI) loginTarget(ctx context.Context) (idpConfig, *idpBinding, error) {
	c, err := u.loadIdP(ctx)
	if err != nil {
		return idpConfig{}, nil, err
	}
	b, err := u.loadBinding(ctx)
	if err != nil {
		return idpConfig{}, nil, err
	}
	if !c.Configured() || b == nil || b.Issuer != c.Issuer {
		return c, nil, nil
	}
	return c, b, nil
}

// idpRedirect は、IdP のログイン・紐づけの結果の画面へ移す（errCode が空でなければ idp_error を付ける）。
func idpRedirect(w http.ResponseWriter, r *http.Request, link bool, errCode string) {
	target := "/"
	if link {
		target = "/settings"
	}
	q := url.Values{}
	switch {
	case errCode != "":
		q.Set("idp_error", errCode)
	case link:
		q.Set("idp_linked", "1")
	}
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// startIdPLogin は IdP のログインを始める（top-level の GET）。Web UI の URL の host でなければ、まずそこへ移る。
func (u *UI) startIdPLogin(w http.ResponseWriter, r *http.Request) {
	hash, err := u.Passwords.PasswordHash(r.Context())
	if err != nil {
		u.log().Error("パスワードを読めない", "err", err)
		http.Error(w, "内部エラー", http.StatusInternalServerError)
		return
	}
	if hash == "" {
		// パスワードがまだ無い間は IdP のログインを出さない（画面はセットアップコードの画面）。
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	c, b, err := u.loginTarget(r.Context())
	if err != nil {
		u.log().Error("IdP の設定を読めない", "err", err)
		http.Error(w, "内部エラー", http.StatusInternalServerError)
		return
	}
	if b == nil {
		idpRedirect(w, r, false, idpErrUnavailable)
		return
	}
	if !strings.EqualFold(r.Host, c.WebHost()) {
		http.Redirect(w, r, c.WebURL+"/api/idp/login", http.StatusFound)
		return
	}
	authURL, err := u.beginIdP(r.Context(), w, c, false)
	if err != nil {
		u.log().Warn("IdP のログインを始められない", "issuer", c.Issuer, "err", err)
		idpRedirect(w, r, false, idpErrFailed)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// provider は issuer の discovery の結果を返す（成功したものだけ使い回す）。
func (u *UI) provider(issuer string) (*oidc.Provider, error) {
	f := u.idp
	f.mu.Lock()
	p := f.providers[issuer]
	f.mu.Unlock()
	if p != nil {
		return p, nil
	}
	// 鍵の取り直しは、NewProvider に渡した ctx で行うので、要求の ctx ではなく、取り消さない ctx を渡す（待つ時間は client の Timeout）。
	p, err := oidc.NewProvider(oidc.ClientContext(context.Background(), u.idpClient()), issuer)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.providers[issuer] = p
	f.mu.Unlock()
	return p, nil
}

func (u *UI) oauth2Config(c idpConfig, p *oidc.Provider) *oauth2.Config {
	ep := p.Endpoint()
	if c.clientSecret == "" {
		// secret を送らない public client（PKCE だけ）。client_id は本文で送る。
		ep.AuthStyle = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID: c.ClientID, ClientSecret: c.clientSecret, Endpoint: ep,
		RedirectURL: c.RedirectURL(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// beginIdP はログインの途中の状態を作り、state の cookie を付けて、IdP へ移る URL を返す。
func (u *UI) beginIdP(ctx context.Context, w http.ResponseWriter, c idpConfig, link bool) (string, error) {
	p, err := u.provider(c.Issuer)
	if err != nil {
		return "", err
	}
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	now := u.now()
	u.idp.add(state, idpPending{
		link: link, nonce: nonce, verifier: verifier, issuer: c.Issuer, clientID: c.ClientID, expires: now.Add(idpPendingTTL),
	}, now)
	http.SetCookie(w, &http.Cookie{
		Name: idpStateCookie, Value: state, Path: idpStatePath, MaxAge: int(idpPendingTTL / time.Second),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	return u.oauth2Config(c, p).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), nil
}

// add はログインの途中の状態を置く。数が idpMaxPending に届いていれば、古いものから捨てる。
func (f *idpFlows) add(state string, p idpPending, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prune(now)
	for len(f.order) >= idpMaxPending {
		delete(f.pending, f.order[0])
		f.order = f.order[1:]
	}
	f.pending[state] = p
	f.order = append(f.order, state)
}

// prune は期限を過ぎたログインの途中の状態を捨てる（f.mu を持って呼ぶ）。
func (f *idpFlows) prune(now time.Time) {
	kept := f.order[:0]
	for _, s := range f.order {
		p, ok := f.pending[s]
		if !ok {
			continue
		}
		if !now.Before(p.expires) {
			delete(f.pending, s)
			continue
		}
		kept = append(kept, s)
	}
	f.order = kept
}

// take は state のログインの途中の状態を取り出して消す（1 回しか使えない）。無い・期限を過ぎたなら false。
func (f *idpFlows) take(state string, now time.Time) (idpPending, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pending[state]
	if ok {
		delete(f.pending, state)
	}
	f.prune(now)
	if !ok || !now.Before(p.expires) {
		return idpPending{}, false
	}
	return p, true
}

// idpCallback は IdP からの戻り先。state を突き合わせ、code を引き換えて ID トークンを確かめ、ログインならセッションを作り、
// 紐づけなら紐づける。ここでの失敗は、ログインの失敗の数に入れない。
func (u *UI) idpCallback(w http.ResponseWriter, r *http.Request) {
	// state の cookie は 1 回で使い切る。
	http.SetCookie(w, &http.Cookie{
		Name: idpStateCookie, Value: "", Path: idpStatePath, MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	q := r.URL.Query()
	state := q.Get("state")
	c, err := r.Cookie(idpStateCookie)
	if state == "" || err != nil || c.Value != state {
		u.log().Warn("IdP から戻った state が、ログインの途中の cookie と合わない")
		idpRedirect(w, r, false, idpErrExpired)
		return
	}
	pend, ok := u.idp.take(state, u.now())
	if !ok {
		u.log().Warn("IdP から戻った state のログインの途中の状態が無い（10 分を過ぎた・起動し直した）")
		idpRedirect(w, r, false, idpErrExpired)
		return
	}
	if e := q.Get("error"); e != "" {
		u.log().Warn("IdP がログインを拒んだ", "error", e, "description", q.Get("error_description"))
		idpRedirect(w, r, pend.link, idpErrDenied)
		return
	}
	cfg, err := u.loadIdP(r.Context())
	if err != nil {
		u.log().Error("IdP の設定を読めない", "err", err)
		idpRedirect(w, r, pend.link, idpErrFailed)
		return
	}
	if cfg.Issuer != pend.issuer || cfg.ClientID != pend.clientID {
		u.log().Warn("IdP のログインの途中で IdP の設定が変わったので断る")
		idpRedirect(w, r, pend.link, idpErrUnavailable)
		return
	}
	tok, err := u.verifyIdP(r.Context(), cfg, pend, q.Get("code"))
	if err != nil {
		u.log().Warn("IdP のログインを確かめられない", "issuer", cfg.Issuer, "err", err)
		idpRedirect(w, r, pend.link, idpErrFailed)
		return
	}
	if pend.link {
		u.finishIdPLink(w, r, cfg, tok)
		return
	}
	b, err := u.loadBinding(r.Context())
	if err != nil {
		u.log().Error("紐づけを読めない", "err", err)
		idpRedirect(w, r, false, idpErrFailed)
		return
	}
	if b == nil {
		idpRedirect(w, r, false, idpErrUnavailable)
		return
	}
	if tok.Issuer != b.Issuer || tok.Subject != b.Subject {
		u.log().Warn("IdP のログインの利用者が紐づけと違うので断る", "issuer", tok.Issuer)
		idpRedirect(w, r, false, idpErrMismatch)
		return
	}
	cookie, err := u.newSessionCookie(r.Context())
	if err != nil {
		u.log().Error("セッションを作れない", "err", err)
		idpRedirect(w, r, false, idpErrFailed)
		return
	}
	http.SetCookie(w, cookie)
	u.log().Info("IdP のログインでログインした", "issuer", tok.Issuer)
	http.Redirect(w, r, "/", http.StatusFound)
}

// idpIdentity は確かめた ID トークンの中身。
type idpIdentity struct {
	Issuer, Subject, Email, PreferredUsername string
}

// verifyIdP は code を引き換え、ID トークンの署名・iss・aud・期限（go-oidc）と nonce を確かめる。
func (u *UI) verifyIdP(ctx context.Context, cfg idpConfig, pend idpPending, code string) (idpIdentity, error) {
	if code == "" {
		return idpIdentity{}, errors.New("戻り先に code が無い")
	}
	p, err := u.provider(cfg.Issuer)
	if err != nil {
		return idpIdentity{}, err
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, u.idpClient()), idpTimeout)
	defer cancel()
	t, err := u.oauth2Config(cfg, p).Exchange(ctx, code, oauth2.VerifierOption(pend.verifier))
	if err != nil {
		return idpIdentity{}, fmt.Errorf("code を引き換えられない: %w", err)
	}
	raw, ok := t.Extra("id_token").(string)
	if !ok || raw == "" {
		return idpIdentity{}, errors.New("token の答えに id_token が無い")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID, Now: u.now}).Verify(ctx, raw)
	if err != nil {
		return idpIdentity{}, fmt.Errorf("ID トークンを確かめられない: %w", err)
	}
	if idt.Nonce != pend.nonce {
		return idpIdentity{}, errors.New("ID トークンの nonce が合わない")
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idt.Claims(&claims); err != nil {
		return idpIdentity{}, fmt.Errorf("ID トークンの中身が読めない: %w", err)
	}
	return idpIdentity{Issuer: idt.Issuer, Subject: idt.Subject, Email: claims.Email, PreferredUsername: claims.PreferredUsername}, nil
}

func (u *UI) finishIdPLink(w http.ResponseWriter, r *http.Request, cfg idpConfig, tok idpIdentity) {
	u.idp.save.Lock()
	defer u.idp.save.Unlock()
	// 引き換えの間に IdP の設定が変わっていたら紐づけない。
	cur, err := u.loadIdP(r.Context())
	if err != nil || cur.Issuer != cfg.Issuer || cur.ClientID != cfg.ClientID {
		idpRedirect(w, r, true, idpErrUnavailable)
		return
	}
	b, err := json.Marshal(idpBinding{Issuer: tok.Issuer, Subject: tok.Subject, Email: tok.Email, PreferredUsername: tok.PreferredUsername})
	if err == nil {
		err = u.IdP.SetIdPBinding(r.Context(), string(b))
	}
	if err != nil {
		u.log().Error("紐づけを保存できない", "err", err)
		idpRedirect(w, r, true, idpErrFailed)
		return
	}
	u.log().Info("IdP のログインを紐づけた", "issuer", tok.Issuer)
	idpRedirect(w, r, true, "")
}
