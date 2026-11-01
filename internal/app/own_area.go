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
// 呼ばれても詰まらないように）。展開役は、まず RadikoAreaGuide の覚えている自分のエリアを新しいエリアに差し替え（setOwnArea）、
// 新しいエリアの局一覧を取ってから、まだ終わっていないジョブの録音期限を付け直し（reviseDeadlines。会員種別を保存したときと
// 同じ処理）、新しいエリアを DB に覚え、自分のエリアが変わったことを通知し、番組表の同期を 1 回行う。局一覧を取れない・
// 付け直せない・覚えられないときは、App と DB が覚えている自分のエリアを変えず、次の認証でやり直す（RadikoAreaGuide の覚えは、
// radiko が判定した今のエリアなので、新しい値のまま）。
//
// 会員種別を保存したときの付け直しも、展開役に頼む（requestRevise。SettingsApplied は待たない）。付け直しは展開役だけが行い、
// Store.ExpandDay の書き込みとも順番に並ぶ。会員種別と自分のエリアは、展開役が付け直す直前に読むので、2 つの付け直しが重なって
// 古い値で上書きすることは無い（付け直しの間に会員種別が保存されたら、その後にもう一度付け直す）。

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

// requestRevise は、会員種別を保存したときの録音期限の付け直しを展開役に頼む（待たない）。
func (a *App) requestRevise() {
	a.ownAreaMu.Lock()
	a.reviseByMembership = true
	a.ownAreaMu.Unlock()
	a.initExpander()
	select {
	case a.expandWake <- struct{}{}:
	default:
	}
}

// reviseIfRequested は、頼まれた録音期限の付け直し（自分のエリアの変化・会員種別の保存）を行う。展開役で呼ぶ。
// 自分のエリアの変化があれば、その付け直し（今の会員種別で行う）を先に行い、済めば会員種別の保存の分はそれで足りる。
// 会員種別と自分のエリアは、ここで読む（頼んだ時点の値ではない）。
func (a *App) reviseIfRequested(ctx context.Context) {
	a.ownAreaMu.Lock()
	from, to := a.ownAreaSeen, a.ownAreaPending
	byMembership := a.reviseByMembership
	a.reviseByMembership = false
	a.ownAreaMu.Unlock()
	if to != "" && to != from && a.reviseForOwnArea(ctx, from, to) {
		return
	}
	if byMembership {
		a.reviseForMembership(ctx)
	}
}

// reviseForMembership は、会員種別を保存したときの録音期限の付け直し。展開役で呼ぶ。今の会員種別と、今の自分のエリア
// （RadikoAreaGuide が覚えているもの）の局一覧で付け直す。局一覧を取れなければ、ジョブの放送回の印（エリアの外から録るか）の
// まま付け直す。
func (a *App) reviseForMembership(ctx context.Context) {
	m := a.membership()
	own, err := a.ownStations(ctx, m, "")
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.log().Warn("会員種別を変えたが、自分のエリアの局一覧を取れないので、エリアの外から録るかはジョブの放送回の印のまま録音期限を付け直す",
			"membership", m, "err", err)
		own = nil
	}
	r, err := a.reviseDeadlines(ctx, m, own)
	if err != nil {
		if ctx.Err() == nil {
			a.log().Error("会員種別を変えたが、まだ終わっていないジョブの録音期限を付け直せない", "membership", m, "err", err)
		}
		return
	}
	a.log().Info("会員種別を変えたので、まだ終わっていないジョブの録音期限を付け直した", "membership", m, "jobs", r.Revised, "passed", r.Passed)
}

// reviseForOwnArea は、自分のエリアが from から to に変わったときの録音期限の付け直し。展開役で呼ぶ。付け直せたら true。
func (a *App) reviseForOwnArea(ctx context.Context, from, to string) bool {
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
		return false
	}
	r, err := a.reviseDeadlines(ctx, m, own)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("自分のエリアが変わったが、まだ終わっていないジョブの録音期限を付け直せない（次の認証でやり直す）", "err", err)
		}
		finish(false)
		return false
	}
	if err := a.Store.SetOwnArea(ctx, to); err != nil {
		log.Error("録音期限を付け直したが、新しい自分のエリアを DB に覚えられない（次の認証でやり直す）", "err", err)
		finish(false)
		return true
	}
	finish(true)
	log.Info("自分のエリアが変わったので、まだ終わっていないジョブの録音期限を付け直した", "membership", m, "jobs", r.Revised, "passed", r.Passed)
	a.notifyOwnAreaChanged(ctx, log, from, to, r)
	// 展開が触るジョブ（ライブで録る waiting を含む）は、新しいエリアで番組表に合わせ直す。
	a.syncOnExpander(ctx)
	return true
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

// reviseDeadlines は録音期限の付け直し。会員種別を保存したとき（reviseForMembership）と、自分のエリアが変わったとき
// （reviseForOwnArea）の両方から、展開役で呼ぶ。まだ終わっていないジョブの録音期限を、会員種別 m と自分のエリアの局一覧 own
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
