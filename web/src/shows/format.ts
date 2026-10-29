import type { UnmatchedDay, Weekly, Station } from "./api";
import { broadcastClock, broadcastDayOf, dayLabel, weekdayOf } from "../guide/format";

/** 曜日の名前。添字は 0 = 日曜。 */
export const weekdayNames = ["日", "月", "火", "水", "木", "金", "土"];

/** 月曜始まりの週での曜日の順（0 = 月 … 6 = 日）。 */
function weekdayRank(weekday: number): number {
  return (weekday + 6) % 7;
}

/** 放送日の時刻は 5 時から始まる（05:00〜28:59）。 */
const BROADCAST_DAY_START_HOUR = 5;

/**
 * API の開始時刻（00:00〜23:59）を放送日の時刻（05:00〜28:59）にする（01:00 → 25:00）。HH:MM でない値はそのまま返す。
 * 人が読み書きする口（一覧・フォーム・番組表の詳細）で使う。
 */
export function toBroadcastClock(start: string): string {
  const m = /^(\d\d):(\d\d)$/.exec(start);
  if (!m) return start;
  const h = Number(m[1]);
  return h < BROADCAST_DAY_START_HOUR ? `${h + 24}:${m[2]}` : start;
}

/** 放送日の時刻を API の開始時刻に戻した結果。early は 00:00〜04:59（前の曜日の 24:00〜28:59 で書くべき値）、invalid は HH:MM の 05:00〜28:59 でない値。 */
export type FromBroadcastClock = { ok: true; start: string } | { ok: false; reason: "early" | "invalid" };

/** 放送日の時刻（05:00〜28:59）を API の開始時刻（00:00〜23:59）に戻す（25:00 → 01:00）。00:00〜04:59 とそれ以外の値は断る。 */
export function fromBroadcastClock(text: string): FromBroadcastClock {
  const m = /^(\d\d):(\d\d)$/.exec(text);
  if (!m) return { ok: false, reason: "invalid" };
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (min > 59 || h > 28) return { ok: false, reason: "invalid" };
  if (h < BROADCAST_DAY_START_HOUR) return { ok: false, reason: "early" };
  return { ok: true, start: h >= 24 ? `${String(h - 24).padStart(2, "0")}:${m[2]}` : text };
}

/** fromBroadcastClock が断ったときにフォームの欄に出す文。 */
export const broadcastClockErrors: Record<"early" | "invalid", string> = {
  early: "深夜 0〜4 時台の放送は、前の曜日の 24:00〜28:59 で入れる（例: 月曜の深夜 1 時 → 日 25:00）",
  invalid: "開始は 05:00〜28:59 の HH:MM で入れてほしい",
};

/** フォームのカードと一覧の 1 行になる、毎週の予約のまとまり。weekdays は月曜始まりの順。 */
export type WeeklyGroup = { station: string; weekdays: number[]; start: string; from?: string; until?: string };

/**
 * 局・開始時刻・開始日・終了日が同じ毎週の予約を、並びで続いているかに関係なく 1 つのまとまりにする。
 * 同じ曜日が重なるものは別のまとまりにする。まとまりの順は、いちばん早い曜日（月曜始まり）→ 開始時刻（放送日の時刻で比べる）→ 放送局。
 * start は API の値（00:00〜23:59）のまま持つ。元の配列は変えない。
 */
export function groupWeekly(weekly: Weekly[]): WeeklyGroup[] {
  const groups: WeeklyGroup[] = [];
  for (const r of weekly) {
    const from = r.from || undefined;
    const until = r.until || undefined;
    const g = groups.find(
      (x) =>
        x.station === r.station && x.start === r.start && x.from === from && x.until === until && !x.weekdays.includes(r.weekday),
    );
    if (g) g.weekdays.push(r.weekday);
    else groups.push({ station: r.station, weekdays: [r.weekday], start: r.start, ...(from ? { from } : {}), ...(until ? { until } : {}) });
  }
  for (const g of groups) g.weekdays.sort((a, b) => weekdayRank(a) - weekdayRank(b));
  // 開始時刻は放送日の時刻で比べる（同じ曜日の中で 01:00 は 25:00 として 22:00 の後）
  const startKey = (g: WeeklyGroup) => toBroadcastClock(g.start);
  return groups.sort(
    (a, b) =>
      weekdayRank(a.weekdays[0]) - weekdayRank(b.weekdays[0]) ||
      (startKey(a) < startKey(b) ? -1 : startKey(a) > startKey(b) ? 1 : 0) ||
      (a.station < b.station ? -1 : a.station > b.station ? 1 : 0),
  );
}

/** まとまりを、まとまりの順に、まとまりの中の曜日の順に、曜日ごとの行へ戻す。 */
export function ungroupWeekly(groups: WeeklyGroup[]): Weekly[] {
  return groups.flatMap((g) =>
    g.weekdays.map((weekday) => ({
      station: g.station,
      weekday,
      start: g.start,
      ...(g.from ? { from: g.from } : {}),
      ...(g.until ? { until: g.until } : {}),
    })),
  );
}

function stationLabel(id: string, stations: Station[]): string {
  return stations.find((s) => s.id === id)?.name || id;
}

/**
 * まとまり（start は API の値）の要約（一覧と番組表の詳細に出す）。時刻は放送日の時刻。例: `TBSラジオ 月・火・水・木 08:30（2026-10-20 から）`、`ニッポン放送 土 25:00`。
 * 後ろの括弧は、今日の放送日（today。`YYYY-MM-DD`。既定は今）と比べて決める。
 *
 * - 終了日が今日より前: `（2026-12-26 で終了）`（開始日によらない）
 * - 開始日と終了日が今日以降: `（2026-10-20 から 2026-12-26 まで）`
 * - 終了日だけが今日以降（開始日が無いか今日より前）: `（2026-12-26 まで）`
 * - 開始日だけが今日以降（終了日が無い）: `（2026-10-20 から）`
 * - どちらも無いか、開始日が今日より前で終了日が無い: 付けない（今日から先の放送回には効かないので）
 */
export function groupSummary(g: WeeklyGroup, stations: Station[] = [], today: string = broadcastDayOf(new Date())): string {
  const from = g.from && g.from >= today ? g.from : "";
  let tail = "";
  if (g.until && g.until < today) tail = `（${g.until} で終了）`;
  else if (g.until) tail = from ? `（${from} から ${g.until} まで）` : `（${g.until} まで）`;
  else if (from) tail = `（${from} から）`;
  return `${stationLabel(g.station, stations)} ${g.weekdays.map((w) => weekdayNames[w] ?? "?").join("・")} ${toBroadcastClock(g.start)}${tail}`;
}

/** 毎週の予約を、まとまりごとに 1 つずつの要約にする（並びはまとまりの順）。 */
export function weeklySummaries(weekly: Weekly[], stations: Station[] = [], today: string = broadcastDayOf(new Date())): string[] {
  return groupWeekly(weekly).map((g) => groupSummary(g, stations, today));
}

/**
 * 放送回の開始を、その放送日と放送日の時刻で「10/20(火) 27:40」の形にする（放送日は番組表の日付の見出しと同じ形）。
 * 放送回の時刻なので、暦の日付ではなく放送日で書く（ADR 0012）。次の放送予定と 1 回の予約の一覧に使う。
 */
export function airingStartLabel(start: string): string {
  const day = broadcastDayOf(start);
  return `${dayLabel(day)} ${broadcastClock(start, day)}`;
}

/** 次の放送予定（開始時刻が今より後の放送回）。無効な番組は「無効」、無ければ 7 日先まで番組表に無いと出す。 */
export function nextAiringLabel(next: string | undefined, enabled = true): string {
  if (!enabled) return "無効（番組表の同期で当てない）";
  if (!next) return "7 日先まで番組表に無い";
  return airingStartLabel(next);
}

/**
 * 当たらない日の 1 件を、人が読む文にする（番組の画面の次の放送予定の下に出す）。時刻は放送日の時刻（25:00 など）。
 * 例: `10/19（月）17:40 は放送回が無い。その時刻は「辛坊治郎 ズーム そこまで言うか！」（15:30〜18:00）` と `番組名の合う放送回: 19:00〜19:30`。
 */
export function unmatchedDayLines(u: UnmatchedDay): string[] {
  const [, m, d] = u.day.split("-").map(Number);
  const range = (a: { start: string; end: string }) => `${broadcastClock(a.start, u.day)}〜${broadcastClock(a.end, u.day)}`;
  let head = `${m}/${d}（${weekdayNames[weekdayOf(u.day)]}）${broadcastClock(u.start, u.day)} は放送回が無い。`;
  head += u.covering ? `その時刻は「${u.covering.title}」（${range(u.covering)}）` : "その時刻を含む放送回も番組表に無い";
  const titled = u.titled.length === 0 ? "番組名の合う放送回は無い" : `番組名の合う放送回: ${u.titled.map(range).join("、")}`;
  return [head, titled];
}
