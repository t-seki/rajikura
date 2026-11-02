package app

// キーワード（用語集。ADR 0014）を番組表に当て、当たったまだ始まっていない放送回を、番組名の番組の 1 回の予約として
// 足す（無ければ番組を作る）。展開役でだけ行う（applyKeywords）。
//
//   - いつ: 番組表の同期のとき（Sync が、局・放送日の展開の前に当てる。足した 1 回の予約の局・放送日は、続けて同期の
//     展開に入る）と、キーワードを作った・変えた直後（Web UI・API が RequestKeywords で展開役に頼む。足した番組の局・放送日を
//     続けて展開する）
//   - どこに: エリアの日別番組表（AreaGuide。番組表の検索と同じ口で、radiko.Client の番組表のキャッシュを共有する）を、今日から
//     7 日先までの放送日について引き、局の絞り込みはその結果から選ぶ。番組表を取れない・空の放送日には当てない。
//     会員種別がエリアフリー・ダブルプランなら、有効なキーワードが名指しした自分のエリアの外の局（その放送日の自分のエリアの
//     局一覧に無い局）も、局ごとの日別の番組表（Guide.Airings。展開と同じ口で、番組表の同期と同じ鮮度で読む）で足して引き、
//     エリアの外から録る印（domain.Airing.OutOfArea）を立てて当てる（OutOfAreaKeywordStations）。全国は引かない。
//     足して引いた局のある放送日を取れない・空なら、その局のその放送日だけ飛ばす。非会員・タイムフリー30 では足さない
//     （キーワードは消さず、stations も書き換えない。当てるたびに、その時点の会員種別で決める）
//   - 何に: まだ始まっていない放送回のうち、キーワードの局の内側で、語に当たり除外する語に当たらないもの（domain.KeywordMatcher）
//   - 足し方・飛ばし方・上限・キーワードどうしの重なり: domain.PlanKeywords。判定と、1 回の予約・番組・記録・通知の書き込みは
//     1 つのトランザクションで行う（store.ApplyKeywords。HTTP のハンドラが同じ時に番組や 1 回の予約を書くため）
//   - 通知: 足すたびに keyword_added を 1 回（パートをまとめて足したときも 1 回。分割番組でない番組が、足した後にその放送日に
//     2 つ以上の放送回を当てるときは、パートごとに別のファイルになる旨を本文に添える（domain.KeywordAddition の Separate。
//     ADR 0003）、上限で足すのを止めたら keyword_limit を 1 回
//     （上限を下回ってから、また達したら、もう一度）。どちらも notify.success = false では止めない
//
// AreaGuide が nil なら、キーワードを当てない。

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

func (a *App) keywordMaxPending() int {
	if n := a.current().keywordMaxPending; n > 0 {
		return n
	}
	return domain.DefaultKeywordMaxPending
}

// RequestKeywords は、キーワードを作った・変えた直後に、キーワードを番組表に当てることを展開役に頼み、待たずに戻る
// （Web UI・API が呼ぶ）。展開役は、積まれた展開の依頼と一緒に、キーワードを当ててから展開する（足した番組の局・放送日も
// 展開する）。依頼はプロセスの中に積むだけなので、serve を再起動すると消える（起動時の同期で当て直す）。
func (a *App) RequestKeywords() {
	a.initExpander()
	a.expandMu.Lock()
	a.keywordsRequested = true
	a.expandMu.Unlock()
	select {
	case a.expandWake <- struct{}{}:
	default:
	}
}

// applyKeywordsLogged はキーワードを当て、足した先の番組の名前を返す。失敗したらログに出して nil を返す（次の同期で当て直す）。
// 展開役で呼ぶ。
func (a *App) applyKeywordsLogged(ctx context.Context) []string {
	names, err := a.applyKeywords(ctx)
	if err != nil && ctx.Err() == nil {
		a.log().Error("キーワードを番組表に当てられない（次の番組表の同期で当て直す）", "err", err)
	}
	return names
}

// applyKeywords は、有効なキーワードを、今日から 7 日先までの放送日の番組表に当て、当たった放送回を番組の 1 回の予約に
// 足す。足した先の番組の名前を（重ねずに）返す。展開役で呼ぶ。
func (a *App) applyKeywords(ctx context.Context) ([]string, error) {
	if a.AreaGuide == nil {
		return nil, nil
	}
	keywords, err := a.Store.ListKeywords(ctx)
	if err != nil {
		return nil, err
	}
	keywords = slices.DeleteFunc(keywords, func(k domain.Keyword) bool { return k.Disabled })
	if len(keywords) == 0 {
		return nil, nil
	}
	now := a.now()
	today := domain.BroadcastDayOf(now)
	hits := a.keywordHits(ctx, keywords, now, today)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	max := a.keywordMaxPending()
	changes, err := a.Store.ApplyKeywords(ctx, today, now, func(st store.KeywordState) store.KeywordChanges {
		plan := domain.PlanKeywords(domain.KeywordInput{
			Keywords: st.Keywords, Hits: hits, Shows: st.Shows, Jobs: st.Jobs, Recorded: st.Recorded,
			Pending: st.Pending, LimitNotified: st.LimitNotified, MaxPending: max, Now: now, Membership: a.membership(),
		})
		var ns []store.Notification
		for _, add := range plan.Additions {
			// 足した先の番組の名前を持たせる（通知の画面とトーストがその番組の編集を開く）。
			n := a.newNotification(notify.KindKeywordAdded, "", keywordAddedMessage(add))
			n.Show = add.ShowName
			ns = append(ns, n)
		}
		for _, k := range plan.Limited {
			ns = append(ns, a.newNotification(notify.KindKeywordLimit, "", keywordLimitMessage(k, max)))
		}
		return store.KeywordChanges{Plan: plan, Notifications: ns}
	})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, add := range changes.Plan.Additions {
		a.log().Info("キーワードに当たった放送回を番組の 1 回の予約に足した", "keyword", add.Keyword.ID, "words", add.Keyword.Words,
			"show", add.ShowName, "created", add.Created, "title", add.Hit.Airing.Title, "station", add.Hit.Airing.StationID,
			"start", add.Hit.Airing.Start, "parts", len(add.Airings))
		if !slices.Contains(names, add.ShowName) {
			names = append(names, add.ShowName)
		}
	}
	for _, k := range changes.Plan.Limited {
		a.log().Warn("キーワードの上限に達したので、当たった放送回を足さない", "keyword", k.ID, "words", k.Words, "max", max)
	}
	for _, n := range changes.Notifications {
		a.log().Info("通知を書いた", "kind", n.Kind, "message", n.Message)
	}
	return names, nil
}

// keywordHits は、今日から 7 日先までの放送日のエリアの番組表から、キーワード keywords が当たる、まだ始まっていない放送回を
// キーワードの ID ごとに開始の順で返す。番組表を取れない・空の放送日は飛ばす（ログに出す）。名指しされた自分のエリアの外の局は
// OutOfAreaKeywordStations で足して引く（取れない局は、その放送日だけ飛ばす。ログに出す）。
func (a *App) keywordHits(ctx context.Context, keywords []domain.Keyword, now time.Time, today domain.BroadcastDay) map[int64][]domain.KeywordHit {
	matchers := make([]domain.KeywordMatcher, len(keywords))
	for i, k := range keywords {
		matchers[i] = k.Matcher()
	}
	hits := map[int64][]domain.KeywordHit{}
	// 放送日をまたぐ放送回（深夜）が隣の日の番組表にも出うるので、Key で重ねを除く。
	seen := map[string]bool{}
	for i := 0; i <= syncDaysAhead; i++ {
		day := today.AddDays(i)
		stations, err := a.AreaGuide.AreaAirings(ctx, day)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			a.log().Warn("エリアの番組表を取れないので、この放送日にはキーワードを当てない", "day", day, "err", err)
			continue
		}
		if !slices.ContainsFunc(stations, func(s AreaStation) bool { return len(s.Airings) > 0 }) {
			a.log().Warn("エリアの番組表が空なので、この放送日にはキーワードを当てない", "day", day)
			continue
		}
		if a.Guide != nil {
			extra, failed := OutOfAreaKeywordStations(ctx, a.Guide, stations, keywords, a.membership(), day, func(id string) string {
				return cmp.Or(a.station(ctx, id).Name, id)
			})
			if ctx.Err() != nil {
				return nil
			}
			for _, f := range failed {
				a.log().Warn("名指しされた自分のエリアの外の局の番組表を取れないので、この局のこの放送日にはキーワードを当てない",
					"station", f.Station, "day", day, "err", f.Err)
			}
			stations = append(slices.Clip(stations), extra...)
		}
		for _, st := range stations {
			for _, ar := range st.Airings {
				if seen[ar.Key()] {
					continue
				}
				seen[ar.Key()] = true
				if !ar.Start.After(now) {
					continue
				}
				for j, k := range keywords {
					if k.InStations(st.ID) && matchers[j].Match(ar) {
						hits[k.ID] = append(hits[k.ID], domain.KeywordHit{Airing: ar, StationName: st.Name, DayAirings: st.Airings})
					}
				}
			}
		}
	}
	for id := range hits {
		slices.SortStableFunc(hits[id], func(x, y domain.KeywordHit) int {
			return cmp.Or(x.Airing.Start.Compare(y.Airing.Start), cmp.Compare(x.Airing.StationID, y.Airing.StationID))
		})
	}
	return hits
}

// OutOfAreaFailure は、名指しされた自分のエリアの外の局の、ある放送日の番組表を取れなかったこと（OutOfAreaKeywordStations）。
type OutOfAreaFailure struct {
	Station string
	Err     error
}

// OutOfAreaKeywordStations は、キーワード keywords を放送日 day の番組表に当てるときに、自分のエリアの番組表 own に足して引く局
// （キーワードが名指しした自分のエリアの外の局）の放送回を返す（キーワードの照合 App.keywordHits と、キーワードのプレビュー
// internal/server/keywords.go が使う）。
//
//   - 足すのは、会員種別 m がエリアフリー・ダブルプランのときだけ（ほかでは何も返さない）
//   - 自分のエリアの外の局かは、局の本拠地のエリアではなく、own の局一覧（stationSet）に無いかで決める（App.markOutOfArea と同じ）
//   - 番組表は局ごとの日別の番組表（g.Airings。展開と同じ口）で引き、放送回にはエリアの外から録る印（domain.Airing.OutOfArea）を立てる
//   - 局の名前は name で決める
//   - 取れない・空の局は返さず、failed に入れる（呼び出し側が、その局のその放送日だけ飛ばすか、まとめて断るかを決める）
//
// 返す局は、キーワードの順・名指しの順（重ねない）。
func OutOfAreaKeywordStations(ctx context.Context, g Guide, own []AreaStation, keywords []domain.Keyword, m domain.Membership,
	day domain.BroadcastDay, name func(id string) string) (extra []AreaStation, failed []OutOfAreaFailure) {
	if !m.AreaFree() {
		return nil, nil
	}
	inOwn := stationSet(own)
	seen := map[string]bool{}
	for _, k := range keywords {
		for _, id := range k.Stations {
			if inOwn[id] || seen[id] {
				continue
			}
			seen[id] = true
			airings, err := g.Airings(ctx, id, day)
			if err == nil && len(airings) == 0 {
				err = errors.New("番組表が空")
			}
			if err != nil {
				if ctx.Err() != nil {
					return extra, failed
				}
				failed = append(failed, OutOfAreaFailure{Station: id, Err: err})
				continue
			}
			marked := slices.Clone(airings)
			for i := range marked {
				marked[i].OutOfArea = true
			}
			extra = append(extra, AreaStation{ID: id, Name: name(id), Airings: marked})
		}
	}
	return extra, failed
}

// broadcastClock は時刻 t を、放送日 day の時刻（05:00〜28:59）で書く。
func broadcastClock(day domain.BroadcastDay, t time.Time) string {
	t = t.In(domain.JST)
	h := int(t.Sub(day.Start()).Hours()) + 5
	return fmt.Sprintf("%02d:%02d", h, t.Minute())
}

// broadcastClockSec は時刻 t を、放送日 day の時刻で秒まで書く（05:00:00〜28:59:59。終わりの時刻は 29:00 を越えてよい）。
// ライブ録音の欠けた区間のように、放送の中の時刻を秒まで書くところに使う（ADR 0012）。
func broadcastClockSec(day domain.BroadcastDay, t time.Time) string {
	return fmt.Sprintf("%s:%02d", broadcastClock(day, t), t.In(domain.JST).Second())
}

// keywordAddedMessage はキーワードで録音予定にした通知の本文。
func keywordAddedMessage(add domain.KeywordAddition) string {
	first, last := add.Airings[0], add.Airings[len(add.Airings)-1]
	day := first.Day()
	station := cmp.Or(add.Hit.StationName, first.StationID)
	msg := fmt.Sprintf("【キーワードで録音予定】キーワード「%s」に当たった「%s」（放送日 %s %s〜%s %s）を、番組「%s」に足した",
		oneLine(add.Keyword.Words), oneLine(add.Hit.Airing.Title), day, broadcastClock(day, first.Start), broadcastClock(day, last.End),
		station, add.ShowName)
	if len(add.Airings) >= 2 {
		msg += fmt.Sprintf("（%d パートをまとめて）", len(add.Airings))
	}
	if add.Created {
		msg += "。番組「" + add.ShowName + "」は新しく作った"
	}
	if add.Separate {
		msg += "。番組「" + add.ShowName + "」は分割番組でないので、パートごとに別のファイルになる"
	}
	return msg + "。"
}

// keywordLimitMessage は上限で足すのを止めた通知の本文。
func keywordLimitMessage(k domain.Keyword, max int) string {
	return fmt.Sprintf("【キーワードの上限】キーワード「%s」は、足した放送回のうちまだ録音を終えていないものが上限（%d 件）に達したので、"+
		"当たった放送回を足さなかった。録音が終われば、次の番組表の同期で足す。上限は設定の keyword_max_pending で変えられる。",
		oneLine(k.Words), max)
}
