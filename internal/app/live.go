package app

// ライブ録音。タイムフリーの対象外の放送回を持つジョブ（domain.Job.Live）を、放送中のストリームから録る。
//
// # ライブの録音役
//
// ライブの録音役（runLive）は、タイムフリーの録音のループ（Serve）とは別の goroutine で、LiveCheckInterval（既定 10 秒）ごとに
// 次を行う。保存先が決まっていなければ何もしない。
//
//  1. 前回のプロセスが録っている途中で終わった（running のまま残った）ライブで録るジョブを拾い直す（Store.RunningLive）。
//     このプロセスが録っているジョブは除き、拾う直前に DB でまだ running かを引き直す（一覧を読んだ後に録り終えたジョブを、
//     古い一覧のまま録り直さないため。resumeRunningLive）。
//  2. 次の試行の時刻を過ぎた、録音期限を過ぎたライブで録るジョブ（録れた分の保存だけをやり直すもの）を Store.ClaimDueLiveSave で
//     running にし、goroutine を起こして保存する。
//  3. 最初の試行の時刻（放送の開始の 1 分前。domain.JobFirstAttemptAt）を過ぎた waiting のライブで録るジョブがあれば、その
//     放送日の番組表を取り直して展開をやり直す（展開役に頼み、終わるまで待つ。refreshJobDay）。開始と終了が変わっていれば
//     新しい値で録る。タイムフリーの対象外でなくなっていれば、そのジョブはタイムフリーの録音のループに任せる。
//  4. Store.ClaimDueLive で running にし、ジョブごとに goroutine を起こして録る（録り終わるのを待たずに次の周に進む）。
//
// 同時に録るのは maxLiveJobs（8 本）まで（1 と 4 を合わせて数える）。設定にしない固定の値。2026-10-25 に、非会員（area=JP13）の
// 1 回の認証で、ライブを 8 本まで（違う局）・同じ局に 3 本まで並べ、各 3 分の間、200 以外の応答も通信のエラーも本数による欠けも
// 出なかった（internal/radiko の Client で FetchLive を並べた使い捨てのプログラムで測った。rajikura の上では測っていない）。
// 9 本以上、3 分より長く繋いだとき、radiko に上限があるか・超えたときに何が起きるかは未確認。録り終えた後の格納・保存の間も、
// goroutine が終わるまで枠を使う。録音期限を過ぎたジョブ（1 で拾い直すものと 2）はこの枠に数えない。録音期限を過ぎた
// 試行は radiko に繋がず、残したセグメントから保存するだけなので、枠の趣旨と関わらない。保存先が固まって保存が戻らない間に、
// 枠をふさいで放送を録り逃さないため（保存先が固まったときに保存がいつ戻るかは未確認）。
//
// 枠が 0 で拾えないライブで録るジョブがあるとき（枠待ち。Store.ClaimDueLive が拾う対象か、1 が拾い直す対象で、録音期限の前の
// もの）は、見回りで見つけたジョブ id ごとに、待ち始めた時刻と予定の時刻（その時点の次の試行の時刻）をメモリに覚え（liveWaits）、
// ログに Warn を 1 回出す。拾って繋ぎ始めたらログに Info で待った長さを出す。その試行が欠落ありか録音できなかったになったら、
// 通知の本文の末尾に、枠待ちで繋ぎ始めが遅れた（または繋げなかった）ことを 1 行足す（liveWaitLine）。待ったが欠けずに済んだとき
// は、録音済みの通知には足さず、ログだけにする。覚えたことは DB に残さないので、serve を起動し直すと消え、その分の 1 行は付かない。
// 試行が終わったら（通知を書いた後）忘れる。
//
// あわせて、1 時間に 1 回、録れた分を残している expired のライブで録るジョブの作業用フォルダのうち、残す期限
// （domain.LiveKeepUntil。最後の試行の終わりから 30 日）を過ぎたものを消す（sweepKeptLive）。
//
// 見回りのたびに、スリープ中の PC を起こすタイマーも掛け直す（下の「スリープ中の PC を起こす」）。
//
// DB に書く者は、展開役・タイムフリーの録音のループ・バックアップの見回り・HTTP に、ライブの録音役（とその録音の goroutine）が
// 加わる。書き込みはどれも短いトランザクションで、store の開き方（WAL・busy_timeout・BEGIN IMMEDIATE。internal/store の
// package doc）で順番を待つ。
//
// # 1 回の試行
//
// ライブで録るジョブは、録音（入手）を自動では再試行しない。1 回の試行で放送回を開始時刻の順に録る。retrying にするのは、録れた分の
// 格納・保存が失敗したときだけ（下の「保存のやり直し」）。
//
//   - 放送回ごとに、作業ディレクトリ/<ジョブ ID>/<開始時刻>.aac へ書き、どこまで書いたかを <開始時刻>.live に残す（liveCheckpoint）。
//     radiko.Client.FetchLive を liveChunk（1 分）ずつの範囲で呼び、呼ぶたびに .live を書き直す（プロセスが落ちても、そこまでの
//     セグメントを残す）。保存する範囲は、PDT で放送回の開始から終了まで（前後の余白は付けない）。
//   - 繋ぎ直しを続ける期限は、放送回の終了の 5 分後（domain.LiveDeadlineAfter）。それまでに終了に届かなければ、残りを欠けた
//     区間にして次の放送回に進む。
//   - 繋ぎ直しても埋められなかった区間（欠けた区間。radiko.LiveGap）は .live に足していく。
//   - 全放送回を終えたら、取れたセグメントを連結して格納・検証・保存する（save。検証は格納した長さを測ること）。長さが
//     #EXTINF の合計と lengthTolerance を超えてずれていても、タイムフリーと違って失敗にせず、ずれたまま保存して、録音済みか
//     欠落ありの通知にずれを書く（liveLengthLine。取り直せないので、止めても得るものが無い）。欠けた区間を持つ放送回を欠落
//     （Job.Missing）として記録し、欠落ありの通知に欠けた長さを書く（notifyLiveMissing）。1 つもセグメントが取れなければ
//     （入手の段階の失敗）expired にして作業ディレクトリを消す（録音期限は最後の放送回の終了の 5 分後なので、何も録れないジョブは
//     そこで録音できなかったになる）。radiko.ErrNotAvailable（局にライブのストリームが無い）と書き込みの失敗はそこで録るのを
//     やめ、それまでに取れた分で同じく片付ける。
//   - 試行の間は、タイムフリーと同じく OS にスリープしないよう伝える（attemptAwake）。
//   - 試行の間は、ジョブの進捗を持つ（app.go の「進捗」）。入手の間の録れた長さは、.live の届いた音の長さ（Extinf）の合計で、
//     前の起動から続けるときは読んだ .live の値から始め、.live を書き直すたびに進める（欠けた区間は含まない）。録音期限を過ぎた
//     試行（保存だけのやり直し）は長さを持たず、段階だけを持つ。
//   - ctx が取り消されたら（SIGINT / SIGTERM）、ジョブを running のまま残す。次の起動時の回復（Store.Recover）はライブで録る
//     ジョブを retrying に戻さず、ライブの録音役が拾い直して、.live までのセグメントを残したまま繋ぎ直す（radiko が遡って
//     返せる範囲（l=270）より前に途切れた分は、欠けた区間になる）。
//   - retrying のジョブ（下の保存のやり直しと、人が手動再試行したもの）も拾い、同じく試行する（作業ディレクトリが残っていれば、
//     そこから続ける）。.live の Done が立った放送回は入手を飛ばし、録音期限を過ぎていれば Done でない放送回も radiko に繋がずに
//     残りを欠けた区間にするので、録音期限を過ぎた試行は保存だけになる。
//
// # 保存のやり直し
//
// 試行が保存の段階か検証の段階（長さを測れなかった）で失敗したら、expired にせず、作業ディレクトリ（録れたセグメント）を残して
// retrying にする。次の試行の時刻は domain.LiveSaveRetryAt（前の試行の開始から 15 分 → 1 時間 → 3 時間 → 以降 6 時間ごと）。
// 打ち切りは放送の終了から 24 時間（domain.Job.LiveSaveCutoff）で、録音期限を過ぎても続ける。自動でやり直している間は通知せず、
// ログに Warn を出す。打ち切りの時刻以降に失敗したら expired にし、作業ディレクトリは残して（expireKeeping）、録音できなかったの
// 通知に残した場所・消す日時・保存からやり直す手順を書く。残した分は、最後の試行の終わりから 30 日の間、録音期限を過ぎていても
// 人の手動再試行（store.Store.ManualRetry）で保存からやり直せ、過ぎたら sweepKeptLive が消す。
//
// # スリープ中の PC を起こす
//
// ライブの録音役は、waiting のライブで録るジョブの最初の試行（放送の開始の 1 分前。domain.JobFirstAttemptAt。waiting の
// ジョブでは Job.NextAttemptAt）の liveWakeLead（3 分）前に、スリープ中の PC を起こすタイマー（App.Waker。Windows の
// SetWaitableTimer。internal/power）を掛ける。タイムフリーから録るジョブのためには起こさない。running のライブで
// 録るジョブ（録音中に人がフタを閉じた・スリープを選んだもの）のためにも起こさない（人が選んだスリープを覆さない）。
//
//   - タイマーは 1 本だけで、起こす時刻がまだ来ていないジョブのうち、最初の試行の時刻が一番早いものの分を掛ける。見回りの
//     たびに求め直し、変わったときだけ掛け直す。対象のジョブが無くなったら取り消す（tendWake）。
//   - タイマーが鳴ったら（または、掛けた起こす時刻を見回りで過ぎていたら）、KeepAwake で「スリープしない」を掛け、最初の
//     試行の時刻の liveWakeHold（2 分）後に外す。ジョブの状態は見ず、時刻だけで外す（試行が始まれば、試行の側が自分の分を
//     掛ける。attemptAwake）。続けて次の分を掛ける。
//   - 録り終えた後にスリープに戻す処理は無い。
//   - 起こせないと分かったとき（power.ErrWakeNotSupported・power.ErrWakeTimersDisallowed）と、タイマーを掛けられないときは、
//     ログに Warn を出す。同じ理由では 1 回だけ出す（warnWake）。掛けられなかったときは掛けていない
//     ものとして扱い、次の見回りで掛け直す（起こす時刻を過ぎても、鳴ったものとして扱わない）。
//   - 起こせないと分かったとき（power.ErrWakeNotSupported・power.ErrWakeTimersDisallowed）は、あわせてスリープから起こせない見込みのサービスの異常（notify.KindWake）の
//     始まりを通知し、次にタイマーを掛けられて起こせないと分かった理由が無ければ復旧を通知する（wakeAnomaly）。
//     判定し直すのは次にライブで録るジョブの分のタイマーを掛けたときだけで、それまでは（タイマーを取り消しても）続く。
//     タイマーを掛けられない・取り消せない（Windows の API の失敗）は、人が直せるものではないので、ログのままにする。
//     足したら直す: サービスの異常
//
// サービス（仮想アカウント）から掛けたタイマーで起きるか、Modern Standby の PC と休止状態から起きるかは未確認。
//
// 長く繋いだときにセッションやトークンが切れるか、medialist を何秒ごとに取り直せばよいか（radiko.Client.LivePollInterval）、
// liveChunk ごとに繋ぎ直す（呼ぶたびに lsid が変わる）ことを radiko がどう扱うかは未確認。
//
// 足したら直す: ジョブの状態の移り方

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/power"
	"github.com/t-seki/rajikura/internal/radiko"
	"github.com/t-seki/rajikura/internal/store"
)

const (
	// maxLiveJobs は、同時に録るライブ録音のジョブの数の上限（設定にしない）。確かめた範囲の一番上（package doc）。
	maxLiveJobs = 8
	// DefaultLiveCheckInterval は、ライブの録音役が録るジョブを探す間隔の既定値。
	DefaultLiveCheckInterval = 10 * time.Second
	// DefaultLiveRetryInterval は、ライブの入手が FetchLive の外で失敗した（認証できない、エリア外など）ときに、繋ぎ直すまでの
	// 待ちの既定値（決め打ちした値）。
	DefaultLiveRetryInterval = 30 * time.Second
	// liveChunk は、FetchLive を 1 回呼ぶ範囲の長さ（.live を書き直す間隔）。
	liveChunk = time.Minute
	// liveSeam は、範囲の終わりに届いたとみなす差（radiko の継ぎ目の許容と同じ 0.5 秒）。
	liveSeam = 500 * time.Millisecond
	// keptSweepInterval は、残す期限を過ぎた作業ディレクトリを探す間隔（sweepKeptLive）。
	keptSweepInterval = time.Hour
	// liveWakeLead は、ライブで録るジョブの最初の試行の時刻のどれだけ前に、スリープ中の PC を起こすか（放送の開始の 4 分前）。
	liveWakeLead = 3 * time.Minute
	// liveWakeHold は、起こした後に掛ける「スリープしない」を、最初の試行の時刻のどれだけ後に外すか。
	liveWakeHold = 2 * time.Minute
)

// liveWake は、スリープ中の PC を起こすタイマーの状態（package doc の「スリープ中の PC を起こす」）。ライブの録音役の
// goroutine だけが触る。
type liveWake struct {
	w power.Waker
	// own は、w をライブの録音役が作った（戻るときに Close する）こと。
	own bool
	// at は掛けている起こす時刻、first はその対象の最初の試行の時刻（零値なら掛けていない）。
	at, first time.Time
	// holds は、起こした後に掛けている「スリープしない」。
	holds []wakeHold
	// warned は、警告を出した理由（warnWake）。
	warned map[string]bool
}

// wakeHold は、起こした後に掛けた「スリープしない」と、外す時刻。
type wakeHold struct {
	until   time.Time
	release func()
}

// newLiveWake は App.Waker（nil なら power.NewWaker）でタイマーの状態を作る。
func (a *App) newLiveWake() *liveWake {
	lw := &liveWake{w: a.Waker, warned: map[string]bool{}}
	if lw.w == nil {
		lw.w, lw.own = power.NewWaker(), true
	}
	return lw
}

// closeWake はタイマーを取り消し、起こした後に掛けた「スリープしない」を全部外す（ライブの録音役が戻るとき）。
func (a *App) closeWake(lw *liveWake) {
	if !lw.at.IsZero() {
		if err := lw.w.Cancel(); err != nil {
			a.log().Warn("スリープから起こすタイマーを取り消せない", "err", err)
		}
		lw.at, lw.first = time.Time{}, time.Time{}
	}
	if lw.own {
		lw.w.Close()
	}
	for _, h := range lw.holds {
		h.release()
	}
	lw.holds = nil
}

// tendWake は、起こした後の「スリープしない」のうち外す時刻を過ぎたものを外し、タイマーを掛け直す（package doc の
// 「スリープ中の PC を起こす」）。掛けた起こす時刻を過ぎていれば、鳴ったものとして扱う（鳴った知らせより先に見回りが
// 来ても、掛け直して知らせを捨てないため）。
func (a *App) tendWake(ctx context.Context, lw *liveWake) {
	now := a.now()
	kept := lw.holds[:0]
	for _, h := range lw.holds {
		if now.Before(h.until) {
			kept = append(kept, h)
			continue
		}
		h.release()
		a.log().Info("ライブ録音の前に起こした後の、スリープしないよう伝えるのを外した", "until", h.until)
	}
	lw.holds = kept
	if !lw.at.IsZero() && !now.Before(lw.at) {
		a.holdAfterWake(lw)
	}

	jobs, err := a.Store.ListJobs(ctx, domain.StateWaiting)
	if err != nil {
		if ctx.Err() == nil {
			a.log().Error("スリープから起こすタイマーの対象のジョブを探せない", "err", err)
		}
		return
	}
	var first time.Time
	for _, j := range jobs {
		// waiting のジョブの次の試行の時刻は、最初の試行の時刻。起こす時刻を過ぎたもの（鳴った分と、間に合わないもの）は除く。
		if !j.Live() || !j.NextAttemptAt.Add(-liveWakeLead).After(now) {
			continue
		}
		if first.IsZero() || j.NextAttemptAt.Before(first) {
			first = j.NextAttemptAt
		}
	}
	if first.IsZero() {
		if !lw.at.IsZero() {
			lw.at, lw.first = time.Time{}, time.Time{}
			if err := lw.w.Cancel(); err != nil {
				a.warnWake(lw, fmt.Errorf("スリープから起こすタイマーを取り消せない: %w", err))
			}
			a.log().Info("ライブ録音の前に起こすジョブが無くなったので、スリープから起こすタイマーを取り消した")
		}
		return
	}
	at := first.Add(-liveWakeLead)
	if at.Equal(lw.at) {
		return
	}
	problems, err := lw.w.Set(at)
	if err != nil {
		// 掛けていない状態のままにする（次の見回りで掛け直す。時刻を過ぎても鳴ったものとして扱わない）。
		// 掛けていたタイマーがあっても、Set の失敗の後に残っているかは分からないので、掛けていないとみなす。
		lw.at, lw.first = time.Time{}, time.Time{}
		a.warnWake(lw, fmt.Errorf("スリープから起こすタイマーを掛けられない: %w", err))
		return
	}
	lw.at, lw.first = at, first
	a.log().Info("ライブ録音の前にスリープから起こすタイマーを掛けた", "wake", at, "firstAttempt", first)
	for _, p := range problems {
		a.warnWake(lw, p)
	}
	a.wakeAnomaly(ctx, problems)
}

// wakeAnomaly は、タイマーを掛けられた結果（Waker.Set の problems）で、スリープから起こせない見込みのサービスの異常
// （notify.KindWake）の始まりか復旧を通知する。problems に起こせないと分かった理由（power.ErrWakeNotSupported・
// power.ErrWakeTimersDisallowed）があれば始まり（続いている間は書かない）、problems が空なら、続いていれば復旧を書く。
// ほかの理由（「スリープ解除タイマーの許可」を読めないなど。Windows の API の失敗）だけのときは、起こせるかが分からないので
// どちらも書かない（ログの Warn だけ）。タイマーを掛けられない・取り消せないときは呼ばない（ログのまま）。
func (a *App) wakeAnomaly(ctx context.Context, problems []error) {
	for _, p := range problems {
		if errors.Is(p, power.ErrWakeNotSupported) || errors.Is(p, power.ErrWakeTimersDisallowed) {
			a.addAnomaly(ctx, a.log(), notify.KindWake, notify.KindWake,
				"【スリープから起こせない】ライブ録音の前にスリープ中の PC を起こせない見込み。放送の時刻に PC がスリープしていると、ライブで録る放送回を録れない。"+
					"対処は docs/windows.md の「スリープ」（「起こせないと分かったときの警告」）。\n理由: "+oneLine(p.Error()))
			return
		}
	}
	if len(problems) == 0 {
		a.addAnomaly(ctx, a.log(), notify.KindRecovered, notify.KindWake,
			"【復旧】ライブ録音の前にスリープ中の PC を起こすタイマーを、起こせる見込みで掛けられた。")
	}
}

// onWakeFired は、タイマーが鳴った知らせ（鳴ったタイマーの起こす時刻 at）を受ける。掛け直した後に届いた古い知らせは捨てる。
func (a *App) onWakeFired(lw *liveWake, at time.Time) {
	if lw.at.IsZero() || !at.Equal(lw.at) {
		return
	}
	a.holdAfterWake(lw)
}

// holdAfterWake は、掛けていたタイマーの分の「スリープしない」を掛け（最初の試行の時刻の liveWakeHold 後に外す）、
// タイマーを掛けていない状態に戻す（次の分は tendWake が掛ける）。
func (a *App) holdAfterWake(lw *liveWake) {
	until := lw.first.Add(liveWakeHold)
	lw.at, lw.first = time.Time{}, time.Time{}
	keep := a.KeepAwake
	if keep == nil {
		keep = power.Acquire
	}
	release, err := keep(power.WakeReason)
	if err != nil {
		a.log().Warn("ライブ録音の前に起こした後、スリープを止められない", "err", err)
	}
	if release != nil {
		lw.holds = append(lw.holds, wakeHold{until: until, release: release})
	}
	a.log().Info("ライブ録音の前に起こす時刻になった。最初の試行の 2 分後までスリープしないよう伝える", "until", until)
}

// warnWake は、ライブ録音の前に起こせないと分かった理由を Warn で出す。同じ理由では 1 回だけ出す
// （power.ErrWakeNotSupported・power.ErrWakeTimersDisallowed は、添えた値が違っても同じ理由とみなす）。
func (a *App) warnWake(lw *liveWake, err error) {
	key := err.Error()
	for _, s := range []error{power.ErrWakeNotSupported, power.ErrWakeTimersDisallowed} {
		if errors.Is(err, s) {
			key = s.Error()
		}
	}
	if lw.warned[key] {
		return
	}
	lw.warned[key] = true
	a.log().Warn("ライブ録音の前にスリープ中の PC を起こせない見込み（docs/windows.md の「スリープ」）", "reason", err)
}

// liveGap は、ライブ録音で欠けた区間 [From, To)（radiko.LiveGap を放送回ごとに貯めたもの）。用語集の「欠落」は、ライブ録音
// では放送回の中の録れなかった区間も指し、この区間がそれに当たる。ジョブの記録（domain.Job.Missing）は放送回の単位で、
// 欠けた区間を持つ放送回を入れる。
type liveGap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// airingGap は、どの放送回の欠けた区間か（通知の本文に使う）。
type airingGap struct {
	airing domain.Airing
	liveGap
}

// liveCheckpoint は、ライブ録音の放送回 1 つの、どこまで書いたかの記録（<開始時刻>.live に JSON で置く）。
type liveCheckpoint struct {
	// Next は、書いた最後のセグメントの終わり（次の FetchLive の LiveRequest.Resume）。零値ならまだ何も見ていない。
	Next time.Time `json:"next"`
	// Size は、<開始時刻>.aac のうち書き終えた大きさ（バイト）。拾い直したら、これより後ろを捨てる。
	Size int64 `json:"size"`
	// Extinf は、書いたセグメントの #EXTINF の合計（秒）。Segments は書いたセグメントの数。
	Extinf   float64 `json:"extinf"`
	Segments int     `json:"segments"`
	// Gaps は、欠けた区間（古い順）。
	Gaps []liveGap `json:"gaps,omitempty"`
	// Done は、放送回の終了まで（欠けた区間を含めて）見終えたこと。
	Done bool `json:"done"`
}

// addGap は欠けた区間 [from, to) を足す。前の区間と重なる・接するならつなぐ。
func (c *liveCheckpoint) addGap(from, to time.Time) {
	if !to.After(from) {
		return
	}
	if n := len(c.Gaps); n > 0 && !c.Gaps[n-1].To.Before(from) {
		if to.After(c.Gaps[n-1].To) {
			c.Gaps[n-1].To = to
		}
		return
	}
	c.Gaps = append(c.Gaps, liveGap{From: from, To: to})
}

// next は、次に書く所（記録が無ければ放送回の開始）。
func (c liveCheckpoint) next(ar domain.Airing) time.Time {
	if c.Next.Before(ar.Start) {
		return ar.Start
	}
	return c.Next
}

// readLiveCheckpoint は放送回の記録を読む。無ければ零値（.aac があっても、頭から書き直す）。
func readLiveCheckpoint(base string) (liveCheckpoint, error) {
	var c liveCheckpoint
	b, err := os.ReadFile(base + ".live")
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return liveCheckpoint{}, fmt.Errorf("%s.live を読めない: %w", base, err)
	}
	return c, nil
}

// writeLiveCheckpoint は放送回の記録を書く。先に .aac をディスクに書き出してから、記録を別名で書いて差し替える
// （記録が指す大きさまでは、.aac に書けている）。
func writeLiveCheckpoint(base string, f *os.File, c liveCheckpoint) error {
	if err := f.Sync(); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := base + ".live.tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, base+".live")
}

// countingWriter は書いた大きさを数え、書き込みの失敗を覚える。
type countingWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil && c.err == nil {
		c.err = err
	}
	return n, err
}

// runLive はライブの録音役（goroutine 1 本）の中身。ctx が取り消されたら、録っている途中のジョブが戻るのを待って戻る。
func (a *App) runLive(ctx context.Context) {
	defer a.liveWG.Wait()
	lw := a.newLiveWake()
	defer a.closeWake(lw)
	t := time.NewTicker(cmp.Or(a.LiveCheckInterval, DefaultLiveCheckInterval))
	defer t.Stop()
	var swept time.Time
	for {
		if err := a.startDueLive(ctx); err != nil && ctx.Err() == nil {
			a.log().Error("ライブ録音のジョブを始められない", "err", err)
		}
		a.tendWake(ctx, lw)
		if now := a.now(); swept.IsZero() || now.Sub(swept) >= keptSweepInterval || now.Before(swept) {
			swept = now
			if err := a.sweepKeptLive(ctx); err != nil && ctx.Err() == nil {
				a.log().Error("残す期限を過ぎたライブ録音の作業用フォルダを探せない", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case at := <-lw.w.Fired():
			a.onWakeFired(lw, at)
		}
	}
}

// liveFree は、あと何本ライブ録音を始められるか（枠に数えるものだけを数える）。
func (a *App) liveFree() int {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	n := 0
	for _, slot := range a.liveRunning {
		if slot {
			n++
		}
	}
	return a.liveLimit() - n
}

// liveLimit は、同時に録る本数の上限。maxLiveJobs（テストだけが liveMax で差し替える）。
func (a *App) liveLimit() int {
	return cmp.Or(a.liveMax, maxLiveJobs)
}

// liveWait は、枠待ちのライブで録るジョブ 1 件の覚え（liveWaits）。
type liveWait struct {
	// since は待ち始めた（見回りで見つけた）時刻、planned はその時点の次の試行の時刻（予定の時刻）。
	since, planned time.Time
	// connected は、枠が空いて繋ぎ始めた時刻（まだ繋いでいなければ零値）。
	connected time.Time
}

// noteLiveWait は、枠待ちのジョブ job を覚える（すでに覚えていれば何もしない）。見つけたジョブごとに Warn を 1 回出す。
func (a *App) noteLiveWait(job domain.Job) {
	a.liveMu.Lock()
	if _, ok := a.liveWaits[job.ID]; ok {
		a.liveMu.Unlock()
		return
	}
	if a.liveWaits == nil {
		a.liveWaits = map[int64]liveWait{}
	}
	a.liveWaits[job.ID] = liveWait{since: a.now(), planned: job.NextAttemptAt}
	a.liveMu.Unlock()
	a.log().Warn("同時に録る本数の上限で、ライブ録音が枠が空くのを待っている",
		"job", job.ID, "show", job.ShowName, "day", job.Day, "limit", a.liveLimit(), "planned", job.NextAttemptAt)
}

// pruneLiveWaits は、keep に入らず、このプロセスが録ってもいないジョブの覚えを忘れる（人が取り消したなど、録られずに終わった分）。
func (a *App) pruneLiveWaits(keep map[int64]bool) {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	for id := range a.liveWaits {
		if _, held := a.liveRunning[id]; !held && !keep[id] {
			delete(a.liveWaits, id)
		}
	}
}

// liveWaitLine は、枠待ちだったジョブの通知の本文に足す行（先頭の改行を含む）。覚えが無ければ空。繋ぎ始めていれば
// 繋ぎ始めた時刻を、繋げないまま録音期限を過ぎてセグメントが 1 つも無い（noSegments）ときだけ繋げなかったことを書く。欠落あり・録音できなかったの通知から呼ぶ。
func (a *App) liveWaitLine(id int64, noSegments bool) string {
	a.liveMu.Lock()
	w, ok := a.liveWaits[id]
	a.liveMu.Unlock()
	if !ok {
		return ""
	}
	if w.connected.IsZero() {
		if !noSegments {
			return "" // 録音期限を過ぎて保存だけの試行で拾われたが、前のプロセスのセグメントがある。どちらの行にも当たらない
		}
		return fmt.Sprintf("\n同時に録る本数の上限（%d 本）で枠が空くのを待ち、録音期限までに繋げなかった（予定は %s）。",
			a.liveLimit(), fmtTime(w.planned))
	}
	return fmt.Sprintf("\n同時に録る本数の上限（%d 本）で枠が空くのを待ったので、繋ぎ始めが %s になった（予定は %s）。",
		a.liveLimit(), fmtTime(w.connected), fmtTime(w.planned))
}

// liveHeld は、このプロセスがジョブ id を試行しているか（枠に数えるかを問わない）。
func (a *App) liveHeld(id int64) bool {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	_, ok := a.liveRunning[id]
	return ok
}

// sweepKeptLive は、録れた分を残している expired のライブで録るジョブの作業ディレクトリのうち、残す期限
// （domain.LiveKeepUntil。最後の試行（保存か検証の段階の失敗）の終わりから 30 日）を過ぎたものを消す。
// 消すのは expired のライブで録るジョブのものだけ。最後の試行が保存か検証の段階の失敗でないもの（残す期限が無いもの）は触らない。
func (a *App) sweepKeptLive(ctx context.Context) error {
	jobs, err := a.Store.ListJobs(ctx, domain.StateExpired)
	if err != nil {
		return err
	}
	now := a.now()
	for _, j := range jobs {
		if !j.Live() {
			continue
		}
		if _, err := os.Stat(a.jobDir(j.ID)); err != nil {
			continue // 残していない（消した、または入手の段階の失敗で消えた）
		}
		as, err := a.Store.Attempts(ctx, j.ID)
		if err != nil {
			return err
		}
		if len(as) == 0 {
			continue
		}
		keep := domain.LiveKeepUntil(j, &as[len(as)-1])
		if keep.IsZero() || now.Before(keep) {
			continue
		}
		log := a.log().With("job", j.ID, "show", j.ShowName, "day", j.Day)
		log.Info("残す期限を過ぎたので、ライブ録音の録れた分（作業用フォルダ）を消す", "until", keep)
		a.removeJobDir(log, j.ID)
	}
	return nil
}

// startDueLive はライブの録音役の 1 周（package doc の 1〜3）。拾ったジョブは goroutine で録り、録り終わるのを待たずに戻る。
// ライブの録音役の goroutine からだけ呼ぶ（liveRefreshed を触る）。
func (a *App) startDueLive(ctx context.Context) error {
	cur := a.current()
	if cur.library == "" {
		return nil
	}
	running, err := a.Store.RunningLive(ctx)
	if err != nil {
		return err
	}
	if err := a.resumeRunningLive(ctx, cur, running); err != nil {
		return err
	}
	// 録音期限を過ぎたもの（保存だけをやり直す）は枠に数えずに拾う。
	for {
		job, ok, err := a.Store.ClaimDueLiveSave(ctx, a.now())
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		a.startLive(ctx, cur, job, false)
	}
	for a.liveFree() > 0 {
		if err := a.refreshDueLive(ctx); err != nil {
			return err
		}
		job, ok, err := a.Store.ClaimDueLive(ctx, a.now())
		if err != nil || !ok {
			return err
		}
		delete(a.liveRefreshed, job.ID)
		a.startLive(ctx, cur, job, true)
	}
	return a.noteLiveWaits(ctx, running)
}

// noteLiveWaits は、枠が 0 で拾えないライブで録るジョブ（枠待ち）を覚え、録られずに終わったものの覚えを忘れる。
// running は startDueLive が読んだ running のライブで録るジョブ（resumeRunningLive が覚えたものを残すため）。
func (a *App) noteLiveWaits(ctx context.Context, running []domain.Job) error {
	keep := map[int64]bool{}
	for _, j := range running {
		keep[j.ID] = true
	}
	if a.liveFree() <= 0 {
		jobs, err := a.Store.ListJobs(ctx, domain.StateWaiting, domain.StateRetrying)
		if err != nil {
			return err
		}
		now := a.now()
		for _, j := range jobs {
			// ClaimDueLive が拾う対象（次の試行の時刻を過ぎ、録音期限の前）。
			if j.Live() && !j.NextAttemptAt.After(now) && now.Before(j.Deadline) {
				keep[j.ID] = true
				a.noteLiveWait(j)
			}
		}
	}
	a.pruneLiveWaits(keep)
	return nil
}

// resumeRunningLive は、running のライブで録るジョブの一覧 running（Store.RunningLive で読んだもの）のうち、このプロセスが
// 録っていないものを拾い直す（package doc の 1）。一覧は読んだ時点のものなので、読んでから見るまでの間に、このプロセスの
// 録音の goroutine が done・expired にして liveRunning から抜けたジョブが混ざりうる。そのため、録っていないと分かってから
// （録音の goroutine は状態を変えた後に liveRunning から抜ける）、DB でまだ running かを引き直し、running のものだけを拾う。
// 録音期限を過ぎたもの（保存だけになる）は、同時に録る本数の枠に数えず、枠が埋まっていても拾う。
func (a *App) resumeRunningLive(ctx context.Context, cur live, running []domain.Job) error {
	for _, j := range running {
		saveOnly := !a.now().Before(j.Deadline)
		if a.liveHeld(j.ID) {
			continue
		}
		if !saveOnly && a.liveFree() <= 0 {
			a.noteLiveWait(j)
			continue
		}
		now, err := a.Store.GetJob(ctx, j.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if now.State != domain.StateRunning || !now.Live() {
			continue // 一覧を読んだ後に、このプロセスが録り終えた（または人が変えた）
		}
		a.log().Info("録っている途中で止まったライブ録音を、取ったセグメントを残して繋ぎ直す", "job", now.ID, "show", now.ShowName, "day", now.Day, "saveOnly", saveOnly)
		a.startLive(ctx, cur, now, !saveOnly)
	}
	return nil
}

// refreshDueLive は、最初の試行の時刻を過ぎた waiting のライブで録るジョブについて、まだ取り直していなければ、その放送日の番組表を
// 取り直して展開をやり直す（展開役に頼み、終わるまで待つ）。番組表を取れなければ、ログに出して今の放送回のまま進む（refreshJobDay）。
func (a *App) refreshDueLive(ctx context.Context) error {
	if a.liveRefreshed == nil {
		a.liveRefreshed = map[int64]bool{}
	}
	for range maxRefresh {
		jobs, err := a.Store.ListJobs(ctx, domain.StateWaiting)
		if err != nil {
			return err
		}
		now := a.now()
		var due *domain.Job
		for i, j := range jobs {
			if !j.Live() || j.NextAttemptAt.After(now) || a.liveRefreshed[j.ID] {
				continue
			}
			if due == nil || j.NextAttemptAt.Before(due.NextAttemptAt) ||
				j.NextAttemptAt.Equal(due.NextAttemptAt) && j.ID < due.ID {
				due = &jobs[i]
			}
		}
		if due == nil {
			return nil
		}
		a.liveRefreshed[due.ID] = true
		j := *due
		if err := a.onExpander(ctx, func(ctx context.Context) error {
			_, err := a.refreshJobDay(ctx, j)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// startLive は running にしたライブで録るジョブ job を goroutine で録る。cur はジョブを拾ったときの設定の写し。
// slot は、同時に録る本数の枠に数えるか（録音期限を過ぎて保存だけになるものは false）。liveRunning の値に入れる。
func (a *App) startLive(ctx context.Context, cur live, job domain.Job, slot bool) {
	a.liveMu.Lock()
	if a.liveRunning == nil {
		a.liveRunning = map[int64]bool{}
	}
	a.liveRunning[job.ID] = slot
	var waited time.Duration
	wasWaiting := false
	if w, ok := a.liveWaits[job.ID]; ok && slot {
		now := a.now()
		waited, wasWaiting = now.Sub(w.since), true
		w.connected = now
		a.liveWaits[job.ID] = w
	}
	a.liveMu.Unlock()
	if wasWaiting {
		a.log().Info("ライブ録音が枠の空きを待って繋ぎ始めた", "job", job.ID, "show", job.ShowName, "day", job.Day, "waited", waited)
	}
	a.liveWG.Add(1)
	go func() {
		defer a.liveWG.Done()
		defer func() {
			a.liveMu.Lock()
			delete(a.liveRunning, job.ID)
			delete(a.liveWaits, job.ID) // 試行が終わった（通知は書き終えている）
			a.liveMu.Unlock()
		}()
		if err := a.runLiveJob(ctx, cur, job); err != nil && ctx.Err() == nil {
			a.log().Error("ライブ録音のジョブを片付けられない", "job", job.ID, "err", err)
		}
	}()
}

// runLiveJob は running のライブで録るジョブを 1 回試行し、done か expired にする。保存か検証の段階で失敗したら、打ち切りの
// 前なら retrying にして保存をやり直す（package doc の「保存のやり直し」）。
// ctx が取り消されたら running のまま残す（次の起動時に拾い直す）。
// 足したら直す: ジョブの状態の移り方
func (a *App) runLiveJob(ctx context.Context, cur live, job domain.Job) error {
	log := a.log().With("job", job.ID, "show", job.ShowName, "day", job.Day, "part", job.Part, "attempt", job.Attempts, "live", true)
	started := a.now()
	// 進捗は試行の間だけ持つ（成功でも失敗でも、DB の状態を変えた後に消す）。
	defer a.clearProgress(job.ID)
	log.Info("ライブ録音を始める", "deadline", job.Deadline)
	res, err := a.attemptAwake(ctx, cur, log, job)
	cancelable := ctx
	if err == nil {
		// 保存先に確定した後は、ctx が取り消されていても done まで進める（runJob と同じ）。
		ctx = context.WithoutCancel(ctx)
		if err := a.record(ctx, job, started, res, nil); err != nil {
			return err
		}
		if err := a.Store.Complete(ctx, job.ID, res.path, res.missing, domain.ChapterStatus{}); err != nil {
			return err
		}
		a.markSaved(ctx, log)
		if len(res.missing) > 0 {
			log.Warn("ライブ録音で欠けた区間のまま保存した", "path", res.path, "missing", len(res.missing), "gaps", len(res.gaps))
			a.notifyLiveMissing(ctx, log, job, res.path, res.missing, res.gaps, liveLengthLine(res))
		} else {
			log.Info("保存した", "path", res.path, "length", res.probe)
			a.notifyRecorded(ctx, log, job, res.probe, liveLengthLine(res))
		}
		a.removeDoneOnces(ctx, log, job)
		a.removeJobDir(log, job.ID)
		a.rescan(cancelable, cur, log)
		return nil
	}
	if ctx.Err() != nil {
		log.Info("中断した（running のまま残し、次の起動時に繋ぎ直す）")
		return nil
	}
	if err := a.record(ctx, job, started, res, err); err != nil {
		return err
	}
	stage := stageOf(err)
	if stage == domain.StageFetch {
		// 1 つも録れなかった。取り直せず、残すものも無い。
		log.Warn("ライブ録音が失敗したので期限切れにする", "stage", stage, "err", err)
		return a.expire(ctx, log, job, failureText(err))
	}
	cutoff := job.LiveSaveCutoff()
	if next, ok := domain.LiveSaveRetryAt(job.Attempts, started, cutoff, a.now()); ok {
		log.Warn("ライブ録音の録れた分を保存できない。作業用フォルダを残して、保存をやり直す", "stage", stage, "err", err, "next", next, "cutoff", cutoff)
		return a.Store.Retry(ctx, job.ID, next)
	}
	log.Warn("ライブ録音の録れた分を、打ち切り（放送の終了から 24 時間）までに保存できないので期限切れにする。作業用フォルダは残す",
		"stage", stage, "err", err, "cutoff", cutoff)
	return a.expireKeeping(ctx, log, job, failureText(err))
}

// liveAttempt はライブで録るジョブの全放送回を入手し、取れた分を格納・検証・保存する（attemptAwake から呼ぶ）。
// 1 つもセグメントが取れなければ、入手の段階の失敗を返す。ctx が取り消されたら ctx のエラーを返す。
// 入手の間は進捗の録れた長さを、届いた音の長さ（記録の Extinf の合計。前の起動から続けるときは読んだ記録の値から）にし、
// 記録を書くたびに進める。録音期限を過ぎた試行（保存だけのやり直し）は入手しないので、長さを出さず、保存から段階だけを出す。
func (a *App) liveAttempt(ctx context.Context, cur live, log *slog.Logger, job domain.Job) (saved, error) {
	if len(job.Airings) == 0 {
		return saved{}, failAt(domain.StageFetch, errors.New("ジョブに放送回が無い"))
	}
	dir := a.jobDir(job.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return saved{}, failAt(domain.StageFetch, err)
	}
	lf := &liveFetch{a: a, log: log}
	// got は放送回ごとの届いた音の長さ（秒）。入手の段階の進捗の録れた長さは、その合計。
	got := make([]float64, len(job.Airings))
	sum := func() time.Duration {
		var t float64
		for _, g := range got {
			t += g
		}
		return time.Duration(t * float64(time.Second))
	}
	counted := a.now().Before(job.Deadline)
	if counted {
		for i, ar := range job.Airings {
			if cp, err := readLiveCheckpoint(airingBase(dir, ar)); err == nil {
				got[i] = cp.Extinf
			}
		}
		a.startFetchProgress(job, sum())
	}
	var stop error
	var airings, missing []domain.Airing
	var files []string
	var extinf time.Duration
	var gaps []airingGap
	for i, ar := range job.Airings {
		base := airingBase(dir, ar)
		var cp liveCheckpoint
		var err error
		if stop == nil {
			if counted {
				lf.onWrite = func(extinf float64) {
					got[i] = extinf
					a.setFetched(job.ID, sum())
				}
			}
			cp, err = lf.airing(ctx, base, ar)
			if ctx.Err() != nil {
				return saved{}, failAt(domain.StageFetch, ctx.Err())
			}
			if err != nil {
				stop = err
				log.Warn("ライブ録音を続けられないので、取れた分で片付ける", "start", ar.Start, "err", err)
			}
		} else if cp, err = readLiveCheckpoint(base); err != nil {
			log.Warn("ライブ録音の記録を読めないので、放送回を欠けた区間にする", "start", ar.Start, "err", err)
			cp = liveCheckpoint{}
		}
		if !cp.Done {
			// 録るのをやめた放送回の残り。
			cp.addGap(cp.next(ar), ar.End)
		}
		if cp.Segments > 0 {
			airings = append(airings, ar)
			files = append(files, base+".aac")
			extinf += time.Duration(cp.Extinf * float64(time.Second))
		}
		if len(cp.Gaps) > 0 {
			missing = append(missing, ar)
			for _, g := range cp.Gaps {
				gaps = append(gaps, airingGap{airing: ar, liveGap: g})
			}
		}
	}
	if len(airings) == 0 {
		cause := "ライブ録音でセグメントを 1 つも取れなかった"
		if last := cmp.Or(stop, lf.lastErr); last != nil {
			cause += ": " + last.Error()
		}
		return saved{}, failAt(domain.StageFetch, errors.New(cause))
	}
	log.Info("ライブ録音の入手を終えた。取れた分を保存する", "fetched", len(airings), "missing", len(missing), "extinf", extinf)
	res, err := a.save(ctx, cur, log, job, airings, files, extinf, false)
	res.missing, res.gaps = missing, gaps
	return res, err
}

// liveFetch は、ライブ録音の 1 回の試行で使い回すもの（トークンと直近の失敗）。
type liveFetch struct {
	a       *App
	log     *slog.Logger
	token   *radiko.Token
	lastErr error
	// onWrite があれば、airing が記録を書き直すたびに、その放送回の届いた音の長さ（記録の Extinf）で呼ぶ（進捗を進める）。
	onWrite func(extinf float64)
}

// liveReached は、next が範囲の終わり end に届いたか。
func liveReached(next, end time.Time) bool {
	return !next.Add(liveSeam).Before(end)
}

// airing は放送回 ar を base.aac に入手し、記録（base.live）を返す。記録があれば、その大きさまでを残してそこから続ける。
// 繋ぎ直しは放送回の終了の 5 分後まで続け、終了に届かなければ残りを欠けた区間にして Done にする。
// 返すエラーは、ctx の取り消し（記録は Done でない）と、続けても直らない失敗（radiko.ErrNotAvailable、書き込みの失敗）。
func (lf *liveFetch) airing(ctx context.Context, base string, ar domain.Airing) (liveCheckpoint, error) {
	a := lf.a
	cp, err := readLiveCheckpoint(base)
	if err != nil {
		lf.log.Warn("ライブ録音の記録を読めないので、放送回を頭から取り直す", "start", ar.Start, "err", err)
		cp = liveCheckpoint{}
	}
	if cp.Done {
		return cp, nil
	}
	f, err := os.OpenFile(base+".aac", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return cp, err
	}
	defer f.Close()
	if err := f.Truncate(cp.Size); err != nil {
		return cp, err
	}
	if _, err := f.Seek(cp.Size, io.SeekStart); err != nil {
		return cp, err
	}
	if cp.Size > 0 || !cp.Next.IsZero() {
		lf.log.Info("前に取ったセグメントを残して、そこから続ける", "start", ar.Start, "next", cp.Next, "segments", cp.Segments)
	}
	w := &countingWriter{w: f, n: cp.Size}
	deadline := ar.End.Add(domain.LiveDeadlineAfter)
	retry := cmp.Or(a.LiveRetryInterval, DefaultLiveRetryInterval)
	for {
		next := cp.next(ar)
		if liveReached(next, ar.End) {
			break
		}
		now := a.now()
		if !now.Before(deadline) {
			lf.log.Warn("繋ぎ直しの期限（放送の終了の 5 分後）を過ぎたので、残りを欠けた区間にする", "start", ar.Start, "from", next)
			cp.addGap(next, ar.End)
			cp.Next = ar.End
			break
		}
		if lf.token == nil {
			tok, err := a.Fetcher.Authenticate(ctx)
			a.authResult(ctx, lf.log, err)
			if err != nil {
				if ctx.Err() != nil {
					return cp, ctx.Err()
				}
				lf.lastErr = fmt.Errorf("認証できない: %w", err)
				lf.log.Warn("ライブ録音の認証ができない。待って繋ぎ直す", "err", err, "wait", retry)
				if err := waitCtx(ctx, retry); err != nil {
					return cp, err
				}
				continue
			}
			lf.token = &tok
		}
		end := ar.End
		if e := laterOf(next, now).Add(liveChunk); e.Before(end) {
			end = e
		}
		res, err := a.Fetcher.FetchLive(ctx, *lf.token, radiko.LiveRequest{
			StationID: ar.StationID, Start: ar.Start, End: end, Deadline: deadline, Resume: next,
		}, w)
		// ctx が取り消されたときも、書けた所までは記録に残す（次の起動時に、そこから続ける）。
		cp.Segments += res.Segments
		cp.Extinf += res.DurationSeconds
		for _, g := range res.Gaps {
			cp.addGap(g.From, g.To)
		}
		if res.Next.After(next) {
			cp.Next = res.Next
		}
		cp.Size = w.n
		if werr := writeLiveCheckpoint(base, f, cp); werr != nil {
			return cp, fmt.Errorf("ライブ録音の記録を書けない: %w", werr)
		}
		if lf.onWrite != nil {
			lf.onWrite(cp.Extinf)
		}
		switch {
		case err == nil:
			if cp.next(ar).After(next) {
				continue
			}
			// 進まなかった（ふつうは起きない）。すぐに呼び直さない。
			lf.lastErr = errors.New("ライブの入手が進まない")
		case ctx.Err() != nil:
			return cp, ctx.Err()
		case w.err != nil:
			return cp, fmt.Errorf("ライブ録音の音声を書けない: %w", w.err)
		case errors.Is(err, radiko.ErrNotAvailable):
			return cp, err
		default:
			lf.lastErr = err
			if errors.Is(err, radiko.ErrOutOfArea) {
				a.authResult(ctx, lf.log, err)
				lf.token = nil
			}
			if res.Complete {
				// 繋ぎ直しの期限まで続けた（1 つも書けなかった）。次の周で残りを欠けた区間にする。
				lf.log.Warn("ライブの入手で、繋ぎ直しの期限までセグメントを取れなかった", "start", ar.Start, "err", err)
				continue
			}
			lf.log.Warn("ライブの入手が失敗した。待って繋ぎ直す", "start", ar.Start, "err", err, "wait", retry)
		}
		if err := waitCtx(ctx, retry); err != nil {
			return cp, err
		}
	}
	cp.Done = true
	if err := writeLiveCheckpoint(base, f, cp); err != nil {
		return cp, fmt.Errorf("ライブ録音の記録を書けない: %w", err)
	}
	lf.log.Info("ライブ録音の放送回を見終えた", "start", ar.Start, "segments", cp.Segments, "gaps", len(cp.Gaps))
	return cp, nil
}

// laterOf は x と y の遅い方。
func laterOf(x, y time.Time) time.Time {
	if x.After(y) {
		return x
	}
	return y
}

// waitCtx は d だけ待つ。ctx が取り消されたら ctx のエラーを返す。
func waitCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// fmtLength は長さを「N 分 M 秒」の形にする（秒に丸める）。
func fmtLength(d time.Duration) string {
	d = d.Round(time.Second)
	m, s := int(d/time.Minute), int(d%time.Minute/time.Second)
	switch {
	case m == 0:
		return fmt.Sprintf("%d 秒", s)
	case s == 0:
		return fmt.Sprintf("%d 分", m)
	}
	return fmt.Sprintf("%d 分 %d 秒", m, s)
}

// liveGapLines は、欠けた区間を通知の本文の行にする。欠けた区間は放送の中の時刻なので、その放送回の開始の放送日の時刻で
// 秒まで書く（ADR 0012）。
func liveGapLines(gaps []airingGap) string {
	var b strings.Builder
	for _, g := range gaps {
		day := g.airing.Day()
		fmt.Fprintf(&b, "\n- %s %s〜%s（%s）%s", g.airing.StationID, broadcastClockSec(day, g.From),
			broadcastClockSec(day, g.To), fmtLength(g.To.Sub(g.From)), g.airing.Title)
	}
	return b.String()
}
