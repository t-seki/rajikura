# API

`serve` が `-listen` のアドレスで待ち受ける HTTP の口（`/healthz`・`/metrics`・Web UI の JSON API）をまとめる。用語は [GLOSSARY.md](../GLOSSARY.md) に従う。Web UI の画面の使い方は [web-ui.md](web-ui.md)、設定のキーは [settings.md](settings.md)。

- [動作確認（`GET /healthz`）](#動作確認get-healthz)
- [メトリクス（`GET /metrics`）](#メトリクスget-metrics)
- [Web UI の JSON API（`/api/`）](#web-ui-の-json-apiapi)
  - [セットアップとログイン](#セットアップとログイン)
  - [ブラウザ経由の攻撃とログインの総当たりへの守り](#ブラウザ経由の攻撃とログインの総当たりへの守り)
  - [API トークン](#api-トークン)
  - [ジョブ](#ジョブ)
  - [通知](#通知)
  - [設定](#設定)
  - [フォルダ](#フォルダ)
  - [バックアップ](#バックアップ)
  - [番組](#番組)
  - [1 回の予約](#1-回の予約) <!-- glossary-ok: 回 -->
  - [番組表](#番組表)
  - [キーワード](#キーワード)

## 動作確認（`GET /healthz`）

`rajikura serve` は、`-listen` のアドレスで HTTP を待ち受け、`GET /healthz` に答える。`/healthz` は死活監視（コンテナの liveness probe など）に使うためのもので、認証は付けない。Web UI・API と同じ `-listen` のポートに載るので、Web UI を公開する経路で `/healthz` を除いていなければ、そこからも認証なしで読める（Web UI と API は次の節）。 <!-- 足したら直す: 待ち受ける口 -->

- ジョブの実行のループ（1 分ごと、または人の操作で起こされたとき）が最後に 1 周した時刻が 30 分以内なら 200、そうでなければ 503 を返す。本文は短いテキストで、その時刻が入る。
- 「最後に 1 周した時刻」は、ジョブを取りに行く周（実行するジョブが続くときは、1 件ごと）の始まりで記録する。周の途中（録音・通知が長引いている間や、最初の試行の前の番組表の取り直しが、展開役の番組表の同期・展開の後に待たされている間）は記録が進まないので、それを見込んだ長さにしてある。起動直後でまだ 1 周していないときは、起動時刻から数える。
- 待ち受けを開けない（ポートが埋まっているなど）と、`serve` は起動せずに終わる。`serve` の終了で `/healthz`・`/metrics`・Web UI・API の待ち受けも止まる。

```bash
curl -i http://localhost:8080/healthz
```

## メトリクス（`GET /metrics`）

`rajikura serve` は、`-listen` のアドレスの `GET /metrics` で、Prometheus の形式のメトリクスを返す。認証は付けない。Web UI・API と同じ `-listen` のポートに載るので、Web UI を公開する経路（リバースプロキシなど）で `/metrics` を除いていなければ、そこからも認証なしで読める。中身は種類ごとの数と時刻だけで、秘密や番組名は含まない。「サービスが動いていること」と「通知先が壊れていないこと」は、ログの文言ではなくこのメトリクスで外から確かめる（[ADR 0010](adr/0010-per-event-notifications-and-metrics.md)）。

| メトリクス | 種類 | 意味 |
| --- | --- | --- |
| `rajikura_last_saved_timestamp_seconds` | gauge | 最後にジョブを保存した時刻（Unix 秒）。起動時に DB（`app_state` の `last_saved_at`）から読むので、再起動で 0 に戻らない。一度も保存していなければ 0 |
| `rajikura_notify_failures_total{kind}` | counter | 通知先に送れなかった通知の数。作ってから 1 時間を過ぎた通知を送ろうとして送れず、「送れなかった」の印を付けたときに 1 つ数える（1 時間を過ぎる前の、送り直している間の失敗は数えない）。`kind` は通知の種類（`recorded`（録音済み）・`missing`（欠落あり）・`expired`（録音できなかった）・`skipped`（録らなかった）・`skip_planned`（録らない予定にした）・`unskipped`（録音予定に戻した）・`deadline_near`（期限間近）・`keyword_added`（キーワードで録音予定）・`keyword_limit`（キーワードの上限）・`unmatched_day`（当たらない日）・`area_changed`（自分のエリアが変わった）・`sync`（番組表の同期の失敗）・`out_of_area`（エリア外）・`login`（radiko のログインの失敗）・`backup`（バックアップの失敗）・`wake`（スリープから起こせない）・`rescan`（再スキャンの失敗）・`recovered`（復旧））。テスト通知（`test`）は送れなくても数えず、`kind="test"` の系列も出さない（人がその場で結果を見ている）。起動時に種類ごとに 0 で出す。再起動で 0 に戻る（Prometheus の `increase()` が扱う） <!-- 足したら直す: 通知の種類 --> |

- メトリクスの名前とラベルは、監視との約束なので変えない（足すことはある）。`kind` の値は、通知の種類が増えると増える。
- ラベルに番組名・番組の名前は入れない（`kind` だけ）。秘密を出さず、ラベルの組み合わせが際限なく増えないようにするため。

Prometheus のアラートの例。 <!-- glossary-ok: アラート -->

```yaml
groups:
  - name: rajikura
    rules:
      # 48 時間、何も保存していない（再起動直後の 0 では鳴らさないよう、保存したことがあるときだけ見る）
      - alert: RajikuraNothingSaved
        expr: rajikura_last_saved_timestamp_seconds > 0 and time() - rajikura_last_saved_timestamp_seconds > 48*3600
        for: 10m
      # 通知先に送れなかった（直近 1 時間に 1 回でも）
      - alert: RajikuraNotifyFailing
        expr: sum by (kind) (increase(rajikura_notify_failures_total[1h])) > 0
```

## Web UI の JSON API（`/api/`）

`serve` は、`/healthz` と同じ `-listen` で、Web UI の JSON API（`/api/`）と SPA も待ち受ける。作りは [ADR 0008](adr/0008-web-ui-shape.md)。ログインする利用者は 1 人で、パスワードの bcrypt の hash は DB に置く。パスワードは、初めて Web UI を開いた人が、`serve` の作ったセットアップコードを入れて決める（[ADR 0016](adr/0016-first-password-by-setup-code.md)）。

### セットアップとログイン

パスワードが決まるまでの画面の流れは [web-ui.md の「初めて開いたとき」](web-ui.md#初めて開いたとき)。

- パスワードがまだ無い間は、`POST /api/setup` のほかの `/api/` は、API トークンがあっても 503（JSON `{"error": "...", "setup": true, "locked": <5 回間違えて受け付けないか>}`）を返す。SPA と `/healthz` は返す。
- `POST /api/setup`（JSON `{"code": "<セットアップコード>", "password": "<新しいパスワード>"}`）: 決めると 200 と、ログインと同じセッションの cookie。コードが違えば 401（`{"error": "...", "remaining": <あと何回間違えてよいか>, "locked": <これで受け付けなくなったか>}`）、5 回間違えた後は 429、パスワードがもう決まっていれば 409、パスワードが空か 72 バイトを超える・JSON が読めないときは 400（間違えた回数に数えない）。コードは時間で漏れない比べ方で比べる。
- パスワードを変える: ログインしてから `PUT /api/password`（JSON `{"current": "<今のパスワード>", "password": "<新しいパスワード>"}`）。今のパスワードが違えば 403、新しいパスワードが空か 72 バイトを超えれば 400。今のパスワードの間違いは、ログインの間違いと同じ数に入り、ログインの受け付けを止めている間は 429（下の「ログインの総当たりの制限」）。変えても、ログインのセッションと API トークンはそのまま使える。
- `POST /api/login`（JSON `{"password": "..."}`）でログインすると、セッションの cookie（`HttpOnly`、`Secure`、`SameSite=Strict`、`Path=/`）が返る。期限は 30 日。cookie には元のセッションの ID を渡し、DB にはその SHA-256 の hash を置く（API トークンと同じ。DB のファイルが外に出ても、そのセッションでログインした状態にならない）。`POST /api/logout` でセッションを消し、cookie も消す。`GET /api/me` は、認証が通れば 200（JSON `{"authenticated": true, "version": "..."}`。`version` は `rajikura version` と同じ値で、開発のビルドでは `dev`）、通らなければ 401、パスワードがまだ無ければ 503（上）。`/api/login`・`/api/setup` 以外の `/api/` は、認証（セッション、または下の API トークン）が通らなければ 401。パスワードが違えば `POST /api/login` は 401、ログインの受け付けを止めている間は 429（下）。

### ブラウザ経由の攻撃とログインの総当たりへの守り

決定は [ADR 0021](adr/0021-web-ui-origin-host-and-login-limit.md)。

- `Host` の検査: `/api/` と SPA は、要求の `Host` が IP のアドレス（v4・v6）・`localhost`・`-allowed-hosts` に並べた名前のときだけ受ける（DNS rebinding への手当て）。名前は大文字小文字を区別せず、ポートは見ない。ほかの `Host` は 421 で断り（`/api/` は JSON `{"error": "..."}`、SPA は平文。どちらも `-allowed-hosts` に足すよう書く）、ログに Warn で断った `Host` を出す。`/healthz`・`/metrics` は `Host` を見ない（監視は IP のアドレスで叩くことがあるため）。名前で開くとき（`rajikura.example.com` など）は、その名前を `-allowed-hosts` に足す（[起動の設定とデータのフォルダ](cli.md#起動の設定とデータのフォルダ)）。
- ほかのオリジンからの要求: `/api/` の GET・HEAD 以外は、Go の標準の `http.CrossOriginProtection` で確かめ、ほかのオリジンのページからの要求を 403（JSON `{"error": "..."}`）で断る。OPTIONS も標準の検査が通す（`/api/` に OPTIONS の口は無い）。ログインする前の `POST /api/login`・`POST /api/setup` も含む。`Sec-Fetch-Site` も `Origin` も付けない要求（CLI・curl など、ブラウザ以外）は通す。
- ログインの総当たりの制限: `POST /api/login` のパスワードの間違いと、`PUT /api/password` の今のパスワードの間違いを、接続元を区別せず `serve` 全体で数える。5 回続けて間違えたら 1 分受け付けず、その後も間違えるたびに止める時間を倍にする（上限 15 分）。止めている間は、正しいパスワードも 429（`Retry-After` ヘッダと JSON `{"error": "...", "retry_after": <あと何秒>}`）で断る。成功したら 0 に戻す。止め始めたときにログに Warn を 1 行出す（通知は送らない）。回数は `serve` の中にだけ持ち、起動し直すと 0 に戻る。今のセッションと API トークンは、止めている間も（`PUT /api/password` のほかは）そのまま使える。ログインの画面は、429 のとき「あと N 秒待ってほしい」と出す。
- セットアップコードの制限（5 回続けて間違えたら次の起動まで受け付けない）は、[web-ui.md の「初めて開いたとき」](web-ui.md#初めて開いたとき)のとおりで、ログインの制限とは別に数える。

### API トークン

- API トークン（用語は [GLOSSARY.md](../GLOSSARY.md)）は、CLI や AI が Web UI を通さずに `/api/` を叩くための鍵。Web UI のナビの「API トークン」で、名前（例:「手元の CLI」「AI」）を付けて発行し、失効する。何本でも持てる。一覧には名前・発行日時・最後に使った日時を出す。失効が成功したら、画面にトーストで知らせる（失敗は画面のエラー表示に出す）。期限は無く、人が失効させる。権限は 1 種類で、どのトークンも Web UI でできることは全部できる。
  - トークンそのもの（`rjk_` で始まる文字列）は、発行した直後に 1 回だけ画面に出す。DB には sha256 の hash だけを置くので、後からは見られない。なくしたら失効して発行し直す。トークンは秘密として扱い、ログに出さない。
  - リクエストは `Authorization: Bearer <トークン>` で送る。`/api/` は、有効なトークンか有効なセッションの cookie のどちらかで通る。`Authorization: Bearer` を付けたリクエストは、トークンが失効している・知らないものなら、cookie があっても 401 を返す。
  - パスワードがまだ無いときは、トークンがあっても `/api/` は 503 を返す。最初のトークンは Web UI でログインして発行するので、パスワードを決めるのが先に要る。`serve -reset-password` で戻すと、トークンは全部消える。
  - `GET /api/tokens`（一覧。`{"tokens": [{id, name, createdAt, lastUsedAt}]}`。まだ使っていなければ `lastUsedAt` は `null`）、`POST /api/tokens`（JSON `{"name": "..."}` で発行。201 と、`token`（トークンそのもの）を含む 1 件を返す。名前が空なら 400）、`DELETE /api/tokens/{id}`（失効。204。無い ID は 404）。トークンでも叩ける。
  - 例（`<トークン>` は発行した値、アドレスは `-listen`）: `curl -H "Authorization: Bearer <トークン>" http://localhost:8080/api/jobs`

### ジョブ

要ログイン。 <!-- 足したら直す: ジョブの状態の移り方 -->

- `GET /api/jobs?state=<状態>[,<状態>...]`: 一覧。
  - 新しい放送日が上。
  - 放送日が今日の放送日の 120 日前以降（120 日前の放送日を含み、今日から先は全部）のジョブだけを返し、範囲の外を含めて一覧する口は無い。
  - `state` を省くと範囲の中の全部。
  - ジョブごとに保存先 `outputPath`、欠落の放送回の数 `missingCount`、直近の試行が失敗していればその段階 `lastFailedStage` とエラー `lastError`、局と時刻の組 `stations`（`stationId`・`start`・`end`）を付ける。
  - 組は局ごとに 1 つで、その局の最初の放送回の開始から最後の放送回の終わりまで、開始の順。
  - 並べ替えは画面でする。
  - ジョブが済んだ時刻を `finishedAt`（RFC 3339）で付け、画面の既定の並びが使う。`done` と `expired` は最後の試行の終わり（`expired` で試行が無ければ録音期限 `deadline`）、確定した `skipped`（録らなかった）は録りにいく時刻 `nextAttemptAt`（確定した時刻そのものではない。人が録らないにした・録音期限で確定したときなどは、実際の確定より前の時刻になりうる）。「これから」のジョブ（`waiting`・`running`・`retrying`・確定の前の `skipped`）と、試行が無い `done` は空文字。
  - 状態は `state` で、`skipped` には確定したか `settled`（`false` なら録らない予定、`true` なら録らなかった）を、人が録る・録らないを決めたジョブには `byPerson` を付ける。
  - [ライブ録音](recording.md#ライブ録音)のジョブか（放送回の 1 つでもタイムフリーの対象外か）は `live` で、全状態のジョブに常に付ける（録る前（`waiting`・確定の前の `skipped`）のジョブの値は、その時点の番組表での見込みで、番組表の同期で変わりうる。拾った後は、その方式で録ったことを表す）。
  - いま受け付ける操作に `retryable`（手動再試行）・`skippable`（録らない）・`recordable`（録る）・`rerecordable`（録り直し）・`chaptersRedoable`（チャプターの入れ直し）を付ける。`rerecordable` は、エリアフリー・ダブルプランで自分のエリアの局一覧を取れないとき、`done` のタイムフリーから録ったジョブだけ `false` になる（応答は 200 のまま。[ADR 0036](adr/0036-rerecord-deadline-follows-current-membership-and-own-area.md)）。チャプターの入れ直しを頼んで、まだ書き込んでいない間は `chaptersRedoRequested` が `true`。
  - [ライブ録音](recording.md#ライブ録音)のジョブで録れた分の保存をやり直している間（`retrying` で、最後の試行が保存か検証の段階で失敗し、打ち切りの前）は保存のやり直しの打ち切り `saveRetryUntil` を、保存できずに録れた分を残している間（`expired` で、残す期限の前）は残す期限 `keepUntil` を付ける（どちらも、ほかのときはフィールドを出さない）。
  - 録音中のジョブの[進捗](../GLOSSARY.md)は `progress` で、状態が `running` で、かつ `serve` のメモリに進捗があるときだけ付ける（ほかのときはフィールドを出さない）。
  - `progress` の `stage` は今の試行の段階（`fetch`・`verify`・`save`。格納も `save`）で、入手の間だけ、それまでに録れた音の秒数 `fetchedSeconds`（前の試行で入手し終えた放送回の分を含む。[ライブ録音](recording.md#ライブ録音)では届いた音の秒数で、欠けた区間を含まない）と、ジョブの放送回の長さの合計の秒数 `totalSeconds` を付ける（検証・保存の段階と、ライブ録音の録れた分の保存だけのやり直しでは、この 2 つを出さない）。
  - 一覧の各ジョブのこれらの項目は、詳細と再試行・録らない・録る・録り直し・ジョブ 1 件のチャプターの入れ直しの応答にも同じ形で付く。
- `GET /api/jobs/{id}`: 範囲の外のジョブも引ける。放送回、欠落、保存先、長さ、試行の履歴。保存したファイルにチャプターが入ったかを `chapters` に付ける: `added`（入った。取りに行った全部の放送回で取れた）、`waiting`（待っている。[チャプターの取り直し](settings.md#チャプターの取り直し)を待っている間）、`not_added`（入らなかった。打ち切った、または待たない理由で一部か全部が取れなかった。一部だけ入ったものも含む。チャプターの取り直しで書き込めなかったものも含む）。取りに行っていないジョブ（ライブ録音、`chapters` が `false` のときに保存した、まだ保存していない）ではフィールドを出さない。一覧の各ジョブにも同じ形で付く。[`jobs <ID>`](cli.md#jobs) とジョブの画面の詳細が呼ぶ。
  - 詳細には、保存したファイルへのチャプターの書き込みの履歴を `chapterWrites`（書き込んだ順の配列。無ければ空の配列。試行の履歴 `attemptLog` とは別）に付ける。1 行は `at`（時刻）、`by`（誰が。`refetch` はチャプターの取り直し、`redo` はチャプターの入れ直し）、`result`（`written` は書き込んだ、`not_fetched` はチャプターを取れなかったので書かなかった、`failed` は書き込めなかった）、`count`（書き込んだチャプターの件数。`written` のときだけ、ほかは 0）、`error`（書き込めなかった理由。`failed` のときだけ、ほかは空）。一覧には付けない（[ADR 0033](adr/0033-chapters-written-into-saved-file.md)）。
- `GET /api/jobs/{id}/audio`: 録音のファイルを返す。ジョブの保存先 `outputPath` のファイルを `Content-Type: audio/mp4` で返し、`Range` に 206 で、`HEAD` にも答える。パスは受け取らない。出すのは今の設定の `library` の下の `.m4a`（拡張子の大文字と小文字は区別しない）だけ。ジョブの画面の詳細の再生が呼ぶ。断る状態: ジョブが無い 404、保存先が空（まだ保存していない）404、`library` が空・`library` の外・拡張子が `.m4a` でない 403、ファイルが無い（移された・消された）404、そのほかの開けない・読めない 500（`error` は `ファイルを読めない: <OS のエラーの文>` で、ログにも出す）。
- `POST /api/jobs/{id}/retry`: 手動再試行。`retrying`・`expired`・確定した `skipped`（録らなかった）を受け付ける。録音期限を過ぎたものは拒むが、[ライブ録音](recording.md#ライブ録音)で録れた分を残している間（最後の試行が保存か検証の段階で失敗し、その試行の終わりから 30 日以内の `retrying`・`expired`）は受け付ける。[`retry`](cli.md#retry) とジョブの画面の詳細が呼ぶ。
- `POST /api/jobs/{id}/skip`: 録らない。試行がまだ無い `waiting` を受け付ける。[`skip`](cli.md#skiprecord) と、番組表とジョブの画面の詳細の「録らない」が呼ぶ。
- `POST /api/jobs/{id}/record`: 録る。`skipped` を受け付け、録らない予定は `waiting` に戻し、録らなかったは手動再試行にする。[`record`](cli.md#skiprecord) と、番組表とジョブの画面の詳細の「録る」が呼ぶ。
- `POST /api/jobs/{id}/rerecord`: 録り直し。タイムフリーから録った `done` で録音期限の内側のものを受け付け、手動再試行と同じく `retrying` にする。録音期限は今の会員種別と今の自分のエリアの局一覧で求める。`done` でない・[ライブ録音](recording.md#ライブ録音)・録音期限を過ぎた・自分のエリアの局一覧を取れない（エリアフリー・ダブルプランのとき。ジョブは `done` のまま）は、わけを分けた文で 409。[`rerecord`](cli.md#rerecord) とジョブの画面の詳細の「録り直す」が呼ぶ。
- `POST /api/jobs/{id}/chapters`: ジョブ 1 件の[チャプターの入れ直し](../GLOSSARY.md)。body は無い。タイムフリーから録った `done` のジョブを、チャプターの値によらず受け付ける（`added` のジョブも、ファイルのチャプターを置き換える）。録音期限は問わない。DB のジョブに頼みを書き、録音のループを起こして、受け付けたジョブを一覧の 1 行と同じ形で返す（このときはまだ書き込んでいない）。録音のループが、radiko から音声を取り直さずに、保存したファイルにチャプターを書き込む（書き込み方はチャプターの取り直しと同じ。[ADR 0033](adr/0033-chapters-written-into-saved-file.md)）。設定 `chapters` が `false` でも行い、チャプターの待ちは起こさない。保存した放送回の全部でチャプターを取れなければファイルを書かない。結果は `chapters` と `chapterWrites`（`by` が `redo`）に出て、通知はしない。`done` でない・[ライブ録音](recording.md#ライブ録音)は、わけを分けた文で 409。[`chapters`](cli.md#chapters) とジョブの画面の詳細の「チャプターを入れ直す」が呼ぶ。
- `POST /api/jobs/chapters`: まとめてのチャプターの入れ直し。body は無い。タイムフリーから録った `done` のジョブのうち、`chapters` が無い（取りに行っていない）か `not_added` のものの全部に頼みを書き、録音のループを起こして、受け付けたジョブの数を `{"accepted": <数>}` で返す（当たるジョブが無ければ 0）。`added`・`waiting` のジョブは飛ばす。放送日では絞らない（120 日の範囲の外のジョブにも当たる）。[`chapters -all`](cli.md#chapters) が呼ぶ。
- 不明な状態は 400、無い ID は 404、受け付けない状態と録音期限を過ぎたジョブは 409。

### 通知

要ログイン。API トークンでも叩ける。 <!-- 足したら直す: 通知の種類 -->

- `GET /api/notifications`: 通知の履歴。
  - `{"notifications": [{id, kind, message, createdAt, failed, read, link}]}` を新しい順に全部。
  - `kind` は通知の種類（`recorded`・`missing`・`expired`・`skipped`・`skip_planned`・`unskipped`・`deadline_near`・`keyword_added`・`keyword_limit`・`unmatched_day`・`area_changed`・`sync`・`out_of_area`・`login`・`backup`・`wake`・`rescan`・`recovered`・`test`）、`failed` は「送れなかった」の印、`read` は既読。
  - `link` は通知が指す Web UI の画面のパスで（接続先の URL は含まない）、ジョブの出来事（`recorded`・`missing`・`expired`・`skipped`・`skip_planned`・`unskipped`・`deadline_near`）は `/jobs/{id}`、`keyword_added`（足した先の番組）と `unmatched_day` は `/shows/{番組の名前}`（名前はパスの 1 区切りとしてエスケープし、`/` も `%2F` にする）、`keyword_limit` は `/keywords`。
  - 自分のエリアが変わった（`area_changed`）・サービスの異常・復旧・テスト通知には付かず、キーごと出さない。
  - 番組の名前を変えたら、その番組を指す通知の `link` も新しい名前になる。
  - 指す先のジョブ・番組が今もあるかは確かめない（無ければ開いた画面が一覧に移す。[web-ui.md の「ナビと URL」](web-ui.md#ナビと-url)）。
- `POST /api/notifications/read`: 未読の通知を全部既読にする。200 と `{"marked": <既読にした数>}`。通知の画面が呼ぶ。
- `POST /api/notifications/{id}/read`: その 1 件だけを既読にする。204。もう既読でも 204。通知が無い、または `{id}` が整数でないなら 404。トーストから通知が指す画面を開いたとき、Web UI が URL の `notification=<id>` で呼ぶ（[web-ui.md の「通知の画面と帯」](web-ui.md#通知の画面と帯)）。
- `GET /api/notifications/status`: ナビの未読の数と帯の元と通知先の種類。`{"unread": <未読の数>, "failed": <送れなかった通知の数>, "anomalies": [{kind, message, since}], "channel": <notify.channel の値>}`。`failed` と `anomalies` の決め方は [web-ui.md の「通知の画面と帯」](web-ui.md#通知の画面と帯)。`channel` は `discord`・`slack`・`email`・`none` などの種類の名前だけで、Webhook の URL は返さない。
- `POST /api/notifications/test`: テスト通知をその場で通知先に送り、結果を付けた行を書く。送れても送れなくても 200 と `{"sent": <送れたか>, "sentAt": <送れた時刻>, "error": <送れなかったときの通知先のエラー>, "notification": <書いた行。履歴と同じ形>}`（`sentAt` は送れたときだけ、`error` は送れなかったときだけ付く）。通知先が `none` なら送らずに行だけ書き、200 と `{"sent": false, "noChannel": true, "notification": <書いた行>}`（`noChannel` は `none` のときだけ付き、`sentAt` と `error` は付かない）。決め方は [web-ui.md の「テスト通知」](web-ui.md#テスト通知)。

### 設定

要ログイン。API トークンでも叩ける。[設定](settings.md)。

- `GET /api/settings`: 今の設定。`{"settings": {...}, "secrets": {"<秘密の名前>": {"set": <設定済みか>, "unreadable": <復号できないか>}}, "library_set": <保存先が決まっているか>, "unreadable_secrets": [<復号できない秘密の名前>, ...], "last_backup_at": <最後に自動のバックアップを取れた時刻（RFC 3339）。まだ取れていないなら null>, "radiko_login": null}`（`radiko_login` は `PATCH` の答えでだけ値が入る。下の `PATCH`）。`settings` のキーは [設定](settings.md) の表のキー（`library`・`notify` の `channel` など）で、秘密の値は含まない。秘密の名前は `notify.discord_webhook`・`notify.slack_webhook`・`notify.smtp.password`・`jellyfin.api_key`・`radiko.password`。復号できない秘密は `set` も `true`。
- `PATCH /api/settings`: body は `settings` と同じ形の JSON で、書いた項目だけを変える。秘密は `notify.discord_webhook` などの位置に値を書き、空の文字列なら消す。Web UI の設定の画面が呼ぶ。成功すると 200 と、`GET` と同じ形の当てた後の設定。
- 知らないキー・JSON が読めない・値の誤り（[設定](settings.md)の確かめと同じ）・保存先のフォルダを作れない・書けないときは 400 で、何も当てない。
- `membership`・`radiko.mail`・`radiko.password` のどれかを書き、当てた後の `membership` が `free` でなければ、保存する前に radiko のログインを 1 回試す（[settings.md の「会員種別と radiko のログイン」](settings.md#会員種別と-radiko-のログインmembership)）。
  - radiko が拒んだら 400 と `{"error": "..."}` で、何も当てない（値の誤りと同じ形）。
  - 200 の答えの `radiko_login` は、試した結果。`"ok"`（通った）、`"unchecked"`（通信の失敗・radiko の 5xx・時間切れで確かめられなかった。保存はした）、`"mismatch"`（通ったが、radiko の応答のエリアフリーの値が `membership` と食い違う。保存はした）。試さなかった保存では `null`。
  - radiko の応答を待つのは 30 秒まで。その間、ほかの `PATCH /api/settings` は待たされる。
- 保存したら、再起動しなくてもすぐに効く（`radiko.program_cache_mb` を除く）（`GET /api/notifications/status` の `channel` も、保存した通知先の種類になる）。`membership` を変えたら、番組表の API の範囲も変わり、まだ終わっていないジョブの録音期限も、保存を待たずに付け直す（[settings.md の「会員種別と radiko のログイン」](settings.md#会員種別と-radiko-のログインmembership)）。

### フォルダ

要ログイン。API トークンでも叩ける。設定の画面のフォルダを選ぶダイアログが使う。

- `GET /api/folders?path=<絶対パス>`: `serve` のマシンのフォルダの一覧。`{"path": <正規化したパス>, "parent": <親のパス。ドライブの根・UNC の共有の根・根の一覧では空>, "folders": [{"name": <名前>, "path": <子のパス>}]}` を名前の順に返す。ファイルは返さない。フォルダへのシンボリックリンクとジャンクションは先をたどり、先が無い・読めないものは出さない。隠しフォルダも出す。`path` が空なら根の一覧（Linux は `/`、Windows はドライブ）。UNC のパス（`\\nas\radio` など）は絶対パスとして受ける。
- 相対パス・無いフォルダ・フォルダでないもの・読めないフォルダは 400 で `{"error": <理由>}`。
- たどれる範囲は絞らない: ログインした人は、設定で `serve` が書けるところならどこでも保存先にできて、見るより書く方が強いため。

### バックアップ

要ログイン。API トークンでも叩ける。[バックアップと戻し方](backup.md)。

- `GET /api/backup`: その場で DB のバックアップを作って返す。200 で、本文は SQLite の DB のファイル、`Content-Type` は `application/vnd.sqlite3`、`Content-Disposition` は `attachment; filename="rajikura-<日時>.db"`（日時は日本時間の `YYYYMMDD-HHMMSS`）。ログインのセッションは空にしてあり、秘密の鍵は含まない。[`backup`](cli.md#backup) と設定の画面の「ダウンロード」が呼ぶ。 <!-- glossary-ok: ダウンロード -->
- 作れなければ 500。
- 作ったバックアップは、返すまで作業用フォルダ（`work/backup-*/`）に置き、返したら消す。

### 番組

要ログイン。

- `GET /api/shows`: 一覧。番組ごとに次の放送予定と、当たらない日の並び（`unmatched_days`。枠の開始時刻が今より後のものを枠の開始時刻の順に。1 つの JSON は `{"day": "<放送日 YYYY-MM-DD>", "station": "<局>", "start": "<枠の開始時刻>", "covering": <その時刻を含む放送回 {"title", "start", "end"}。無ければ null>, "titled": [<同じ局・同じ放送日で番組名の合う放送回 {"title", "start", "end"}>]}`。無い番組と無効な番組には付けない）を付ける。
- `POST /api/shows`: 作成。
- `GET /api/shows/{name}`
- `PUT /api/shows/{name}`: 丸ごと置き換え。body の `name` が違えば名前の変更。
- `DELETE /api/shows/{name}`
- `PUT /api/shows/{name}/enabled`: JSON `{"enabled": true}` で有効／無効の切り替え。
- `GET /api/stations`: radiko の局の一覧（`full.xml` の全局）。`[{id, name, areaName, outOfArea}]`。`areaName` は局のエリアの名前（`full.xml` の局の `area_id` から引いた都道府県名）、`outOfArea` は自分のエリアの外の局か（今日の放送日の、自分のエリアの番組表の局一覧に無ければ `true`。局の本拠地のエリアでは決めない）。`areaName` は局のエリアが分からないとき、`outOfArea` は自分のエリアの局一覧を取れない・空のときは付かない（一覧そのものは返す）。
- `GET /api/show-name?station=<局>&weekday=<0〜6、0 = 日曜>&start=<HH:MM>`: 新しい番組の補助。
- `{name}` は番組の名前を URL エンコードしたもの。
- 作成と置き換えで `enabled`・`check_title` を省けば `true`、`split` を省けば `"none"`（分割番組でない）。
- `split` は `"none"`・`"join"`・`"separate"` の文字列（[番組](shows.md#番組)）で、bool やほかの値を送ると 400（[ADR 0003](adr/0003-job-per-show-and-broadcast-day.md)）。
- 毎週の予約は 0 個でもよい（1 回の予約だけを持つ番組）。
- 毎週の予約は `weekly` に並べ、1 つの JSON は `{"station": "<局>", "weekday": <0〜6、0 = 日曜。放送日の曜日>, "start": "<HH:MM>", "from": "<YYYY-MM-DD>", "until": "<YYYY-MM-DD>"}`（`from` は任意の開始日、`until` は任意の終了日で、どちらも放送日で、その放送日を含む。省くか空なら制限なし。`YYYY-MM-DD` の放送日として読めなければ 400。`from` と `until` の両方があって `until` が `from` より前なら 400（同じ日はよい）。意味は [毎週の予約](shows.md#毎週の予約)）。
- 1 回の予約は作成・置き換えの body には書かず（`GET /api/shows/{name}` は `onces` を付ける）、置き換えでは今の番組のものを残し（名前の変更なら新しい名前の番組に引き継ぐ）、削除では番組と一緒に消す。
- JSON が読めない・値が正しくない（名前が空、時刻が `HH:MM` でないなど）・必要なパラメータが無いときは 400、無い番組は 404、同じ名前の番組がもうあれば 409、radiko から局の一覧・番組表を取れなければ 502。

### 1 回の予約

要ログイン。

- `POST /api/shows/{name}/onces`: 番組に 1 回の予約を足す。
- `DELETE /api/shows/{name}/onces/{id}`: 消す。
- 一覧は `GET /api/shows/{name}` の `onces` に付く（番組の一覧 `GET /api/shows` には付けない）。
- 足す body は `{"airings": [{"station": "<局>", "start": "<開始時刻（RFC 3339）>"}, ...]}`。1 回で複数の放送回を受け取り（body の大きさの上限は 64KB）、全部足すか 1 つも足さないか（1 つのトランザクションで足す）。成功すると 201 と、足した 1 回の予約の `{"onces": [...]}` を返す。
- 足す前に、エリアの日別番組表（下の番組表の API と同じもの。番組表は[番組表のキャッシュ](settings.md#番組表のキャッシュ)を通る）でその放送回があることを確かめる。自分のエリアの番組表に無ければ、会員種別がエリアフリー・ダブルプランのときに限り、その局のエリア（`full.xml` の局の `area_id`）の番組表で探す（body に `area` は要らない）。次のとき断る。
  - 400: JSON が読めない、`airings` が空、局か開始時刻が無い、同じ放送回が重なっている、番組表の範囲（下の番組表の API と同じ放送日）の外、番組表にその放送回が無い、録音期限を過ぎた放送回（タイムフリーの対象外の放送回は、放送の終了の 5 分後を過ぎたら断る。タイムフリー30・ダブルプランでは、タイムフリー30 の対象外の放送回（[settings.md の「キー」](settings.md#キー)の `membership`）が、7 日を過ぎたら断る）。
  - 404: 番組が無い。
  - 409: ほかの番組が当てている（`other_show`。`showName` がその番組）、同じ番組がすでに当てている（`already`）。
    - 「当てている」かは、下の番組表の API の `state` に合わせて決める（ジョブがあるときと、同じ番組の扱いは同じ。同じ番組の当たり方は展開と同じ（[毎週の予約](shows.md#毎週の予約)））。
    - 放送回にジョブがあれば、そのジョブの番組で決める。
    - ジョブが無ければ、ほかの番組の毎週の予約・1 回の予約に当たるとき（今日より前の放送日でも）`other_show`。足すと展開で矛盾になるため。番組表ではその放送回は、開始日を持たない毎週の予約なら「予約なし」と出る。
    - ジョブが無ければ、同じ番組は、1 回の予約が同じ放送回を指しているか、同じ番組の予約で録るとき `already`。
    - 今日より前の放送日で、開始日を持たない毎週の予約だけに当たる放送回と、録らない予定・録らなかったにする放送回（どの放送日でも）でジョブが無いもの（展開の前、連結する番組で一部だけ番組名が違ったとき）は、1 回の予約で足せる。
    - 409 の本文には断った放送回の `airing`（`station`・`start`）も付く（並行するリクエストとの競合で保存先に断られたときは、どの放送回か分からないので `airing` は付かない）。
  - 502: radiko から番組表を取れない。
- 無効な番組の予約は当たらないので、ほかの番組が無効なら断る理由にならない。無効な番組にも足せる（当たらないだけ）。タイムフリーの対象外の放送回も足せる（[ライブ録音](recording.md#ライブ録音)で録る）。
- `onces` の 1 件は `id`、`stationId`、`start`、そして番組表から引けたときの `stationName`・`end`・`title`、`missing`（番組表の範囲の放送日なのに、その放送回が番組表に無い。開始時刻が変わった・消えた）。番組表を取れなかった・範囲の外の放送日では、`title` などは付かず `missing` は `false` のまま（分からない）。[キーワード](shows.md#キーワード)が足した 1 回の予約には、`keywordId`（足したキーワードの ID）と `keywordWords`（そのキーワードの語）も付く。人が足したもの、足したキーワードを消した後は付かない。
- 足せるかの判定（上の 409 の `reason`）は、キーワードが 1 回の予約を足すときと同じ。
- 消すときは `{name}` の番組の 1 回の予約でなければ 404。
- 足した・消した直後に、[recording.md の「番組を変えた直後の展開」](recording.md#番組を変えた直後の展開)と同じやり方で、その局・放送日の展開を展開役に頼む（展開を待たずに応答を返す。失敗しても保存は成功のまま）。

### 番組表

要ログイン。

- `GET /api/guide?day=<YYYY-MM-DD>&area=<エリア ID>`: その放送日の、エリアの全局の放送回。局ごとに `{id, name, airings}`。`area` を省くと自分のエリア。
- `GET /api/guide/areas`: 番組表で選べるエリアの一覧 `{own, switchable, pastDays, areas: [{id, name}]}`。`own` は自分のエリア ID、`pastDays` は番組表で選べる過去の日数（会員種別の録音期限。非会員・エリアフリーは 7、タイムフリー30・ダブルプランは 30。画面が放送日の範囲を決めるのに使う）、`switchable` は会員種別がエリアフリー・ダブルプランか（`true` のときだけ、画面はエリアの切り替えを出す）。`areas` はエリア ID の番号の順で、自分のエリアを含む。radiko から取れなければ 502。
- `GET /api/guide/search?q=<語>&area=<エリア ID>&scope=all`: 表示する範囲の全部の放送日から、番組名・出演者・説明に当たる放送回を開始時刻の順に返す。`q` は空白（全角も）で区切った語の AND で、どの語も 3 つの欄のどれかに含まれれば当たる（語が別々の欄に当たってもよい）。語と欄を正規化（全角 → 半角、空白を除く、大文字にそろえる）してから部分一致を見るので、全角・半角、大文字・小文字の違いを無視する。照合は、[キーワード](shows.md#キーワード)を番組表に当てるときと、下のキーワードの API の `GET /api/keywords/preview` と同じ（除外する語と局の絞り込みはキーワード（キーワードの API）だけで、検索の入力は `q` のまま）。500 件を超えたら `truncated` が `true`。
- 放送日は今日の 7 日前（会員種別がタイムフリー30・ダブルプランなら 30 日前。会員種別の録音期限）から 7 日先までで、範囲の外の `day`・読めない `day`・空の `q` は 400。
- 局は、`area` を省くと認証（auth2）で radiko が判定したエリアの全局で、エリアが分からない・radiko から番組表を取れなければ 502。
- `area`（`GET /api/guide`・`GET /api/guide/search`）は、会員種別がエリアフリー・ダブルプランのときだけ自分のエリア以外を指せる。そうでない会員種別（非会員・タイムフリー30）が自分のエリア以外を指す、または radiko の局の一覧（`full.xml`）に無いエリア ID を指すと 400。自分のエリアの ID を指すのは、どの会員種別でも省いたのと同じ。キーワードの `stations` が空のときの対象は、今までどおり自分のエリアの全局（`area` では変わらない）。`stations` で名指しした自分のエリアの外の局は、会員種別がエリアフリー・ダブルプランのときだけ当たる（`area` では変わらない）。
- `scope=all` は[全国の検索](../GLOSSARY.md)で、`area` のエリアではなく、radiko の全局（`full.xml` の局の一覧）の局ごとの日別の番組表（`/v3/program/station/date/<YYYYMMDD>/<局>.xml`）から探す（[ADR 0035](adr/0035-nationwide-search-from-prefetched-station-guides.md)）。
  - `scope` は `all` だけを受け付ける（省くか空なら自分のエリア、または `area` のエリアの検索）。ほかの値は 400。会員種別がエリアフリー・ダブルプランでないのに `scope=all` を指すと 400。`scope=all` と `area` を一緒に指すと 400。
  - 応答に `"scope": "all"` と `failedStations`（番組表を、ある放送日を 1 つでも取れなかった局の数）を足す。取れなかった局があっても、取れた分の放送回を返す。全部の局を取れなかったとき、局の一覧か自分のエリアが分からないときだけ 502。
  - 放送回には、局が自分のエリアの局一覧（その放送日の自分のエリアの番組表）に無ければ `stationAreaName`（局のエリアの都道府県名。`full.xml` の局の `area_id` から引く）と `stationAreaId`（局のエリアの ID。`full.xml` の局の `area_id`。空なら付けない）を付ける。画面が、全国の検索の結果から開いた詳細でほかのパートを探す番組表のエリアに使う。自分のエリアの局と、`scope=all` でない検索の放送回には付かない。そうした局の放送回は、エリアの外から録る局の放送回として `notAvailable`・`timeFree30Excluded` を決める（`area` で自分のエリア以外を指したときと同じ）。
  - 同じ時刻・同じ番組名の放送回（系列のネット番組）が複数の局に出ても、まとめずに局ごとに返す。`state` の付け方と 500 件で切る決まりは、自分のエリアの検索と同じ。
  - 局ごとの日別の番組表は[番組表のキャッシュ](settings.md#番組表のキャッシュ)を通り、取り直しを緩めて読む（同じ節）。
- エリアの一覧は `full.xml` の局の `area_id` から作る（1 時間覚える）。`full.xml` にエリアの名前は無く、取れるのはエリア ID（`JP1`〜`JP47`）だけ。名前はエリア ID の番号から引いた都道府県名（引けない ID は ID のまま）。局が 1 つも出てこないエリアは一覧に入らない。
- エリアの日別番組表（`/v3/program/date/<YYYYMMDD>/<エリア ID>.xml`）は[番組表のキャッシュ](settings.md#番組表のキャッシュ)を通る（番組表の同期と同じキャッシュ。API 側では覚えない）。
- エリア ID は 1 時間覚える。
- 放送回ごとに `state` を付ける: `none`（予約なし）、`planned`（録音予定。ジョブが `waiting` / `running` / `retrying`、またはジョブが無くても有効な番組の予約で録る）、`done`（録音済み）、`missed`（録音できなかった。ジョブが `expired`）、`skip_planned`（録らない予定。ジョブが確定の前の `skipped`、またはジョブが無くても有効な番組の予約で録らないにする放送の前の放送回）、`skipped`（録らなかった。ジョブが確定した `skipped`、またはジョブが無くても有効な番組の予約で録らないにする放送の後の放送回）。
- ジョブが無い放送回の放送の前か後かは、今日より前の放送日なら後、ほかはその放送回だけのジョブの最初の試行の時刻の前か後かで決める（展開が録らない予定を確定する時刻と同じ。タイムフリーの対象外の放送回は放送の開始の 1 分前、それ以外は、局の `tf_max_delay` を見ないので最も短い遅れで数える）。
- ジョブとは放送回の Key（局 + 開始時刻）で照合する。
- ジョブが無い放送回の当たり方は展開と同じで、今日より前の放送日では、1 回の予約と、開始日を持つ毎週の予約（録音期限の内側）だけが当たる（[毎週の予約](shows.md#毎週の予約)）。
- 無効な番組の予約は当たらない（`none`）。
- `showName` はジョブか当たっている番組の名前（`none` では付けない）、`jobId` は放送回のジョブの ID（ジョブがあるときだけ。番組表の詳細の「録らない」「録る」が使う）、`skipBy` は `skip_planned`・`skipped` のとき誰が録らないと決めたか（`title`: rajikura が番組名の食い違いで、`person`: 人が）、`skippable` は「録らない」を受け付けるか（ジョブが、試行がまだ無い `waiting`）、`notAvailable` はタイムフリーの対象外（`ts_in_ng` が 0 でない。自分のエリアの外の局は `ts_out_ng`。[ライブ録音](recording.md#ライブ録音)で録る）、`timeFree30Excluded` はタイムフリー30 の対象外（`tsplus_in_ng` が 0 でなく、録音期限が 7 日になる放送回。自分のエリアの外の局は `tsplus_out_ng`。[録音期限](settings.md#会員種別と-radiko-のログインmembership)）で、会員種別がタイムフリー30・ダブルプランのときだけ立つ（非会員・エリアフリーは録音期限が 7 日で変わらないので、立てない）。

### キーワード

要ログイン。API トークンでも叩ける。[キーワード](shows.md#キーワード)。JSON のキーは `onces` の 1 件と同じ camelCase。

- `GET /api/keywords`: 一覧。作った順。`{"keywords": [{id, words, exclude, stations, enabled, pending}]}`。`stations` は局 ID の配列で、空なら自分のエリアの全局。名指しした自分のエリアの外の局（その放送日の自分のエリアの局一覧に無い局）は、会員種別がエリアフリー・ダブルプランのときだけ当たり（その局の番組表を足して引く）、ほかの会員種別では当たらない（`stations` はそのまま残す）。`pending` は、そのキーワードが足した放送回のうち、まだ録音を終えていない放送回の数（上限 `keyword_max_pending` と比べる数）。
- `POST /api/keywords`: 作る。body は `{"words": "<語>", "exclude": "<除外する語>", "stations": ["<局 ID>", ...], "enabled": <true か false>}`。`exclude`・`stations` は省ける。`enabled` を省けば `true`。201 とキーワード 1 つ。
- `PUT /api/keywords/{id}`: 置き換える。body は作るときと同じ。200 とキーワード 1 つ。足した 1 回の予約と、足した放送回の記録は残す。
- `DELETE /api/keywords/{id}`: 消す。204。足した 1 回の予約と作った番組は残し、1 回の予約の `keywordId` は付かなくなる。足した放送回の記録は消える。
- `GET /api/keywords/{id}/airings`: 足した放送回の記録を、開始時刻の新しい順に。`{"airings": [{stationId, stationName, start, end, title, showName, state}]}`。`stationName`・`end`・`title`・`showName` は足したときのもの。`state` は `planned`（録音予定）・`done`（録音済み。その放送回のジョブが `done`）・`missed`（録音できなかった。ジョブが `expired`）・`skip_planned`（録らない予定。ジョブが確定の前の `skipped`。録音予定には数えない）・`skipped`（録らなかった。ジョブが確定した `skipped`。録音予定には数えない）・`removed`（録音を終える前に 1 回の予約が外された。記録にあるのに、その放送回の 1 回の予約もジョブも無い）。
- `GET /api/keywords/preview?words=<語>&exclude=<除外する語>&stations=<局 ID をカンマ区切り>`: 今キーワードを作ったら当たる、まだ始まっていない放送回。今日から 7 日先までの放送日の番組表から探す。`{"airings": [...], "truncated": <true か false>}`。放送回の形と `state` の付け方は `GET /api/guide/search` と同じで、500 件を超えたら `truncated` が `true`。当てる範囲はキーワードを番組表に当てるときと同じで、名指しした自分のエリアの外の局の放送回には、エリアの外から録る印で `notAvailable`・`timeFree30Excluded` を決め、`stationAreaName`（局のエリアの都道府県名。分かるときだけ）を付ける。その局の番組表を取れない放送日があれば 502。
- 断るとき: JSON が読めない、`words` が正規化して空（`preview` では `words` が空）、`stations` に空の局がある、`{id}` が整数でないときは 400。無いキーワードは 404。語の形が同じほかのキーワードがあれば 409。`preview` で radiko から番組表を取れなければ 502。
- 作った・置き換えた直後に、次の番組表の同期を待たずに、キーワードを番組表に当てることを展開役に頼む（API は待たずに応答を返す。当たった放送回の 1 回の予約と、そのジョブは少し後にできる）。
