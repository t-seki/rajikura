import { describe, expect, it } from "vitest";
import type { JobSummary } from "./jobsApi";
import { defaultOrder } from "./jobsSort";

const job = (id: number, state: JobSummary["state"], day: string, finishedAt?: string): JobSummary =>
  ({
    id,
    showName: `番組${id}`,
    day,
    part: 0,
    state,
    attempts: 0,
    nextAttemptAt: `${day}T10:00:00+09:00`,
    deadline: `${day}T23:00:00+09:00`,
    settled: state === "skipped",
    finishedAt,
    stations: [{ stationId: "TBS", start: `${day}T10:00:00+09:00`, end: `${day}T11:00:00+09:00` }],
  }) as unknown as JobSummary;

describe("defaultOrder", () => {
  it("済んだものを済んだ時刻の新しい順に並べる（放送日の古いジョブを後から録音済みにすると一番上に来る）", () => {
    const recent = job(1, "done", "2026-10-11", "2026-10-11T12:00:00+09:00");
    const old = job(2, "done", "2026-10-04", "2026-10-12T09:00:00+09:00"); // 1 週間前の放送回を今録った
    const expired = job(3, "expired", "2026-10-10", "2026-10-10T23:00:00+09:00");
    expect(defaultOrder([recent, expired, old]).map((j) => j.id)).toEqual([2, 1, 3]);
  });

  it("済んだ時刻が同じなら開始時刻の新しい順、無いものは最後にする", () => {
    const a = job(1, "done", "2026-10-04", "2026-10-12T09:00:00+09:00");
    const b = job(2, "skipped", "2026-10-06", "2026-10-12T09:00:00+09:00");
    const c = job(3, "done", "2026-10-08");
    const d = job(4, "done", "2026-10-09", "");
    expect(defaultOrder([a, b, c, d]).map((j) => j.id)).toEqual([2, 1, 4, 3]);
  });

  it("これからは開始時刻の古い順のまま上に置く", () => {
    const later = job(1, "waiting", "2026-10-14", "");
    const sooner = job(2, "retrying", "2026-10-13", "");
    const done = job(3, "done", "2026-10-01", "2026-10-12T09:00:00+09:00");
    expect(defaultOrder([done, later, sooner]).map((j) => j.id)).toEqual([2, 1, 3]);
  });
});
