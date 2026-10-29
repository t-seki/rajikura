import { broadcastMinutes } from "../guide/format";
import type { JobProgress } from "./jobsApi";

/** 日時を日本時間の「10/12 14:30」の形にする。 */
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  const p = new Intl.DateTimeFormat("ja-JP", {
    timeZone: "Asia/Tokyo",
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).formatToParts(d);
  const g = (t: string) => p.find((x) => x.type === t)?.value ?? "";
  return `${g("month")}/${g("day")} ${g("hour")}:${g("minute")}`;
}

const timeFormat = new Intl.DateTimeFormat("ja-JP", { timeZone: "Asia/Tokyo", hour: "numeric", minute: "2-digit", hourCycle: "h23" });

/**
 * 日時を、放送日 day（YYYY-MM-DD。ジョブの放送日）の放送日の時刻で「25:00」の形にする（深夜 1 時の放送は 25:00）。
 * 放送回の時刻（ジョブの一覧の局と時刻）を、放送日がすぐ近くに出ているところで時刻だけ書くのに使う（ADR 0012）。
 * day の 0 時より前の日時なら、暦の「1:00」の形で出す。
 */
export function formatTime(iso: string, day: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  const m = broadcastMinutes(d, day);
  if (m === null) return timeFormat.format(d);
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}`;
}

/** 長い失敗の理由を切る（全文は詳細で見る）。 */
export function truncate(s: string, max = 60): string {
  const chars = [...s];
  return chars.length > max ? `${chars.slice(0, max).join("")}…` : s;
}

/** 秒を「1時間30分」の形にする。 */
export function formatDuration(sec: number): string {
  const m = Math.round(sec / 60);
  const h = Math.floor(m / 60);
  return h > 0 ? `${h}時間${m % 60}分` : `${m}分`;
}

/** 試行の段階（API の failedStage の値）の画面の言葉。 */
const stageLabels: Record<string, string> = { fetch: "入手", verify: "検証", save: "保存" };

/** 試行の失敗した段階を画面の言葉にする（fetch → 入手）。知らない値はそのまま返す。 */
export function stageLabel(stage: string): string {
  return stageLabels[stage] ?? stage;
}

/** 進捗の入手の間の割合（0〜100 の整数）。入手の間でない、または合計が無ければ null。 */
export function progressPercent(p: JobProgress): number | null {
  if (p.fetchedSeconds === undefined || !p.totalSeconds) return null;
  return Math.min(100, Math.floor((p.fetchedSeconds / p.totalSeconds) * 100));
}

/**
 * 進捗を文字にする。入手の間は「入手中 42分 / 60分（70%）」、検証・保存の段階は「検証中」「保存中」（秒数が無ければ入手も「入手中」だけ）。
 * 録れた分は切り捨て、合計は四捨五入の分にする。
 */
export function progressText(p: JobProgress): string {
  const label = `${stageLabel(p.stage)}中`;
  const percent = progressPercent(p);
  if (p.fetchedSeconds === undefined || p.totalSeconds === undefined || percent === null) return label;
  return `${label} ${Math.floor(p.fetchedSeconds / 60)}分 / ${Math.round(p.totalSeconds / 60)}分（${percent}%）`;
}
