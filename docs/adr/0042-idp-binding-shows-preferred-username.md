# Web UI のログインに汎用の OIDC を足し、紐づけた 1 人だけを通す。紐づけた相手は IdP の利用者名で見分ける

## 決定

[ADR 0040](0040-idp-login-by-generic-oidc.md) を置き換える。ADR 0040 の決定を全部ここに書き直したうえで、IdP の利用者名（`preferred_username`）も紐づけに保存し、紐づけた相手の表示に使う。

Web UI のログインに、IdP のログインを足す。rajikura が OpenID Connect の RP になり、どの IdP でも同じ作りで使える汎用の形にする（Authorization Code + PKCE、issuer の discovery）。パスワードのログイン（[ADR 0008](0008-web-ui-shape.md)・[ADR 0041](0041-first-password-by-setup-code-and-reset-clears-binding.md)）は残し、IdP のログインはもう 1 つの入り方にする。アクセスは今までどおり LAN と VPN の内側に限り、インターネットに出す前提は足さない。

- 通すのは、紐づけた 1 つの `iss` + `sub` の組だけ。ID トークンの `preferred_username` と `email` は、紐づけた相手を画面で見分けるためにだけ使い、通すかどうかには使わない。求める scope は `openid profile email`。
- 紐づけには `iss`・`sub`・`email` に加えて `preferred_username` も保存する（ID トークンに無ければ空）。`GET /api/idp` の `binding` に `preferred_username` の欄を足し、`email` は残す。「紐づけた相手」の表示は、`preferred_username` → `email` → `sub` の順で、空でない最初の値を出す。
- 前から保存してある紐づけ（`preferred_username` の欄が無い JSON）は、そのまま読めて空として扱う。作り直しや移し替えはしない。紐づけ直すと入る。
- 設定できる IdP は 1 つだけ。issuer・client id・Web UI の URL は設定に、client secret は秘密に置く（[ADR 0015](0015-settings-and-secrets-in-db.md)）。client secret は省けて、省いたら secret を送らない public client（PKCE だけ）として扱う。
- IdP の設定は、`PATCH /api/settings` とは別の口で変え、Web UI のセッションでしか変えられない（API トークンでは変えられない）。issuer か client id を変えたら、紐づけを消す。
- 戻り先の URL は、設定の「Web UI の URL」（例: `https://rajikura.example`）の下の決まったパスにする。要求の `Host` や `X-Forwarded-Proto` からは組み立てない。設定の画面に、IdP に登録する戻り先の URL を出す。Web UI の URL の名前が `-allowed-hosts`（[ADR 0021](0021-web-ui-origin-host-and-login-limit.md)）に無ければ、設定の画面で知らせる。ほかのホスト（IP のアドレスなど）で開いた画面から IdP のログインを始めたら、まず Web UI の URL へ移ってから IdP へ向かう。
- 紐づけは、パスワードでログインした Web UI の設定の画面で行う。紐づけを始めるときは今のパスワードを求め、IdP から戻ってきた `iss` + `sub` を紐づける。紐づけを外すときはパスワードを求めない。
- ログインの画面は、紐づけが済んでいれば「`<issuer のホスト名>` でログイン」を出す。パスワードがまだ無い間（セットアップコードの画面）は出さない。画面を開いたときに自動で IdP へ移らない。
- ログインの途中の状態（state・nonce・PKCE の verifier）は、`serve` のメモリに state をキーにして 10 分置く。ブラウザには、state を入れた `SameSite=Lax`・`HttpOnly`・`Secure` の短い cookie を渡し、IdP から戻ってきた state と突き合わせる。起動し直すと、途中のログインは失敗する。
- IdP のログインで作るセッションは、パスワードのログインと同じ（cookie は `HttpOnly`・`Secure`・`SameSite=Strict`、期限は 30 日、DB には ID の SHA-256 を置く。ADR 0008）。
- IdP のログインの失敗（IdP が拒んだ、紐づけと違う `sub` だった）は、ログインの失敗の数（ADR 0021）に入れない。
- パスワードを戻す（`serve -reset-password`）と、紐づけも消す。IdP の設定は残す（ADR 0041）。
- rajikura からログアウトしても、IdP からはログアウトしない。
- API トークン・CLI・トレイの認証は変えない。
- ID トークンの検証は、`github.com/coreos/go-oidc/v3` と `golang.org/x/oauth2` に任せる。

## 理由

- 利用者が自宅の IdP（Pocket ID など）や Google にログインをまとめられ、パスワードを打たずに済む。パスキーの IdP なら、パスワードより強い守りになる。
- rajikura は OSS として配る（[ADR 0028](0028-publish-as-oss-under-radiko-terms.md)）。前段に認証の仕組みを組まなくても、IdP の client を 1 つ作れば使える形にする。
- `sub` は IdP の中で一意で変わらない。メールアドレスは IdP の中で付け替えられ、IdP によっては返さない。
- 画面で相手を見分けるには、`sub`（不透明な文字列のことが多い）やメールアドレスより、IdP の利用者名（OIDC の標準のクレーム `preferred_username`。scope `profile` で返る）のほうが分かりやすい。通すかどうかには今までどおり使わないので、付け替えられても通る相手は変わらない。
- IdP の設定を API トークンで変えられると、トークンを持つ者が自分の IdP を差して紐づけ、Web UI のセッションを得られる。パスワードを変える口が今のパスワードを求めるのと同じく、紐づけには今のパスワードを求める。
- 戻り先を要求のヘッダから組み立てると、どのプロキシのヘッダを信じるかの設定が要る。URL を 1 つ設定に持てば、ヘッダを信じずに済み、IdP に登録する文字列を画面で示せる。ログインの途中の cookie はホストごとに分かれるので、始める前に Web UI の URL へ移る。
- IdP から戻る移動はほかのサイトからの移動なので、`SameSite=Strict` の cookie は送られない。途中の状態の cookie だけを Lax にし、セッションの cookie は Strict のままにする。途中の状態をメモリに置くのは、ログインの失敗の数（ADR 0021）と同じ考え方で、cookie に入れて暗号化するより扱いが少ない。
- IdP のログインには総当たりで当てられる値が無いので、ログインの失敗の数に入れると、IdP の不具合で持ち主のパスワードのログインまで止まるだけになる。
- パスワードを残せば、IdP が止まったとき・設定を壊したときも締め出されない。アクセスは LAN と VPN の内側に限るので、パスワードの口を狙えるのはその内側の者だけで、ログインの失敗の数（ADR 0021）もある。
- パスワードを戻すのは、締め出されたときの戻し方を 1 つにするため。誰に紐づいているか分からない紐づけを残さない。

## 検討した案

- 前段のリバースプロキシの認証（forward auth・oauth2-proxy など）が付けるヘッダを信じる。rajikura に直接届く経路（Pod の IP、LAN の待ち受け）があるとヘッダを偽れ、ヘッダを送ってよい相手の設定を利用者の構成に頼ることになる。
- rajikura を変えず、Cloudflare Access などで外から囲う。インターネットに出す前提を足すことになり、rajikura のパスワードのログインも残って二重になる。アクセスは LAN と VPN の内側に限ったままにするので採らない。
- Google だけに対応する。Google も OIDC の IdP の 1 つで、汎用の形なら Google も使える。Google は戻り先に https と公開の TLD の名前を求めるので、IP のアドレスで開く構成では使えない。
- `name`・`given_name` など、ほかのクレームも出す。見分けるには利用者名で足りる。
- メールアドレス（`email_verified` が true のもの）で通す。上の理由で採らない。
- 複数の IdP を並べて使う。利用者は 1 人で、IdP が止まったときの逃げ道はパスワードのログインで足りる。要るようになったら、設定を IdP の一覧にし、紐づけを IdP ごとに持つ形に広げられる（`iss` + `sub` で通す考え方は変わらない）。
- IdP のログインを設定したら、パスワードのログインを止められる切り替えを持つ、またはパスワードを無くす。止めている間に IdP が壊れると、`-reset-password` で API トークンまで消すことになる。要望を見てから考える。
- IdP の設定を `PATCH /api/settings` に混ぜる。上の理由で採らない。
- ログインの途中の状態を、暗号化した cookie に全部入れる。起動し直しても途中のログインが通るだけで、暗号の扱いが増える。
- ID トークンの検証を、標準のライブラリと `x/crypto` で自前に書く。守りの要で間違えやすい。

## 帰結

- 依存に `github.com/coreos/go-oidc/v3` と `golang.org/x/oauth2` が増える。
- IdP のログインを使うには、IdP が受け付ける Web UI の URL が要る。Google なら https で、公開の TLD の名前（IP のアドレスではない）で開く構成が要る。
- 紐づけと IdP の設定は DB に入るので、バックアップ（[ADR 0017](0017-backup-by-db-copy.md)）にも入る。client secret は秘密なので、戻すときは鍵のファイルも要る（ADR 0015）。
- IdP の側で利用者を止めても、rajikura のセッションは期限（30 日）かログアウトまで残る。
- IdP を替えると紐づけが消えるので、紐づけ直す。
- `profile` を求めるので、IdP によっては同意の画面に出る項目が増える。
- この決定より前に紐づけた利用者は、紐づけ直すまで `preferred_username` が空で、今までどおり `email`（無ければ `sub`）が出る。
