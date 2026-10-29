package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/store"
)

// 通知（docs/notifications.md。ADR 0010・0011）。
//
// 通知を作る処理は、通知を DB の 1 行（store.Notification）として書くだけにする。外部の通知先（Notifier）には、
// 録音のループの周ごとに、まだ送れていない行を古い順に送る（deliverNotifications）。送ろうとして送れなかった行が
// 作ってから 1 時間を過ぎていれば、諦めて「送れなかった」の印を付け、rajikura_notify_failures_total{kind} を数える
// （通知先がある間に作った行は、プロセスが止まっていた間に 1 時間を過ぎても、通知先が動いていれば送る）。
// 通知先なし（Notifier が nil）のときに作った行は送らないものとして書き（後で通知先を選び直しても送らない）、
// 送れなかった扱いにもしない。録音済みは MuteSuccess なら送らない（DB には書く）。
//
// 通知の源は、ジョブの出来事・キーワードの出来事・番組の出来事・サービスの異常・テスト通知の 5 つ。キーワードの出来事（キーワードで録音予定にした・
// 上限で足すのを止めた）は、展開役がキーワードを当てたときに、足すのと同じトランザクションで書く（keyword.go。送るのは
// ほかの通知と同じく録音のループの周ごと。notify.success = false でも送る）。番組の出来事（毎週の予約の当たらない日を
// 新しく見つけた）は、展開役が番組表の同期 1 回・番組を変えた直後の展開 1 回の終わりに、番組ごとに 1 通にまとめ、当たらない日に
// 通知した印を立てるのと同じトランザクションで書く（unmatched.go。送るのは同じく録音のループの周ごと。notify.success = false でも送る）。サービスの異常のうちバックアップの失敗は、録音の
// ループではなく別の goroutine（runBackups）が作る（送るのは同じく録音のループの周ごと）。スリープから起こせない見込み
// （wakeAnomaly。live.go）はライブの録音役が、タイマーを掛けたときに作る。再スキャンの失敗（rescan。run.go）は、保存した後に
// 再スキャンを頼んだ者（タイムフリーの録音のループか、ライブの録音役の goroutine）が作る。その復旧のうち、頼む先が無くなったことで
// 閉じるもの（closeRescanIfGone）は、バックアップの失敗と同じく別の goroutine（runBackups）も作る。録音のループで作るのは次の 2 系統。
//
//   - ジョブの出来事: 録音済み（保存した。欠落ありを除く）・欠落あり（欠落ありで保存した。ライブ録音では欠けた長さを書く）・
//     録音できなかった（expired にした）・期限間近（録音期限の 24 時間前を過ぎても done / expired でないジョブ。ライブで録る
//     ジョブは除く。CheckNotifications が周ごとに判定し、DB に書けたらジョブに通知した時刻を立てる）。録音済み・欠落あり・
//     録音できなかったは、ライブで録るジョブではライブの録音役の goroutine（live.go）が書く。録音済みと録音できなかったの通知には、番組名の
//     食い違いを 2 行目に添える（check_title = false の番組は除く）。ライブで録るジョブが同時に録る本数の枠待ちだったときは、欠落あり・録音できなかったの
//     末尾に枠待ちの 1 行を足す（liveWaitLine）。保存・expired の後なので、ctx が取り消されていても書く。
//   - サービスの異常: 番組表の同期が 2 回続けて失敗した（syncLogged）・radiko がエリア外と判定した（authResult）。
//     復旧（同期が成功した・認証が通った）も通知する。バックアップの異常（取れない。BackupIfDue。backup.go）の復旧は、
//     次に取れたときと、取る先を空にして自動のバックアップを止めたときに書く。スリープから起こせない見込み（スリープ解除
//     タイマーの許可が有効でない・この PC はタイマーで起きられない。Windows だけ）の復旧は、次にライブで録るジョブの分の
//     タイマーを起こせる見込みで掛けられたときに書く（それまでは、タイマーを取り消しても続く）。再スキャンの失敗（頼めなかった・
//     上限を超えた）の復旧は、次に頼めたときと、頼む先（jellyfin.url。API キーを復号できないときも無い）が無いまま保存したときと、バックアップと同じ見回り（runBackups。1 時間ごと）が回ったときに書く。異常が続いているかは通知の行（始まりがあって復旧が無い）から
//     判定するので、再起動しても始まりを送り直さない。
//
// 足したら直す: サービスの異常
//
// ジョブの出来事のうち、rajikura が番組名の食い違いで録るか録らないかを決めたもの（録らない予定にした・録音予定に戻した・
// 録らなかった。ADR 0018・0019）は、録音のループではなく展開役が、展開の差分を当てたときに書く（app.go の syncDay。
// notifySkipPlanned・notifyUnskipped・notifySkipped）。確定したジョブは展開が触らないので、録らなかったは 1 回だけになる。
// 録らない予定のジョブがそのまま確定したときと、人が「録る」「録らない」を選んだときは通知しない。
// 録らない予定・録らなかったのジョブは期限間近の対象（waiting・running・retrying）に入らない。
//
// 再試行待ちは通知しない。通知の行は 90 日で消す（まだ復旧していないサービスの異常の始まりは残す）。
//
// テスト通知（SendTestNotification）だけは、録音のループを通さず、人が Web UI かトレイから押したその場で通知先に送り、
// 結果を付けた行を書く（ADR 0011）。通知先なしなら送らずに行だけ書く（ADR 0011）。

// NotifyKinds は、rajikura_notify_failures_total{kind} で数える通知の種類（notify.Kinds からテスト通知を除いたもの）。
// 系列を 0 で出しておくために使う。テスト通知は送れなくても数えない（人がその場で失敗を見ている）。
var NotifyKinds = slices.DeleteFunc(slices.Clone(notify.Kinds), func(k string) bool { return k == notify.KindTest })

// testNotificationMessage はテスト通知の本文。
const testNotificationMessage = "【テスト】rajikura からのテスト通知。"

// TestNotificationResult はテスト通知を送った結果。
type TestNotificationResult struct {
	// Notification は書いた行（ID 付き）。送れたら SentAt、送れなければ FailedAt が付く。
	// 通知先なし（NoChannel）なら、どちらも付かず、Deliver も付かない。
	Notification store.Notification
	// SendErr は通知先に送れなかったときのエラー（通知先のエラーは Webhook の URL を含まない）。送れたら nil。
	SendErr error
	// NoChannel は、通知先なし（notify.channel = "none"）なので送らずに行だけ書いたこと。
	NoChannel bool
}

// SendTestNotification は、テスト通知をその場で通知先に送り、結果を付けた行（既読）を DB に書く。送り直しはしない。
// notify.success = false（MuteSuccess）でも送る。送れなくても rajikura_notify_failures_total では数えない。
// 送れた行は、Web UI の「送れなかった通知」の帯を消す条件（最後に送れなかった時刻より後に送れた通知がある）に数える。
// 通知先なしなら送らずに行（既読。通知先に送る印も、送れた・送れなかったの印も付けない）だけを書き、NoChannel を返す。
// トレイは既読かどうかを見ずに新しい行をトーストにするので、通知先なしでもトレイに届くかを確かめられる。
// DB に書けなければエラーを返す。待ち時間の上限は通知先のもの（Webhook は 30 秒）をそのまま使う。
func (a *App) SendTestNotification(ctx context.Context) (TestNotificationResult, error) {
	notifier := a.current().notifier
	if notifier == nil {
		n := store.Notification{Kind: notify.KindTest, Message: testNotificationMessage, CreatedAt: a.now(), Read: true}
		id, err := a.Store.AddNotification(context.WithoutCancel(ctx), n)
		if err != nil {
			return TestNotificationResult{}, err
		}
		n.ID = id
		a.log().With("notification", id, "kind", notify.KindTest).Info("テスト通知を履歴に書いた（通知先なし）")
		return TestNotificationResult{Notification: n, NoChannel: true}, nil
	}
	n := store.Notification{Kind: notify.KindTest, Message: testNotificationMessage, CreatedAt: a.now(), Deliver: true, Read: true}
	sendErr := notifier.Notify(ctx, notify.KindTest, n.Message)
	if sendErr == nil {
		n.SentAt = a.now()
	} else {
		n.FailedAt = a.now()
	}
	// 送った後は、要求が取り消されていても結果を書く。
	id, err := a.Store.AddNotification(context.WithoutCancel(ctx), n)
	if err != nil {
		return TestNotificationResult{}, err
	}
	n.ID = id
	log := a.log().With("notification", id, "kind", notify.KindTest)
	if sendErr != nil {
		log.Warn("テスト通知を送れない", "err", sendErr)
	} else {
		log.Info("テスト通知を送った")
	}
	return TestNotificationResult{Notification: n, SendErr: sendErr}, nil
}

const (
	// deadlineNearWithin は、録音期限のどれだけ前から期限間近とするか。
	deadlineNearWithin = 24 * time.Hour
	// sendGiveUpAfter は、通知を作ってからどれだけたった後に送ろうとして送れなければ諦めるか。
	sendGiveUpAfter = time.Hour
	// notificationRetention は、通知の行を残す期間。
	notificationRetention = 90 * 24 * time.Hour
)

// newNotification は、今作る種類 kind の通知の行を返す。通知先に送るか（Deliver）もここで決める。
func (a *App) newNotification(kind, anomaly, msg string) store.Notification {
	cur := a.current()
	deliver := cur.notifier != nil && !(kind == notify.KindRecorded && cur.muteSuccess)
	return store.Notification{Kind: kind, Anomaly: anomaly, Message: msg, CreatedAt: a.now(), Deliver: deliver}
}

// addEvent はジョブ job の出来事の通知を DB に書く。通知にはジョブの id を持たせる（通知の画面とトーストがジョブの詳細を開く）。
// 保存や expired の後なので、ctx が取り消されていても書く。書けなければ Error でログに出し（本文も出す）、ジョブの処理は止めない。
func (a *App) addEvent(ctx context.Context, log *slog.Logger, job domain.Job, kind, msg string) {
	n := a.newNotification(kind, "", msg)
	n.JobID = job.ID
	id, err := a.Store.AddNotification(context.WithoutCancel(ctx), n)
	if err != nil {
		log.Error("通知を DB に書けない", "kind", kind, "message", msg, "err", err)
		return
	}
	log.Info("通知を書いた", "notification", id, "kind", kind, "message", msg)
}

// addAnomaly はサービスの異常 anomaly の始まり（kind が anomaly）か復旧（notify.KindRecovered）の通知を書く。
// 始まりは異常が続いていないときだけ、復旧は続いているときだけ書く（store.AddAnomalyNotification）。
// ctx が取り消されていたら書かない。書けなければ Error でログに出す。
func (a *App) addAnomaly(ctx context.Context, log *slog.Logger, kind, anomaly, msg string) {
	if ctx.Err() != nil {
		return
	}
	// ふだん（異常が無く、復旧を書くことも無い）は、書き込みのトランザクションを始めずに読むだけで済ませる。
	ongoing, err := a.Store.AnomalyOngoing(ctx, anomaly)
	if err == nil && ongoing != (kind == notify.KindRecovered) {
		return
	}
	added := false
	if err == nil {
		added, err = a.Store.AddAnomalyNotification(ctx, a.newNotification(kind, anomaly, msg))
	}
	if err != nil {
		if ctx.Err() == nil {
			log.Error("通知を DB に書けない", "kind", kind, "message", msg, "err", err)
		}
		return
	}
	if added {
		log.Info("通知を書いた", "kind", kind, "anomaly", anomaly, "message", msg)
	}
}

// jobLabel は通知の本文でジョブを指す言い方（番組名・パート・放送日）。
func jobLabel(j domain.Job) string {
	name := j.ShowName
	if j.Part > 0 {
		name = fmt.Sprintf("%s（パート %d）", name, j.Part)
	}
	return fmt.Sprintf("%s・放送日 %s", name, j.Day)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(domain.JST).Format("2006-01-02 15:04")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// notifyRecorded は、欠落なしで保存したことを通知する。length は保存したファイルの長さ。note は末尾に足す行（先頭の改行を
// 含む。ライブ録音の長さのずれ（liveLengthLine）。無ければ空）。
func (a *App) notifyRecorded(ctx context.Context, log *slog.Logger, job domain.Job, length time.Duration, note string) {
	msg := fmt.Sprintf("【録音済み】%s（%d 分）", jobLabel(job), int(length.Round(time.Minute)/time.Minute))
	msg += a.titleMismatch(ctx, log, job)
	a.addEvent(ctx, log, job, notify.KindRecorded, msg+note)
}

// notifyExpired はジョブが expired になった（録音できなかった）ことを通知する。cause は直近のエラー。note は末尾に足す行
// （先頭の改行を含む。ライブ録音の録れた分を残したこと（keptLines）。無ければ空）。
// 手動再試行の後にまた expired になれば、もう 1 度通知する。
// note が空なのは録れた分を残していない録音できなかったで、枠待ちの行のうち繋げなかった旨は、このときだけ付く（liveWaitLine）。
func (a *App) notifyExpired(ctx context.Context, log *slog.Logger, job domain.Job, cause, note string) {
	msg := fmt.Sprintf("【録音できなかった】%s を録音できず、expired にした（試行 %d 回、録音期限 %s）。",
		jobLabel(job), job.Attempts, fmtTime(job.Deadline))
	msg += a.titleMismatch(ctx, log, job)
	if cause != "" {
		msg += "\n直近のエラー: " + cause
	}
	a.addEvent(ctx, log, job, notify.KindExpired, msg+note+a.liveWaitLine(job.ID, note == ""))
}

// keptLines は、ライブ録音の録れた分を保存できずに expired にしたとき、録音できなかったの通知の本文に足す行（先頭の改行を
// 含む）。録れた分を残した場所（作業用フォルダ dir）、消す日時 keepUntil、保存からやり直す手順を書く。
func keptLines(job domain.Job, dir string, keepUntil time.Time) string {
	return fmt.Sprintf("\n録れた分は作業用フォルダ %s に残している（%s に消す）。保存先などの原因を直してから、"+
		"`rajikura retry %d` か、ジョブの画面の詳細の「手動で再試行」で、保存からやり直せる。", dir, fmtTime(keepUntil), job.ID)
}

// liveLengthLine は、ライブ録音で保存した長さが #EXTINF の合計と lengthTolerance を超えてずれていたとき、録音済みか欠落ありの
// 通知の本文に足す行（先頭の改行を含む）。ずれていなければ空。
func liveLengthLine(res saved) string {
	if !res.lengthOff {
		return ""
	}
	return fmt.Sprintf("\n長さのずれ: 保存した長さ %s が #EXTINF の合計 %s と %s ずれている（ライブ録音は取り直せないので、ずれたまま保存した）。",
		fmtLength(res.probe), fmtLength(res.extinf), fmtLength((res.probe - res.extinf).Abs()))
}

// notifySkipped は、展開が確定した録らなかった（skipped）のジョブにしたことを通知する。今日より前の放送日で作ったときと、
// 最初の試行の時刻を過ぎた展開で録音予定のジョブを録らないにしたとき（domain.Expand）。番組表の番組名が番組の name で
// 始まらない（domain.Show.Hit）。
func (a *App) notifySkipped(ctx context.Context, log *slog.Logger, job domain.Job) {
	msg := fmt.Sprintf("【録らなかった】%s は、放送済みの回で番組名が食い違うので録らなかった（録音期限 %s）。録るなら「録る」（手動で再試行）を選ぶ。",
		jobLabel(job), fmtTime(job.Deadline))
	msg += titleMismatchLine(job)
	a.addEvent(ctx, log, job, notify.KindSkipped, msg)
}

// notifySkipPlanned は、確定の前の展開が、録音予定のジョブ（新しく作ったジョブを含む）を録らない予定（確定の前の skipped）に
// したことを通知する。番組表の番組名が番組の name で始まらない（domain.Show.Hit）。
func (a *App) notifySkipPlanned(ctx context.Context, log *slog.Logger, job domain.Job) {
	msg := fmt.Sprintf("【録らない予定】%s は、番組名が食い違うので録らない予定にした（録りにいく時刻 %s）。録るなら「録る」を選ぶ。",
		jobLabel(job), fmtTime(job.NextAttemptAt))
	msg += titleMismatchLine(job)
	a.addEvent(ctx, log, job, notify.KindSkipPlanned, msg)
}

// notifyUnskipped は、確定の前の展開が、録らない予定のジョブを録音予定（waiting）に戻したことを通知する（番組名が直った）。
func (a *App) notifyUnskipped(ctx context.Context, log *slog.Logger, job domain.Job) {
	msg := fmt.Sprintf("【録音予定に戻した】%s は、番組名が直ったので録る（録りにいく時刻 %s）。", jobLabel(job), fmtTime(job.NextAttemptAt))
	a.addEvent(ctx, log, job, notify.KindUnskipped, msg)
}

// notifyMissing は欠落ありで保存したことを通知する。
func (a *App) notifyMissing(ctx context.Context, log *slog.Logger, job domain.Job, path string, missing []domain.Airing) {
	var b strings.Builder
	fmt.Fprintf(&b, "【欠落あり】%s を、取れた分だけで保存した（放送回 %d 件のうち %d 件が欠落）。\n保存先: %s\n欠落した放送回:",
		jobLabel(job), len(job.Airings), len(missing), path)
	for _, ar := range missing {
		// 放送回の時刻なので、開始の放送日の時刻で書く（ADR 0012）。放送日は jobLabel にある。
		day := ar.Day()
		fmt.Fprintf(&b, "\n- %s %s〜%s %s", ar.StationID, broadcastClock(day, ar.Start), broadcastClock(day, ar.End), ar.Title)
	}
	a.addEvent(ctx, log, job, notify.KindMissing, b.String())
}

// notifyLiveMissing は、ライブ録音で欠けた区間を残したまま（欠落ありで）保存したことを通知する。欠けた長さの合計と、
// 欠けた区間（放送回ごと）を書く。missing は欠けた区間を持つ放送回。note は末尾に足す行（notifyRecorded と同じ）。
func (a *App) notifyLiveMissing(ctx context.Context, log *slog.Logger, job domain.Job, path string, missing []domain.Airing, gaps []airingGap, note string) {
	var total time.Duration
	for _, g := range gaps {
		total += g.To.Sub(g.From)
	}
	msg := fmt.Sprintf("【欠落あり】%s を、ライブ録音で録れた分だけで保存した（欠けた長さ 合計 %s。放送回 %d 件のうち %d 件に欠落）。\n保存先: %s\n欠けた区間:",
		jobLabel(job), fmtLength(total), len(job.Airings), len(missing), path)
	a.addEvent(ctx, log, job, notify.KindMissing, msg+liveGapLines(gaps)+note+a.liveWaitLine(job.ID, false))
}

// titleMismatch は、ジョブの放送回の番組表の番組名がジョブの番組の name と食い違っていれば、通知の 2 行目
// （先頭の改行を含む）を返す。無ければ空。比べ方は domain.TitleMatchesName。check_title = false の番組は調べない。
// 番組を読めないときは、調べる（既定）ものとして扱う。
func (a *App) titleMismatch(ctx context.Context, log *slog.Logger, job domain.Job) string {
	switch r, err := a.Store.GetShow(context.WithoutCancel(ctx), job.ShowName); {
	case err == nil && !r.CheckTitle:
		return ""
	case err != nil && !errors.Is(err, store.ErrNotFound):
		log.Warn("番組を読めないので、番組名の食い違いを check_title によらず調べる", "err", err)
	}
	return titleMismatchLine(job)
}

// titleMismatchLine は、ジョブの放送回のうち、番組表の番組名が番組の name と食い違うものの番組名（重ねない）を
// 1 行にして返す（先頭に改行を付ける）。無ければ空。
func titleMismatchLine(job domain.Job) string {
	var titles []string
	for _, ar := range job.Airings {
		if domain.TitleMatchesName(ar.Title, job.ShowName) {
			continue
		}
		if t := "「" + oneLine(ar.Title) + "」"; !slices.Contains(titles, t) {
			titles = append(titles, t)
		}
	}
	if len(titles) == 0 {
		return ""
	}
	return "\n番組名の食い違い: 番組表の番組名 " + strings.Join(titles, "") + " が番組の name で始まらない"
}

// failureText は試行のエラーを "[段階] エラー" の形にする。
func failureText(err error) string {
	return fmt.Sprintf("[%s] %s", stageOf(err), oneLine(err.Error()))
}

// lastError はジョブの直近の試行のエラーを "[段階] エラー" の形で返す。無ければ空。
func (a *App) lastError(ctx context.Context, id int64) string {
	as, err := a.Store.Attempts(ctx, id)
	if err != nil || len(as) == 0 || as[len(as)-1].Error == "" {
		return ""
	}
	last := as[len(as)-1]
	return fmt.Sprintf("[%s] %s", last.FailedStage, oneLine(last.Error))
}

// syncLogged は番組表を同期し、失敗したらログに出す。2 回続けて失敗したら、番組表の同期の異常の始まりを通知する
// （続いている間は書かない）。成功したら数え直し、異常が続いていれば復旧を通知する。ctx が取り消されたら何もしない。
func (a *App) syncLogged(ctx context.Context) {
	err := a.Sync(ctx)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		a.syncFailures = 0
		a.addAnomaly(ctx, a.log(), notify.KindRecovered, notify.KindSync, "【復旧】番組表の同期ができた。")
		return
	}
	a.log().Error("番組表を同期できない", "err", err)
	a.syncFailures++
	if a.syncFailures >= 2 {
		a.addAnomaly(ctx, a.log(), notify.KindSync, notify.KindSync,
			"【番組表の同期の失敗】番組表の同期が 2 回続けて失敗した。新しいジョブが作られない。\n直近のエラー: "+oneLine(err.Error()))
	}
}

// authResult は認証の結果を見て、エリア外と判定されたらエリア外の異常の始まりを通知する（続いている間は書かない）。
// 認証が通ったら、エリア外が続いていれば復旧を通知する。ほかのエラーでは何もしない。
func (a *App) authResult(ctx context.Context, log *slog.Logger, err error) {
	if err == nil {
		a.addAnomaly(ctx, log, notify.KindRecovered, notify.KindOutOfArea, "【復旧】radiko の認証が通った（エリア外ではなくなった）。")
		return
	}
	if !errors.Is(err, radiko.ErrOutOfArea) {
		return
	}
	a.addAnomaly(ctx, log, notify.KindOutOfArea, notify.KindOutOfArea,
		"【エリア外】radiko がエリア外と判定した。録音できないので、ジョブは再試行を待つ。外向きの通信の経路を確かめる。\nエラー: "+oneLine(err.Error()))
}

// CheckNotifications は録音のループの周ごとの通知の処理をする。期限間近を判定して書き、まだ送れていない通知を
// 通知先に送り、古い通知を消す。DB の失敗はエラーを返す。
func (a *App) CheckNotifications(ctx context.Context) error {
	if err := a.notifyDeadlineNear(ctx); err != nil {
		return err
	}
	if err := a.deliverNotifications(ctx); err != nil {
		return err
	}
	n, err := a.Store.DeleteNotificationsBefore(ctx, a.now().Add(-notificationRetention))
	if err != nil {
		return err
	}
	if n > 0 {
		a.log().Info("古い通知を消した", "count", n)
	}
	return nil
}

// notifyDeadlineNear は期限間近のジョブを 1 件ずつ通知として書き、同じトランザクションでジョブを通知済みにする。
func (a *App) notifyDeadlineNear(ctx context.Context) error {
	jobs, err := a.Store.DeadlineNearJobs(ctx, a.now(), deadlineNearWithin)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Live() {
			// ライブで録るジョブの録音期限は放送の終了の 5 分後なので、期限の 24 時間前は放送の前で、まだ録っていないのがふつう
			// （放送中の途中経過も知らせない）。録れなければ、録音できなかったで知らせる。
			continue
		}
		if j.ChapterRefetch {
			// チャプターの取り直しの試行は通知しない（録音は済んでいて、録れなくても録音済みに戻す。ADR 0027）。
			continue
		}
		msg := fmt.Sprintf("【期限間近】%s をまだ録音できていない（状態 %s、試行 %d 回、次の試行 %s、録音期限 %s）。",
			jobLabel(j), j.State, j.Attempts, fmtTime(j.NextAttemptAt), fmtTime(j.Deadline))
		if e := a.lastError(ctx, j.ID); e != "" {
			msg += "\n直近のエラー: " + e
		}
		n := a.newNotification(notify.KindDeadlineNear, "", msg)
		n.JobID = j.ID
		id, err := a.Store.AddDeadlineNearNotification(ctx, j.ID, n)
		if errors.Is(err, store.ErrNotFound) {
			continue // 読んだ後にジョブが消えた
		}
		if err != nil {
			return err
		}
		a.log().Info("通知を書いた", "notification", id, "kind", notify.KindDeadlineNear, "job", j.ID, "message", msg)
	}
	return nil
}

// deliverNotifications は、まだ送れていない通知を古い順に通知先へ送り、送れたら送った時刻を記録する。
// 諦めて「送れなかった」の印を付け、rajikura_notify_failures_total{kind} を数えるのは、その周で送ろうとして送れなかった後だけ:
//
//   - 送ろうとして送れなかった通知が、作ってから sendGiveUpAfter を過ぎていれば諦める。過ぎていなければ次の周で送り直す
//   - 1 通でも送れなければ、この周ではそれより後の通知を送らない（通知先が落ちているときに、通知の数だけ待たない）。
//     そのうち作ってから sendGiveUpAfter を過ぎたものは、通知先が落ちていることをこの周で確かめた後なので、試さずに諦める
//   - 送れなかった通知が無い周では、作ってから sendGiveUpAfter を過ぎた通知も送る（止まっていた間に溜まった通知など）
//
// 通知先なしなら何もしない。
func (a *App) deliverNotifications(ctx context.Context) error {
	notifier := a.current().notifier
	if notifier == nil {
		return nil
	}
	pending, err := a.Store.PendingNotifications(ctx)
	if err != nil {
		return err
	}
	failed := false
	for _, n := range pending {
		if ctx.Err() != nil {
			return nil
		}
		log := a.log().With("notification", n.ID, "kind", n.Kind)
		if !failed {
			err := notifier.Notify(ctx, n.Kind, n.Message)
			if err == nil {
				if err := a.Store.MarkNotificationSent(ctx, n.ID, a.now()); err != nil {
					return err
				}
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			failed = true
			log.Warn("通知を送れない", "err", err)
		}
		now := a.now()
		if now.Sub(n.CreatedAt) < sendGiveUpAfter {
			continue // 次の周で送り直す
		}
		if err := a.Store.MarkNotificationFailed(ctx, n.ID, now); err != nil {
			return err
		}
		a.Metrics.NotifyFailed(n.Kind)
		log.Warn("作ってから 1 時間を過ぎた通知を送れなかったので、諦める", "created", n.CreatedAt)
	}
	return nil
}
