# はじめに

rajikura の入れ方と、初めて動かすときの手順をまとめる。用語は [GLOSSARY.md](../GLOSSARY.md) に従う。コマンドは [cli.md](cli.md)、設定は [settings.md](settings.md)、Web UI は [web-ui.md](web-ui.md)、API は [api.md](api.md)。

## 入れ方を選ぶ

- Windows の PC に常駐させる: `serve` を Windows のサービスとして入れる（[windows.md](windows.md)）。
- Linux・Windows でバイナリを動かす: GitHub Release のバイナリを入れ（[手元に CLI を入れる](#手元に-cli-を入れる)）、`rajikura serve` を起動する（[初めて入れるとき](#初めて入れるとき)）。
- コンテナで動かす: `ghcr.io/t-seki/rajikura` のイメージを使う（[コンテナで動かす](#コンテナで動かす)）。

## コンテナで動かす

イメージは `ghcr.io/t-seki/rajikura:vX.Y.Z`（x64）。`vX.Y.Z` は [GitHub Release](https://github.com/t-seki/rajikura/releases) の版で、`latest` のタグは無い。

- コンテナは uid/gid 1000 の、root でないユーザーで動く。
- データのフォルダは `/data`、ライブラリは `/library`（どちらも `VOLUME`）。
- 既定の引数は `-data /data serve` で、`-listen` の既定は `:8080`。
- `serve` は HTTP だけで待ち受ける。LAN や VPN の内側で使い、インターネットに出さない。
- イメージに ffmpeg は入っていない（[m4a への格納](settings.md#m4a-への格納muxer)の既定の `builtin` は ffmpeg を使わない）。

`docker run` の最小の例（`vX.Y.Z` は版に替える）。

```bash
mkdir -p rajikura-data rajikura-library
sudo chown 1000:1000 rajikura-data rajikura-library      # コンテナの uid/gid 1000 が書けるようにする
docker run -d --name rajikura -p 8080:8080 \
  -v "$PWD/rajikura-data:/data" -v "$PWD/rajikura-library:/library" \
  ghcr.io/t-seki/rajikura:vX.Y.Z
docker logs rajikura 2>&1 | grep setup_code      # セットアップコード
```

同じマシンのブラウザだけから開くなら、公開するアドレスを `127.0.0.1` に絞る（`-p 127.0.0.1:8080:8080`）。上の例の `-p 8080:8080` は、マシンの全部のインターフェイスで公開する。

この後は、[初めて入れるとき](#初めて入れるとき)の手順 2 から続ける。手順 3 の保存先には `/library` を入れる。

- Web UI を名前（`rajikura.example.com` など）で開くなら、その名前を `-allowed-hosts` に渡す（[起動の設定とデータのフォルダ](cli.md#起動の設定とデータのフォルダ)）。名前が無いと 421 で断られる。
  - イメージ名の後に引数を書くと既定の引数が置き換わるので、`-data /data` と `serve` も書く（例: `ghcr.io/t-seki/rajikura:vX.Y.Z -data /data -allowed-hosts rajikura.example.com serve`）。
- バックアップを自動で取るなら、取る先のフォルダもマウントして、設定の `backup_dir` にする（[自動のバックアップ](backup.md#自動のバックアップ)）。

## 手元に CLI を入れる

手元の PC やほかのマシンから使う CLI は、GitHub Release の配布物（Windows・Linux の x64。素のバイナリと `SHA256SUMS`）を落として入れる。CLI と `serve` は同じバイナリ。下の例は `gh` で落とす。`<タグ>` は [Release のページ](https://github.com/t-seki/rajikura/releases)にある版（`vX.Y.Z` の形）に替える。`gh` を使わないときは、Release のページから、使う OS の `rajikura_*` のファイルと `SHA256SUMS` をブラウザで落とし、同じように確かめて置く。

```bash
gh release download <タグ> -R t-seki/rajikura -p 'rajikura_*_linux_amd64' -p SHA256SUMS      # Windows は 'rajikura_*_windows_amd64.exe'
sha256sum --ignore-missing -c SHA256SUMS      # 落としたものだけを確かめる
chmod +x rajikura_*_linux_amd64      # Linux のみ
mv rajikura_*_linux_amd64 ~/.local/bin/rajikura      # PATH の通った場所へ。名前は rajikura にする
rajikura version
```

Windows では `rajikura_<タグ>_windows_amd64.exe` を `rajikura.exe` に改名して PATH の通った場所へ置く（`chmod` は要らない）。macOS と arm64 の配布物は無い。

## 初めて入れるとき

初めて入れるときは、`serve` を起動し、Web UI だけでパスワード・保存先・番組を決める。設定ファイルも環境変数も CLI も要らない。保存先が決まるまでは録音だけが待たされ、番組表の同期と展開は動く。Windows の PC に常駐させるときは、手順 1 の代わりに `service install` でサービスとして入れる（トレイも入る。[windows.md](windows.md)）。

1. `serve` を起動する。データのフォルダ（ここでは `./data`）は無ければ作られ、DB と作業用フォルダがそこにできる。パスワードがまだ無いので、`serve` はセットアップコードを作り、データのフォルダの `setup-code.txt` とログ（`setup_code=` の値）に出す（[web-ui.md の「初めて開いたとき」](web-ui.md#初めて開いたとき)）。 <!-- 足したら直す: データのフォルダ -->

   ```bash
   rajikura -data ./data serve      # Ctrl+C で止める
   ```

2. ブラウザで `serve` の URL（例 `http://localhost:8080/`）を開き、セットアップコードと、新しいパスワード（2 回）を入れて「パスワードを決める」を押す。決めるとログインした状態になり、`setup-code.txt` は消える。
3. ナビの「設定」の「保存先」に、録音を保存するフォルダ（絶対パス）を入れて「保存」を押す。`serve` がフォルダを作り、書けることを確かめてから保存する。画面の上の帯「保存先を決めて」が消える。同じ画面の「バックアップ」で、自動のバックアップを取る先（`backup_dir`）も決めておく（[自動のバックアップ](backup.md#自動のバックアップ)）。
4. ナビの「番組表」で録音したい放送回を開き、「毎週録音」か「この回を録音」を押して予約する（ナビの「番組」の画面でも作れる。出演者などの語で録音するなら、ナビの「キーワード」で作る）。 <!-- glossary-ok: 回 -->
5. ナビの「ジョブ」で、番組から作られたジョブを見る。

CLI（`jobs`・`retry`・`skip`・`record`・`rerecord`・`backup`）や AI から使うときは、ナビの「API トークン」で名前（例:「手元の CLI」）を付けてトークンを発行し（トークンは発行した直後に 1 回だけ出るので、写しておく）、[接続先と API トークン](cli.md#接続先と-api-トークン)のとおりに渡す。 <!-- 足したら直す: CLI のサブコマンド -->

## 更新する

新しい版は [Release のページ](https://github.com/t-seki/rajikura/releases)で見つける。更新の前に、バックアップを取っておく（[バックアップと戻し方](backup.md)）。データのフォルダの DB は、起動したときに新しい版へ自動で移る。

- コンテナ: `docker stop rajikura && docker rm rajikura` で止めて消し、イメージの版（`vX.Y.Z`）を新しいものに替えて、同じ `-v` で `docker run` し直す。データのフォルダとライブラリはマウントしてあるので残る。
- Linux・Windows のバイナリ: `serve` を止め、[手元に CLI を入れる](#手元に-cli-を入れる)と同じ手順で新しい版を落として置き換え、`serve` を起動し直す。`rajikura version` で版を確かめる。
- Windows のサービス: [windows.md の「更新する」](windows.md#更新する)に従う。

## 困ったとき

- 版は、`rajikura version` か、Web UI のナビの下に出る。
- ログは、コンソールで動かす `serve` では標準エラーに出る。コンテナでは `docker logs rajikura`。Windows のサービスでは、データのフォルダの `logs\rajikura.log`（[windows.md](windows.md)）。ログを人に見せるときは、Webhook の URL、パスワード、API トークン、セットアップコードを伏せる。 <!-- 足したら直す: データのフォルダ -->
- Web UI のパスワードを忘れた: `serve -reset-password` で起動し、セットアップコードから決め直す。API トークンも発行し直しになる（[cli.md の「serve」](cli.md#serve)）。
- Web UI を名前で開くと 421 で断られる: `-allowed-hosts` にその名前を渡す（[起動の設定とデータのフォルダ](cli.md#起動の設定とデータのフォルダ)）。
- 録れない・録り終えなかった: ナビの「ジョブ」で、ジョブの状態と詳細を見る。
- 上で足りないとき: [Issues](https://github.com/t-seki/rajikura/issues) に、[CONTRIBUTING.md](../CONTRIBUTING.md) のとおり書く。
