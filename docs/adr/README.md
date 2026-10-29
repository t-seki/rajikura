# 設計の決定（ADR）

rajikura の設計の決定と、その理由をまとめる。どの ADR も、今の rajikura の決定として書いてある。各 ADR の見出しは「決定／理由／検討した案／帰結」。用語は [GLOSSARY.md](../../GLOSSARY.md) に従う。決定を変えるときの決まりは [development.md](../development.md) にある（新しい ADR を書き、置き換えた ADR はこの表の題の後に「置き換え済み（→ 新しい番号）」と書く）。

| 番号 | 題 | 要約 |
| --- | --- | --- |
| [0001](0001-native-go-fetcher-over-yt-dlp.md) | 音声の入手は yt-dlp を使わず Go で行う | radiko の認証とタイムフリーの音声の入手を、Go で行う |
| [0002](0002-jellyfin-preset-layout.md) | 保存の形は Jellyfin の音楽ライブラリに合わせたプリセットで決める | 局をアルバムアーティスト、番組をアルバム、年と月日を disc と track にする |
| [0003](0003-job-per-show-and-broadcast-day.md) | ジョブは放送回ごとに作り、分割番組でつなぐ番組だけ番組と放送日の組でまとめる | 分割番組の設定を 3 つの値で持ち、つなぐのは分割番組だけにする |
| [0004](0004-station-name-from-radiko.md) | 保存先の局名は radiko の局名にする | 局名は radiko の局一覧の名前をそのまま使い、取れるまで格納を待つ |
| [0005](0005-run-in-container-with-network-share-library.md) | 常駐はコンテナを主にし、ライブラリへはネットワーク越しの共有に書く | public のコンテナイメージを主な配布形態にする |
| [0006](0006-show-name-from-radiko.md) | 録るもののまとまりを「番組」と呼び、その名前は radiko の番組名から写す | 「番組」と「予約」の語と、番組の名前の決め方 |
| [0007](0007-airing-title-in-title-tag.md) | 放送回の番組名が番組の名前と違えば title に足す | 副題や特番の名前を title のタグに残す |
| [0008](0008-web-ui-shape.md) | Web UI は利用者 1 人のログインにし、仮のファイルを埋め込み、/healthz と同じ口で受ける | Web UI の認証・埋め込み・待ち受ける口 |
| [0009](0009-cli-over-http.md) | CLI は HTTP API を呼び、DB に書くのは serve だけにする | CLI は API トークンで serve を呼び、展開は serve の中の展開役だけが行う |
| [0010](0010-per-event-notifications-and-metrics.md) | 通知は出来事が起きるたびに出し、生存確認は /metrics を外から見る | 日ごとのまとめを持たず、生存確認はメトリクスで外から見る |
| [0011](0011-notifications-written-to-db-then-delivered.md) | 通知はまず DB に書き、通知先には別に送る | 通知を DB の行にし、通知先には周ごとに送り直す。テスト通知はその場で送る |
| [0012](0012-airing-time-in-broadcast-day-time.md) | 人が読み書きする口では、放送回の時刻を放送日の時刻で書き、出来事の時刻を暦の時刻で書く | 放送日の時刻（05:00〜28:59）と暦の時刻の書き分け |
| [0013](0013-builtin-m4a-muxer.md) | m4a への格納は ffmpeg を使わず Go で行う | 既定の格納を builtin にし、ffmpeg を要らなくする |
| [0014](0014-keyword-adds-onces-to-shows.md) | キーワードは番組の外に置き、当たった放送回を番組の 1 回の予約として足す | 出演者名などで探して録る仕組みの置き場所 |
| [0015](0015-settings-and-secrets-in-db.md) | 設定と秘密は DB に置き、起動の設定はフラグだけにする | 設定ファイルを持たず、秘密は DB の中で暗号化する |
| [0016](0016-first-password-by-setup-code.md) | 初めてのパスワードはセットアップコードで決める | 動かしている当人だけが最初のパスワードを決められるようにする |
| [0017](0017-backup-by-db-copy.md) | DB を失ったときに戻す手段とマシンを移す手段は、DB の写し（バックアップ）にする | 動いたまま取る DB の写しと、自動のバックアップ |
| [0018](0018-weekly-from-reaches-past-days.md) | 開始日のある毎週の予約は今日より前の放送日にも当たり、番組名の食い違う回は録らなかったにする | 開始日からの過去の回を録り、特番は録らない |
| [0019](0019-skip-by-title-and-by-person.md) | 今日から先の放送回も番組名で録らない予定にし、人も録る・録らないを決められるようにする | 録らない予定・録らなかったと、人の「録る」「録らない」 |
| [0020](0020-windows-service-with-virtual-account.md) | Windows では仮想アカウントのサービスとして入れ、127.0.0.1 で待ち受ける | Windows のサービスとスリープの扱い |
| [0021](0021-web-ui-origin-host-and-login-limit.md) | Web UI はオリジンと Host を確かめ、ログインの失敗を全体で数える | ブラウザ経由の攻撃とログインの総当たりへの守り |
| [0022](0022-live-recording-for-timefree-excluded-airings.md) | タイムフリーの対象外の放送回だけを、別の録音役でライブで録る | ライブ録音の方式・同時に録る数・保存のやり直し |
| [0023](0023-windows-tray-embedded-and-linked-by-loopback.md) | Windows のトレイは別の exe にして rajikura.exe に埋め込み、ループバックの受け口で連携する | トレイの入れ方と、API トークンの渡し方 |
| [0024](0024-web-ui-url-per-screen.md) | Web UI は画面ごとに URL を分け、ルーティングに wouter を使う | 画面の URL と、通知から画面を開くリンク |
| [0025](0025-rerecord-done-job-separately-from-manual-retry.md) | 録音済みのジョブの録り直しは、手動再試行とは別の操作にする | 録り直しの受け付けと、前のファイルの扱い |
| [0026](0026-job-id-not-reused.md) | ジョブの id は使い回さない | `jobs.id` を AUTOINCREMENT にする |
| [0027](0027-chapters-refetched-by-rerecording.md) | チャプターはタイムフリーからの録音の保存のときに取り、取れなければできるのを待って録り直して入れる | チャプターの取り方・形と、チャプターの取り直し |
| [0028](0028-publish-as-oss-under-radiko-terms.md) | radiko の規約の第 4 条を承知で、ソースを公開した OSS として配る | 公開の立場と、守ること |
| [0029](0029-weekly-until-over-title-based-handover.md) | 番組の切り替えは毎週の予約の終了日で前もって決め、矛盾を番組名で振り分けない | 毎週の予約の終了日 |
