# Windows のトレイは別の exe にして rajikura.exe に埋め込み、ループバックの受け口で連携する

## 決定

Windows で、ログインした人ごとに動くトレイを持つ。トレイは `rajikura-tray.exe` という別の exe にして `rajikura.exe` に埋め込み、`rajikura.exe` が書き出してスタートアップに登録する。API トークンは、Web UI で人が確かめたうえで、トレイが `127.0.0.1` に開いた一度きりの受け口に渡す（連携）。状態は API をポーリングで読む。

- トレイは、`rajikura.exe` とは別の exe（`rajikura-tray.exe`。同じ repo の `cmd/rajikura-tray` に置く）で、`-H windowsgui` でビルドする。
- 入れ方は 2 つ。
  - `service install`（[ADR 0020](0020-windows-service-with-virtual-account.md)）は、`%ProgramFiles%\rajikura\rajikura-tray.exe` に書き出し、その PC のサービスに向ける。
  - `rajikura tray install -url <URL> [-token <トークン>]` は、`serve` を Windows のサービスにしていない PC に、トレイだけを入れる（`%LocalAppData%\Programs\rajikura\rajikura-tray.exe`。管理者は要らない）。リモートの `serve`（コンテナなど）に向けられる。消すのは `rajikura tray uninstall`。
- スタートアップには、打ったユーザーの HKCU の `Run` に `"<exe>"` だけを登録する。`service uninstall`・`tray uninstall` は、`Run` の登録がそれぞれの exe を指すときだけ消す。ほかのユーザーは、トレイのメニューの「ログイン時に起動する」で自分の分を登録する。
- トレイの接続先と API トークンは `%AppData%\rajikura\tray.toml` に置く。CLI の `client.toml`（[ADR 0009](0009-cli-over-http.md)）とは分ける。`tray.toml` を書くのは、連携と install（`service install`・`tray install`）の 2 つ。install は、`url` が変われば前に連携したトークンを消し、`tray install -token` を渡せばトークンも書く。
- トレイの接続先は、`-url` → `tray.toml` の `url` → 既定（`http://127.0.0.1:8080`）の順に決める。
- 連携: トレイが `127.0.0.1` の空いているポートで受け口を開き、ブラウザで Web UI の連携の画面（`/tray-link?port=…&state=…&name=…`）を開く。人が確かめのボタンを押すと、Web UI が `POST /api/tokens` でトークンを発行し、フォームの POST で受け口に `token` と `state` を送る。トレイは `state` が合えば `tray.toml` に書いて受け口を閉じる。受け口は 5 分で閉じる。
- 状態は、`GET /api/notifications`・`GET /api/notifications/status`・`GET /api/jobs?state=running` を 1 分ごとに読む。新しい通知はトーストに出し、既読にはしない。
- トレイのライブラリは `fyne.io/systray`、トーストは `git.sr.ht/~jackmordaunt/go-toast/v2`。どちらも cgo 無しでビルドできる。

## 理由

- Windows のサービスは session 0 で動くので、ログインした人の画面にトーストもトレイのアイコンも出せない。出すには、ログインした人のセッションで動くプロセスが要る。トレイはサービスの代わりではなく、API を読むだけのもの。
- 別の exe にするのは、ログイン時に起動したときにコンソールの窓を出さないため。Go の exe は既定でコンソールの exe で、`-H windowsgui` にすると端末に何も出せなくなる。`rajikura.exe` をそうすると、`service install` と CLI の出力が消える。
- 埋め込むのは、落とすファイルを 1 本のままにし、サブコマンドだけでトレイまで入るようにするため。
- 連携にするのは、`/api/` には localhost の例外が無く、API トークンが要るため（[ADR 0021](0021-web-ui-origin-host-and-login-limit.md)）。人が Web UI でトークンを発行してファイルに書く手順は、乗り換える人には一番の関門になる。ループバックの受け口で渡す形は、デスクトップのアプリの OAuth で使われる形（RFC 8252）で、人の操作は確かめのボタンだけになる。
- `tray.toml` を分けるのは、CLI を別の rajikura に向けている人の `client.toml` を、連携で上書きしないため。
- ポーリングにするのは、`serve` に押し出しの口（SSE・WebSocket）が無く、通知は録音のループの周（1 分）ごとに送られるため（[ADR 0011](0011-notifications-written-to-db-then-delivered.md)）。

## 検討した案

- 同じ `rajikura.exe` の `tray` サブコマンドで常駐し、起動直後に `FreeConsole` する。ログインのたびに窓が一瞬ちらつく。
- `rajikura.exe` を windowsgui にし、CLI のときは `AttachConsole` で親の端末につなぐ。端末の入出力やリダイレクトで癖が出やすく、CLI を壊しうる。
- トレイを Release の別のファイルにし、人が置いて自分で登録する。入れる手順が増える。
- 全部のユーザーのスタートアップ（HKLM の `Run`）に登録する。トークンを持たないユーザーのログインにも、未設定のトレイが出る。
- `service install` がトークンを発行し、ログインした人が読めるファイルに置く。トークンの権限は 1 種類なので、PC にログインできる人なら誰でも全部の権限を持てる。
- 認証なしで読める状態の口を足す。`/api/` は要ログインという線を崩し、`-listen` を LAN に開いた人には外にも見える。
- クリップボードで渡す（Web UI でコピーし、トレイのメニューで読む）。人の手順が 2 か所に分かれる。
- トーストを PowerShell の起動で出す（`go-toast/toast` など）。毎回 PowerShell を起動する。`go-toast/v2` が使えなくなったときの逃げ道として残す。

## 帰結

- `rajikura.exe` は、トレイの exe の分だけ大きくなる。ビルドは、トレイを先に作ってから埋め込む順になる。
- `go-toast/v2` は、この決定のとき、v2.0.3 から長く更新が無く、公開の issue tracker も無かった。
- 連携し直しても、古いトークンは自動で失効させない。人が API トークンの画面で失効させる。
- トレイを使う人ごとに `tray.toml` とトークンが 1 本ずつ増える。
