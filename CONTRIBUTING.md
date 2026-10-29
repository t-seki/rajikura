# 不具合の報告・要望・質問の出し方

rajikura は個人の私的利用に限る道具で、立場は [ADR 0028](docs/adr/0028-publish-as-oss-under-radiko-terms.md) にある。

## 受け口

- 不具合: [Issues](https://github.com/t-seki/rajikura/issues) に、テンプレートに沿って書く。
- 要望と質問: [Discussions](https://github.com/t-seki/rajikura/discussions) に書く。
- Pull Request は受けていない。
- 他人が立てた issue は、作者が読んで仕分ける。直すと決めたら、作者が自分の issue として立て直し、元の issue を指す。

## 秘密を貼らない

Webhook の URL、パスワード、API トークン、セットアップコードは貼らない。ログは、秘密を伏せてから貼る。

## 書いてほしいもの

- rajikura の版（`rajikura version`、または Web UI のナビの下の表示）
- OS と入れ方（Windows のサービス、コンテナなど）
- 何をして、何が起きたか
- ログ（秘密を伏せたもの）。場所は [困ったとき](docs/getting-started.md#困ったとき)

## 受けない要望

- エリアの偽装
- らじる★らじるの暗号を解くこと
- 同時ログインの制限を避けること

## 開発の約束事

Pull Request は受けていないが、コードを読むときや自分の fork で試すときの約束事は [開発ガイド](docs/development.md) にある。

## 対応の時期

対応の時期は約束しない。radiko の仕様が変わると録れなくなることがあり、直す時期も約束しない。
