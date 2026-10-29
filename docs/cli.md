# コマンド

rajikura のコマンド（CLI）と、`serve` の起動の設定、データのフォルダをまとめる。用語は [GLOSSARY.md](../GLOSSARY.md) に従う。入れ方は [getting-started.md](getting-started.md)、設定は [settings.md](settings.md) にある。

- [コマンドの一覧](#コマンドの一覧)
- [接続先と API トークン](#接続先と-api-トークン)
- [各コマンド](#各コマンド)
- [起動の設定とデータのフォルダ](#起動の設定とデータのフォルダ)

## コマンドの一覧

<!-- 足したら直す: service の操作 -->
```sh
rajikura [-data <フォルダ>] [-listen <アドレス>] [-allowed-hosts <名前,...>] serve [-reset-password]   # 常駐して番組表の同期と録音を行う。DB を開くのは serve だけ
rajikura [-url <URL>] [-token <トークン>] jobs [-state <状態>]     # ジョブを一覧する
rajikura [-url <URL>] [-token <トークン>] retry <ID>...            # retrying / expired / skipped（録らなかった）のジョブの次の試行をすぐに行わせる
rajikura [-url <URL>] [-token <トークン>] skip <ID>...             # 試行がまだ無い waiting のジョブを録らないにする
rajikura [-url <URL>] [-token <トークン>] record <ID>...           # skipped のジョブを録るに戻す
rajikura [-url <URL>] [-token <トークン>] rerecord <ID>...         # done のタイムフリーのジョブを録り直す（録音期限の内側だけ）
rajikura [-url <URL>] [-token <トークン>] backup [-o <file>]       # DB のバックアップを受け取ってファイルに書く（既定は今のフォルダの rajikura-<日時>.db）
rajikura service install [-listen <アドレス>]                      # Windows のサービスとトレイを入れる・更新する（Windows だけ。管理者で動かす）
rajikura service uninstall                                         # Windows のサービスとトレイを消す（データのフォルダと tray.toml は残す）
rajikura tray install -url <URL> [-token <トークン>]               # Windows のトレイだけを入れる・更新する（Windows だけ。管理者は要らない）
rajikura tray uninstall                                            # tray install で入れたトレイを消す（tray.toml は残す）
rajikura version
rajikura licenses                                                  # 含むソフトウェアのライセンスの一覧を出す
```

- `-data`・`-listen`・`-allowed-hosts` は `serve` の起動の設定。`serve` の前にも後にも書ける（`rajikura -data /data serve` と `rajikura serve -data /data` は同じ）。
- 同じフラグを前と後の両方に書くと、使い方の誤り（終了コード 2）。`serve` のほかのコマンドに付けたときも、使い方の誤り。
- `jobs`・`retry`・`skip`・`record`・`rerecord`・`backup` は DB を開かず、`serve` の API（[api.md](api.md)）を API トークンで呼ぶ（[ADR 0009](adr/0009-cli-over-http.md)）。DB に書くのは `serve` だけ。 <!-- 足したら直す: CLI のサブコマンド -->
- そのため、手元のマシンから、ほかのマシンやコンテナで動く `serve` にも、その URL でつなげる（コンテナの中に入って打たなくてよい）。
- 接続先とトークンの渡し方は、下の「[接続先と API トークン](#接続先と-api-トークン)」。`serve` に `-url`・`-token` を付けると、使い方の誤り（終了コード 2）。
- `serve` が止まっている・URL が違うなどでつながらないときは、「serve につながらない」で終わる（終了コード 1）。トークンが違う・失効しているときは、`serve` が受け付けないと出して終わる（終了コード 1）。
- 使い方の誤り（引数が無い、不明な状態など）は、接続先とトークンが無くても先に知らせる。

## 接続先と API トークン

接続先（`serve` の URL）と API トークンは、値ごとに、次の順に、先にあるものを使う。URL とトークンは別々に選ばれるので、たとえば URL は `client.toml`、トークンは環境変数から読める。

1. フラグ: `-url`・`-token`（サブコマンドの前に書く。例 `rajikura -url https://rajikura.example -token <トークン> jobs`）
2. 環境変数: `RAJIKURA_URL`・`RAJIKURA_TOKEN`
3. CLI の設定ファイル `client.toml`: Windows は `%AppData%\rajikura\client.toml`、Linux は `~/.config/rajikura/client.toml`（`XDG_CONFIG_HOME` があれば `$XDG_CONFIG_HOME/rajikura/client.toml`）。場所を変えるフラグは無い。

```toml
url = "https://rajikura.example"   # serve の URL（http:// か https://）。パスを付ければ、その下の /api/ を呼ぶ
token = "rjk_..."                  # Web UI で発行した API トークン
```

- API トークンは、Web UI のナビの「API トークン」で発行する（[api.md の「API トークン」](api.md#api-トークン)）。
- `client.toml` のキーは `url` と `token` だけで、ほかのキーは綴りの誤りとみなしてエラーにする。ファイルが無いのはエラーにしない。
- どこにも接続先かトークンが無ければ、そのことを出して終わる（終了コード 1）。
- Linux / macOS では、`client.toml` の権限が 0600 より広いと、警告を出して続ける。`chmod 600` で狭める。Windows では確かめない。
- 接続先が `http://` で localhost 以外なら、トークンが平文で流れうると警告を出して続ける（Tailscale を通す使い方もあるので止めない）。
- `serve` は平文の HTTP で待ち受けるので、`https://` でつなぐには、`serve` の前に HTTPS を受けるもの（リバースプロキシなど）を置く。
- 接続先の URL を名前（`https://rajikura.example` など）で書くなら、`serve` の `-allowed-hosts` にその名前が要る。無いと `serve` が 421 で断る。IP のアドレスと `localhost` の URL は、`-allowed-hosts` が無くてもつながる。

## 各コマンド

### serve

- 常駐して、番組表の同期と録音を行い、`-listen` のアドレスで Web UI・API・`/healthz`・`/metrics` を待ち受ける（[api.md](api.md)）。 <!-- 足したら直す: 待ち受ける口 -->
- SIGINT / SIGTERM（Windows では Ctrl+C）で止まる。途中だったタイムフリーのジョブは、次の起動時に `retrying` に戻って再試行される。 <!-- 足したら直す: ジョブの状態の移り方 -->
- [ライブ録音](recording.md#ライブ録音)のジョブは `running` のまま残り、次の起動で取ったセグメントを残して繋ぎ直す。
- ログは標準エラーに出す。Windows のサービスとして動くときは、データのフォルダの `logs\rajikura.log` に出す（[windows.md の「ログ」](windows.md#ログ)）。

`serve -reset-password` は、Web UI のパスワードを忘れたときに使う（[ADR 0016](adr/0016-first-password-by-setup-code.md)）。

- 起動するときに、DB のパスワード・ログインのセッション・API トークンを全部消し、そのまま起動を続ける。
- パスワードが無くなるので、「[初めて開いたとき](web-ui.md#初めて開いたとき)」と同じく、セットアップコードからパスワードを決め直す。CLI と AI の API トークンは発行し直す。
- 戻した後は、`-reset-password` を外して起動し直す（付けたままだと、起動のたびに消える）。

### jobs

`jobs` は、ジョブの一覧を表にする。出すのは、放送日が今日の放送日の 120 日前以降のジョブだけ。

- 並びは、放送日・番組・パート・ID の順。
- 列は、番組・放送日・状態・次の試行の時刻・試行回数・録音期限・保存先（欠落があればその数）・直近のエラー（直近の試行が失敗していれば、失敗した段階とエラー）。
- `-state retrying` のように状態で絞れる。`-state retrying,expired` のようにカンマで区切って複数書ける。
- 状態の列は API の値のまま。`skipped` には確定の前か後か（`skipped（録らない予定）`・`skipped（録らなかった）`）を括弧で添える。
- 人が録る・録らないを決めたジョブには「人が決めた」を、ライブ録音のジョブには「ライブ録音」を、同じ括弧に添える（例 `skipped（録らない予定・人が決めた）`、`waiting（ライブ録音）`）。

`retrying` のジョブの「次の試行」が録音期限と同じになっていることがある。これは、試行が尽きたが取れた放送回があるジョブで、録音期限を過ぎたら取れた分を保存して `done` にする（[再試行](recording.md#再試行)）。 <!-- 足したら直す: ジョブの状態の移り方 -->

### retry

`retry` は、`retrying` / `expired` / `skipped`（確定した録らなかった）のジョブの次の試行をすぐに行わせる（手動再試行）。 <!-- 足したら直す: ジョブの状態の移り方 -->

```bash
rajikura -url '<serve の URL>' -token '<発行したトークン>' retry <ID>
```

- ID を 1 つ以上並べる（ID は `jobs` の先頭の列）。
- 状態を `retrying`、次の試行の時刻を今、試行回数を 0 にし、すぐに拾わせる。実行中のジョブや、先に次の試行の時刻を過ぎたジョブがあれば、その後になる。
- 番組表の取り直しと展開のし直しはしない。取り終えた放送回のファイルと試行の履歴は消さない。
- 拒むのは、`waiting` / `running` / `done` のジョブ、録らない予定（確定の前の `skipped`）、録音期限を過ぎたジョブ、無い ID。 <!-- 足したら直す: ジョブの状態の移り方 -->
- 録音済みを録り直すなら `rerecord`、録らない予定を録るなら `record` を使う。
- ただし、[ライブ録音](recording.md#保存のやり直し)で録れた分を残している間は、録音期限を過ぎていても受け付け、保存からやり直す。
- ID ごとに結果を 1 行出し、拒んだ ID があってもほかの ID は続ける。1 件でも拒んだら終了コードは 1、ID が無いなど使い方の誤りは 2。
- Web UI のジョブの画面の詳細の「手動で再試行」も同じ操作。

### skip・record

`skip` は、試行がまだ 1 度も無い `waiting` のジョブを「録らない」にする（[ADR 0019](adr/0019-skip-by-title-and-by-person.md)）。

- 放送の前（最初の試行の時刻の前）なら録らない予定、後なら録らなかったになる。
- 人が決めたジョブとして印が付き、その後の番組表の同期では番組名によって録音予定に戻らない（休止・番組の無効化・番組表の終了時刻などの変化は効く）。
- `waiting` のほかのジョブ、試行のある `waiting` のジョブ、無い ID は拒む。通知はしない。

`record` は、`skipped` のジョブを「録る」に戻す。

- 録らない予定（確定の前）なら、録音予定（`waiting`）に戻して人が決めた印を付ける。番組名が食い違っていても録る。
- 録らなかった（確定した後）なら、`retry` と同じ手動再試行になる（録音期限を過ぎていれば拒む）。
- `skipped` でないジョブと無い ID は拒む。通知はしない。

`skip`・`record` の結果の出し方と終了コードは `retry` と同じ。Web UI では、番組表の詳細とジョブの画面の詳細の「録らない」「録る」が同じ操作。人が決めた印は外せない。

### rerecord

`rerecord` は、録音済み（`done`）のジョブを radiko から録り直させる（録り直し。[ADR 0025](adr/0025-rerecord-done-job-separately-from-manual-retry.md)）。保存したファイルが壊れていた・消してしまった、録れた音がおかしかった、というときに使う。 <!-- 足したら直す: ジョブの状態の移り方 -->

- 受け付けるのは、タイムフリーから録った `done` のジョブで、録音期限の内側のものだけ。
- `done` でないジョブ、ライブ録音のジョブ（放送は一度きり）、録音期限を過ぎたジョブ、無い ID は拒み、拒むわけを 1 行に添える。
- 手動再試行と同じく、状態を `retrying`、次の試行の時刻を今、試行回数を 0 にする。結果の出し方と終了コードは `retry` と同じ。
- どの放送回も radiko から取り直す。試行の履歴は消さない。
- 保存先が前と同じ名前になれば、検証を終えた後に上書きする（前のファイルが消えるのは、新しいファイルが確定するときだけ）。違う名前になったら新しい名前で保存し、前のファイルは消さない。
- 録れずに `expired` になったとき、録音期限までに一部の放送回しか取れなかったときは、前のファイル・保存先の記録・欠落の印を前のまま残す。
- 受け付けたこと自体は通知しない。録れたら録音済み（欠落があれば欠落あり）、録れなければ録音できなかったを、もう一度通知する。 <!-- 足したら直す: 通知の種類 -->
- [チャプターの取り直し](settings.md#チャプターchapters)を待っているジョブを録り直すと、待ちは消える。
- Web UI では、ジョブの画面の詳細の「録り直す」が同じ操作（押すと確かめの画面を出す）。

### backup

`backup` は、`serve` の DB のバックアップを受け取り、`-o` のファイルに書く。中身と戻し方は [backup.md](backup.md)。 <!-- 足したら直す: CLI のサブコマンド -->

- `-o` を省くと、今のフォルダに `rajikura-<日時>.db`（日時は日本時間の `YYYYMMDD-HHMMSS`。例 `rajikura-20261018-120405.db`）で書く。
- 受け取り終えてからファイルの名前を変えるので、途中で切れたファイルは残らない。書き先にファイルがあれば置き換える。
- `serve` は止めなくてよい。

### service・tray

`service` は、Windows で `serve` をサービスとして入れる・消す（[ADR 0020](adr/0020-windows-service-with-virtual-account.md)。手順は [windows.md](windows.md)）。 <!-- 足したら直す: service の操作 -->

- `install` は、サービスを入れ、`rajikura.exe` に埋め込んだトレイも `%ProgramFiles%\rajikura` に入れて、打ったユーザーのログイン時に起動するよう登録し、最後にトレイを起動する。
- `uninstall` は、サービスとトレイの exe を消し、打ったユーザーのトレイのログイン時の起動の登録を消す（データのフォルダと `tray.toml` は残す）。
- `serve` の API は呼ばず、`-url`・`-token`・`-data`・`-allowed-hosts` は使わない（付けると使い方の誤り）。待ち受けるアドレスは `service install -listen` で渡す。
- `-allowed-hosts` は `service install` に渡せないので、LAN からは IP のアドレスで開く。

`tray` は、`serve` を Windows のサービスにしていない PC に、`rajikura.exe` に埋め込んだトレイだけを入れる・消す（手順は windows.md の「[トレイだけを入れる](windows.md#トレイだけを入れる)」）。 <!-- 足したら直す: service の操作 -->

- `install` は、トレイを `%LocalAppData%\Programs\rajikura` に入れ、打ったユーザーの `tray.toml` に `-url`（必須）と `-token`（任意）を書き、ログイン時に起動するよう登録して、その場で起動する。管理者は要らない。
- `uninstall` は、そのトレイを止めてから消す（`tray.toml` は残す）。
- `serve` の API は呼ばず、CLI の `client.toml` も読まない。`tray` の前に `-url`・`-token`・`-data`・`-listen`・`-allowed-hosts` を書くと、使い方の誤り（終了コード 2）。
- トレイを埋め込んでいない `rajikura.exe` の `tray install` は失敗にする（終了コード 1）。Release の `rajikura.exe` には埋め込まれている。

`service`・`tray` を Windows のほかで打つと、Windows だけだと出して止まる（終了コード 2）。 <!-- 足したら直す: service の操作 -->

### version・licenses

- `version` は版を出す。手元で動き、`serve` にも DB にも触らない。
- `licenses` は、rajikura 自身と、バイナリが含むソフトウェアのライセンスの文と NOTICE を、テキストで標準出力に出す。手元で動き、`serve` にも DB にも触らない。
- `licenses` の一覧は、Release の配布物（Windows・Linux のバイナリ）とコンテナイメージ（`docker run --rm ghcr.io/t-seki/rajikura:<版> licenses`）に入っている。一覧が入っていないバイナリでは、そのことを出して終わる（終了コード 0）。

## 起動の設定とデータのフォルダ

`serve` を起動するときにだけ決める値（起動の設定）は、フラグの 3 つだけ。Web UI からは変えられず、変えるには `serve` の再起動が要る。

| フラグ | 内容 | 既定 |
| --- | --- | --- |
| `-data` | データのフォルダ（下の表）。無ければ作る | 実行ファイルと同じフォルダ |
| `-listen` | `serve` が `GET /healthz`・`GET /metrics`・Web UI・API を待ち受けるアドレス（`host:port`） | `:8080` <!-- 足したら直す: 待ち受ける口 --> |
| `-allowed-hosts` | IP のアドレスと `localhost` のほかに、Web UI・API が受ける名前。カンマで区切って並べる（例 `rajikura.example.com,pc.example.ts.net`）。大文字小文字は区別せず、ポートは見ない。名前で開く構成（リバースプロキシ、Tailscale の MagicDNS など）では、その名前を足す（[api.md の「ブラウザ経由の攻撃とログインの総当たりへの守り」](api.md#ブラウザ経由の攻撃とログインの総当たりへの守り)） | 空（IP のアドレスと `localhost` だけ） |

データのフォルダに置くもの: <!-- 足したら直す: データのフォルダ -->

| 名前 | 中身 |
| --- | --- |
| `rajikura.db` | DB。番組・設定・ジョブ・通知などを置く |
| `rajikura.key` | 秘密の鍵のファイル。初めて秘密を保存するときに作る（Linux では権限を 0600 にする） |
| `work` | 作業用フォルダ。録音の途中の音声を `work/<ジョブ ID>/` に置き、保存したら消す。返す前のバックアップも置く |
| `setup-code.txt` | セットアップコードのファイル。Web UI のパスワードが決まるまでの間だけ置く |
| `logs` | ログのフォルダ。Windows のサービスとして動くときだけ（[windows.md の「ログ」](windows.md#ログ)） <!-- 足したら直す: データのフォルダ --> |

- 録音を保存するライブラリ（設定の `library`）は、データのフォルダとは別。
- [ライブ録音](recording.md#保存のやり直し)で録れた分を保存できなかったときは、作業用フォルダを 30 日の間残す。
- DB を別のマシンに移すときは、バックアップを取って移す（[backup.md の「マシンを移す」](backup.md#マシンを移す)）。鍵のファイルも置かないと、秘密を入れ直すまで使えない。 <!-- 足したら直す: データのフォルダ -->
- コンテナの既定の引数は `-data /data serve`（[getting-started.md の「コンテナで動かす」](getting-started.md#コンテナで動かす)）。
- Windows のサービスは `-data %ProgramData%\rajikura -listen 127.0.0.1:8080 serve` で登録する（[windows.md](windows.md)）。そのデータのフォルダは、サービスのアカウント・SYSTEM・Administrators だけが読める。
