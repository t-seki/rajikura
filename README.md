<img src="web/public/favicon.svg" alt="rajikura のアイコン" width="96">

# rajikura（ラジクラ）

radiko の放送を録音し、Jellyfin などのライブラリへ自動で保存するサービス。放送済みの放送回はタイムフリーから録り、タイムフリーの対象外の放送回は放送中にライブで録る。毎週の予約に一致する放送回を、タイムフリーからは放送後に録れるまで再試行して録音し、長さを検証してから保存する。 <!-- 足したら直す: 録音の方式 -->

スマホで聴くなら、Android アプリの [kikidame](https://github.com/t-seki/kikidame) が rajikura の保存の形を読める（[kikidame で聴く](docs/kikidame.md)）。

## 利用範囲

- 個人の私的利用に限る。録音した音声データを共有・再配布しない。
- radiko・NHK とは関係のない、非公式の道具である。
- 使う人は radiko の利用規約を自分で読み、自分の責任で使う。
- エリアを偽装しない。エリアの外の局は録れない。
- エリアフリー会員の正規のログインでエリアの外の局を取る道は、いまは動かない（`membership` が `free` 以外だと「未対応」として断る。[設定](docs/settings.md#キー)）。
- radiko の仕様が変わると録れなくなることがあり、直す時期は約束しない。

公開の立場と理由は [ADR 0028](docs/adr/0028-publish-as-oss-under-radiko-terms.md)。ライセンスは MIT（[LICENSE](LICENSE)）。

不具合は [Issues](https://github.com/t-seki/rajikura/issues)、要望と質問は [Discussions](https://github.com/t-seki/rajikura/discussions) で受ける。Pull Request は受けていない。書き方は [CONTRIBUTING.md](CONTRIBUTING.md)。

## 動かせる条件

- 日本の、自分の radiko のエリアの回線から使う。
- radiko の会員種別は、非会員（`membership` が `free`）だけ。
- `serve` は HTTP だけで待ち受け、LAN や VPN の内側で使う前提である。インターネットに出さない。ポートを開けたり、リバースプロキシで公開したりしない。

## 配布物

- コンテナのイメージ: `ghcr.io/t-seki/rajikura:vX.Y.Z`（x64）。
- バイナリ: GitHub Release の Windows・Linux（x64）。`serve` と手元で使う CLI は同じバイナリ。

入れ方は [はじめに](docs/getting-started.md)。Windows の PC に常駐させる入れ方は [Windows のサービスとして入れる](docs/windows.md)。

## ドキュメント

### 使う人向け

- [はじめに](docs/getting-started.md): 入れ方と、初めて動かすときの手順
- [Windows のサービスとして入れる](docs/windows.md)
- [コマンド](docs/cli.md)
- [設定](docs/settings.md)
- [通知](docs/notifications.md)
- [Web UI](docs/web-ui.md)
- [番組とキーワード](docs/shows.md)
- [録音の流れ](docs/recording.md)
- [バックアップと戻し方](docs/backup.md)
- [kikidame で聴く](docs/kikidame.md)

### API を呼ぶ人向け

- [API](docs/api.md)

### コードを読む人・fork する人向け

- [開発ガイド](docs/development.md)
- [要件定義](docs/requirements.md)
- [用語集](GLOSSARY.md)
- [設計の決定（ADR）](docs/adr/)
