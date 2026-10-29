// 番組表の画面の日付・時刻・状態の表示。時刻は日本時間で扱う。

import type { AiringState, GuideAiring } from "./api";

const JST_OFFSET_MS = 9 * 3600 * 1000;
const BROADCAST_DAY_START_HOUR = 5;
const DAY_MS = 24 * 3600 * 1000;

const pad = (n: number) => String(n).padStart(2, "0");

function ymd(d: Date): string {
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
}

/** 時刻の放送日（5 時で区切る）を YYYY-MM-DD で返す。 */
export function broadcastDayOf(t: Date | string | number): string {
  return ymd(new Date(new Date(t).getTime() + JST_OFFSET_MS - BROADCAST_DAY_START_HOUR * 3600 * 1000));
}

export function addDays(day: string, n: number): string {
  return ymd(new Date(new Date(`${day}T00:00:00Z`).getTime() + n * DAY_MS));
}

/** 放送日の曜日（0 = 日曜）。 */
export const weekdayOf = (day: string) => new Date(`${day}T00:00:00Z`).getUTCDay();

const weekdayNames = ["日", "月", "火", "水", "木", "金", "土"];

/** 「10/12(月)」の形。 */
export function dayLabel(day: string): string {
  const [, m, d] = day.split("-").map(Number);
  return `${m}/${d}(${weekdayNames[weekdayOf(day)]})`;
}

/** 放送日の 5:00（日本時間）。 */
export const dayStart = (day: string) => new Date(`${day}T05:00:00+09:00`);

/** 日本時間の HH:MM（暦の 00:00〜23:59）。 */
export function clock(t: string | Date): string {
  const jst = new Date(new Date(t).getTime() + JST_OFFSET_MS);
  return `${pad(jst.getUTCHours())}:${pad(jst.getUTCMinutes())}`;
}

/**
 * 放送日の時刻を、放送日 day の 0 時（日本時間）から数えた分で返す。深夜 1 時は 25 時（1500 分）。
 * day を省くと t の放送日を基準にする。t が day の 0 時より前（基準の取り違え）なら null。
 */
export function broadcastMinutes(t: string | Date, day: string = broadcastDayOf(t)): number | null {
  const ms = new Date(t).getTime() - new Date(`${day}T00:00:00+09:00`).getTime();
  if (Number.isNaN(ms) || ms < 0) return null;
  return Math.floor(ms / 60000);
}

/**
 * 放送日の時刻の HH:MM（05:00〜28:59。終わりの時刻は 29:00 を越えることもある）。人が読む口で、放送回の時刻（開始・終了）を書くところに使う（ADR 0012）。
 * day は基準の放送日（終わりの時刻は、開始と同じ放送日を基準にする）。省くと t の放送日。基準より前の時刻なら暦の HH:MM。
 */
export function broadcastClock(t: string | Date, day?: string): string {
  const m = broadcastMinutes(t, day);
  if (m === null) return clock(t);
  return `${pad(Math.floor(m / 60))}:${pad(m % 60)}`;
}

/** 放送回の開始〜終わりを、開始の放送日を基準にした放送日の時刻で「23:00〜25:00」の形にする。 */
export function broadcastRange(a: Pick<GuideAiring, "start" | "end">): string {
  const day = broadcastDayOf(a.start);
  return `${broadcastClock(a.start, day)}〜${broadcastClock(a.end, day)}`;
}

/**
 * 放送回の開始の放送日を添えた「10/20(火) 27:40〜28:00」の形。放送日がすぐ近くに出ていないところ（番組表の詳細）に使う。
 * 放送回の時刻なので、暦の日付ではなく放送日と放送日の時刻で書く（ADR 0012）。
 */
export function airingDayRange(a: Pick<GuideAiring, "start" | "end">): string {
  return `${dayLabel(broadcastDayOf(a.start))} ${broadcastRange(a)}`;
}

/** タイムフリーの対象外（ライブで録る）の表示名。 */
export const notAvailableLabel = "ライブで録る";

/** 状態の表示名。バッジ・凡例・リストでは文字で出し、グリッドの枠では `aria-label`・`title` に使う。 */
export const stateLabel: Record<AiringState, string> = {
  none: "予約なし",
  planned: "録音予定",
  done: "録音済み",
  missed: "録音できなかった",
  skip_planned: "録らない予定",
  skipped: "録らなかった",
};

/** 状態の色（Mantine の色名）。 */
export const stateColor: Record<AiringState, string> = {
  none: "gray",
  planned: "blue",
  done: "green",
  missed: "red",
  skip_planned: "yellow",
  skipped: "yellow",
};

/** 状態の小さな印。予約なしは印を出さない。色だけに頼らないための印。グリッドの枠では印と `aria-label`・`title`、バッジ・凡例・リストでは文字（stateLabel）と組にして出す。 */
export const stateMark: Record<AiringState, string> = {
  none: "",
  planned: "●",
  done: "✓",
  missed: "!",
  skip_planned: "○",
  skipped: "–",
};

/** タイムフリーの対象外（ライブで録る）の印。 */
export const notAvailableMark = "◎";

/** ライブ録音の録音期限を放送の終了のどれだけ後にするか（domain.LiveDeadlineAfter と同じ 5 分）。 */
export const LIVE_DEADLINE_AFTER_MS = 5 * 60 * 1000;

/** タイムフリーの対象外（ライブで録る）の放送回が、録音期限（放送の終了の 5 分後。domain.AiringDeadline）を now で過ぎたか。過ぎたら録れないので、1 回の予約の API が断る。 */
export function liveExpired(a: Pick<GuideAiring, "notAvailable" | "end">, now: number = Date.now()): boolean {
  return a.notAvailable && now >= new Date(a.end).getTime() + LIVE_DEADLINE_AFTER_MS;
}

/** 大文字・小文字・全角を問わない `PART`。 */
const PART = "[PpＰｐ][AaＡａ][RrＲｒ][TtＴｔ]";
const SP = "[\\s　]";
const NUM = "[0-9０-９]+";

/**
 * 番組名の末尾の印（domain.TrimTrailingMark の rawTrailingMarkRe と同じ形）。全角・半角を問わない。
 *   - 括弧付き（前の空白の有無を問わない）: `(N)`、`(N時台)`、`(PARTN)`
 *   - 時間の範囲（前の空白の有無を問わない）: `N時～N時`、分が付く `N時M分～N時M分`
 *   - 括弧の無い形（前に空白があるときだけ）: `PartN`・`Part N`、`Hour.N`、丸数字 `①`〜`⑳`、`N時台`（前に `火曜` などの曜日、
 *     後ろに日付 `(MM/DD)` が付いてもよい）
 */
const trailingMarkRe = new RegExp(
  `(?:${SP}*(?:[(（](?:${NUM}|${NUM}時台|${PART}${SP}*${NUM})[)）]` +
    `|${NUM}時(?:${NUM}分)?[~〜～]${NUM}時(?:${NUM}分)?)` +
    `|${SP}+(?:${PART}${SP}*${NUM}|[HＨ][oｏ][uｕ][rｒ][.．]${NUM}|[①-⑳]` +
    `|(?:[月火水木金土日]曜)?${NUM}時台(?:${SP}*[(（]${NUM}[/／]${NUM}[)）])?)` +
    `)${SP}*$`,
);

/** 番組名の末尾の印（trailingMarkRe の形）と前後の空白を落とす（domain.TrimTrailingMark と同じ）。 */
export function trimTrailingMark(title: string): string {
  return title.trim().replace(trailingMarkRe, "").trim();
}

/** 番組名の末尾に印があるか（domain.HasTrailingMark と同じ）。 */
export const hasTrailingMark = (title: string) => trailingMarkRe.test(title.trim());

/** 分割番組のパートかを比べる鍵。末尾の印を落とし、全角・半角、空白、大文字・小文字の違いを無視する。 */
export function titleKey(title: string): string {
  return trimTrailingMark(title).normalize("NFKC").replace(/\s+/g, "").toLowerCase();
}
