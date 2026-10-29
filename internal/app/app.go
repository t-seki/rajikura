// Package app は rajikura の常駐（rajikura serve）の中身。部品（radiko、domain、store、media、library）を
// つなぎ、番組表の同期 → ジョブへの展開 → 入手 → 検証 → 保存を回す。
//
// # goroutine の分け方
//
// Serve は goroutine を 4 本持つ（ほかに、cmd が HTTP の待ち受けを持ち、ライブの録音役がライブで録るジョブごとに
// goroutine を起こす）。
//
//   - 展開役（runExpander）: ジョブへの展開（syncDay）は、展開役だけが行う（2 つの展開が同時に走らない）。
//     展開役が順に処理するのは、Web UI・API からの展開の依頼（RequestExpand）、その依頼で展開できなかった
//     局・放送日の試し直し、起動時と 6 時間ごとの番組表の同期、録音のループとライブの録音役から頼まれる最初の試行の前の番組表の
//     取り直しの 4 つ。キーワードを番組表に当てて 1 回の予約を足すのも展開役だけが行う（同期のときと、展開の依頼に
//     キーワードの依頼（RequestKeywords）が積まれていたとき。keyword.go）。
//   - 録音のループ（Serve）: 1 分ごと、または起こされたとき（WakeRecorder）に、タイムフリーから録るジョブを、実行するものが無くなるまで
//     1 件ずつ実行し（RunDue）、1 件ごとに通知を判定する。録音中も展開役は止まらない
//     （番組の変更の展開は、録音の終わりを待たない）。逆に、最初の試行の前の取り直しは展開役に並ぶので、展開役が
//     ほかの処理（多くの局の展開、同期、試し直し）をしている間は、録音の始まりが待たされる。
//   - バックアップの見回り（runBackups）: 1 時間ごとに、設定 backup_dir があれば、前に取れてから 24 時間が過ぎていないかを見て、
//     過ぎていれば DB の写しを取る（backup.go）。同じ見回りで、再スキャンを頼む先が無くなっていれば、続いている再スキャンの
//     失敗の異常を閉じる（closeRescanIfGone。run.go）。録音とも展開とも別の goroutine で、止まらない。
//   - ライブの録音役（runLive）: タイムフリーの対象外の放送回を持つジョブ（domain.Job.Live）を、放送の開始の 1 分前に拾い、
//     ジョブごとの goroutine で放送中のストリームから録る（同時に maxLiveJobs（8 本）まで。live.go）。録音のループとは別なので、ライブを
//     録っている間も、タイムフリーの録音と通知の判定は止まらない。録音のループはライブで録るジョブを拾わない。
//     見回りのたびに、ライブで録るジョブの最初の試行の 3 分前にスリープ中の PC を起こすタイマー（Waker。Windows だけ）を
//     掛け直す（live.go の「スリープ中の PC を起こす」）。
//
// # 起動時
//
//  1. 回復: running のタイムフリーから録るジョブを retrying に戻す（前回のプロセスが途中で終わった）。ライブで録るジョブは
//     running のまま残し、ライブの録音役が繋ぎ直す。
//  2. 保存先の *.partial.m4a と *.partial.jpg を掃除する。
//  3. ライブの録音役を起こす（番組表の同期を待たない）。
//  4. 番組表を同期する（展開役に頼み、終わるまで待ってから録音のループを始める）。
//
// 足したら直す: ジョブの状態の移り方
//
// # 番組表の同期（Sync）
//
// 展開役が、起動時と 6 時間ごとに、有効な番組の予約（毎週と 1 回）に出てくる局について、今日から 7 日先までの
// 放送日と、録音期限の内側にある展開が触るジョブ（waiting と確定の前の skipped）の過去の放送日と、有効な番組の 1 回の予約の過去の放送日（録音
// 期限の内側）と、有効な番組の開始日を持つ毎週の予約の過去の放送日（開始日から、録音期限の内側）の番組表を取り、domain の展開に渡して差分を store に当てる。同期の始めに、録音期限を過ぎた
// 1 回の予約を消す（ジョブと試行の記録は残る）。続けて、局・放送日の展開の前にキーワードを番組表に当て、当たった
// まだ始まっていない放送回を番組の 1 回の予約として足す（keyword.go。足した局・放送日は、この同期で展開する）。
// 無効な番組の予約は放送回に当たらないので、その番組の waiting のジョブは、番組を消した
// ときと同じく、その局の番組表を取り直したときに消える。
// 番組表を取れなかった、または空だった局・放送日は展開しない（休止と取り違えてジョブを消さないため）。
// 展開した局・放送日では、同じ番組表と番組の一覧から当たらない日（毎週の予約の枠に、その開始時刻で始まる放送回が
// 番組表に無い放送日。今日から 7 日先まで）を求めて記録し（unmatched.go）、同期の終わりに、まだ通知していないものを番組ごとに
// 1 通にまとめて通知する。同期の始めに、放送日が過ぎた記録と、有効な番組でない記録を消す。
// 放送回への当たり方は domain.Show.Hit で決める（ADR 0018・0019）。今日と今日から先の放送日では、予約に当たる
// 放送回に当たる。今日より前の放送日では、1 回の予約の放送回と、開始日を持つ毎週の予約の録音期限の内側の放送回だけを
// 新しいジョブにする（開始日を持たない毎週の予約だけに当たる放送済みの回からは、ジョブを新しく作らない）。
// どの放送日でも、毎週の予約だけに当たり番組名の食い違う放送回（番組の check_title が立っているとき）は、skipped の
// ジョブにして録りにいかない。最初の試行の時刻の前なら録らない予定（確定の前）で、展開のたびに決め直し、番組名が直れば waiting に
// 戻す。今日より前の放送日と、最初の試行の時刻を過ぎた展開では、確定した録らなかったにする。人が録る・録らないを決めたジョブ
// （domain.Job.ByPerson）は、展開が番組名で状態を変えない。通知は syncDay が書く（notifySkipPlanned・notifyUnskipped・notifySkipped）。
// そのため同期は、有効な番組の開始日を持つ毎週の予約が当たりうる過去の放送日（開始日から、録音期限の内側。
// domain.PastWeeklyDays）の番組表も取る。展開が触るジョブ（waiting と確定の前の skipped。domain.Job.Expandable）の更新と、放送回を
// 外すのはどの日も同じ（すでにある waiting のジョブはそのまま録音する）。この判定は、番組を変えた直後の展開と、最初の試行の直前の
// 番組表の取り直しでも同じ。
//
// 1 回の予約は、その放送回のジョブが done になったら消す（部分保存で done になったときも。removeDoneOnces）。
//
// # 番組を変えた直後の展開（RequestExpand）
//
// Web UI・API で番組を変えたら、次の同期を待たずに、関係する局・放送日だけを
// 同期と同じやり方で展開する（当たらない日の記録と通知も同じ。通知は展開 1 回の終わりにまとめる）。Web UI・API は依頼をプロセスの中のキューに積んで展開役を起こし、展開を待たずに
// 応答を返す（保存の直後に読み直すと、ジョブがまだできていないことがある）。展開役は積まれた依頼をまとめて展開し、
// 展開できなかった局・放送日（番組表を取れなかった・空だった）は、その局・放送日だけを、間を延ばしながら試し直す
// （1 分 → 5 分 → 15 分 → 30 分、以降 30 分ごと。試し直すたびに回数と次の時刻を Warn で出す）。次の番組表の同期が
// 局・放送日を展開し終えたら、試し直しを待っている局・放送日のうち、同期で展開できたものと、同期の対象でなかったものを
// 捨てる（捨てたことを 1 行出す）。同期でも展開できなかったものは試し直しを続ける（syncOnExpander）。依頼は DB に残さないので、
// serve を再起動すると消えるが、起動時の同期で拾い直す。
// 展開は局・放送日ごとに、番組の一覧をその都度 store から読み直してから当てる（依頼を積んでから展開するまでに
// 番組が変わっても、変わる前の番組の一覧で展開しないように）。
//
// キーワードを作った・変えた直後も同じく、Web UI・API が依頼（RequestKeywords）を積み、展開役が、積まれた展開の依頼と一緒に
// キーワードを番組表に当ててから、足した先の番組の局・放送日を展開する。
//
// # ジョブの実行（RunDue）
//
// 録音のループは 1 回の見回りで、実行するタイムフリーから録るジョブを 1 件ずつ、無くなるまで取って実行する（録音のループで同時に走るジョブは 1 件。
// ライブで録るジョブの試行は live.go）。
// 試行（入手から保存まで）の間だけ、OS にスリープしないよう伝える（internal/power。Windows だけ。伝えられなくても Warn を出して続ける）。
// 1 回の試行の流れは次のとおり。
//
//  1. 最初の試行の直前（waiting のジョブを取る前）に、その放送日の番組表を取り直して展開をやり直す
//     （展開役に頼み、終わるまで待つ）。
//     ジョブが消えたら（休止）それで終わる。放送日が今日より前なら、番組表の同期と同じ当たり方で新しいジョブを作る
//     （domain.Show.Hit）。取り直した番組表で番組名が食い違えば、録らなかったにして録りにいかない。
//     その前に、最初の試行の時刻を過ぎた録らない予定のジョブも、番組表を取り直して確定させる（番組名が直っていれば waiting に
//     戻って、そのまま録る。settleDueSkipped）。番組表を取り直せなければ確定させずに待ち、取れなかった局・放送日の展開の
//     試し直しと番組表の同期に確定を任せる（録音期限を過ぎたら、そのまま確定させる）。
//  2. 認証する。トークンは 1 回の試行で使い回す。
//  3. 放送回ごとに 作業ディレクトリ/<ジョブ ID>/<開始時刻>.aac へ入手する。前の試行で取り終えた
//     放送回は再利用する（<開始時刻>.extinf があるものだけ。途中のファイルは消して取り直す）。
//  4. 先頭の放送回の画像を取る（artwork.Fetch）。取れなければ（URL が空、取得の失敗、JPEG・PNG・GIF でない、読み込めない、幅か高さが 4096 を超える、
//     大きすぎる）Warn を出して画像なしで続ける。
//     設定 chapters がオンなら、放送回ごとに radiko のチャプターを取る（fetchChapters。取れなければ Info を出してチャプター無しで
//     続け、試行は失敗にしない。ADR 0027）。
//     media.Remux で m4a にする（連結する番組は全放送回を 1 本に。画像があればカバー画像として、チャプターがあれば chpl と
//     チャプタートラックとして埋め込む）。タグは library のプリセットが決める。
//  5. 検証する。media.Duration の長さ（ffprobe、または builtin の読み直し）と #EXTINF の合計との差が 10 秒以内なら成功。番組表の放送時間の合計と
//     5 分以上ずれていたら警告をログに出す。
//  6. library.StoreOver で保存先に確定し、done にする。別名になったらログに出す（保存先のパスは DB に残る）。
//     録り直しの試行（前の保存先がある）で、選んだ名前が前の保存先と同じなら、別名にせずに上書きする（ADR 0025）。
//     done にするときに、チャプターの値（入った・待っている・入らなかった）を決め直し、404 か通信の失敗で取れなかった放送回が
//     あれば、チャプターの取り直しを待つ（同じトランザクションで書く。chapterStatus。ADR 0027）。
//     done にしたら、保存した時刻を DB と /metrics に記録し（markSaved。記録できなくても Warn だけ）、録音済みの通知を書く
//     （チャプターの取り直しの試行では書かない）。
//     続けて、ジョブの放送回の 1 回の予約（その番組の、局と開始時刻が同じもの）を消す。
//     確定した後、画像があれば番組のフォルダの folder.jpg を上書きする（置けなくても Warn だけ）。
//  7. 作業ディレクトリのジョブのフォルダを消す。
//  8. 保存のたびに、Rescanner があればライブラリの再スキャンを 1 回頼む（全体に RescanTimeout の上限。
//     失敗・取り消し・上限超過は Warn に出す。失敗と上限超過はサービスの異常（再スキャンの失敗）の始まりも通知し、
//     頼めたとき・頼む先が無くなったときに、続いていれば復旧を通知する。rescan）。
//
// 足したら直す: 保存の後の処理
//
// ライブで録るジョブも、保存した後は 6〜8 と同じ順に進める（欠けた区間があれば録音済みの代わりに欠落ありを通知する。
// チャプターは取らないので待たない。live.go の runLiveJob）。
//
// ジョブを取る前に、チャプターを待っている done のジョブのうち時刻の来たものについて、待っている放送回のチャプターの API だけを
// 引き、全部取れるようになったら（打ち切りの時点で一部だけなら 1 回）、録り直しと同じ移り方でチャプターの取り直しの試行を
// 起こす（checkChapters。ADR 0027）。取り直しの試行が録れなければ、expired にせず done に戻す（expire）。
//
// 失敗したら、失敗した段階とエラーを試行として記録し、domain.RetryPolicy で次の試行を決める。
// 格納・検証・保存の段階で落ちたら、その試行で使った放送回のファイルを捨て、次の試行で取り直す
// （入手の段階で落ちたなら、取り終えた放送回は次の試行で再利用する。保存の段階でも、局一覧が一度も
// 取れておらず局名を決められなかった失敗（errStationUnknown）は、放送回を捨てずに再利用する）。
// radiko.ErrNotAvailable（恒久的に取れない）なら expired にする。次の試行が無いときは、取れた
// 放送回があれば録音期限まで retrying にして、録音期限を過ぎてから下の扱いにする（取れた分を捨てない。ただし録り直しの試行で一部しか取れて
// いなければ、下のとおり保存しない）。
// 取れた放送回が無ければ expired にする。
//
// 録音期限を過ぎたジョブは、入手せずに domain.JudgeAtDeadline で扱いを決める。一部でも取れていれば
// 取れた分で 4〜6 を行って done にし、欠落を記録する。何も取れていなければ expired にする。録り直しの試行（前の保存先がある）は、
// 一部しか取れていなければ保存せずに expired にし、前のファイルを残す（ADR 0025）。
// チャプターの取り直しの試行（domain.Job.ChapterRefetch）は、上のどこでも expired にするところで done に戻し（前のファイル・
// 保存先・欠落の印は前のまま）、通知しない（ADR 0027）。
// このとき局一覧が一度も取れていなくても、局 ID を局名にして保存する（expired にはしない）。
// 足したら直す: ジョブの状態の移り方
//
// # 通知
//
// ジョブの出来事（録音済み・欠落あり・録音できなかった・録らない予定にした・録音予定に戻した・録らなかった・期限間近）とサービスの異常（番組表の同期が 2 回続けて
// 失敗した・radiko がエリア外と判定した・自動のバックアップを取れない・ライブ録音の前にスリープ中の PC を起こせない見込み・
// ライブラリの再スキャンを頼めない、とその復旧）を、通知として DB に書く（notify.go）。キーワードの出来事
// （キーワードで録音予定にした・上限で足すのを止めた）は、キーワードを当てた展開役が、足すのと同じトランザクションで書く（keyword.go）。
// ジョブの出来事のうち録らない予定にした・録音予定に戻した・録らなかったは、展開役が展開の差分を当てた後に書く（syncDay）。
// 番組の出来事（毎週の予約の当たらない日を新しく見つけた）は、展開役が同期・展開の終わりに、当たらない日に通知した印を
// 立てるのと同じトランザクションで書く（unmatched.go）。期限間近は、
// ジョブの実行と同じ周（1 件実行するごと）で判定する。同期の失敗は展開役が数える。通知先（Notifier）には、同じ周で
// まだ送れていない通知を古い順に送り、作ってから 1 時間を過ぎた通知を送ろうとして送れなければ、諦めて
// rajikura_notify_failures_total{kind} を数える。通知を書けない・送れないときも、ジョブの処理は止めない。
// 足したら直す: 通知の種類
// 足したら直す: サービスの異常
//
// ctx が取り消されたら（SIGINT / SIGTERM）、実行中の格納（ffmpeg、または builtin）と radiko への要求を止めて戻る。実行中だった
// ジョブは running のまま残り、次の起動時の回復で retrying に戻る（ライブで録るジョブは running のまま、ライブの録音役が繋ぎ直す）。ただし保存先に確定した後に
// 取り消されたときは、二重に保存しないように、試行の記録と done までは進める。
// 保存の後の再スキャンは、取り消されたら止める（done までは進めるが、再スキャンは進めない）。
//
// # 進捗
//
// 試行の間、ジョブの進捗（用語集）を serve のメモリにだけ持つ（Progress。読むのは App.JobProgress で、server がジョブの API に
// 付ける）。DB には書かない。試行の始まりに入手の段階を置き、録れた長さ（前の試行で入手し終えた放送回の分を含む）とジョブの
// 放送回の長さの合計を持つ。タイムフリーからの録音は radiko から取るチャンク（5 分）を書くたびに、ライブ録音は記録（.live）を
// 書くたびに、録れた長さを進める。格納・検証・保存に移ったら段階を変え（save が失敗の段階を変える所と同じ所）、試行が終わったら
// （成功でも失敗でも）消す。ライブで録るジョブの、録音期限を過ぎた保存だけのやり直しは、段階だけを持つ。
//
// # 動作確認
//
// ジョブの実行の周が始まった時刻を記録する（LastPoll）。/healthz の待ち受けは cmd が持ち、app は
// LastPoll を渡すだけ。/metrics の待ち受けも cmd が持ち、app は Metrics に値を入れるだけ
// （起動時に LoadMetrics で、最後に保存した時刻を DB から読む）。
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/library"
	"github.com/t-seki/rajikura/internal/metrics"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/power"
	"github.com/t-seki/rajikura/internal/store"
)

const (
	// DefaultSyncInterval は番組表を同期する間隔。
	DefaultSyncInterval = 6 * time.Hour
	// DefaultPollInterval は実行するジョブを探す間隔。
	DefaultPollInterval = time.Minute
	// syncDaysAhead は同期する、今日より先の放送日の数。
	syncDaysAhead = 7
	// lengthTolerance は Media.Duration の長さと #EXTINF の合計との差の許容。超えたら、タイムフリーからの録音では検証の
	// 失敗にする。ライブ録音では失敗にせず、ずれたまま保存して通知に書く（取り直せないため）。
	lengthTolerance = 10 * time.Second
	// guideTolerance は Media.Duration の長さと番組表の放送時間の合計との差。これ以上なら警告を出す。
	guideTolerance = 5 * time.Minute
	// maxRefresh は、最初の試行の前の番組表の取り直しを、1 回の RunDue で繰り返す上限。
	maxRefresh = 10
)

// DefaultExpandRetryDelays は、展開できなかった局・放送日を試し直すまでの間。i 回目の試し直しの前に i 番目
// （0 から数える）を待ち、使い切ったら最後の値をくり返す（1 分 → 5 分 → 15 分 → 30 分、以降 30 分ごと）。
var DefaultExpandRetryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// Rescanner はメディアサーバーにライブラリの再スキャン（用語集）を頼む。
type Rescanner interface {
	Rescan(ctx context.Context) error
}

// App は常駐の中身。フィールドを埋めてから使う。
type App struct {
	Store   *store.Store
	Guide   Guide
	Fetcher Fetcher
	Media   Media
	Preset  library.Preset

	// WorkDir は入手途中の ADTS を置く作業ディレクトリ。
	WorkDir string
	// Library は保存先のルート。空なら保存先が決まっていないので、録音を待たせる（番組表の同期と展開は動かす）。
	Library string
	// ArtworkClient は放送回の画像の取得に使う。nil なら http.DefaultClient。
	ArtworkClient *http.Client
	// Membership は会員種別。
	Membership domain.Membership
	// MaxAttemptsFree は非会員の試行回数の上限。0 以下なら domain の既定。
	MaxAttemptsFree int
	// AreaGuide はエリアの日別番組表の取得元（キーワードを番組表に当てる。keyword.go）。nil ならキーワードを当てない。
	AreaGuide AreaGuide
	// KeywordMaxPending は、キーワードごとの、足した放送回のうちまだ録音を終えていない回の数の上限
	// （設定の keyword_max_pending）。0 以下なら domain.DefaultKeywordMaxPending。
	KeywordMaxPending int
	// Chapters は設定 chapters（タイムフリーから録るファイルに radiko のチャプターを入れるか。ADR 0027）。Live があれば使わない。
	// チャプターは Fetcher が ChapterSource を満たすとき（*radiko.Client）だけ取る（run.go の fetchChapters）。
	Chapters bool

	// BackupDir は自動のバックアップを取る先（設定の backup_dir）、BackupKeep は残す数（backup_keep。0 以下なら config.DefaultBackupKeep）。
	// BackupDir が空なら取らない。Live があれば使わない（バックアップは backup.go）。
	BackupDir  string
	BackupKeep int
	// BackupCheckInterval は、バックアップを取る時期かを見る間。0 なら DefaultBackupCheckInterval。
	BackupCheckInterval time.Duration

	// SyncInterval（展開役の番組表の同期）と PollInterval（録音のループ）は Serve の間隔。0 なら既定。
	SyncInterval time.Duration
	PollInterval time.Duration
	// ExpandRetryDelays は、展開できなかった局・放送日を試し直すまでの間（DefaultExpandRetryDelays と同じ形）。
	// 空なら DefaultExpandRetryDelays。
	ExpandRetryDelays []time.Duration

	// KeepAwake は、ジョブの試行の間と、ライブ録音の前に起こした後（live.go の「スリープ中の PC を起こす」）だけ OS に
	// スリープしないよう伝え、外す関数を返す。nil なら power.Acquire。テストで差し替える。
	KeepAwake func(reason string) (release func(), err error)
	// Waker は、ライブで録るジョブの前にスリープ中の PC を起こすタイマー（live.go の「スリープ中の PC を起こす」）。
	// nil なら、ライブの録音役が power.NewWaker で作る。テストで差し替える。
	Waker power.Waker

	// LiveCheckInterval は、ライブの録音役が録るジョブを探す間隔。0 なら DefaultLiveCheckInterval（live.go）。
	LiveCheckInterval time.Duration
	// LiveRetryInterval は、ライブの入手が FetchLive の外で失敗したときに繋ぎ直すまでの待ち。0 なら DefaultLiveRetryInterval。
	LiveRetryInterval time.Duration

	// Now は現在時刻。nil なら time.Now。テストで固定する。
	Now func() time.Time
	// Log はログの出力先。nil なら slog.Default。
	Log *slog.Logger

	// Metrics は /metrics に出す値。nil なら記録しない。
	Metrics *metrics.Metrics
	// Notifier は通知先。nil なら通知先なし（通知は DB に書くだけで、どこにも送らない）。
	Notifier notify.Notifier
	// MuteSuccess なら、録音済みの通知を通知先に送らない（DB には書く。notify.success = false）。
	MuteSuccess bool
	// Rescanner は、保存のたびにライブラリの再スキャンを頼む先（Jellyfin）。nil なら頼まない。
	// 失敗したら Warn でログに出し、保存は成功のまま。送り直さない。失敗（上限超過を含む。SIGTERM の取り消しは除く）は
	// サービスの異常（再スキャンの失敗）の始まりとしても通知する（rescan）。
	Rescanner Rescanner
	// RescanTimeout は再スキャン全体（要求 2 回の合計）の待ち時間の上限。0 以下なら DefaultRescanTimeout（10 秒）。
	RescanTimeout time.Duration

	// Live は、保存したらすぐに効く設定の写し（Settings.Runtime）を返す。nil なら、上のフィールド（Library・Media・Preset・
	// MaxAttemptsFree・KeywordMaxPending・Chapters・Notifier・MuteSuccess・Rescanner）をそのまま使う（テストで使う）。
	// Live があれば、それらのフィールドは使わず、使うときに Live を呼んで最新の写しを読む。
	Live func() Runtime

	// stationsMu は stations を守る（展開役と録音のループの両方が読み書きする）。
	stationsMu sync.Mutex
	// stations は最後に取れた局の情報。
	stations map[string]Station
	// refreshed は、最初の試行の前に番組表を取り直したジョブ。展開役だけが触る。
	refreshed map[int64]bool
	// settleWaits は、最初の試行の時刻を過ぎた録らない予定のジョブのうち、番組表を取り直せず確定を待っているもの
	// （ジョブ ID → 取り直せなかった局・放送日。settleDueSkipped）。展開役だけが触る。
	settleWaits map[int64][]expandTarget
	// syncFailures は番組表の同期が続けて失敗した数。成功したら 0 に戻す。展開役だけが触る。
	syncFailures int

	// expanderInit は展開役への通り道（expandTasks・expandWake）を作る。
	expanderInit sync.Once
	// expanderRunning は展開役が動いていること（2 本目を起こさないため）。
	expanderRunning atomic.Bool
	// expandTasks は onExpander で頼まれた処理。
	expandTasks chan expandTask
	// expandWake は、展開の依頼を積んだことを展開役に知らせる。
	expandWake chan struct{}
	// recordWake は、録音のループを次の見回りを待たずに起こす（WakeRecorder）。
	recordWake chan struct{}
	// expandMu は expandRequests を守る（RequestExpand は HTTP の goroutine から呼ばれる）。
	expandMu sync.Mutex
	// expandRequests は、積まれてまだ展開していない依頼の番組。
	expandRequests []domain.Show
	// keywordsRequested は、キーワードを当てる依頼（RequestKeywords）が積まれていてまだ当てていないこと。expandMu が守る。
	keywordsRequested bool
	// expandRetries は、展開の依頼で展開できず、試し直しを待っている局・放送日（録らない予定の確定の前に番組表を
	// 取り直せなかった局・放送日も積む。settleDueSkipped）。展開役だけが触る。
	expandRetries []*retryTarget
	// lastSync は、最後の番組表の同期で展開できなかった局・放送日（Sync が残す）。展開役だけが触る。
	lastSync *syncOutcome
	// lastPoll は、ジョブの実行の周が最後に始まった時刻（UnixNano）。0 は、まだ記録していない。
	lastPoll atomic.Int64

	// liveMu は liveRunning と liveWaits を守る（ライブの録音役と、録音の goroutine が触る）。
	liveMu sync.Mutex
	// liveRunning は、このプロセスが今ライブで録っているジョブ（live.go）。録音期限を過ぎて保存だけをやり直しているジョブも
	// 入る。値は、同時に録る本数の枠（maxLiveJobs）に数えるか（startLive の slot。保存だけのやり直しは false）。
	liveRunning map[int64]bool
	// liveWaits は、同時に録る本数の枠が空くのを待っているライブで録るジョブ（枠待ち。live.go の package doc）と、待った後に
	// 繋ぎ始めたジョブの覚え。メモリだけに持ち、DB に残さない（serve を起動し直すと消える）。試行が終わったら忘れる。
	liveWaits map[int64]liveWait
	// liveMax は、同時に録る本数の上限を差し替える口（テストだけが使う。0 なら maxLiveJobs）。設定・フラグ・API には出さない。
	liveMax int
	// liveWG は、ライブで録っている goroutine を待つ。
	liveWG sync.WaitGroup
	// liveRefreshed は、ライブで録る前に番組表を取り直したジョブ。ライブの録音役の goroutine だけが触る。
	liveRefreshed map[int64]bool

	// progressMu は progress を守る（録音のループ・ライブの録音の goroutine が書き、HTTP の goroutine が読む）。
	progressMu sync.Mutex
	// progress は、このプロセスが試行しているジョブの進捗（ジョブ ID → 進捗）。DB には書かない。
	progress map[int64]Progress
	// progressSeen があれば、進捗を置く・変える・消すたびに、変えた後の値で呼ぶ（消したら ok が false）。テストが移り方を見る。
	progressSeen func(id int64, p Progress, ok bool)
}

// Progress は録音中のジョブの進捗（用語集）。今の試行の段階と、入手の間だけ、それまでに録れた音の長さとジョブの放送回の長さの
// 合計を持つ。serve のメモリにだけ持ち、DB には書かない（再起動すると消え、次に試行が進んだときに戻る）。
type Progress struct {
	// Stage は今の試行の段階（入手は domain.StageFetch、格納と保存は domain.StageSave、検証は domain.StageVerify）。
	Stage domain.Stage
	// Counted は、Fetched と Total を出すか（入手の間だけ true。ライブで録るジョブの保存だけのやり直しでは、入手が無いので
	// 立たない）。
	Counted bool
	// Fetched は、それまでに録れた音の長さ（#EXTINF の合計）。前の試行で入手し終えた放送回の分を含む。
	Fetched time.Duration
	// Total は、ジョブの放送回の長さ（End − Start）の合計。
	Total time.Duration
}

// JobProgress は、このプロセスが試行しているジョブ id の進捗を返す。試行していなければ false（server がジョブの API に付ける）。
func (a *App) JobProgress(id int64) (Progress, bool) {
	a.progressMu.Lock()
	defer a.progressMu.Unlock()
	p, ok := a.progress[id]
	return p, ok
}

// startFetchProgress は、試行の始まりに、入手の段階の進捗を置く（録れた長さ fetched、放送回の長さの合計は job から求める）。
func (a *App) startFetchProgress(job domain.Job, fetched time.Duration) {
	var total time.Duration
	for _, ar := range job.Airings {
		total += ar.End.Sub(ar.Start)
	}
	a.putProgress(job.ID, Progress{Stage: domain.StageFetch, Counted: true, Fetched: fetched, Total: total})
}

// setFetched は、入手の段階の進捗の録れた長さを fetched にする（入手の段階の進捗が無ければ何もしない）。
func (a *App) setFetched(id int64, fetched time.Duration) {
	a.progressMu.Lock()
	p, ok := a.progress[id]
	if ok = ok && p.Counted; ok {
		p.Fetched = fetched
		a.progress[id] = p
	}
	a.progressMu.Unlock()
	if ok {
		a.seeProgress(id, p, true)
	}
}

// setStage は進捗の段階を stage にする。入手の後の段階なので、長さは出さない。
func (a *App) setStage(id int64, stage domain.Stage) {
	a.putProgress(id, Progress{Stage: stage})
}

func (a *App) putProgress(id int64, p Progress) {
	a.progressMu.Lock()
	if a.progress == nil {
		a.progress = map[int64]Progress{}
	}
	a.progress[id] = p
	a.progressMu.Unlock()
	a.seeProgress(id, p, true)
}

// clearProgress は、試行が終わった（成功でも失敗でも）ジョブの進捗を消す。
func (a *App) clearProgress(id int64) {
	a.progressMu.Lock()
	delete(a.progress, id)
	a.progressMu.Unlock()
	a.seeProgress(id, Progress{}, false)
}

func (a *App) seeProgress(id int64, p Progress, ok bool) {
	if a.progressSeen != nil {
		a.progressSeen(id, p, ok)
	}
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// markSaved は、ジョブを保存した時刻を DB と /metrics に記録する。記録できなくても保存は済んでいるので、Warn に留める。
func (a *App) markSaved(ctx context.Context, log *slog.Logger) {
	now := a.now()
	a.Metrics.SetLastSaved(now)
	if err := a.Store.SetLastSavedAt(ctx, now); err != nil {
		log.Warn("最後に保存した時刻を DB に記録できない", "err", err)
	}
}

// LoadMetrics は、DB に残した最後にジョブを保存した時刻を /metrics に読み込む（再起動で 0 に戻さないため）。
func (a *App) LoadMetrics(ctx context.Context) error {
	t, err := a.Store.LastSavedAt(ctx)
	if err != nil {
		return err
	}
	a.Metrics.SetLastSaved(t)
	return nil
}

func (a *App) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// Serve は展開役を起こし、起動時の処理をしてから、ctx が取り消されるまで録音のループ（ジョブの実行と通知の判定）を
// 繰り返す。番組表の同期と展開は展開役が行う（runExpander）。ctx が取り消されたら、展開役が止まるのを待って nil を返す。
// 展開役がもう動いていれば（RunExpander を別に起こした）エラーを返す。
func (a *App) Serve(ctx context.Context) error {
	// 最初の周が始まる前（起動時の同期の間）は、起動時刻から数える。
	a.markPoll()
	if !a.expanderRunning.CompareAndSwap(false, true) {
		return errExpanderRunning
	}
	ectx, cancel := context.WithCancel(ctx)
	expanderDone := make(chan struct{})
	go func() {
		defer close(expanderDone)
		defer a.expanderRunning.Store(false)
		a.runExpander(ectx)
	}()
	defer func() {
		cancel()
		<-expanderDone
	}()
	if err := a.startupRecover(ctx); err != nil {
		return err
	}
	// ライブの録音役は、回復と partial の掃除の後、番組表の同期を待たずに起こす（録っている途中で止まったライブ録音を
	// すぐに繋ぎ直すため）。
	liveDone := make(chan struct{})
	go func() {
		defer close(liveDone)
		a.runLive(ectx)
	}()
	defer func() {
		cancel()
		<-liveDone
	}()
	if err := a.startupSync(ctx); err != nil {
		return err
	}
	// バックアップの見回りは録音のループと別の goroutine（長い録音の間も、取る時期を見逃さない）。
	backupsDone := make(chan struct{})
	go func() {
		defer close(backupsDone)
		a.runBackups(ectx)
	}()
	defer func() {
		cancel()
		<-backupsDone
	}()
	pollT := time.NewTicker(cmp.Or(a.PollInterval, DefaultPollInterval))
	defer pollT.Stop()
	a.initExpander()
	a.pollRound(ctx)
	for {
		select {
		case <-ctx.Done():
			a.log().Info("終了する")
			return nil
		case <-pollT.C:
			a.pollRound(ctx)
		case <-a.recordWake:
			a.pollRound(ctx)
		}
	}
}

// pollRound は録音のループの 1 回の見回り。実行するジョブが無くなる（RunDue が false かエラーを返す）まで、ジョブの実行
// （runDueLogged）と通知の判定（checkNotificationsLogged）を 1 件ごとに繰り返す。録り終えた知らせが後のジョブを待たずに
// 届き、/healthz の最後に 1 周した時刻（LastPoll）も 1 件ごとに付け直される。繰り返しの合間に ctx が取り消されていれば抜ける。
// 失敗したジョブの次の試行は最短でも retryIntervals の後で、保存先が空なら RunDue はジョブを取らないので、同じジョブを回し続けない。
func (a *App) pollRound(ctx context.Context) {
	for {
		ran := a.runDueLogged(ctx)
		a.checkNotificationsLogged(ctx)
		if !ran || ctx.Err() != nil {
			return
		}
	}
}

// WakeRecorder は、録音のループを次の見回りを待たずに起こす（人の操作でジョブが録れるようになったときに呼ぶ。Web UI・API の
// ジョブの「再試行」「録る」と、展開役の展開・試し直しの後）。送るときに塞がっていれば捨てるので、録音中に何度起こしても
// 1 回分にまとまる。起こして実行するジョブが無ければ、RunDue が空振りするだけ。
func (a *App) WakeRecorder() {
	a.initExpander()
	select {
	case a.recordWake <- struct{}{}:
	default:
	}
}

// errExpanderRunning は、展開役をもう 1 本起こそうとしたときのエラー。
var errExpanderRunning = errors.New("展開役はもう動いている")

// expandTask は、展開役に頼んで終わるまで待つ処理（起動時の同期、最初の試行の前の番組表の取り直し）。
type expandTask struct {
	fn   func(ctx context.Context) error
	done chan error
}

// retryTarget は、展開の依頼で展開できず、試し直しを待っている局・放送日。retries はもう試し直した回数、at は次に
// 試し直す時刻。
type retryTarget struct {
	expandTarget
	retries int
	at      time.Time
}

// syncOutcome は、番組表の同期で局・放送日を展開し終えた結果。failed は展開できなかった局・放送日（targetKey）。
// 同期の対象にした局・放送日のうち failed に無いものは展開でき、同期の対象でなかった局・放送日はどれも failed に無い。
type syncOutcome struct {
	failed map[string]bool
}

// targetKey は局・放送日を 1 つの文字列にする（syncOutcome.failed のキー）。
func targetKey(station string, day domain.BroadcastDay) string {
	return station + "/" + day.String()
}

// retryDelay は、試し直しを retries 回した後に、次に試し直すまでの間。
func (a *App) retryDelay(retries int) time.Duration {
	delays := a.ExpandRetryDelays
	if len(delays) == 0 {
		delays = DefaultExpandRetryDelays
	}
	return delays[min(retries, len(delays)-1)]
}

func (a *App) initExpander() {
	a.expanderInit.Do(func() {
		a.expandTasks = make(chan expandTask)
		a.expandWake = make(chan struct{}, 1)
		a.recordWake = make(chan struct{}, 1)
	})
}

// RunExpander は展開役を動かし、ctx が取り消されるまで戻らない。Serve が自分で起こすので、Serve を使うときは
// 呼ばない（Serve を使わずに展開役を動かすとき（テストなど）に使う）。展開役がもう動いていればエラーを返す。
func (a *App) RunExpander(ctx context.Context) error {
	if !a.expanderRunning.CompareAndSwap(false, true) {
		return errExpanderRunning
	}
	defer a.expanderRunning.Store(false)
	a.runExpander(ctx)
	return nil
}

// runExpander は展開役（goroutine 1 本）の中身。番組表の同期と展開（syncDay）は、ここから呼ぶ処理だけが行うので、
// 2 つの展開が同時に走らない。順に処理するのは次の 4 つ。
//
//   - 展開の依頼（RequestExpand。expandRequested）
//   - 展開の依頼で展開できなかった局・放送日の試し直し（retryExpand。間は ExpandRetryDelays で延ばしていく）
//   - 番組表の同期（syncOnExpander）。起動時（Startup が onExpander で頼む）と SyncInterval ごと
//   - 最初の試行の前の番組表の取り直し（RunDue が onExpander で頼む）
func (a *App) runExpander(ctx context.Context) {
	a.initExpander()
	syncT := time.NewTicker(cmp.Or(a.SyncInterval, DefaultSyncInterval))
	defer syncT.Stop()
	for {
		var retryT *time.Timer
		var retryC <-chan time.Time
		if len(a.expandRetries) > 0 {
			next := a.expandRetries[0].at
			for _, r := range a.expandRetries[1:] {
				if r.at.Before(next) {
					next = r.at
				}
			}
			retryT = time.NewTimer(time.Until(next))
			retryC = retryT.C
		}
		select {
		case <-ctx.Done():
		case t := <-a.expandTasks:
			t.done <- t.fn(ctx)
		case <-syncT.C:
			a.syncOnExpander(ctx)
		case <-a.expandWake:
			a.expandRequested(ctx)
			a.WakeRecorder()
		case <-retryC:
			a.retryExpand(ctx)
			a.WakeRecorder()
		}
		if retryT != nil {
			retryT.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// syncOnExpander は番組表を同期する（syncLogged）。展開役で呼ぶ。同期が局・放送日を展開し終えたら、試し直しを
// 待っている局・放送日（その同期より前に積まれた依頼で展開できなかったもの）のうち、次の 2 つを捨て、捨てたことを
// ログに 1 行出す。
//
//   - 同期で展開できた局・放送日（同期が拾った）
//   - 同期の対象でなかった局・放送日（消した番組・無効にした番組の局、同期が見ない過去の放送日。これらの waiting の
//     ジョブは、最初の試行の前の取り直しで外れる）
//
// 同期でも展開できなかった局・放送日は、試し直しを続ける（今の回数と時刻のまま。次に同期で展開できたら捨てる）。
// 同期がどの局・放送日も展開できなかったときも同じ規則で扱う（同期の対象は全部、同期でも展開できなかった側になる）。
// 同期が局・放送日の展開まで進まなかったとき（DB を読めないなど）は、何も捨てない。まだ展開していない依頼
// （expandRequests）は捨てない。
func (a *App) syncOnExpander(ctx context.Context) {
	a.syncLogged(ctx)
	outcome := a.lastSync
	if ctx.Err() != nil || outcome == nil || len(a.expandRetries) == 0 {
		return
	}
	var kept []*retryTarget
	for _, r := range a.expandRetries {
		if outcome.failed[targetKey(r.station, r.day)] {
			kept = append(kept, r)
		}
	}
	if dropped := len(a.expandRetries) - len(kept); dropped > 0 {
		a.log().Info("番組表を同期したので、展開できなかった局・放送日の試し直しをやめる（同期で展開できたか、同期の対象でない）",
			"dropped", dropped, "kept", len(kept))
	}
	a.expandRetries = kept
}

// onExpander は fn を展開役に頼み、終わるまで待って fn のエラーを返す。fn は展開役の中で、展開役の ctx で動く。
// 待っている間に ctx が取り消されたら ctx.Err() を返す（動き出した fn は展開役で最後まで動く）。
// 展開役が動いていなければ、動き出すか ctx が取り消されるまで待つ。
func (a *App) onExpander(ctx context.Context, fn func(ctx context.Context) error) error {
	a.initExpander()
	t := expandTask{fn: fn, done: make(chan error, 1)}
	select {
	case a.expandTasks <- t:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-t.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RequestExpand は、番組を変えた直後の展開を展開役に頼み、展開を待たずに戻る（Web UI・API が、変える前と後の
// 番組を渡して呼ぶ）。展開役は、番組 shows の予約に出てくる局・放送日を展開する（expandShows）。
// 依頼はプロセスの中に積むだけなので、serve を再起動すると消える（起動時の同期で拾い直す）。
func (a *App) RequestExpand(shows ...domain.Show) {
	if len(shows) == 0 {
		return
	}
	a.initExpander()
	a.expandMu.Lock()
	a.expandRequests = append(a.expandRequests, shows...)
	a.expandMu.Unlock()
	select {
	case a.expandWake <- struct{}{}:
	default:
	}
}

// ExpandPending は、積まれている展開の依頼を展開役で今すぐ展開し、終わるまで待つ（テストのため）。
// 失敗した依頼は、ほかの依頼と同じく試し直しに回し、エラーは返さない。
func (a *App) ExpandPending(ctx context.Context) error {
	return a.onExpander(ctx, func(ctx context.Context) error {
		a.expandRequested(ctx)
		return nil
	})
}

// expandRequested は、積まれている展開の依頼を全部取り、1 回の展開にまとめて行う。展開役で呼ぶ。
// キーワードを当てる依頼（RequestKeywords）も積まれていれば、先にキーワードを当て、足した先の番組も一緒に展開する。
func (a *App) expandRequested(ctx context.Context) {
	a.expandMu.Lock()
	shows := a.expandRequests
	keywords := a.keywordsRequested
	a.expandRequests, a.keywordsRequested = nil, false
	a.expandMu.Unlock()
	if keywords {
		for _, name := range a.applyKeywordsLogged(ctx) {
			r, err := a.Store.GetShow(ctx, name)
			if err != nil {
				if ctx.Err() == nil {
					a.log().Error("キーワードが足した先の番組を読めない（次の番組表の同期で展開する）", "show", name, "err", err)
				}
				continue
			}
			shows = append(shows, r)
		}
	}
	a.expandLogged(ctx, shows)
}

// retryExpand は、試し直す時刻を過ぎた局・放送日を試し直す。展開役で呼ぶ。また展開できなければ、間を延ばして
// 次の時刻を決め、試し直しの回数と次の時刻を Warn で出す。
func (a *App) retryExpand(ctx context.Context) {
	now := time.Now()
	var rest []*retryTarget
	expanded := false
	for _, r := range a.expandRetries {
		if r.at.After(now) {
			rest = append(rest, r)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		r.retries++
		err := a.expandTargetDay(ctx, r.expandTarget)
		if err == nil {
			a.log().Info("展開できなかった局・放送日を試し直して展開した", "station", r.station, "day", r.day, "retries", r.retries)
			expanded = true
			continue
		}
		if ctx.Err() != nil {
			return
		}
		r.at = time.Now().Add(a.retryDelay(r.retries))
		a.log().Warn("展開できなかった局・放送日を試し直したが、また展開できない", "station", r.station, "day", r.day,
			"retries", r.retries, "next", r.at, "err", err)
		rest = append(rest, r)
	}
	a.expandRetries = rest
	// 試し直しで展開できた局・放送日があれば、そこで見つけた当たらない日を通知する（試し直し 1 回で 1 通）。
	if expanded {
		a.notifyUnmatchedDays(ctx)
	}
}

// expandLogged は番組 shows の局・放送日を展開する。展開役で呼ぶ。展開できなかった局・放送日は、その局・
// 放送日だけを、間を延ばしながら試し直す（retryDelay。次の番組表の同期で展開できたか、同期の対象でなければ捨てる。
// syncOnExpander）。
// 局・放送日に分けられない失敗（DB を読めない）はログに出すだけで、次の番組表の同期で拾う。
func (a *App) expandLogged(ctx context.Context, shows []domain.Show) {
	if len(shows) == 0 {
		return
	}
	failures, err := a.expandShowDays(ctx, shows...)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		a.log().Error("番組を変えた直後の展開ができない（次の番組表の同期で拾う）", "err", err)
		return
	}
	for _, f := range failures {
		r, added := a.queueRetry(f.expandTarget)
		if !added {
			continue
		}
		a.log().Warn("番組を変えた直後の展開で、局・放送日を展開できない。後で試し直す", "station", f.station, "day", f.day,
			"retries", 0, "next", r.at, "err", f.err)
	}
}

// queueRetry は局・放送日 t を試し直しに積み、その試し直しを返す（added は新しく積んだか）。展開役で呼ぶ。もう試し直しを
// 待っている局・放送日は、今の回数と時刻のまま待たせる（積み直さない）。
func (a *App) queueRetry(t expandTarget) (r *retryTarget, added bool) {
	if i := slices.IndexFunc(a.expandRetries, func(r *retryTarget) bool {
		return r.expandTarget == t
	}); i >= 0 {
		return a.expandRetries[i], false
	}
	r = &retryTarget{expandTarget: t, at: time.Now().Add(a.retryDelay(0))}
	a.expandRetries = append(a.expandRetries, r)
	return r, true
}

// retrying は、局・放送日 t が試し直しを待っているか。展開役で呼ぶ。
func (a *App) retrying(t expandTarget) bool {
	return slices.ContainsFunc(a.expandRetries, func(r *retryTarget) bool { return r.expandTarget == t })
}

// markPoll は、ジョブの実行の周が始まったことを記録する。
func (a *App) markPoll() { a.lastPoll.Store(a.now().UnixNano()) }

// LastPoll は、ジョブの実行の周が最後に始まった時刻を返す。Serve を始める前は零値。
// 周（RunDue を 1 回呼ぶごと）の始まりで記録するので、その途中（録音・通知が長引いている間や、最初の試行の前の番組表の取り直しが展開役の同期・展開の後で
// 待たされている間）は進まない。/healthz の判定の
// 時間は、それを見込んで長くしてある。
func (a *App) LastPoll() time.Time {
	n := a.lastPoll.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// runDueLogged は RunDue を 1 回呼び、ジョブを実行したら true を返す。エラーはログに出して false を返す。
func (a *App) runDueLogged(ctx context.Context) bool {
	a.markPoll()
	ran, err := a.RunDue(ctx)
	if err != nil {
		if ctx.Err() == nil {
			a.log().Error("ジョブを実行できない", "err", err)
		}
		return false
	}
	return ran
}

func (a *App) checkNotificationsLogged(ctx context.Context) {
	if err := a.CheckNotifications(ctx); err != nil && ctx.Err() == nil {
		a.log().Error("通知を判定できない", "err", err)
	}
}

// Startup は起動時の処理（回復、partial の掃除、番組表の同期）をする。同期は展開役に頼み、終わるまで待つ
// （展開役が動いている間に呼ぶ）。回復に失敗したらエラーを返す（ctx が切られたことによる失敗は除く。エラーにしない）。掃除と同期の失敗はログに出して続ける。
// Serve は、回復と掃除（startupRecover）と同期（startupSync）の間にライブの録音役を起こす。
func (a *App) Startup(ctx context.Context) error {
	if err := a.startupRecover(ctx); err != nil {
		return err
	}
	return a.startupSync(ctx)
}

// startupRecover は起動時の回復と partial の掃除をする。ライブで録るジョブは回復で戻さない（store.Store.Recover）。
func (a *App) startupRecover(ctx context.Context) error {
	n, err := a.Store.Recover(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// 止めたことによる失敗はエラーにしない（startupSync と同じ）。running のまま残り、次の起動で戻る。partial の掃除も飛ばし、次の起動で行う。
			return nil
		}
		return fmt.Errorf("running のジョブを戻せない: %w", err)
	}
	if n > 0 {
		a.log().Info("前回の途中のジョブを retrying に戻した", "count", n)
	}
	if lib := a.current().library; lib == "" {
		a.log().Warn("保存先が決まっていないので、決まるまで録音を待たせる（Web UI で library を決める）")
	} else if n, err := library.CleanPartials(lib); err != nil {
		a.log().Warn("保存先の partial を消しきれない", "removed", n, "err", err)
	} else if n > 0 {
		a.log().Info("保存先の partial を消した", "count", n)
	}
	return nil
}

// startupSync は起動時の番組表の同期を展開役に頼み、終わるまで待つ。
func (a *App) startupSync(ctx context.Context) error {
	if err := a.onExpander(ctx, func(ctx context.Context) error {
		a.syncOnExpander(ctx)
		return nil
	}); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// refreshStations は局の情報を取り直す。取れなければ前の値のまま。
func (a *App) refreshStations(ctx context.Context) {
	ss, err := a.Guide.Stations(ctx)
	if err != nil {
		a.log().Warn("局の情報を取れない", "err", err)
		return
	}
	m := make(map[string]Station, len(ss))
	for _, s := range ss {
		m[s.ID] = s
	}
	a.stationsMu.Lock()
	a.stations = m
	a.stationsMu.Unlock()
}

// lookupStation は最後に取れた局の情報から id の局を引く。loaded は局一覧が一度でも取れているか、
// listed はその局が載っているか。
func (a *App) lookupStation(id string) (s Station, loaded, listed bool) {
	a.stationsMu.Lock()
	defer a.stationsMu.Unlock()
	s, listed = a.stations[id]
	return s, a.stations != nil, listed
}

func (a *App) station(ctx context.Context, id string) Station {
	if _, loaded, _ := a.lookupStation(id); !loaded {
		a.refreshStations(ctx)
	}
	s, _, ok := a.lookupStation(id)
	if !ok {
		s = Station{ID: id}
	}
	return s
}

// stationName は保存先の局名を返す。局一覧が一度も取れていない（a.stations == nil のまま）ときは
// known が false で、局 ID を返す。載っていない局や局名が空の局は、局 ID を返して known は true。
func (a *App) stationName(ctx context.Context, id string) (name string, known bool) {
	s := a.station(ctx, id)
	_, loaded, listed := a.lookupStation(id)
	if !loaded {
		return id, false
	}
	if !listed {
		a.log().Warn("局の一覧に載っていないので、局 ID で保存する", "station", id)
	}
	return cmp.Or(s.Name, id), true
}

// Sync は番組表を同期する。展開役で呼ぶ（syncLogged）。局・放送日ごとの失敗はログに出して続ける。
// どの局・放送日の番組表も取れなかったときは、同期の失敗としてエラーを返す（2 回続けば通知する）。
// 局・放送日を展開し終えたら、展開できなかった局・放送日を lastSync に残す（syncOnExpander が試し直しを選ぶ）。
// 局・放送日の展開まで進まなかったとき（DB を読めない、ctx の取り消し）は lastSync を nil にする。
func (a *App) Sync(ctx context.Context) error {
	a.lastSync = nil
	now := a.now()
	if n, err := a.Store.DeleteExpiredOnces(ctx, now, a.Membership); err != nil {
		return fmt.Errorf("録音期限を過ぎた 1 回の予約を消せない: %w", err)
	} else if n > 0 {
		a.log().Info("録音期限を過ぎた 1 回の予約を消した", "count", n)
	}
	if n, err := a.Store.DeleteStaleUnmatchedDays(ctx, domain.BroadcastDayOf(now)); err != nil {
		return fmt.Errorf("過ぎた・有効な番組でない当たらない日の記録を消せない: %w", err)
	} else if n > 0 {
		a.log().Info("過ぎた・有効な番組でない当たらない日の記録を消した", "count", n)
	}
	// キーワードを当ててから番組を読む（足した 1 回の予約の局・放送日も、この同期で展開する）。
	a.applyKeywordsLogged(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	shows, err := a.Store.ListShows(ctx)
	if err != nil {
		return err
	}
	var enabled []domain.Show
	for _, s := range shows {
		if !s.Disabled {
			enabled = append(enabled, s)
		}
	}
	stations := showStations(enabled)
	if len(stations) == 0 {
		a.log().Info("有効な番組の予約が無いので番組表を同期しない")
		a.lastSync = &syncOutcome{}
		return nil
	}
	a.refreshStations(ctx)

	today := domain.BroadcastDayOf(now)
	var days []domain.BroadcastDay
	waiting, err := a.Store.ListJobs(ctx, domain.StateWaiting, domain.StateSkipped)
	if err != nil {
		return err
	}
	for _, j := range waiting {
		if j.Expandable() && j.Day.Start().Before(today.Start()) && now.Before(j.Deadline) && !slices.Contains(days, j.Day) {
			days = append(days, j.Day)
		}
	}
	for _, d := range append(a.pastOnceDays(now, enabled), domain.PastWeeklyDays(now, a.Membership, enabled)...) {
		if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	slices.SortFunc(days, func(x, y domain.BroadcastDay) int { return x.Start().Compare(y.Start()) })
	for i := 0; i <= syncDaysAhead; i++ {
		days = append(days, today.AddDays(i))
	}

	failed := 0
	outcome := &syncOutcome{failed: map[string]bool{}}
	for _, day := range days {
		for _, st := range stations {
			if err := a.syncDay(ctx, st, day); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failed++
				outcome.failed[targetKey(st, day)] = true
				a.log().Warn("番組表を展開できない", "station", st, "day", day, "err", err)
			}
		}
	}
	a.lastSync = outcome
	a.notifyUnmatchedDays(ctx)
	if total := len(stations) * len(days); failed == total {
		return fmt.Errorf("どの局・放送日の番組表も取れない（%d 件）", total)
	}
	a.log().Info("番組表を同期した", "stations", len(stations), "days", len(days), "failed", failed)
	return nil
}

// showStations は番組の予約（毎週と 1 回）に出てくる局を、重ならないように名前の順で返す。
// 無効な番組も数える（呼び出し側で選ぶ）。Sync は有効な番組だけを渡すので、無効な番組の予約にしか出てこない
// 局は、番組を消したときと同じく同期しない（その局の waiting のジョブは、最初の試行の前の
// 番組表の取り直しで消える）。
func showStations(shows []domain.Show) []string {
	var out []string
	add := func(st string) {
		if !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	for _, s := range shows {
		for _, r := range s.Weekly {
			add(r.StationID)
		}
		for _, o := range s.Onces {
			add(o.StationID)
		}
	}
	slices.Sort(out)
	return out
}

// pastOnceDays は番組 shows の 1 回の予約のうち、今日より前で録音期限の内側にある放送日を、重ならないように返す。
func (a *App) pastOnceDays(now time.Time, shows []domain.Show) []domain.BroadcastDay {
	today := domain.BroadcastDayOf(now)
	var out []domain.BroadcastDay
	for _, s := range shows {
		for _, o := range s.Onces {
			if d := o.Day(); d.Start().Before(today.Start()) && now.Before(o.Deadline(a.Membership)) && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// expandShows は、番組 shows の予約に出てくる局について、今日から 7 日先までの放送日と、
// 1 回の予約の過去の放送日（録音期限の内側）と、有効な番組の開始日を持つ毎週の予約の過去の放送日（開始日から、
// 録音期限の内側。domain.PastWeeklyDays。無効にした番組を有効に戻したときも、まだ録れる放送回をジョブにする）と、
// shows の名前の展開が触るジョブ（waiting と確定の前の skipped）がある過去の放送日（録音期限の内側。そのジョブの放送回の局も足す）を展開する。展開役で呼ぶ（RequestExpand で積まれた依頼。Web UI・API が、
// 番組を変えた直後に、変える前と後の番組を渡して積む）。無効な番組の予約の局・放送日も展開する（無効にした・消した番組の
// waiting のジョブを、放送済みで最初の試行を待つものも含めて消すため）。
// 過去の放送日の新しいジョブの作り方は Sync と同じ（domain.Show.Hit。Web UI・API で
// 名前を変えた番組の done でないジョブは、store.ReplaceShow が新しい名前に付け替えるので、展開では消えない）。
// 展開に使う番組の一覧は shows ではなく、store から読む今の番組の一覧（syncDay）。
// 局・放送日ごとの失敗は、まとめてエラーで返す。
func (a *App) expandShows(ctx context.Context, shows ...domain.Show) error {
	failures, err := a.expandShowDays(ctx, shows...)
	if err != nil {
		return err
	}
	errs := make([]error, 0, len(failures))
	for _, f := range failures {
		errs = append(errs, fmt.Errorf("局 %s の放送日 %s: %w", f.station, f.day, f.err))
	}
	return errors.Join(errs...)
}

// expandShowDays は expandShows の中身。展開できなかった局・放送日を返す。err は、局・放送日に
// 分けられない失敗（DB を読めない、ctx の取り消し）。
func (a *App) expandShowDays(ctx context.Context, shows ...domain.Show) ([]expandFailure, error) {
	stations := showStations(shows)
	now := a.now()
	today := domain.BroadcastDayOf(now)
	days := a.pastOnceDays(now, shows)
	for _, d := range domain.PastWeeklyDays(now, a.Membership, shows) {
		if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	waiting, err := a.Store.ListJobs(ctx, domain.StateWaiting, domain.StateSkipped)
	if err != nil {
		return nil, err
	}
	for _, j := range waiting {
		if !j.Expandable() || !slices.ContainsFunc(shows, func(s domain.Show) bool { return s.Name == j.ShowName }) ||
			!j.Day.Start().Before(today.Start()) || !now.Before(j.Deadline) {
			continue
		}
		if !slices.Contains(days, j.Day) {
			days = append(days, j.Day)
		}
		for _, ar := range j.Airings {
			if !slices.Contains(stations, ar.StationID) {
				stations = append(stations, ar.StationID)
			}
		}
	}
	if len(stations) == 0 {
		return nil, nil
	}
	slices.Sort(stations)
	slices.SortFunc(days, func(x, y domain.BroadcastDay) int { return x.Start().Compare(y.Start()) })
	for i := 0; i <= syncDaysAhead; i++ {
		days = append(days, today.AddDays(i))
	}
	var failures []expandFailure
	for _, day := range days {
		for _, st := range stations {
			target := expandTarget{station: st, day: day}
			if err := a.expandTargetDay(ctx, target); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				failures = append(failures, expandFailure{expandTarget: target, err: err})
			}
		}
	}
	a.notifyUnmatchedDays(ctx)
	return failures, nil
}

// expandTarget は展開する局・放送日。その日が今日より前かどうかは、展開するときの今日で決める（syncDay。
// 試し直しの間に日をまたぐと、今日だった放送日も今日より前の扱いになる）。
type expandTarget struct {
	station string
	day     domain.BroadcastDay
}

// expandFailure は展開できなかった局・放送日と、そのエラー。
type expandFailure struct {
	expandTarget
	err error
}

// expandTargetDay は局・放送日 t を、同期と同じやり方で展開する。展開役で呼ぶ。番組表を取れなかった・空だったら
// 展開せずにエラーを返す。今日より前の放送日の新しいジョブの作り方も同期と同じ（展開するときの今日で決める。syncDay）。
func (a *App) expandTargetDay(ctx context.Context, t expandTarget) error {
	return a.syncDay(ctx, t.station, t.day)
}

// syncDay は局 station の放送日 day の番組表を取り、展開して差分を当てる。展開役でだけ呼ぶ（Sync・expandTargetDay・
// refreshBeforeFirstAttempt から）。番組の一覧は、番組表を取った後に
// store から読み直す（番組表を取っている間に番組が変わっても、変わった後の番組で展開する）。
// 放送回への当たり方（今日より前の放送日の扱い）は、展開するときの今の時刻で決める（domain.ExpandInput.Now。domain.Show.Hit）。
// rajikura が録るか録らないかを決めたら、ジョブの出来事として通知する（人が決めたジョブは展開が状態を変えないので、人の操作では
// 通知しない）。確定の前に録音予定（新しく作ったジョブを含む）を録らない予定にしたら notifySkipPlanned、録らない予定を
// 録音予定に戻したら notifyUnskipped、確定した録らなかった（今日より前の放送日で作った、または最初の試行の時刻を過ぎた展開で
// 録音予定から録らないにした）にしたら notifySkipped。録らない予定のジョブがそのまま確定したときは通知しない（もう知らせた）。
// 確定したジョブは展開が触らないので、同期を繰り返しても通知は重ならない。番組名が行き来すれば、そのたびに通知する。
// 展開した後、同じ番組表と番組の一覧から当たらない日を求め、その局・放送日の記録を揃える（recordUnmatchedDays。通知は呼び出し側が
// 同期・展開の終わりにまとめて書く）。
func (a *App) syncDay(ctx context.Context, station string, day domain.BroadcastDay) error {
	airings, err := a.Guide.Airings(ctx, station, day)
	if err != nil {
		return err
	}
	if len(airings) == 0 {
		return errors.New("番組表が空")
	}
	shows, err := a.Store.ListShows(ctx)
	if err != nil {
		return err
	}
	in := domain.ExpandInput{
		Shows:      shows,
		StationID:  station,
		Day:        day,
		Airings:    airings,
		Membership: a.Membership,
		TFMaxDelay: a.station(ctx, station).TFMaxDelay,
		Now:        a.now(),
	}
	plan, ids, err := a.Store.ExpandDay(ctx, day, func(jobs []domain.Job) domain.Plan {
		in.Jobs = jobs
		return domain.Expand(in)
	})
	if err != nil {
		return err
	}
	a.recordUnmatchedDays(ctx, station, day, airings, shows)
	log := a.log().With("station", station, "day", day)
	for _, c := range plan.Conflicts {
		log.Warn("展開の矛盾", "airing", c.Airing.Title, "start", c.Airing.Start, "shows", c.Shows, "reason", c.Reason)
	}
	for i, j := range plan.Create {
		log.Info("ジョブを作った", "job", ids[i], "show", j.ShowName, "part", j.Part, "state", j.State, "settled", j.Settled, "airings", len(j.Airings), "next", j.NextAttemptAt)
		if j.State == domain.StateSkipped {
			j.ID = ids[i]
			if j.Settled {
				a.notifySkipped(ctx, log.With("job", j.ID), j)
			} else {
				a.notifySkipPlanned(ctx, log.With("job", j.ID), j)
			}
		}
	}
	before := map[int64]domain.State{}
	for _, j := range in.Jobs {
		before[j.ID] = j.State
	}
	for _, j := range plan.Update {
		log.Info("ジョブを更新した", "job", j.ID, "show", j.ShowName, "part", j.Part, "state", j.State, "settled", j.Settled, "airings", len(j.Airings), "next", j.NextAttemptAt)
		switch from := before[j.ID]; {
		case from == domain.StateWaiting && j.State == domain.StateSkipped && j.Settled:
			a.notifySkipped(ctx, log.With("job", j.ID), j)
		case from == domain.StateWaiting && j.State == domain.StateSkipped:
			a.notifySkipPlanned(ctx, log.With("job", j.ID), j)
		case from == domain.StateSkipped && j.State == domain.StateWaiting:
			a.notifyUnskipped(ctx, log.With("job", j.ID), j)
		}
	}
	for _, j := range plan.Delete {
		log.Info("ジョブを消した（放送回が無くなった）", "job", j.ID, "show", j.ShowName, "part", j.Part)
	}
	return nil
}

// RunDue は実行するジョブ（タイムフリーから録るもの。ライブで録るジョブはライブの録音役が拾う）を 1 件だけ取って実行する。
// 実行したら true。録音のループから、false を返すまで繰り返し呼ぶ（pollRound）。最初の試行の前の
// 番組表の取り直しは展開役に頼み、終わるまで待ってから録音する（展開役が動いている間に呼ぶ）。ジョブを取る前に、チャプターを
// 待っているジョブのチャプターの API を引く（checkChapters。同じ goroutine で、新しい goroutine は起こさない）。
// ジョブの失敗は試行として記録し、エラーにはしない。DB の失敗などはエラーを返す。
// 保存先が決まっていなければ、ジョブを取らずに false を返す（録音だけを待たせる。番組表の同期と展開は展開役が続ける）。
// 試行は、ジョブを取ったときの設定の写しで最後まで進める（runJob）。
func (a *App) RunDue(ctx context.Context) (bool, error) {
	if a.current().library == "" {
		return false, nil
	}
	if err := a.onExpander(ctx, a.refreshBeforeFirstAttempt); err != nil {
		return false, err
	}
	// チャプターを待っているジョブの API を引き、できていればチャプターの取り直しの試行を起こす（すぐ下の ClaimDue で拾う。ADR 0027）。
	if err := a.checkChapters(ctx, a.current()); err != nil {
		return false, err
	}
	job, ok, err := a.Store.ClaimDue(ctx, a.now())
	if err != nil || !ok {
		return false, err
	}
	// 保存先は一度決まったら空にはできない（config.Apply）ので、ここで読む写しにも保存先がある。
	return true, a.runJob(ctx, a.current(), job)
}

// refreshBeforeFirstAttempt は展開役で動く（RunDue が onExpander で頼む）。次に取るジョブ（タイムフリーから録るもの。ライブで録る
// ジョブは除く）が最初の試行（waiting）なら、その放送日の番組表を取り直して
// 展開をやり直す。休止や時間の変更で次に取るジョブが変わったら、そのジョブについても繰り返す。
// 番組表を取れなければ、ログに出して今の放送回のまま進む。
// 放送日が今日より前なら、Sync と同じ当たり方で新しいジョブを作る（domain.Show.Hit。過去の放送日のジョブの
// 実行の直前に、開始日を持たない毎週の予約に当たる放送済みの回までジョブにしないため）。
// 取り直した番組表で番組名が食い違えば、展開が録音予定のジョブを確定した録らなかったにする（domain.Expand。通知は syncDay）。
//
// 先に、最初の試行の時刻を過ぎた録らない予定のジョブ（確定の前の skipped）を確定させる（settleDueSkipped。番組表を
// 取り直せなければ確定させずに待つ）。
func (a *App) refreshBeforeFirstAttempt(ctx context.Context) error {
	if a.refreshed == nil {
		a.refreshed = map[int64]bool{}
	}
	if err := a.settleDueSkipped(ctx); err != nil {
		return err
	}
	for range maxRefresh {
		jobs, err := a.Store.ListJobs(ctx, domain.StateWaiting, domain.StateRetrying)
		if err != nil {
			return err
		}
		now := a.now()
		var due *domain.Job
		for i, j := range jobs {
			if j.NextAttemptAt.After(now) || j.Live() {
				continue // ライブで録るジョブは、ライブの録音役が取り直す（live.go）
			}
			if due == nil || j.NextAttemptAt.Before(due.NextAttemptAt) ||
				j.NextAttemptAt.Equal(due.NextAttemptAt) && j.ID < due.ID {
				due = &jobs[i]
			}
		}
		if due == nil || due.State != domain.StateWaiting || a.refreshed[due.ID] {
			return nil
		}
		a.refreshed[due.ID] = true
		if _, err := a.refreshJobDay(ctx, *due); err != nil {
			return err
		}
		if _, err := a.Store.GetJob(ctx, due.ID); errors.Is(err, store.ErrNotFound) {
			a.log().Info("番組表を取り直したらジョブが消えた", "job", due.ID, "show", due.ShowName, "day", due.Day)
		} else if err != nil {
			return err
		}
	}
	return nil
}

// settleDueSkipped は、最初の試行の時刻を過ぎた録らない予定のジョブ（確定の前の skipped）を確定させる（ADR 0019）。
// 展開役で動く（refreshBeforeFirstAttempt から）。
//
//   - rajikura が決めたジョブは、その放送日の番組表を取り直して展開をやり直し、番組名が直っていれば展開が waiting に戻す
//     （そのまま最初の試行に進む。取り直しは 1 回だけ）。食い違ったままなら展開が確定させる。
//   - 取り直しで、ジョブの局のどれかの番組表を取れなかった（取れない・空）ときは、確定させずに録らない予定のまま残し、
//     取れなかった局・放送日を展開の試し直しに積む（queueRetry。expandLogged の失敗と同じ扱い）。ただし、取れた局の展開が
//     もう確定させた、または waiting に戻したなら、その結果に従う。確定は、番組表が取れた展開（試し直し・番組表の同期）に
//     任せる（毎分は取り直さない）。積んだ局・放送日の試し直しが無くなった（展開できた、または同期が拾った・捨てた）のに
//     まだ録らない予定なら、ここで確定させる。
//   - 録音期限（Job.Deadline）を過ぎた録らない予定は、取り直さずに確定させる（同期は録音期限の内側の放送日しか取らず、
//     試し直しも同期の対象でない局・放送日は捨てるので、待っても確定しない）。
//   - 人が録らないと決めたジョブは、取り直さずにそのまま確定させる。
//
// ここで確定したときは通知しない（録らない予定にしたときに知らせた。人が決めたものは通知しない）。
func (a *App) settleDueSkipped(ctx context.Context) error {
	now := a.now()
	jobs, err := a.Store.DueUnsettledSkipped(ctx, now)
	if err != nil {
		return err
	}
	// 確定を待っていたジョブのうち、もう録らない予定でない（展開が確定させた・waiting に戻した・消した）ものの印を捨てる。
	waits := map[int64][]expandTarget{}
	for _, j := range jobs {
		if t, ok := a.settleWaits[j.ID]; ok {
			waits[j.ID] = t
		}
	}
	a.settleWaits = waits
	for _, j := range jobs {
		log := a.log().With("job", j.ID, "show", j.ShowName, "day", j.Day)
		switch targets, waiting := a.settleWaits[j.ID]; {
		case !now.Before(j.Deadline):
			delete(a.settleWaits, j.ID)
			if err := a.settleSkipped(ctx, log, j, "録音期限を過ぎた"); err != nil {
				return err
			}
			continue
		case j.ByPerson:
		case waiting:
			if slices.ContainsFunc(targets, a.retrying) {
				continue // まだ番組表を取れていない
			}
			delete(a.settleWaits, j.ID)
		case !a.refreshed[j.ID]:
			a.refreshed[j.ID] = true
			failures, err := a.refreshJobDay(ctx, j)
			if err != nil {
				return err
			}
			cur, err := a.Store.GetJob(ctx, j.ID)
			if errors.Is(err, store.ErrNotFound) {
				log.Info("番組表を取り直したらジョブが消えた")
				continue
			}
			if err != nil {
				return err
			}
			if !cur.Expandable() || cur.State != domain.StateSkipped {
				continue // 展開が確定させた、または waiting に戻した
			}
			if len(failures) > 0 {
				var ts []expandTarget
				for _, f := range failures {
					r, _ := a.queueRetry(f.expandTarget)
					ts = append(ts, f.expandTarget)
					log.Warn("番組表を取り直せないので、録らない予定のジョブを確定させずに待つ。後で試し直す",
						"station", f.station, "retries", r.retries, "next", r.at, "deadline", j.Deadline, "err", f.err)
				}
				a.settleWaits[j.ID] = ts
				continue
			}
		}
		if err := a.settleSkipped(ctx, log, j, ""); err != nil {
			return err
		}
	}
	return nil
}

// settleSkipped は録らない予定のジョブ j を確定させ（store.SettleSkipped）、確定したらログに出す。why は確定させた理由（空なら出さない）。
func (a *App) settleSkipped(ctx context.Context, log *slog.Logger, j domain.Job, why string) error {
	ok, err := a.Store.SettleSkipped(ctx, j.ID)
	if err != nil {
		return err
	}
	if ok {
		args := []any{"by_person", j.ByPerson}
		if why != "" {
			args = append(args, "why", why)
		}
		log.Info("録らない予定のジョブを確定した（録らなかった）", args...)
	}
	return nil
}

// refreshJobDay は、ジョブ j の放送日の番組表を、ジョブの放送回の局ごとに取り直して展開をやり直す（syncDay）。展開役で呼ぶ。
// 番組表を取れなければ、ログに出して続け、取れなかった局・放送日を返す。返すエラーは ctx の取り消しだけ。
func (a *App) refreshJobDay(ctx context.Context, j domain.Job) ([]expandFailure, error) {
	var stations []string
	for _, ar := range j.Airings {
		if !slices.Contains(stations, ar.StationID) {
			stations = append(stations, ar.StationID)
		}
	}
	var failures []expandFailure
	for _, st := range stations {
		if err := a.syncDay(ctx, st, j.Day); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			a.log().Warn("最初の試行の前に番組表を取り直せない", "job", j.ID, "station", st, "day", j.Day, "err", err)
			failures = append(failures, expandFailure{expandTarget: expandTarget{station: st, day: j.Day}, err: err})
		}
	}
	a.notifyUnmatchedDays(ctx)
	return failures, nil
}

// jobDir はジョブの作業フォルダ。
func (a *App) jobDir(id int64) string {
	return filepath.Join(a.WorkDir, strconv.FormatInt(id, 10))
}

func (a *App) removeJobDir(log *slog.Logger, id int64) {
	if err := os.RemoveAll(a.jobDir(id)); err != nil {
		log.Warn("作業フォルダを消せない", "err", err)
	}
}
