# kikidame で聴く

rajikura で録った録音を、Android アプリの [kikidame](https://github.com/t-seki/kikidame) で聴く組み方をまとめる。kikidame は、Jellyfin・NAS の共有フォルダ（SMB）・端末のフォルダにある番組型の音声を聴くアプリ。rajikura の Jellyfin プリセット（[ADR 0002](adr/0002-jellyfin-preset-layout.md)）で保存したファイルは、下の対応表のとおり、kikidame が読む形とほぼ同じ。用語は [GLOSSARY.md](../GLOSSARY.md) に従う。

## kikidame が読むものと、rajikura が書くもの

左の列は [kikidame の README](https://github.com/t-seki/kikidame) の記述、右の列は rajikura の保存の形（[shows.md の「保存の形」](shows.md#保存の形)）。

| kikidame が読むもの | rajikura が書くもの |
| --- | --- |
| `<配信元>/<番組>/<ファイル>` の 3 段 | `{局}/{番組}/{番組} YYYY-MM-DD.m4a` |
| `albumartist` = 配信元 | `album_artist` = 局 |
| `album` = 番組 | `album` = 番組の名前 |
| 日付（`©day`）= 公開日 | `date` = 放送日 | <!-- glossary-ok: 日付 -->
| `artist` = 出演者 | `artist` = 出演者 |
| 番組のフォルダの `folder.jpg` | 番組のフォルダの `folder.jpg` |

- 配信元は局の名前で、番組の中の放送回は放送日の順に並ぶ。0:00〜4:59 に始まる放送回も、番組表の表記の放送日にそろう。
- 各放送回に出演者と長さが出る。
- 自動のバックアップを取る先（`backup_dir`）を保存先の中に置くなら、フォルダの名前を `.backup` にすると、SMB で読むときに番組として出ない。

## Jellyfin を通す

rajikura が保存先に録音を保存し、Jellyfin がそのフォルダをライブラリとして読み、kikidame が Jellyfin につなぐ。

1. rajikura の設定で、Jellyfin の連携を入れる（[settings.md の「ライブラリの再スキャン」](settings.md#ライブラリの再スキャンjellyfin)）。rajikura は、保存のたびに Jellyfin にライブラリの再スキャンを頼む。Jellyfin の URL は、Jellyfin に直接届く形（ポート番号を含めて）で書く。
2. kikidame に、Jellyfin のサーバーとライブラリを登録する。kikidame の前提（Jellyfin の版、音楽ライブラリなど）は [kikidame の README](https://github.com/t-seki/kikidame) を見る。

## 共有フォルダを SMB で読む

Jellyfin が無い人向けの形。保存先のフォルダを共有し、kikidame の SMB で読む。

### Windows の共有を読む

1. [Windows のサービスとして入れる](windows.md)のとおり rajikura を入れ、保存先のフォルダを決める。
2. 保存先のフォルダを Windows の「詳細な共有」で共有する。フォルダのプロパティの「共有」タブで「詳細な共有」を開き、「このフォルダーを共有する」を入れ、共有名を決める。「アクセス許可」には、kikidame でつなぐ Windows のアカウントの読み取りだけを付ける。画面の言葉は Windows の版で違うことがある。
3. kikidame の SMB に、PC の LAN の IP アドレス、共有名、2 のアカウントの Windows のアカウント名とパスワードを入れる。

共有の下の番組のフォルダが、番組として並ぶ。

共有には LAN からつなぐ。信頼できる LAN だけで共有する。
