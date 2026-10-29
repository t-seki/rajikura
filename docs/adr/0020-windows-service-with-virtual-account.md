# Windows では仮想アカウントのサービスとして入れ、127.0.0.1 で待ち受ける

## 決定

Windows で常駐させるときは、`serve` を Windows のサービスとして入れる。ログオンのアカウントは仮想アカウント（`NT SERVICE\rajikura`）にし、待ち受けは `127.0.0.1:<port>` にする。入れる・消すのは `rajikura service install` / `uninstall`（管理者で動かす）で行う。コードの既定（データのフォルダは実行ファイルと同じフォルダ、`-listen` は `:8080`）は変えず、サービスとして入れるときの引数で渡す。

- `service install` は、自分を `%ProgramFiles%\rajikura\` に写し、データのフォルダ `%ProgramData%\rajikura` を作って、ACL をサービスのアカウント・SYSTEM・Administrators だけに絞る。そのうえで、サービスを `-data %ProgramData%\rajikura -listen 127.0.0.1:<port> serve` の引数で登録して起動する。打ち直すと、止めて、差し替えて、起動し直す（更新）。`-allowed-hosts` は渡さない。
- 初めて入れたときは、`service install` がセットアップコード（[ADR 0016](0016-first-password-by-setup-code.md)）と URL を端末に出す。`setup-code.txt` は ACL で管理者にしか読めない。
- `service install` は、トレイの exe を書き出し、打ったユーザーのログイン時に起動するよう登録する（[ADR 0023](0023-windows-tray-embedded-and-linked-by-loopback.md)）。
- `service uninstall` はサービスを消し、データのフォルダは残す。
- `serve` は、サービスとして起動されたかを自分で見分ける。サービスのときは、停止と PC の終了の要求で SIGTERM と同じく止まり、ログはデータのフォルダのファイルに出す。
- 保存先を NAS に置く人は、サービスのログオンのアカウントを自分のアカウントに切り替える（手順は [windows.md](../windows.md)）。rajikura は NAS の資格情報を持たない。

### スリープ

- 録音している間（ジョブの試行の間）は、`serve` が PC のスリープを止める（`internal/power`）。
- タイムフリーから録るジョブのためには、スリープ中の PC を起こさない。
- ライブで録るジョブ（[ADR 0022](0022-live-recording-for-timefree-excluded-airings.md)）のためには、`serve` のプロセスの中で `SetWaitableTimer` を掛け、最初の試行の 3 分前に PC を起こし、最初の試行の 2 分後までスリープしないよう伝える（[windows.md の「ライブ録音の前に起こす」](../windows.md#ライブ録音の前に起こす)）。

## 理由

- 市販のラジオ録音ソフトから乗り換える Windows の人（[ADR 0015](0015-settings-and-secrets-in-db.md)）には、Windows Update の再起動の後に誰もログインしなくても録り続けることが要る。ログオン時に起動する形では、ログイン画面のまま録れない。
- 仮想アカウントは、Windows が自動で作るサービス専用のアカウントで、パスワードを持たず、権限が小さい。ACL で名指しできるので、鍵のファイルや DB を、ほかのユーザーから隠せる。
- LAN に開くのは、人が選んだときだけにする。Web UI の守り（[ADR 0021](0021-web-ui-origin-host-and-login-limit.md)）があっても、開く範囲は狭い方がよい。
- タイムフリーの録音は放送の後に録るので、スリープから戻れば録音期限の内で追いつける。ライブ録音は放送中に録るので、放送の時刻にスリープしていると録れない。
- 起こす仕組みは `serve` のプロセスの中に閉じているので、後で仕組みを替えても戻しにくくない。

## 検討した案

- ログオン時に起動する（タスクスケジューラかスタートアップ）。ユーザーのフォルダやドライブがそのまま見えるが、ログインするまで動かない。
- LocalSystem で動かす。作りは簡単だが、権限が大きすぎる。NAS に書けないのは仮想アカウントと同じ。
- 初めから人のアカウントで動かす（`service install` がパスワードを聞く）。パスワードを変えると、サービスが起動しなくなる。
- rajikura が NAS の資格情報を持ち、自分で接続する。Windows だけのコードとつなぎ直しが要る。需要が見えたら作る。
- MSI か winget で入れる。要るときに別に作る。
- すべての OS で、既定の `-listen` を `127.0.0.1` にするか、別の番号にする。コンテナの構成と docs を広く直すわりに、ぶつかるかどうかは PC 次第で、避けきれない。
- タイムフリーから録るジョブのためにも PC を起こす。録音期限の内で追いつけるので要らない。

## 帰結

- LAN のスマホから Web UI を開くには、`-listen` を変えて `service install` を打ち直し、ファイアウォールの規則を足す必要がある。LAN からは IP のアドレスで開く。
- 保存先のフォルダには、`NT SERVICE\rajikura` の書き込みを許す手間がかかる（書けなければ、Web UI で保存先を決めるときに誤りになる）。
- コード署名はしていないので、SmartScreen に止められうる。
- サービスは session 0 で動くので、PC の画面に状態や通知を出せない。そのためにトレイを持つ（[ADR 0023](0023-windows-tray-embedded-and-linked-by-loopback.md)）。
- 起こすには、電源オプションの「スリープ解除タイマーの許可」が要る。Modern Standby の PC で起きるかは確かめていない。
