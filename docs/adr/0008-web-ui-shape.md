# Web UI は利用者 1 人のログインにし、仮のファイルを埋め込み、/healthz と同じ口で受ける

## 決定

Web UI は、`serve` が `/healthz` と同じ `-listen` で JSON API と SPA を受ける。ログインする利用者は 1 人だけ。SPA（TypeScript + React、Vite でビルド）のビルドの結果は git に入れず、埋め込む先には仮のファイルを置いて、Node が無くても `go build` と `go test` が通るようにする。

- パスワードは、初めて Web UI を開いた人がセットアップコードを入れて決める（[ADR 0016](0016-first-password-by-setup-code.md)）。bcrypt の hash を DB に置く。
- パスワードが無い間、Web UI はパスワードを決める画面だけを出す。`/healthz` は認証の外に置く。
- ログインすると、セッションの ID を cookie（`HttpOnly`、`Secure`、`SameSite=Strict`）で渡す。DB には ID の SHA-256 の hash を置く。期限は 30 日で、ログアウトで消す。
- リリースのバイナリとコンテナイメージには、ビルドした SPA を埋め込む（Dockerfile に Node の段がある）。

## 理由

- アクセスは手元の LAN と VPN に限り、使うのも 1 人という前提なので、利用者の管理は要らない。
- セッションの ID を hash で置けば、DB のファイルが外に出ても、期限の切れていないセッションでログインした状態にならない。API トークンの置き方と同じ。
- ビルドの結果を git に入れると、差分とレビューがビルドの結果で埋まる。仮のファイルを埋め込めば、Go の開発の流れ（Windows の `go test` を含む）に Node を持ち込まずに済む。
- `/healthz` と同じ口にすれば、コンテナの死活監視と Web UI を 1 つのポートで済ませられる。

## 検討した案

- 複数の利用者を持つ。家族などで使う場面はあるが、要望を見てから考える。
- パスワードの hash を環境変数で渡す。TOML や環境変数を書かずに始められるようにすること（[ADR 0015](0015-settings-and-secrets-in-db.md)）と合わない（[ADR 0016](0016-first-password-by-setup-code.md)）。
- ビルドした SPA を git に入れる。上の理由で採らない。
- Web UI を別のポートで受ける。分ける理由が無い。

## 帰結

- Node が無い環境で作ったバイナリでは、Web UI は仮の画面になる。
- ブラウザ経由の攻撃とログインの総当たりへの守りは、[ADR 0021](0021-web-ui-origin-host-and-login-limit.md) にある。
