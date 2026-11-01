package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// 自分のエリアの変化と録音期限の付け直し（docs/adr/0032-revise-deadlines-when-own-area-changes.md）。
//
// 最後に見た自分のエリアを DB（app_state。store.Store.OwnArea）に覚え、radiko の認証の応答のエリアと比べる。比べるのは、
// 認証している場所のどれも（タイムフリーの試行 run.go・ライブの試行 live.go・番組表の RadikoAreaGuide。展開が自分のエリアを
// 知るのも RadikoAreaGuide を通る）。起動の後に初めて認証したときも、DB から読んだ値と比べる。何も覚えていなければ覚えるだけにし、
// radiko がエリア外と判定したとき（認証の失敗）は比べない。
//
// 違ったら、付け直しを展開役に頼む（seeOwnArea は展開役の起き口 expandWake を叩くだけで、待たない。展開役の中の認証から
// 呼ばれても詰まらないように）。展開役は、新しいエリアの局一覧を取ってから、まだ終わっていないジョブの録音期限を付け直し
// （reviseDeadlines。会員種別を保存したときと同じ処理）、新しいエリアを DB に覚え、自分のエリアが変わったことを通知し、
// 番組表の同期を 1 回行う。局一覧を取れない・付け直せない・覚えられないときは、覚えている自分のエリアを変えず、次の認証でやり直す。

// ownAreaSetter は、覚えている自分のエリアを差し替えられる AreaGuide（RadikoAreaGuide）。自分のエリアが変わったと分かったら、
// TTL を待たずに新しいエリアで番組表を引くように差し替える。
type ownAreaSetter interface {
	setOwnArea(area string)
}

// SeeOwnArea は、radiko の認証の応答の自分のエリア area を見たことを知らせる（cmd が RadikoAreaGuide.OnArea につなぐ）。
func (a *App) SeeOwnArea(area string) {
	a.seeOwnArea(context.Background(), a.log(), area)
}

// seeOwnArea は、radiko の認証が通って自分のエリア area が分かったときに呼ぶ。覚えている自分のエリアと違えば、付け直しを
// 展開役に頼む（待たない）。何も覚えていなければ覚えるだけにする。area が空なら何もしない。
func (a *App) seeOwnArea(ctx context.Context, log *slog.Logger, area string) {
	if area == "" {
		return
	}
	a.ownAreaMu.Lock()
	defer a.ownAreaMu.Unlock()
	if !a.ownAreaLoaded {
		v, err := a.Store.OwnArea(ctx)
		if err != nil {
			log.Warn("最後に見た自分のエリアを DB から読めない（次の認証で比べ直す）", "err", err)
			return
		}
		a.ownAreaSeen, a.ownAreaLoaded = v, true
	}
	if area == a.ownAreaSeen {
		a.ownAreaPending = ""
		return
	}
	if a.ownAreaSeen == "" {
		if err := a.Store.SetOwnArea(ctx, area); err != nil {
			log.Warn("自分のエリアを DB に覚えられない（次の認証で覚え直す）", "area", area, "err", err)
			return
		}
		a.ownAreaSeen = area
		log.Info("自分のエリアを覚えた", "area", area)
		return
	}
	if a.ownAreaPending == area {
		return
	}
	a.ownAreaPending = area
	log.Info("自分のエリアが変わった。まだ終わっていないジョブの録音期限の付け直しを展開役に頼む", "from", a.ownAreaSeen, "to", area)
	a.initExpander()
	select {
	case a.expandWake <- struct{}{}:
	default:
	}
}

// reviseForOwnArea は、seeOwnArea が頼んだ自分のエリアの変化があれば、録音期限を付け直す。展開役で呼ぶ。
func (a *App) reviseForOwnArea(ctx context.Context) {
	a.ownAreaMu.Lock()
	from, to := a.ownAreaSeen, a.ownAreaPending
	a.ownAreaMu.Unlock()
	if to == "" || to == from {
		return
	}
	log := a.log().With("from", from, "to", to)
	finish := func(ok bool) {
		a.ownAreaMu.Lock()
		if a.ownAreaPending == to {
			a.ownAreaPending = ""
		}
		if ok {
			a.ownAreaSeen = to
		}
		a.ownAreaMu.Unlock()
	}
	if g, ok := a.AreaGuide.(ownAreaSetter); ok {
		g.setOwnArea(to)
	}
	m := a.membership()
	own, err := a.ownStations(ctx, m, to)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("自分のエリアが変わったが、新しいエリアの局一覧を取れないので、録音期限を付け直さない（次の認証でやり直す）", "err", err)
		}
		finish(false)
		return
	}
	r, err := a.reviseDeadlines(ctx, m, own)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("自分のエリアが変わったが、まだ終わっていないジョブの録音期限を付け直せない（次の認証でやり直す）", "err", err)
		}
		finish(false)
		return
	}
	if err := a.Store.SetOwnArea(ctx, to); err != nil {
		log.Error("録音期限を付け直したが、新しい自分のエリアを DB に覚えられない（次の認証でやり直す）", "err", err)
		finish(false)
		return
	}
	finish(true)
	log.Info("自分のエリアが変わったので、まだ終わっていないジョブの録音期限を付け直した", "jobs", r.Revised, "passed", r.Passed)
	a.notifyOwnAreaChanged(ctx, log, from, to, r)
	// 展開が触るジョブ（ライブで録る waiting を含む）は、新しいエリアで番組表に合わせ直す。
	a.syncOnExpander(ctx)
}

// ownStations は、会員種別 m で録音期限を付け直すときに使う、エリア area（空なら今の自分のエリア）の局一覧を返す。
// エリアフリー・ダブルプランでなければ、エリアの外から録るかを使わないので、取らずに nil を返す。AreaGuide が無ければ nil
// （ジョブの放送回の印をそのまま使う）。取れない・空ならエラー。
func (a *App) ownStations(ctx context.Context, m domain.Membership, area string) (map[string]bool, error) {
	if !m.AreaFree() || a.AreaGuide == nil {
		return nil, nil
	}
	ss, err := a.AreaGuide.AreaAiringsOf(ctx, area, domain.BroadcastDayOf(a.now()))
	if err != nil {
		return nil, fmt.Errorf("自分のエリアの局一覧を取れない: %w", err)
	}
	if len(ss) == 0 {
		return nil, errors.New("自分のエリアの局一覧が空")
	}
	return stationSet(ss), nil
}

// reviseDeadlines は録音期限の付け直し。会員種別を保存したとき（SettingsApplied）と、自分のエリアが変わったとき
// （reviseForOwnArea）の両方から呼ぶ。まだ終わっていないジョブの録音期限を、会員種別 m と自分のエリアの局一覧 own
// （nil なら分からない）で付け直す（store.Store.ReviseDeadlines）。
func (a *App) reviseDeadlines(ctx context.Context, m domain.Membership, own map[string]bool) (store.RevisedDeadlines, error) {
	return a.Store.ReviseDeadlines(ctx, m, own, a.now())
}

// notifyOwnAreaChanged は、自分のエリアが from から to に変わったこと（自分のエリアの出来事）を通知として書く。
// 変わるたびに 1 通。notify.success = false でも送る（newNotification が止めるのは録音済みだけ）。
// 書けなければ Error でログに出す。
func (a *App) notifyOwnAreaChanged(ctx context.Context, log *slog.Logger, from, to string, r store.RevisedDeadlines) {
	msg := fmt.Sprintf("【自分のエリアが変わった】radiko が判定した自分のエリアが %s から %s に変わったので、まだ終わっていないジョブの"+
		"録音期限を付け直した（%d 件。うち録音期限が過ぎた値になったジョブ %d 件）。", from, to, r.Revised, r.Passed)
	if r.Passed > 0 {
		msg += "\n録音期限が過ぎたジョブは、次の試行で、取れた分を保存するか録音できなかったにする。"
	}
	n := a.newNotification(notify.KindAreaChanged, "", msg)
	id, err := a.Store.AddNotification(context.WithoutCancel(ctx), n)
	if err != nil {
		log.Error("通知を DB に書けない", "kind", notify.KindAreaChanged, "message", msg, "err", err)
		return
	}
	log.Info("通知を書いた", "notification", id, "kind", notify.KindAreaChanged, "message", msg)
}
