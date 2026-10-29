import type { SortState } from "../ui/sort";
import { jobViewStates, viewState, type JobSummary } from "./jobsApi";

export type JobSortKey = "state" | "day" | "name" | "attempts" | "next";

/** 一覧の「次の試行か録音期限」の列に出す日時（録音予定・再試行待ちは次の試行、それ以外は録音期限）。 */
export const nextOrDeadline = (j: JobSummary) => (j.state === "waiting" || j.state === "retrying" ? j.nextAttemptAt : j.deadline);

/** 「これから」のジョブ（録音予定・録らない予定・録音中・再試行待ち）。録らない予定は確定の前なので、これからに置く。 */
const upcoming = (j: JobSummary) => {
  const s = viewState(j);
  return s === "waiting" || s === "skip_planned" || s === "running" || s === "retrying";
};

const stateOrder = (j: JobSummary) => jobViewStates.findIndex((s) => s.value === viewState(j));

/** ジョブの最初の放送回の開始時刻（ミリ秒）。放送回を持たない（型の上では空になりうる）ジョブは null。 */
const firstStart = (j: JobSummary): number | null => (j.stations.length === 0 ? null : Math.min(...j.stations.map((s) => Date.parse(s.start))));

/** 放送日、同じ放送日の中は最初の放送回の開始時刻で比べる。どちらかが放送回を持たなければ放送日だけで比べる。 */
const dayThenStart = (a: JobSummary, b: JobSummary) => {
  const byDay = a.day.localeCompare(b.day);
  if (byDay !== 0) return byDay;
  const sa = firstStart(a);
  const sb = firstStart(b);
  return sa === null || sb === null ? 0 : sa - sb;
};

const keyCompare: Record<JobSortKey, (a: JobSummary, b: JobSummary) => number> = {
  state: (a, b) => stateOrder(a) - stateOrder(b),
  day: dayThenStart,
  name: (a, b) => a.showName.localeCompare(b.showName, "ja"),
  attempts: (a, b) => a.attempts - b.attempts,
  next: (a, b) => Date.parse(nextOrDeadline(a)) - Date.parse(nextOrDeadline(b)),
};

/**
 * 既定の並び: 「これから」（録音予定・録らない予定・録音中・再試行待ち）を最初の放送回の開始時刻の古い順に上へ、「済んだもの」（録音済み・期限切れ・録らなかった）を
 * その下に開始時刻の新しい順に（放送日の順にもなる）。開始時刻が同じなら API の並びのまま。
 */
export function defaultOrder(jobs: JobSummary[]): JobSummary[] {
  return [
    ...jobs.filter(upcoming).sort(dayThenStart),
    ...jobs.filter((j) => !upcoming(j)).sort((a, b) => dayThenStart(b, a)),
  ];
}

/** 見出しで選んだ列で全体を並べる。sort が null なら既定の並び。同じ値は API の並びのまま。 */
export function sortJobs(jobs: JobSummary[], sort: SortState<JobSortKey>): JobSummary[] {
  if (!sort) return defaultOrder(jobs);
  const sign = sort.dir === "asc" ? 1 : -1;
  return [...jobs].sort((a, b) => sign * keyCompare[sort.key](a, b));
}
