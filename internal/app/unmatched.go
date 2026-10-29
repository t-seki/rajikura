package app

// 当たらない日（用語集）を求めて記録し、新しく見つけたものを番組の出来事として通知する。
//
//   - 求める: 展開役が局・放送日を展開したとき（syncDay。番組表の同期・番組を変えた直後の展開・その試し直し・最初の試行の前の
//     番組表の取り直し）に、展開と同じ番組表と番組の一覧から domain.FindUnmatchedDays で求め、その局・放送日の記録を
//     store.ReplaceUnmatchedDays で揃える。対象は今日から 7 日先までの放送日。番組表を取れなかった・空だった局・放送日は
//     展開と同じく記録に触らない
//   - 通知する: 番組表の同期 1 回（番組を変えた直後の展開なら、展開 1 回）の終わりに、まだ通知していない当たらない日を番組ごとに
//     1 通にまとめて書く（notifyUnmatchedDays）。通知した印は、通知を DB に書くのと同じトランザクションで立てる
//     （store.AddUnmatchedDayNotifications）。notify.success = false でも送る（止めるのは録音済みだけ）

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/t-seki/rajikura/internal/domain"
	"github.com/t-seki/rajikura/internal/notify"
	"github.com/t-seki/rajikura/internal/store"
)

// recordUnmatchedDays は、局 station の放送日 day の番組表 airings と番組の一覧 shows（syncDay が展開に使ったもの）から
// 当たらない日を求め、その局・放送日の記録を揃える。書けなければログに出すだけで、展開は成功のまま（次の展開で書き直す）。
func (a *App) recordUnmatchedDays(ctx context.Context, station string, day domain.BroadcastDay, airings []domain.Airing,
	shows []domain.Show) {
	now := a.now()
	today := domain.BroadcastDayOf(now)
	res := domain.FindUnmatchedDays(domain.UnmatchedInput{
		Shows: shows, StationID: station, Day: day, Airings: airings, Now: now, Until: today.AddDays(syncDaysAhead),
	})
	if err := a.Store.ReplaceUnmatchedDays(ctx, station, day, today, res); err != nil {
		if ctx.Err() == nil {
			a.log().Error("当たらない日の記録を書けない（次の展開で書き直す）", "station", station, "day", day, "err", err)
		}
	}
}

// notifyUnmatchedDays は、まだ通知していない当たらない日を、番組ごとに 1 通の通知（unmatched_day）にまとめて書く。
// 展開役で、同期 1 回・展開 1 回の終わりに呼ぶ。書けなければログに出すだけ（次の同期・展開で書く）。
func (a *App) notifyUnmatchedDays(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ns, err := a.Store.AddUnmatchedDayNotifications(ctx, a.now(), func(name string, days []domain.UnmatchedDay) store.Notification {
		// 番組の名前を持たせる（通知の画面とトーストがその番組の編集を開く）。
		n := a.newNotification(notify.KindUnmatchedDay, "", unmatchedDayMessage(name, days))
		n.Show = name
		return n
	})
	if err != nil {
		if ctx.Err() == nil {
			a.log().Error("当たらない日の通知を DB に書けない（次の同期・展開で書く）", "err", err)
		}
		return
	}
	for _, n := range ns {
		a.log().Info("通知を書いた", "notification", n.ID, "kind", n.Kind, "message", n.Message)
	}
}

var weekdayNames = [...]string{"日", "月", "火", "水", "木", "金", "土"}

// unmatchedDayLabel は当たらない日の枠の言い方（例: 10/19（月）17:40。時刻は放送日の時刻で、25:00 などにもなる）。
func unmatchedDayLabel(u domain.UnmatchedDay) string {
	return fmt.Sprintf("%d/%d（%s）%s", int(u.Day.Month), u.Day.Day, weekdayNames[u.Day.Weekday()], broadcastClock(u.Day, u.Start))
}

// dayClock は時刻 t を放送日 day の時刻で書く（broadcastClock）。放送日の始まりより前の時刻（前の放送日から続く放送回の開始）は、
// そのままの時刻で書く。
func dayClock(day domain.BroadcastDay, t time.Time) string {
	if t.Before(day.Start()) {
		return t.In(domain.JST).Format("15:04")
	}
	return broadcastClock(day, t)
}

// unmatchedDayFacts は、当たらない日の番組表の事実（その時刻を含む放送回、番組名の合う放送回）を 1 行にする。
func unmatchedDayFacts(u domain.UnmatchedDay) string {
	var b strings.Builder
	b.WriteString(unmatchedDayLabel(u) + " は放送回が無い。")
	if c := u.Covering; c != nil {
		fmt.Fprintf(&b, "その時刻は「%s」（%s〜%s）。", oneLine(c.Title), dayClock(u.Day, c.Start), dayClock(u.Day, c.End))
	} else {
		b.WriteString("その時刻を含む放送回も番組表に無い。")
	}
	if len(u.Titled) == 0 {
		b.WriteString("番組名の合う放送回は無い。")
	} else {
		var ts []string
		for _, t := range u.Titled {
			ts = append(ts, broadcastClock(u.Day, t.Start)+"〜"+broadcastClock(u.Day, t.End))
		}
		b.WriteString("番組名の合う放送回: " + strings.Join(ts, "、"))
	}
	return b.String()
}

// unmatchedDayMessage は当たらない日の通知の本文。番組の名前と、当たらない日ごとに 1 行の事実。
func unmatchedDayMessage(name string, days []domain.UnmatchedDay) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【当たらない日】番組「%s」の毎週の予約の枠に、番組表の放送回が無い日がある（録音しない）。", name)
	for _, u := range days {
		b.WriteString("\n- " + unmatchedDayFacts(u))
	}
	return b.String()
}
