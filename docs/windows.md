# Windows のサービスとして入れる

Windows の PC で rajikura を常駐させる手順をまとめる。`serve` を Windows のサービスとして入れ、誰もログインしていなくても、再起動の後も動くようにする。あわせて、ログインした人に状態と通知を見せる[トレイ](#トレイ)も入れる。`serve` を Linux のサーバーやコンテナで動かしているときは、手元の Windows にトレイだけを入れられる（[トレイだけを入れる](#トレイだけを入れる)）。決定は [ADR 0020](adr/0020-windows-service-with-virtual-account.md)、コマンドは [cli.md の「service・tray」](cli.md#servicetray) にある。用語は [GLOSSARY.md](../GLOSSARY.md) に従う。 <!-- 足したら直す: service の操作 -->

日本語以外の Windows では、動かして確かめていない。

- [入れる](#入れる)
- [更新する](#更新する)
- [消す](#消す)
- [保存先のフォルダ](#保存先のフォルダ)
- [NAS に保存する](#nas-に保存する)
- [保存先のフォルダを共有して kikidame で聴く](#保存先のフォルダを共有して-kikidame-で聴く)
- [LAN のスマホから開く](#lan-のスマホから開く)
- [スリープ](#スリープ)
- [ログ](#ログ)
- [トレイ](#トレイ)
  - [トレイだけを入れる](#トレイだけを入れる)

## 入れる

1. [GitHub Release](https://github.com/t-seki/rajikura/releases) の `rajikura_<タグ>_windows_amd64.exe` を落とし、`rajikura.exe` に改名する（落とし方と `SHA256SUMS` での確かめ方は、getting-started.md の「[手元に CLI を入れる](getting-started.md#手元に-cli-を入れる)」）。置く場所はどこでもよい（`service install` が `%ProgramFiles%\rajikura` に写す）。
2. スタートメニューの「ターミナル」を右クリックし、「管理者として実行」で開く。
3. `rajikura.exe` を置いたフォルダで、`service install` を打つ。

   ```powershell
   .\rajikura.exe service install
   ```

   - 署名していない実行ファイルなので、SmartScreen の「Windows によって PC が保護されました」が出ることがある。そのときは「詳細情報」を押し、「実行」を押す。
   - `127.0.0.1:8080` がほかのアプリに使われていると、「`127.0.0.1:8080` で待ち受けられない」と出して、サービスを登録する前に止まる。別のポートで入れるときは `-listen` を付ける（例 `.\rajikura.exe service install -listen 127.0.0.1:18080`）。
4. `service install` は、サービスを起動して Web UI が応えるまで待ち、セットアップコードと URL を出す（ブラウザは開かない）。

   ```text
   ブラウザで http://127.0.0.1:8080/ を開き、セットアップコード XXXX-XXXX を入れて Web UI のパスワードを決める
   ```

   ブラウザで URL を開き、getting-started.md の「[初めて入れるとき](getting-started.md#初めて入れるとき)」の手順 2 から続ける（パスワード → 保存先 → 番組）。

   その前に、トレイの接続先を `tray.toml` に書いたことと、トレイを入れてログイン時に起動するよう登録したことを出す。

   ```text
   トレイの接続先 http://127.0.0.1:8080 を C:\Users\<ユーザー>\AppData\Roaming\rajikura\tray.toml に書いた。トレイのメニューの「Web UI と連携する」で連携する
   トレイを C:\Program Files\rajikura\rajikura-tray.exe に入れ、このユーザーのログイン時に起動するよう登録した（HKCU\Software\Microsoft\Windows\CurrentVersion\Run の rajikura-tray: "C:\Program Files\rajikura\rajikura-tray.exe"）
   …
   トレイを起動した（通知領域に出る）
   ```

5. トレイは、`service install` の最後に起動し、通知領域に出る（管理者の権限では動かさない）。Web UI のパスワードを決めた後、トレイのメニューの「Web UI と連携する」で連携する（[接続先と連携](#接続先と連携)）。
   - 起動できなかったとき（SSH から打ったなど）も、`service install` は失敗にしない。端末に「トレイを起動できなかった（<理由>）。エクスプローラーで C:\Program Files\rajikura\rajikura-tray.exe をダブルクリックして起動する」と出すので、そのとおりエクスプローラーから起動する（管理者のターミナルからは起動しない）。

### 入るもの

- サービスの名前は `rajikura`。ログオンのアカウントは仮想アカウント `NT SERVICE\rajikura`（Windows がサービスごとに作る、パスワードの無いアカウント）。自動で起動する。
- サービスは `-data %ProgramData%\rajikura -listen 127.0.0.1:8080 serve` で起動する。Web UI は同じ PC のブラウザから `http://127.0.0.1:8080/` で開く。
- 実行ファイルは `%ProgramFiles%\rajikura\rajikura.exe`、データのフォルダは `%ProgramData%\rajikura`（ふつうは `C:\Program Files\rajikura` と `C:\ProgramData\rajikura`）。
- データのフォルダの中身は [cli.md の「起動の設定とデータのフォルダ」](cli.md#起動の設定とデータのフォルダ)のとおり。フォルダの ACL は継承を切り、`NT SERVICE\rajikura`（変更）・SYSTEM・Administrators（フルコントロール）だけにする。ほかのユーザーは鍵のファイルも DB も読めない。 <!-- 足したら直す: データのフォルダ -->
- [トレイ](#トレイ)は `%ProgramFiles%\rajikura\rajikura-tray.exe` に入る。`service install` を打ったユーザーのログイン時に起動するよう、HKCU の `Run` に値 `rajikura-tray` として登録する。
- トレイの接続先は、打ったユーザーの `tray.toml`（`%AppData%\rajikura\tray.toml`）に書く。ホストは、`-listen` が `127.0.0.1`・空（`:8080`）・`0.0.0.0` なら `127.0.0.1`、特定の IP なら、その IP。ポートは `-listen` のもの。

### ほかのユーザーのトレイ

`service install` が登録し、`tray.toml` を書くのは、打ったユーザーの分だけ。

- 同じ PC のほかのユーザーは、自分でログインした中で `C:\Program Files\rajikura\rajikura-tray.exe` をエクスプローラーから一度起動し、トレイのメニューの「ログイン時に起動する」で自分の分を登録する（[メニュー](#メニュー)）。
- そのユーザーの `tray.toml` は無いので、トレイは既定の `http://127.0.0.1:8080` に向く。`-listen` を既定から変えたときは、`-url` を付けて起動してから（例 `"C:\Program Files\rajikura\rajikura-tray.exe" -url http://127.0.0.1:18080`）連携する。

標準ユーザーが、別の管理者のパスワードで昇格したターミナルで `service install` を打ったときは、次のとおりになる。

- 登録と `tray.toml` は、その管理者の分に入り、標準ユーザーには入らない。
- トレイは、画面の前の標準ユーザーのトレイとして起動する。端末には、そのユーザーにはログイン時の起動を登録していないことと、`tray.toml` も書いていないことを出す。
- 標準ユーザーは、トレイのメニューの「ログイン時に起動する」で登録する。
- `-listen` を既定から変えたときは、起動したトレイをメニューの「終了」で終えてから、標準ユーザーで `"C:\Program Files\rajikura\rajikura-tray.exe" -url <URL>` を起動し、「Web UI と連携する」で連携する。

## 更新する

新しい版の `rajikura.exe` を落とし、管理者のターミナルで `service install` を打ち直す。

```powershell
.\rajikura.exe service install
```

- サービスを止め、`%ProgramFiles%\rajikura\rajikura.exe` を差し替え、起動し直す。データのフォルダ（DB・鍵のファイル・パスワード）はそのまま残る。セットアップコードは出さず、Web UI の URL だけを出す。
- `-listen` を変えて入れていたときは、打ち直すときにも同じ `-listen` を付ける（付けないと既定の `127.0.0.1:8080` に戻る）。
- 止めた後に失敗したとき（待ち受けられない、実行ファイルを写せないなど）は、止めたサービスを起動し直してから理由を出して止まる。どの実行ファイルで起動し直したかは端末に出す。
- 切り替えたログオンのアカウント（[NAS に保存する](#nas-に保存する)）はそのまま残る。

トレイも、新しい版に差し替え、打ったユーザーの `tray.toml` と `Run` の登録を書き直し、最後に新しい版を起動する。

- 差し替える前に、同じセッションで動いている前のトレイに終了を頼み、終わるのを最大 10 秒待つ。終わったら「動いていた前のトレイを終了した」と出す。
- 10 秒待っても終わらなかったときは、新しい版の exe は置くが、新しいトレイは起動しない。端末の案内のとおり、前のトレイをメニューの「終了」で終えてから、エクスプローラーで `C:\Program Files\rajikura\rajikura-tray.exe` を起動する。
- 動いている exe は上書きできないので、前の版を `rajikura-tray.exe.<数字>.old` によけてから新しい版を置く。よけたファイルは、次の `service install`・`service uninstall` で消える（PC の次の再起動のときに消すようにも Windows に頼む）。
- ほかのユーザーのセッションで動くトレイは前の版のまま動き続け、終了して起動し直すか、ログインし直すと新しい版になる。
- `-listen` を変えて `tray.toml` の `url` が変わったときは、前に連携したトークンを消すので、「Web UI と連携する」で連携し直す。

```text
すでに入っているので更新する。サービスを止める
動いていた前のトレイを終了した
トレイを C:\Program Files\rajikura\rajikura-tray.exe に入れ、このユーザーのログイン時に起動するよう登録した（…）
サービス rajikura を入れて起動した（実行ファイル C:\Program Files\rajikura\rajikura.exe、データのフォルダ C:\ProgramData\rajikura、ログ C:\ProgramData\rajikura\logs\rajikura.log）
Web UI: http://127.0.0.1:8080/
トレイを起動した（通知領域に出る）
```

## 消す

管理者のターミナルで、落とした `rajikura.exe`（`%ProgramFiles%\rajikura` の外のもの）から `service uninstall` を打つ。

```powershell
.\rajikura.exe service uninstall
```

- サービスを止めて消し、`%ProgramFiles%\rajikura` の `rajikura.exe` とトレイの `rajikura-tray.exe` を消す。
- 消す前に、同じセッションで動いているトレイに終了を頼み、終わるのを最大 10 秒待つ。終わらなければ、exe をよけて PC の次の再起動のときに消すよう頼み、トレイのメニューの「終了」で終えるよう端末に出す。
- 打ったユーザーの `Run` のトレイの登録は、`%ProgramFiles%\rajikura\rajikura-tray.exe` を指すときだけ消す。ほかの exe を指していれば（後に [tray install](#トレイだけを入れる) を打ったなど）消さず、そう出す。
- データのフォルダ `%ProgramData%\rajikura` は残し、その場所を出す。要らなければ手で消す。
- 打ったユーザーの `tray.toml` も残す。要らなければ手で消し、連携したトークンは Web UI の「API トークン」の画面で失効させる。
- ほかのユーザーの `Run` の登録には届かない。ほかのユーザーは、消す前にトレイのメニューの「ログイン時に起動する」を外しておく。
- `%ProgramFiles%\rajikura\rajikura.exe` で打ったときは、動いている実行ファイル自身は消せないので、そのことを出して残す（手で消してよい）。

## 保存先のフォルダ

サービスは `NT SERVICE\rajikura` で動くので、保存先（設定の `library`）とバックアップを取る先（`backup_dir`）のフォルダには、`NT SERVICE\rajikura` の書き込みを許す必要がある。許していないフォルダを Web UI の設定で選ぶと、`serve` が書けないことを確かめて誤りにする（設定は保存されない）。

管理者のターミナルで、フォルダを作ってから `icacls` で許す（例は `D:\Radio`）。`icacls` は、サービスを入れた後でないと `NT SERVICE\rajikura` を名指しできない。

```powershell
New-Item -ItemType Directory -Force D:\Radio
icacls D:\Radio /grant 'NT SERVICE\rajikura:(OI)(CI)M'
```

- `C:\` の直下に作ったフォルダのように、`Authenticated Users` に変更の許可が付いているフォルダは、`icacls` を流さなくても保存先にできることがある。設定を保存できなければ、上のとおり許す。
- ユーザーのフォルダ（`C:\Users\<名前>\Music` など）を保存先にするときも、同じく書き込みを許す。

設定の画面の「選ぶ…」のダイアログに出るのは、サービスのアカウントから見えるフォルダ。

- 割り当てたドライブ（`Z:` など）は出ない（ドライブの割り当てはログインしたユーザーごとのもの）。
- NAS の共有（UNC のパス）は、仮想アカウントのサービスからは開けないことがある。そのときは「`フォルダを開けない: … Access is denied.`」の形の理由が出る。NAS に保存するときは、下の「[NAS に保存する](#nas-に保存する)」。

## NAS に保存する

仮想アカウントは、ネットワークの上ではコンピューターのアカウントとして扱われるので、ふつうの NAS の共有には書けない。NAS に保存するときは、サービスのログオンのアカウントを自分のアカウントに切り替える。rajikura は NAS の資格情報を持たない。

1. 自分のアカウントで NAS の共有に書けることを確かめておく。保存先は、ドライブ文字（`Z:` など）ではなく UNC のパス（`\\nas\radio` など）で書く（割り当てたドライブはサービスからは見えない）。
2. データのフォルダの ACL に、自分のアカウントを足す（サービスが自分のアカウントで動いても、DB と鍵のファイルを読み書きできるように）。管理者のターミナルで打つ。

   ```powershell
   icacls "$env:ProgramData\rajikura" /grant "${env:USERDOMAIN}\${env:USERNAME}:(OI)(CI)M"
   ```

3. `services.msc` を開き、`rajikura` のプロパティの「ログオン」で「アカウント」を選んで自分のアカウントとパスワードを入れ、サービスを再起動する。
4. Web UI の設定で、保存先に UNC のパスを入れる。設定のダイアログでも、上の欄に UNC のパスを打って「移動」すれば、共有の中をたどれる（割り当てたドライブは、切り替えた後も出ない）。
5. 自分のパスワードを変えたら、3 のパスワードも入れ直す（入れ直さないと、サービスが起動しなくなる）。

`service install` を打ち直して更新しても、切り替えたログオンのアカウントはそのまま残る。

### ログオンを仮想アカウントに戻す

自分のアカウントに切り替えたログオンを仮想アカウントに戻すには、`service uninstall` で消して `service install` で入れ直す。

```powershell
.\rajikura.exe service uninstall
.\rajikura.exe service install
```

- データのフォルダ（DB など）は残るので、番組と設定はそのまま使える。セットアップコードは出ない。
- 保存先が NAS の UNC のパスのままだと、仮想アカウントは書けず、保存が失敗する。戻した後は、保存先を仮想アカウントが書けるフォルダに戻して保存する。

## 保存先のフォルダを共有して kikidame で聴く

Jellyfin が無くても、保存先のフォルダを Windows で共有すれば、Android の kikidame の SMB で読める。手順は [kikidame.md の「Windows の共有を読む」](kikidame.md#windows-の共有を読む)。

## LAN のスマホから開く

既定では、同じ PC のブラウザからしか開けない（`127.0.0.1` で待ち受ける）。LAN のスマホなどから開くときは、待ち受けるアドレスを変えて `service install` を打ち直し、Windows のファイアウォールに受信の規則を足す（例はポート 8080）。

```powershell
.\rajikura.exe service install -listen 0.0.0.0:8080
New-NetFirewallRule -DisplayName rajikura -Direction Inbound -Protocol TCP -LocalPort 8080 -Action Allow -Profile Private
```

- LAN からは、PC の IP のアドレスで開く（例 `http://192.168.1.10:8080/`）。`-allowed-hosts` は `service install` に渡せないので、PC の名前（`http://mypc:8080/` など）では開けない（421 で断る）。
- Web UI は、ほかのオリジンのページからの要求を断る検査、`Host` の検査、ログインの総当たりの制限を持つ（[api.md の守り](api.md#ブラウザ経由の攻撃とログインの総当たりへの守り)、[ADR 0021](adr/0021-web-ui-origin-host-and-login-limit.md)）。
- `serve` は HTTPS で待ち受けないので、開くのは信頼できる LAN だけにする。

## スリープ

録音している間（ジョブの試行の間）だけ、`serve` が Windows にスリープしないよう伝える。スリープ中の PC を起こすのは、ライブで録るジョブのためだけ（[ライブ録音の前に起こす](#ライブ録音の前に起こす)）。

- 録音中は、管理者のターミナルの `powercfg /requests` の `SYSTEM:` に、理由「rajikura: 録音中」で rajikura が出る。
- 番組表の同期とバックアップの間は、スリープしないよう伝えない。
- 画面は消えてよい（点けたままにはしない）。
- 人がフタを閉じたときや、スリープを選んだときは止めない。
- 伝えられなかったときは、ログに警告を出して録音を続ける。
- 録音中のシャットダウンや再起動は、止めることも待たせることもしない。途中だったタイムフリーのジョブは、次の起動時に `retrying` に戻って再試行する。ライブ録音のジョブは `running` のまま残り、次の起動で取ったセグメントを残して繋ぎ直す（[recording.md の「ジョブの状態と操作」](recording.md#ジョブの状態と操作)）。

タイムフリーから録る放送回は、放送の後に録る。スリープ中だった間に来た録音は、スリープから戻った後に、録音期限（7 日か 30 日）の中なら追いついて録る。タイムフリーから録るジョブのためには、PC を起こさない。

### ライブ録音の前に起こす

タイムフリーの対象外の放送回は、放送中にライブで録る（[recording.md の「ライブ録音」](recording.md#ライブ録音)）。放送の時刻に PC がスリープしていると録れないので、`serve` は、放送の開始の 4 分前にスリープ中の PC を起こすタイマーを掛ける。

- タイマーは、まだ起こす時刻が来ていないライブで録るジョブのうち、一番早いものの分を掛ける。番組や番組表が変われば掛け直し、ライブで録るジョブが無くなれば取り消す。
- 起きたら、最初の試行の 2 分後までスリープしないよう伝える。試行が始まれば、録音中の分が続けて掛かる。
- 録り終えた後にスリープに戻す処理は無い。人が触っていなければ、Windows が自分でスリープに戻る。スリープまでの時間を「スリープしない」にしている PC では、戻らない。
- 録音中に人がフタを閉じた・スリープを選んだときは、その録音のためには起こさない。
- 起こせるのは、スタンバイ（S3）の PC。休止状態（S4）と Modern Standby（S0 低電力アイドル）の PC で起きるかは、確かめていない。PC がどちらかは、`powercfg /a` の「利用可能」の側に「スタンバイ (S0 低電力アイドル)」があるかで分かる。
- 起きない PC でライブで録る放送回があるなら、Windows の設定の電源の項で、電源に接続しているときにスリープしないようにする（画面の言葉は Windows の版で違う）。

起きなかったとき（放送の時刻に PC がスリープしたままだったとき）は、その間は録れない。録音期限（放送の終了の 5 分後）の内に戻れば、取った分を残して繋ぎ直し、radiko が遡って返す範囲（約 270 秒）で埋める。それより前の分は欠落にして、取れた分を保存する。

#### 電源オプションの設定

タイマーで起こすには、使っている電源プランの「スリープ解除タイマーの許可」を「有効」にしておく。コントロール パネルの「電源オプション」で、使っているプランの「プラン設定の変更」→「詳細な電源設定の変更」→「スリープ」→「スリープ解除タイマーの許可」を、「バッテリー駆動」と「電源に接続」の両方で「有効」にする（画面の言葉は Windows の版で違う。「重要なスリープ解除タイマーのみ」は、`serve` は有効でないとみなして警告を出す）。 <!-- glossary-ok: プラン -->

今の値は、ターミナルで次を流すと、`現在の AC 電源設定のインデックス`（電源に接続）と `現在の DC 電源設定のインデックス`（バッテリー駆動）に出る（`0x00000001` が有効、`0x00000000` が無効、`0x00000002` が重要なスリープ解除タイマーのみ）。

```powershell
powercfg /query SCHEME_CURRENT SUB_SLEEP RTCWAKE
```

#### 起こせないと分かったときの警告

`serve` は、タイマーを掛けるときに起こせないと分かったら、[ログ](#ログ)に警告（`level=WARN`）を出す。文は「ライブ録音の前にスリープ中の PC を起こせない見込み（docs/windows.md の「スリープ」）」で、`reason` に理由が出る。同じ理由では、`serve` を起動してから 1 回だけ出す。

- `reason` が「電源プランの「スリープ解除タイマーの許可」が有効でない」: 上の[電源オプションの設定](#電源オプションの設定)で、両方を「有効」にする（括弧の中に、今の電源に接続・バッテリーの値が出る）。 <!-- glossary-ok: プラン -->
- `reason` が「この PC はタイマーでスリープから起きられない（…）」: ライブで録る放送回があるなら、電源に接続しているときにスリープしないようにする。
- `reason` が「スリープから起こすタイマーを掛けられない」「スリープから起こすタイマーを取り消せない」、または「スリープ解除タイマーの許可」を読めない旨: Windows の API が失敗した。後ろに Windows の誤りが出る。

上の 1 つ目か 2 つ目の理由で起こせないと分かったら、ログの警告にあわせて、サービスの異常（スリープから起こせない）の始まりを通知し、Web UI のどの画面にも帯で出す（[notifications.md](notifications.md)）。 <!-- 足したら直す: サービスの異常 -->

- 異常が続いている間は、理由が変わっても通知し直さない。
- 次にタイマーを掛けて、上の 3 つの理由のどれも出なければ、復旧を通知して帯が消える。タイマーを掛け直すのは起こす対象のジョブが変わったときだけなので、設定を直しても、次に掛け直すまで帯は残る。
- 3 つ目の理由（Windows の API の失敗）は、人が直せるものではないので、ログの警告だけにして、通知しない。

## ログ

サービスとして動くときのログは、データのフォルダの `logs\rajikura.log` に出す（コンソールで `serve` を動かすときは、標準エラー）。10 MB を超えそうになったら新しいファイルに替え、古いものを `rajikura.log.1`〜`rajikura.log.4` に残す（今のファイルを含めて 5 本）。Windows のイベントログには出さない。 <!-- 足したら直す: データのフォルダ -->

データのフォルダは管理者にしか読めないので、ログは管理者のターミナルかエディターで開く。

```powershell
Get-Content "$env:ProgramData\rajikura\logs\rajikura.log" -Tail 50
```

## トレイ

トレイ（`rajikura-tray.exe`）は、PC にログインした人ごとに動き、接続先の rajikura（その PC のサービスか、Linux のサーバーやコンテナで動く `serve`）の状態を通知領域のアイコンに、通知をトーストに出す（[ADR 0023](adr/0023-windows-tray-embedded-and-linked-by-loopback.md)）。サービスの起動・停止と、次の録音の表示はしない（`services.msc` と Web UI を使う）。

トレイは Release の `rajikura.exe`（Windows のもの）に埋め込まれていて、次のどちらかで入れる。どちらも、打ったユーザーの `tray.toml` に接続先を書き、ログイン時に起動するよう登録して、起動する。

- [入れる](#入れる)の `service install`: `%ProgramFiles%\rajikura\rajikura-tray.exe` に入れる。接続先は `-listen` から決める。
- [トレイだけを入れる](#トレイだけを入れる)の `tray install -url <URL>`: `%LocalAppData%\Programs\rajikura\rajikura-tray.exe` に入れる。接続先は `-url`。

同じユーザーで両方を打つと、後に打った方が勝つ（ログイン時の起動の登録と `tray.toml` は共通。トレイは 1 ユーザーに 1 つ）。

トレイは、起動しても窓は出ず、端末にも何も出さない。出すものは[トレイのログ](#トレイのログ)に書く。

### 二重起動

トレイは、ログインしたユーザーごとに 1 つだけ動く。そのユーザーでもう動いていれば、2 つ目は何もせずにすぐ終わる。ほかのユーザーのセッションで動くトレイとは数えない。

### 接続先と連携

トレイは、接続先と API トークンを `%AppData%\rajikura\tray.toml`（`url` と `token`）から読む。CLI の `client.toml` は読みも書きもしない。

- 接続先は、起動の引数の `-url`（例 `rajikura-tray.exe -url http://127.0.0.1:18080`）、`tray.toml` の `url`、既定の `http://127.0.0.1:8080` の順に使う。
- トークンは `tray.toml` の `token` だけから読む。
- `tray.toml` は手で書かない。書くのは、`service install`・`tray install` と、下の連携の 2 つ。

`service install`・`tray install` は、打ったユーザーの `tray.toml` を次のとおりに書く。`tray.toml` を読めないときは、上書きし、そのことを出す。

| 場合 | `url` | `token` | 端末に出すこと |
| --- | --- | --- | --- |
| `tray install` で `-token` を渡した | 渡した URL を書く | 渡したトークンを書く | 書いたこと |
| `-token` を渡さず、`tray.toml` の `url` が URL と同じ | そのまま | そのまま | なし |
| `-token` を渡さず、`tray.toml` が無いか `url` が URL と違う | URL を書く | 消す | 書いたことと、「Web UI と連携する」で連携する（し直す）こと |

`service install` は、`-token` を渡さない `tray install` と同じ扱い（`-listen` を変えて `url` が変われば、前に連携したトークンを消す）。

連携は、その時の接続先を `url` に、受け取ったトークンを `token` に書く。

1. 通知領域のトレイのアイコンを右クリックし、「Web UI と連携する」を押す。ブラウザで Web UI の連携の画面が開く。
2. Web UI にログインしていなければログインし、画面の名前（`<PC 名>\<ユーザー名>`）が自分のものであることを確かめて、「このトレイと連携する」を押す。
3. ブラウザに「連携した。この画面は閉じてよい」と出たら、連携が済んでいる。

連携は、開いてから 5 分の内に済ませる。連携し直しても古いトークンは失効しないので、Web UI の「API トークン」の画面で「トレイ（<名前>）」の古いものを失効させる。

`tray.toml` が無い・読めない、トークンが無い、接続先が http(s) の URL として読めない、トークンが受け付けられない（401）ときは、アイコンを「未設定」にし、起動してから一度だけ「Web UI と連携してください」のトーストを出す。

### 読むものとトースト

トレイは、1 分ごとに接続先の通知と録音中のジョブを読む。

- 前に見た通知より新しい通知を、種類を問わず全部トーストに出す（外部の通知先と同じものが出る）。
- 起動して最初に読めたときに溜まっていた未読は、1 件ずつは出さず、「未読 N 件」の 1 枚にまとめる。
- トレイ自身は通知を既読にしない。トーストから開いた Web UI の画面が、その通知を既読にする。
- 接続先に 3 度続けて届かなければ（約 3 分）、「3 回続けて rajikura（<ホスト>）に届かなかった。rajikura が動いているか、ネットワークを確かめる。」のトーストを一度出し、また届くようになったら「rajikura（<ホスト>）に、また届くようになった。」を出す。起動してから最初の 3 分は、届かなくても出さない。
- 通知のトーストを押すと、通知が指す Web UI の画面（ジョブの詳細・番組の編集・キーワードの画面）をブラウザで開く。指す先の無い通知（サービスの異常・復旧・テスト通知）と「未読 N 件」のトーストは、Web UI の通知の画面を開く。

### アイコンの状態

アイコンの地は Web UI の favicon（rajikura のロゴ）で、状態は右下の小さな丸（バッジ）で重ねる。アイコンにマウスを載せると、ツールチップに状態と件数が出る。

| 状態 | アイコン | いつ |
| --- | --- | --- |
| 待機中 | favicon だけ（バッジなし） | 届いていて、下のどれでもない |
| 録音中 | favicon ＋ 赤の丸に白の点のバッジ | 録音中のジョブがある |
| 異常あり | favicon ＋ 橙の丸に白の「!」のバッジ | 続いているサービスの異常があるか、通知先に送れなかった通知がある（録音中より先に出す） |
| 止まっている | favicon ＋ 灰の丸に白の横棒のバッジ | 接続先の rajikura に届かない。ツールチップは「rajikura: 止まっている（<ホスト> に届かない）」 |
| 未設定 | favicon ＋ 灰の輪（中は白）のバッジ | `tray.toml` が無い・読めない、トークンが無い、接続先が URL として読めない、401 を受けた（理由は[トレイのログ](#トレイのログ)に出す） |

### メニュー

- 「Web UI を開く」: 接続先の Web UI をブラウザで開く。
- 「通知を開く」: Web UI の通知の画面をブラウザで開く。
- 「テスト通知を送る」: テスト通知を送る（[web-ui.md の「テスト通知」](web-ui.md#テスト通知)）。数秒でテスト通知のトーストが出る。通知先が `none` のままでも、トレイのトーストを試せる。送れないとき（接続先に届かない・トークンが無いなど）は、「rajikura: テスト通知を送れない」のトーストに理由を出す。
- 「Web UI と連携する」: 上の[接続先と連携](#接続先と連携)。
- 「ログイン時に起動する」: 自分のログイン時に、今のトレイ（`-url` を付けて起動していれば同じ `-url` も）を起動するよう登録する。もう一度押すと消す。`service install`・`tray install` を打ったユーザーでは、チェックが付いて出る。
- 「終了」: トレイを終える。

### トレイのログ

トレイのログは `%LocalAppData%\rajikura\tray.log` に出す（サービスのログとは別。自分のアカウントで読める）。1 MB を超えそうになったら `tray.log.1` に替え、古いものは 1 本だけ残す。トーストを出せなかったときと、連携を断ったときも、ここに出す。

### トレイだけを入れる

`serve` を Windows のサービスにせず、Linux のサーバーやコンテナで動かしているときは、手元の Windows に[トレイ](#トレイ)だけを入れて、その `serve` に向けられる。Release の `rajikura.exe` の `tray install` で入れる。管理者は要らない（書くのは、打ったユーザーの分だけ）。

#### 入れる・更新する

1. [GitHub Release](https://github.com/t-seki/rajikura/releases) の `rajikura_<タグ>_windows_amd64.exe` を落とし、`rajikura.exe` に改名する（[入れる](#入れる)の手順 1 と同じ）。置く場所はどこでもよい。
2. ふつうのターミナル（管理者でなくてよい）で、`rajikura.exe` を置いたフォルダから、`serve` の Web UI の URL を `-url` に渡して `tray install` を打つ（例は、リバースプロキシ越しの URL）。

   ```powershell
   .\rajikura.exe tray install -url https://rajikura.example.com
   ```

   - `-url` は必須。`http://` か `https://` で始まる URL でなければ、何もせずに使い方の誤りにする（終了コード 2）。
   - API トークンを Web UI の「API トークン」の画面で発行してあれば、`-token` で渡せる（`.\rajikura.exe tray install -url https://rajikura.example.com -token <トークン>`）。渡せば、下の手順 4 の連携は要らない。
3. `tray install` は、前のトレイを止め、トレイを `%LocalAppData%\Programs\rajikura\rajikura-tray.exe` に入れ、`tray.toml` を書き、ログイン時に起動するよう登録して、その場で起動する（起動できなくても失敗にしない）。

   ```text
   トレイの接続先 https://rajikura.example.com を C:\Users\<ユーザー>\AppData\Roaming\rajikura\tray.toml に書いた。トレイのメニューの「Web UI と連携する」で連携する
   トレイを C:\Users\<ユーザー>\AppData\Local\Programs\rajikura\rajikura-tray.exe に入れ、このユーザーのログイン時に起動するよう登録した（HKCU\Software\Microsoft\Windows\CurrentVersion\Run の rajikura-tray: "C:\Users\<ユーザー>\AppData\Local\Programs\rajikura\rajikura-tray.exe"）
   トレイを起動した（通知領域に出る）
   ```

4. `-token` を渡さなかったときは、トレイのメニューの「Web UI と連携する」で連携する（[接続先と連携](#接続先と連携)）。連携できなければ、`-token` を渡して入れ直す。

更新するときは、新しい版の `rajikura.exe` で同じ `tray install` を打ち直す。`-url` が同じで `-token` を渡さなければ、連携したトークンは残る。`-url` を変えると、前に連携したトークンを消すので、連携し直す。

`tray install` の後に `service install` を打つと、ログイン時の起動は `%ProgramFiles%\rajikura\rajikura-tray.exe` を指し、`tray.toml` の `url` は `-listen` の URL になる。

#### トレイだけを消す

ふつうのターミナルで `tray uninstall` を打つ。

```powershell
.\rajikura.exe tray uninstall
```

- 打ったユーザーのトレイのログイン時の起動の登録は、`%LocalAppData%\Programs\rajikura\rajikura-tray.exe` を指すときだけ消す。ほかの exe を指していれば（後に `service install` を打ったなど）消さず、そう出す。
- 動いているトレイを止めてから、exe を消す（`service install` が入れたトレイには触らない）。止まらず exe が消せなければ、名前を変えてよけ、トレイのメニューの「終了」で終えることを出す。
- exe が無ければ、無いことを出して終える（終了コード 0）。
- `tray.toml` は消さない。端末にそのパスと、連携したトークンを Web UI の「API トークン」の画面で失効させることを出す。
