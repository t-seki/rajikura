// rajikura-tray は Windows のトレイ（用語は GLOSSARY.md の「トレイ」）。PC にログインした人ごとに動き、接続先の rajikura
// （その PC の Windows のサービスか、k3s などで動くリモートの serve）の状態と通知を、通知領域のアイコンとトーストで出す
// （docs/adr/0023-windows-tray-embedded-and-linked-by-loopback.md）。
//
// 入れるのは rajikura の service install（その PC のサービスと一緒に）か tray install（トレイだけ）。接続先は -url、
// tray.toml の url、既定の http://127.0.0.1:8080 の順に決める（config.go）。
//
// `-H windowsgui` でビルドする Windows だけの exe で、ほかの OS では起動しても何もせずに止まる（main_other.go）。
// OS に依らない部分（設定のファイル、API の読み、状態の判定、連携の受け口、アイコンの絵、ログのファイル）は、
// どの OS でもテストできるよう build tag を付けずに置く。
//
// することは次のとおり。docs/windows.md の「トレイ」の節も同じ PR で直す。
//   - 起動の最初に名前付き mutex を取る。取れなければ（2 つ目）何も始めずにすぐ終わる（single_windows.go）
//   - 1 分ごとに GET /api/notifications・GET /api/notifications/status・GET /api/jobs?state=running を読む（api.go）
//   - 前に見た id より新しい通知をトーストに出す。押すと、通知の link（ジョブの詳細・番組の編集・キーワードの画面）があれば
//     その画面（URL に notification=<通知の id> のクエリを足し、開いた Web UI がその 1 件を既読にする）、無ければ通知の画面を開く。起動して最初に読めたときに溜まっていた未読は「未読 N 件」の 1 枚にまとめる
//     （押すと通知の画面）。トレイ自身は既読にしない（monitor.go）
//   - 接続先に 3 回続けて届かなければトーストを 1 回出し、戻ったら「つながった」を出す。文言には接続先のホストを出す。起動して最初の 3 分は出さない（monitor.go）
//   - アイコンの状態は、待機中・録音中・異常あり・止まっている・未設定の 5 つ（monitor.go・icon.go）。
//     未設定は、tray.toml が無い、tray.toml を読めない（TOML の誤り、知らないキー）、トークンが無い、
//     接続先（-url か tray.toml の url）が http(s) の URL として読めない、401 を受けたとき
//   - 連携: 127.0.0.1 の受け口で Web UI からトークンを受け取り、tray.toml に書く（link.go・config.go）
//   - メニュー: Web UI を開く、通知を開く、テスト通知を送る（POST /api/notifications/test を呼び、200 ならすぐ 1 回ポーリングする。送れなければ「rajikura: テスト通知を送れない」のトースト。api.go・main_windows.go）、Web UI と連携する、ログイン時に起動する、終了（main_windows.go）
//   - トーストの AUMID を起動のたびに HKCU に書き直す（toast_windows.go）
//   - ログは %LocalAppData%\rajikura\tray.log に出す（logfile.go）
package main
