# 開発ガイド

rajikura を手元でビルド・テストする手順、開発の決まり、用語集とコードの名前の対応、リリースの手順をまとめる。設計の背景は [要件定義](requirements.md)、用語は [GLOSSARY.md](../GLOSSARY.md)、決定の理由は [docs/adr/](adr/) にある。

- [手元でビルド・テストする](#手元でビルドテストする)
- [開発の決まり](#開発の決まり)
- [docs の書き方](#docs-の書き方)
- [ADR の決まり](#adr-の決まり)
- [コード上の名前](#コード上の名前)
- [足したら直す列挙](#足したら直す列挙)
- [リリース](#リリース)

このファイルの全体で、経緯と実例（「PR #… で起きた」の列挙、決まりの根拠にした実測の値など）は issue に置き、本文には決まりの 1 文と `（#N）` だけを書く。

## 手元でビルド・テストする

### Go

- Go の版は `go.mod` の `go` の行に書き、そこを正とする。構成は `cmd/rajikura`（入口）と `internal/...`（中身）。Windows のトレイは別の exe で、`cmd/rajikura-tray` に置く（`-H windowsgui` でビルドする。ほかの OS では起動しても止まるだけ）。

- Go の手順。CI が実行するのは `go vet`・`go test`・`gofmt -l` と、Windows の job だけで見る staticcheck の U1000（使われていない識別子）で、`go build` は手元で足す確かめ:

  ```bash
  go test ./...
  go vet ./...
  gofmt -l .      # 何も出なければよい
  # 使われていない識別子（U1000）。CI は Windows の job で、これと同じ呼び方をする
  printf 'windows: GOOS=windows\nlinux: GOOS=linux\n' | go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 -matrix -checks U1000 ./...
  go build ./cmd/rajikura      # CI は実行しない。手元で足す
  ```

CI は `go vet`・`go test`・`gofmt -l` を Linux と Windows で実行し、Windows の job ではさらに staticcheck の U1000（使われていない識別子）を `-matrix` で GOOS=windows と GOOS=linux の両方のビルドについて実行する（全部の組み合わせで使われていないものだけが落ちる）。job は全部 GitHub-hosted の標準の runner（ubuntu-latest と windows-latest）で動く。細部は `.github/workflows/ci.yml` の冒頭に書いてある。

ffmpeg / ffprobe を使うテストは、見つからなければ `t.Skip` する。CI は Linux の ffmpeg を workflow の中で apt で入れて有ることを確かめ、Linux で skip されたテストがあれば失敗にする。Windows は skip を許す。

### フロントエンド（Web UI）

SPA は `web/` にある（TypeScript + React + Mantine、Vite、pnpm）。Node と pnpm の版は `mise.toml` で固定してある。作りは [ADR 0008](adr/0008-web-ui-shape.md)。Go だけを触る人は Node が要らない（`web/dist/.gitkeep` があるので `go build` と `go test` は通り、仮の画面が出る）。

共通の部品は `web/src/ui/` にある。成功の知らせは `notifySuccess`（`toast.ts`）で、操作の API が成功した直後にトーストに出す（そのあとの一覧の読み直しの失敗は別のエラー）。失敗は消えると読み逃すのでその画面の Alert に出す。表の見出しは `SortableTh` で並べ替える。

開発サーバー: 依存を入れ、`rajikura serve` を起動しておいて Vite の開発サーバーを `/api` の中継先につなぐ。初めて起動したときは、ログか `./data/setup-code.txt` のセットアップコードを画面に入れてパスワードを決める（[web-ui.md の「初めて開いたとき」](web-ui.md#初めて開いたとき)）。

```bash
mise install                  # mise.toml の Node と pnpm を入れる
cd web && pnpm install
cd ..
rajikura -data ./data serve   # 端末 1: API（既定の -listen は :8080）
cd web && pnpm dev            # 端末 2: http://localhost:5173。/api は :8080 に中継する
```

`serve` の `-listen` を変えているときは、環境変数 `RAJIKURA_API`（例: `http://localhost:9090`）で中継先を指す。セッションの cookie は `Secure` だが、`localhost` は例外として扱うブラウザが多い（Chrome で確かめた手順ではない。未確認）。

画面の操作を足すときは、API が断ったとき・失敗したときの画面の表示のテストも足す（成功の経路だけにしない）。

検査とビルド（CI の `web` の job と同じ）:

```bash
cd web
pnpm typecheck
pnpm test
pnpm build
```

ビルドと埋め込み:

- 出力先は `web/dist`。中身は `.gitignore` で外し、`.gitkeep` だけを入れてある（go:embed が通るように。`web/embed.go`）。
- Vite の `build.emptyOutDir` は `false`（`true` だと `.gitkeep` が消えて Node なしの `go build` が通らなくなる）。古い成果物は、`pnpm build` が先に `scripts/clean-dist.mjs` で消す（`vite build` だけでは消えない）。
- ビルドした後に `go build ./cmd/rajikura` すると、`web/dist` が埋め込まれる。
- コンテナイメージは、Dockerfile の Node の段で `web/` をビルドして埋め込む。Node の版は Dockerfile の `FROM node:...` の tag で指すので、`mise.toml` の `node` と揃える（ずれると CI の `web` の job が落ちる）。

### Windows のトレイを埋め込んでビルドする

`rajikura.exe` には Windows のトレイ（`rajikura-tray.exe`）を埋め込む。repo で自分でビルドするときは、トレイを先にビルドして `cmd/rajikura/traybin` に置いてから `rajikura.exe` をビルドすると、トレイが埋め込まれる（Release の `rajikura.exe` も、Dockerfile の中で同じ順にビルドする）。

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui" -o cmd/rajikura/traybin/rajikura-tray.exe ./cmd/rajikura-tray
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o rajikura.exe ./cmd/rajikura
```

- 置かずにビルドした `rajikura.exe` の `service install` は、「この rajikura.exe にはトレイが入っていないので、トレイは入れない」と出して、トレイを入れずにサービスを入れる。
- 置かずにビルドした `rajikura.exe` の `tray install` は、何もせずに「この rajikura.exe にはトレイが入っていない（Release の rajikura.exe には入っている）」と出して失敗する（終了コード 1）。
- トレイの exe だけを試すときは、1 行目で `cmd/rajikura/traybin` にできた `rajikura-tray.exe` をダブルクリックで起動する（ログイン時の起動は、トレイのメニューで登録する）。
- `-H windowsgui` でビルドするので、起動しても窓は出ず、端末にも何も出さない。出すものは [windows.md のトレイのログ](windows.md#トレイのログ)に書いてある。

`service install` は、トレイを差し替える前に、動いている前のトレイを次のとおり終わらせる。

- 差し替える前に、`service install` と同じセッション（端末を開いた画面）で動いている前のトレイに終了を頼み、終わるのを最大 10 秒待つ。
- 頼み方は、トレイのメニューの「終了」と同じ（トレイの隠れた窓に `WM_CLOSE` を送る）。fyne systray v1.12.2 のソースでは、それで通知領域のアイコンを消して終わる。外から送ったときの振る舞いは、動かして確かめていない（未確認）。
- 止めるのは、exe の名前が `rajikura-tray.exe` のトレイで、置き場所は問わない（`cmd/rajikura/traybin` から起動したトレイも止める）。終わったら「動いていた前のトレイを終了した」と出す。
- ほかのユーザーのセッションで動くトレイには頼まないので、そのトレイは前の版のまま動き続け、終了して起動し直すか、ログインし直すと新しい版になる。

## 開発の決まり

- SQLite のドライバは `modernc.org/sqlite`（cgo 不要）。マイグレーションは、埋め込んだ SQL を順に当てる仕組みを自前で書く。
- 時刻は日本時間で扱う。`time.LoadLocation("Asia/Tokyo")` を使い、`time/tzdata` を埋め込む（Windows とコンテナでも動かすため）。
- テストで radiko に実際につながない。`httptest` で偽のサーバーを立てる。
- 設定（保存先、会員種別など）は DB に置き、人は Web UI で変える（[ADR 0015](adr/0015-settings-and-secrets-in-db.md)）。設定・番組を TOML で受け渡す口は無い（[ADR 0017](adr/0017-backup-by-db-copy.md)）。DB を開く前に要る起動の設定は、`serve` のフラグ `-data`・`-listen`・`-allowed-hosts` だけ。
- Web UI の画面の挙動（並び、知らせ方、入力の受け付け方など）を変えるときは、`docs/web-ui.md` のその画面の節を、番組・キーワード・設定の画面なら `docs/shows.md`・`docs/settings.md` のその箇所も、同じ PR で直す。API の挙動（`/api/` のルート、要求と応答の形、断るとき）を変えるときは、`docs/api.md` を同じ PR で直す。
- コミットは Conventional Commits。次の PR の決まり（`Closes #N`）は作者の開発の決まりで、外の人に求めるものではない（Pull Request は受けていない）。作者の PR は `Closes #N` を書く（Renovate の PR は issue を持たないので例外）。
- docs の地の文は用語集（`GLOSSARY.md`）の言葉で書く。Web UI の画面のラベルや文を引くところは、括弧か鉤括弧に入れ、用語集の言葉と画面の言葉が違うときだけ添える（例: 連結（画面では「パートを 1 つのファイルにつなぐ」））。
- シェルのテスト（`scripts/*_test.sh`）は、通った例ごとに `ok: <名前>` を出す（落ちた例は `FAIL:`、最後に `通った: N 件、失敗: M 件`）。テストを足したと書くときは、この出力に名前が出ていることを確かめる。
- コードのコメントとテスト名には、issue の決定の番号（「決定 4」）を書かず、中身で書く。引くなら issue の番号を付ける。
- docs と README.md と `GLOSSARY.md` の定義文に、`GLOSSARY.md` の `_Avoid_` の語を書かない（`scripts/check-glossary.sh` が CI の Linux で検査する。`docs/adr/` は対象外）。`_Avoid_` の行には語だけを書く。照合の細則、免除の書き方、`GLOSSARY.md` に求める書式はスクリプトの冒頭に書いてある。検査の正規表現を変えたら `scripts/check-glossary_test.sh` に例を足す。
- docs と README.md がバッククォートで名指しするコードのパス（`internal/…`・`cmd/…`）と `型.メソッド`（`Store.ListJobs` など）は、実在するものだけを書く（`scripts/check-code-refs.sh` が CI の Linux で検査する。`docs/adr/` も対象）。免除と細則はスクリプトの冒頭に書いてある。検査の形を変えたら `scripts/check-code-refs_test.sh` に例を足す。
- Go のコメントが名指しする `.go` のファイルは、実在するものだけを書く（`scripts/check-go-comment-refs.sh` が CI の Linux で検査する）。`internal/…`・`cmd/…` で始まるパスは repo root から、スラッシュの無い名前は同じディレクトリで照合する。別のパッケージのファイルはパスで書く。免除（コメント末尾の `code-ref-ok: 名指し`）と限界はスクリプトの冒頭に書いてある。検査の形を変えたら `scripts/check-go-comment-refs_test.sh` に例を足す。
- 「足したら直す列挙」の印は、列挙の項の名前で書く（`scripts/check-fix-markers.sh` が CI の Linux で検査する。git が追うファイルが対象で、`docs/adr/` とこの docs の「足したら直す列挙」の節は対象外）。列挙のどれかの項の印が 1 つも無いか、列挙に無い名前の印があるか、Go・TS の行だけのコメントの印がコメントのブロックの途中（次の行が文字のある `//` の行）にあると落ちる。印の付け忘れは見つけない。細則はスクリプトの冒頭に書いてある。検査の形を変えたら `scripts/check-fix-markers_test.sh` に例を足す。

## docs の書き方

- 使う人向けの docs（`README.md`・`docs/getting-started.md`・`docs/cli.md`・`docs/settings.md`・`docs/notifications.md`・`docs/web-ui.md`・`docs/shows.md`・`docs/recording.md`・`docs/backup.md`・`docs/windows.md`・`docs/kikidame.md`）と、API を呼ぶ人向けの `docs/api.md` には、今どう動き、どう使うかだけを書く。理由と経緯は ADR に置き、リンクで指す。
- 使う人向けの docs に書かないもの:
  - 検証の記録（「実機」で確かめた話、「未確認」の書き方の多く）、計測値、テストファイルの名前、作者の機器、「人の経験による」の書き方。使う人の判断に効く制限だけを、事実の 1 文で残す
  - 実装の仕様（aria-label、localStorage のキー、内部の関数名・型名、送る周期、内部で叩く API の口など）。API を呼ぶ人に要るもの（API の口・欄）は `docs/api.md` に置く
  - 廃止したものの「無い」の文。新しい読者は、前にあったことを知らない
  - 決めた・確かめた日、「人が申告した」、設計の議論や PR の話。決まりの 1 文と ADR・issue へのリンクにする
- 1 段落は 1 つの事柄にし、目安は 200 字まで（検査にはしない）。箇条書きの 1 項目にも事柄を 1 つだけ書く。
- 100 行を超えるファイルは、冒頭に節の目次を置く。

## ADR の決まり

公開した後は、決定を変えるときに、古い ADR に注記を足さず、新しい ADR を書く。古い ADR は、索引（`docs/adr/README.md`）で「置き換え済み（→ 新しい番号）」にする。

## コード上の名前

用語集には実装の話を書かない決まりなので、用語とコードで使う英語名の対応はここに置く。

| 用語 | コード |
| --- | --- |
| 番組 | `Show`（DB は `shows`、API は `/api/shows`） |
| 番組名 | `Airing.Title` |
| 放送回 | `Airing`（radiko の番組表の `Program` も放送回に当たる） |
| 放送日 | `BroadcastDay` |
| 休止回 | 状態としては持たない（当たらなくなった放送回をジョブから外す処理で表す） |
| 当たらない日 | domain の `UnmatchedDay`（求めるのは `domain.FindUnmatchedDays`）。DB は `unmatched_days`、API は `GET /api/shows` の `unmatched_days`、通知の種類は `unmatched_day` |
| 予約 | 総称の型は無い（`Weekly` と `Once`。総称の名前が要るときは `Schedule`） |
| 毎週 | `Weekly`（作るのは `NewWeekly`。DB・JSON は `weekly`） |
| 1 回 | `Once`（DB は `onces`、API は `/api/shows/{name}/onces`、JSON は `onces`） |
| キーワード | `Keyword`（domain の `domain.Keyword`。DB は `keywords`、API は `/api/keywords`） |
| 録音 | （一連の処理を指す型は無い） |
| 入手 | `Fetch`（試行の段階の値は `fetch`。画面では「入手」） |
| タイムフリーからの録音 | （方式を表す型は無い。`Job.Live` が false のジョブ。入手は radiko の `Client.FetchTimefree`） |
| ライブ録音 | domain の `Job.Live`（判定は `domain.LiveAirings`）。拾うのは `Store.ClaimDueLive` と `Store.ClaimDueLiveSave`（録音期限を過ぎた、保存だけのやり直し）（途中で止まったものは `Store.RunningLive`）、録るのは app の `internal/app/live.go`、入手は radiko の `Client.FetchLive`。ジョブの API の JSON は `live`（画面では「ライブ録音」） |
| タイムフリーの対象外 | domain の `Airing.TimeFreeExcluded`（値は `TimeFreeFlags.InNG`。radiko の番組表の `ts_in_ng`、`Program.TSInNG`。DB は `job_airings.ts_in_ng`）。番組表の API の JSON は `notAvailable`（画面では「ライブで録る」） |
| ジョブ | `Job` |
| 再試行待ち | domain の `StateRetrying`（DB と JSON は `retrying`）。録音か検証に失敗したジョブの状態で、画面では「録音予定（失敗・再試行待ち）」。人は手動再試行で戻せる（CLI は `retry`） |
| 期限切れ | domain の `StateExpired`（DB と JSON は `expired`）。録音期限までに録音できなかったジョブの状態で、画面では「録音できなかった」 |
| 録音予定 | DB には持たず、読むときに決める。番組表の API とキーワードの API の `state` は `planned`（決め方は `internal/server/guide.go` の冒頭と store の `recordState`。キーワードの側の定数は `KeywordAiringPlanned`） |
| 録らない予定 | 状態は `StateSkipped`（DB と JSON は `skipped`）で、確定の前（`Job.Settled` が false。DB は `jobs.settled` が 0、JSON は `settled` が `false`、番組表の API の `state` は `skip_planned`）。放送回をそうするかは `Show.Hit`（`HitSkip`）。人が決めたものは `Job.ByPerson`（DB は `jobs.by_person`、JSON は `byPerson`。操作は `Store.SkipByPerson`・`Store.RecordByPerson`、API は `POST /api/jobs/{id}/skip`・`record`）。通知の種類は `skip_planned`・`unskipped` |
| 録らなかった | 「録らない予定」と同じ状態で、確定した後（`Job.Settled` が true。DB は `jobs.settled` が 1、JSON は `settled` が `true`）。確定させるのは domain の `Expand`（最初の試行の時刻を過ぎた展開）と `Store.SettleSkipped` |
| 録り直し | 受け付けるかは domain の `domain.CheckRerecord`、操作は `Store.Rerecord`、前の保存先の上書きは library の `StoreOver`。API は `/api/jobs/{id}/rerecord`、JSON は `rerecordable`、CLI は `rerecord` |
| 試行 | `Attempt` |
| 進捗 | app の `app.Progress`（serve のメモリにジョブごとに持ち、読むのは `App.JobProgress`。DB には持たない）。server が読む口は `server.Progresses`（`UI.Recorder` が満たせば使う）。ジョブの API の JSON は `progress`（`stage`・`fetchedSeconds`・`totalSeconds`）。タイムフリーの入手でチャンクを書くたびに呼ぶ口は、radiko の `Client.FetchTimefree` の引数 `onChunk` |
| 録音期限 | `Deadline` |
| 会員種別 | `Membership` |
| 分割番組 | domain の `Show.Split`（型は `SplitMode`。値は `SplitNone`・`SplitJoin`・`SplitSeparate`、JSON のキーは `split` で値は `"none"`・`"join"`・`"separate"`。分割番組かは `Show.IsSplit`）、library の `Input.Split`。DB は `shows.split` と `shows.join_airings` の 2 つの列 |
| 無効な番組 | domain の `Show.Disabled`（JSON は `enabled` が `false`、DB は `shows.enabled` が 0） |
| 連結 | domain の `Show.Joins`（`Show.Split` が `SplitJoin`）。DB は `shows.join_airings` |
| パート | `Part` |
| 欠落 | `Missing` |
| チャプター | radiko から取るのは radiko の `Client.Chapters`（型は `radiko.Chapter`）。ファイルに入れる形は media の `media.Chapter` で、書くのは `Tools.RemuxWithChapters`・`Builtin.RemuxWithChapters`（形は `internal/media/chapter.go`）。保存の試行で取るのは app の `App.fetchChapters`。設定のキーは `chapters`（`config.Config` の `Chapters`）。入ったかの値は domain の `domain.Chapters`（`Job.Chapters`。値は `added`・`waiting`・`not_added`、値なしは空。DB は `jobs.chapters`、ジョブの API（一覧と詳細）の JSON は `chapters`。[ADR 0027](adr/0027-chapters-refetched-by-rerecording.md)） |
| チャプターの取り直し | 保存のときに待つかを決めるのは `domain.ChaptersAfterSave`、API を引いた後に決めるのは `domain.CheckChapters`（間隔は `domain.ChapterCheckInterval`、打ち切りは `domain.ChapterCheckFor` と `Job.ChapterCutoff`）。待っているジョブの API を引くのは app の `App.checkChapters`。DB は `jobs.chapters_check_at`（`Job.ChapterCheckAt`）と `job_airings.chapter_wait`（`Job.ChapterWaits`）、取り直しの試行の印は `jobs.chapters_refetch`（`Job.ChapterRefetch`）。読むのは `Store.DueChapterChecks`、書くのは `Store.UpdateChapterWait`・`Store.StartChapterRefetch`・`Store.RevertChapterRefetch`（[ADR 0027](adr/0027-chapters-refetched-by-rerecording.md)） |
| 設定 | config の `config.Config`（変え方は `config.Patch`。DB は `settings`、API は `/api/settings`、DB の正本と写しの差し替えは app の `app.Settings`） |
| 起動の設定 | config の `config.Boot`（`serve` のフラグ `-data`・`-listen`・`-allowed-hosts`。許すホスト名は `Boot.AllowedHosts`） |
| データのフォルダ | `Boot.DataDir`（`serve` のフラグ `-data`） |
| バックアップ | 作るのは `Store.Backup`（ファイルの名前は store の `store.BackupFileName`。API は `/api/backup`、CLI は `rajikura backup`）。`serve` が自動で取るのは `App.BackupIfDue`（設定は `backup_dir`・`backup_keep`、前に取れた日時は `Store.LastBackupAt`） <!-- 足したら直す: CLI のサブコマンド --> |
| 秘密 | 名前は `config.SecretNames`（`PATCH /api/settings` の本文のキーを `.` でつないだもの）。DB は `secrets`、暗号化は `internal/secret`（鍵のファイルは `rajikura.key`） |
| セットアップコード | server の `server.SetupCode`（作るのは `server.NewSetupCode`。ファイルはデータのフォルダの `setup-code.txt`、API は `/api/setup`） |
| トレイ | `cmd/rajikura-tray`（exe は `rajikura-tray.exe`。接続先とトークンのファイルは `tray.toml` で、読み書きはトレイと `rajikura` の `service install`・`tray install` が共有する `internal/trayconf`。アイコンの状態は `iconState`）。入れる・消すのは `cmd/rajikura/tray_windows.go`（`service` と `tray` が共有する処理）と `cmd/rajikura/tray_command_windows.go`（`tray` のサブコマンド） |
| 連携 | トレイの受け口は `cmd/rajikura-tray/link.go`（`startLink`、`POST /callback`）、Web UI の画面は `web/src/tray/TrayLinkPage.tsx`（URL は `/tray-link`）。発行するトークンの名前は「トレイ（<name>）」 |

## 足したら直す列挙

処理・DB の表・待ち受ける口・データのフォルダに置くもの・通知の種類・サービスの異常・CLI のサブコマンドは、コメントと docs に散文で列挙してあり、用語とコードの名前の対応は「コード上の名前」の表にある。機械では漏れを見つけにくいので、足したりやめたりしたときは同じ PR で次を直す。直す場所には、項ごとの名前を付けた印（`足したら直す: <項の名前>`）を、その場所のコメント（Go・TS は `//`、Dockerfile は `#`、md は行末の `<!-- -->`。表の行では最後のセルの中に置き、コードブロックの中や表の見出しの行のように行末に置けない・表を壊しうる場所では、その直前の行や段落に置く）に置いてある。列挙の項に当たる場所を足したら、その場所に印を付ける。Go・TS では、印をコメントのブロックの最後の行に置くか、`//` だけの行で区切った段落に置き、文の間や箇条の途中に挟まない。md の同じ行に `<!-- glossary-ok: … -->` か `<!-- code-ref-ok: … -->` を置くときは、印をそれらより前に置く。過去の ADR（`docs/adr/`）には印を置かず、直さない。印のほかの言及は、「探す語」で repo 全体を grep して探す。

- 保存の後の処理を足す・順番を変える: 印 `足したら直す: 保存の後の処理` を repo 全体で grep する。
- 用語集（`GLOSSARY.md`）に、コードに型・DB の表・JSON のキー・API を持つ語を足す: この docs の「コード上の名前」の表に行を足す。
- DB の表を足す: 印 `足したら直す: DB の表` を repo 全体で grep する。
- `-listen` で待ち受ける口（`/healthz`・`/metrics`・Web UI・API）を足す・やめる: 印 `足したら直す: 待ち受ける口` を repo 全体で grep する。探す語: `/healthz`。
- データのフォルダに置くファイル・フォルダ（`rajikura.db`・`rajikura.key`・`work`・`setup-code.txt`・`logs` など）を足す・やめる: 印 `足したら直す: データのフォルダ` を repo 全体で grep する（この docs の「リリース」の節の `/data` の行には、コンテナに置かない `logs` などは書かない）。探す語: 置くものの名前と「データのフォルダ」。
- 通知の種類を足す・やめる、または既にある種類を送る時期や作る者を変える: 印 `足したら直す: 通知の種類` を repo 全体で grep する。通知の源の数や作り手を変えるなら、`internal/app/notify.go` の package doc も直す。探す語: 既にある種類の定数（`KindSkipped` など）と日本語の言い方（「録らなかった」など）。
- サービスの異常を足す・やめる、または既にある異常の始まり・復旧を書く時期や作る者を変える（上の項も直す）: 印 `足したら直す: サービスの異常` を repo 全体で grep する。探す語: 既にある異常の定数（`KindOutOfArea` など）と日本語の言い方（「エリア外」など）。
- CLI のサブコマンド（`jobs`・`retry`・`skip`・`record`・`rerecord`・`backup`・`service`・`tray` など）を足す・やめる: 印 `足したら直す: CLI のサブコマンド` を repo 全体で grep する。探す語: 既にあるサブコマンドの名前の並び（`jobs・retry` や `retry`・`skip`）と `rajikura <名前>`。
  - `serve` の API を呼ばないサブコマンド（`version`・`licenses` と、Windows だけの `service`・`tray`）は、`-url`/`-token` の説明・`client_commands.go`・`internal/client/client.go` の列挙には入れない。`service` の操作（`install`・`uninstall`）を足す・やめるとき、`service` とトレイを入れる・消す振る舞いを変えるとき、`tray` の操作（`install`・`uninstall`）を足す・やめるときは、印 `足したら直す: service の操作` を repo 全体で grep する（`tray` の操作を並べる場所にも、この印を置く）。
- 録音の方式（入手の経路、録る時刻、再試行の有無）を足す・変える・やめる: サービスが何をするかの 1 文を、印 `足したら直す: 録音の方式` の場所（repo 全体で grep する）で揃える。探す語: 「録音」「タイムフリー」「ライブ」「再試行」。過去の ADR は直さない。
- ジョブの状態の移り方（どの失敗で retrying・expired にするか、起動時の回復、手動再試行を受け付ける条件、作業用フォルダを残すか消すか）を変える: 印 `足したら直す: ジョブの状態の移り方` を repo 全体で grep する。散文で機械では食い違いが見つからない。探す語: 「retrying」「expired」「再試行」「作業用フォルダ」。過去の ADR は直さない。
- 機能をやめる: やめた機能の名前（型名・関数名・docs の言葉）で repo 全体を grep し、コメントと docs に残った言及を直す。


## リリース

main で `vX.Y.Z` のタグを打つと、`.github/workflows/release.yml` がイメージを `ghcr.io/t-seki/rajikura:vX.Y.Z` に push し、Windows・Linux（x64）のバイナリと `SHA256SUMS` を付けた GitHub Release を作る。タグは lightweight で打つ（`release.yml` はタグ名しか見ない）。細部は `release.yml` の冒頭に書いてある。コンテナで動かす手順は [getting-started.md](getting-started.md#コンテナで動かす)。

配布物（Windows・Linux のバイナリ）とコンテナイメージのバイナリには、含むソフトウェア（Go の依存の module と Web UI の依存）のライセンスの文と NOTICE の一覧を埋め込み、`rajikura licenses` で出す。一覧はビルドのたびに Dockerfile の中で作り（Go の分は `scripts/gen-licenses.sh`、Web UI の分は Vite の `build.license`）、repo には置かないので、依存を更新しても一覧を直す手間は無い。手元の `go build` には一覧が入らない（`rajikura licenses` はそのことを出す）。細部は `release.yml` と Dockerfile の冒頭に書いてある。イメージには OCI のラベル（`org.opencontainers.image.source`・`org.opencontainers.image.licenses`・`org.opencontainers.image.description`）を付ける。

コンテナの置き場所（uid/gid 1000 の root でないユーザーで動く）:

| パス | 用途 |
| --- | --- |
| `/data` | データのフォルダ（DB の `rajikura.db`、秘密の鍵のファイル `rajikura.key`、作業用フォルダ `work`、Web UI のパスワードが決まるまでの間だけセットアップコードのファイル `setup-code.txt`） <!-- 足したら直す: データのフォルダ --> |
| `/library` | ライブラリ（設定の `library` を `/library` にする） |

既定の引数は `-data /data serve`。設定は DB に置く（[settings.md](settings.md)）。
