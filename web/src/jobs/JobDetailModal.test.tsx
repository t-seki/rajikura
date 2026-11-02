import { MantineProvider } from "@mantine/core";
import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { JobDetailModal } from "./JobDetailModal";
import type { JobDetail } from "./jobsApi";

const job: JobDetail = {
  id: 1,
  showName: "番組A",
  day: "2026-10-11",
  part: 0,
  state: "done",
  attempts: 1,
  nextAttemptAt: "2026-10-12T05:00:00Z",
  deadline: "2026-10-18T05:00:00Z",
  retryable: false,
  settled: false,
  byPerson: false,
  live: false,
  skippable: false,
  recordable: false,
  rerecordable: false,
  chapters: "added",
  outputPath: "",
  missingCount: 0,
  lastFailedStage: "",
  lastError: "",
  stations: [{ stationId: "TBS", start: "2026-10-11T01:00:00Z", end: "2026-10-11T02:00:00Z" }],
  airings: [{ stationId: "TBS", start: "2026-10-11T01:00:00Z", end: "2026-10-11T02:00:00Z", title: "番組A", performer: "", description: "", missing: false }],
  attemptLog: [{ startedAt: "2026-10-11T02:05:00Z", endedAt: "2026-10-11T02:06:00Z", failedStage: "", error: "", durationSeconds: 3600 }],
  durationSeconds: 3600,
};

function mockJob(body: JobDetail) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      if (key !== "GET /api/jobs/1") throw new Error(`想定外の呼び出し: ${key}`);
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
}

const renderModal = () =>
  render(
    <MantineProvider>
      <JobDetailModal id={1} onClose={() => {}} onChanged={() => {}} />
    </MantineProvider>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("JobDetailModal のチャプターの書き込みの履歴", () => {
  it("書き込みのたびの行を、時刻・誰が・結果（件数・理由）で、試行の履歴とは別の欄に出す", async () => {
    mockJob({
      ...job,
      chapterWrites: [
        { at: "2026-10-11T02:40:00Z", by: "refetch", result: "failed", count: 0, error: "保存先にファイルが無い" },
        { at: "2026-10-11T03:10:00Z", by: "refetch", result: "not_fetched", count: 0, error: "" },
        { at: "2026-10-11T03:40:00Z", by: "redo", result: "written", count: 12, error: "" },
      ],
    });
    renderModal();
    const rows = await screen.findAllByTestId("chapter-write");
    expect(screen.getByText("チャプターの書き込みの履歴")).toBeInTheDocument();
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByText("10/11 11:40 ・ チャプターの取り直し ・ 書き込めなかった（ファイルは前のまま）")).toBeInTheDocument();
    expect(within(rows[0]).getByText("保存先にファイルが無い")).toBeInTheDocument();
    expect(within(rows[1]).getByText("10/11 12:10 ・ チャプターの取り直し ・ チャプターを取れなかったので書かなかった")).toBeInTheDocument();
    expect(within(rows[2]).getByText("10/11 12:40 ・ チャプターの入れ直し ・ 12 件を書き込んだ")).toBeInTheDocument();
    // 試行の履歴の行には混ざらない。
    expect(screen.getAllByTestId("attempt")).toHaveLength(1);
  });

  it("履歴が無ければ（空の配列・古い serve で欄が無い）欄を出さない", async () => {
    for (const body of [{ ...job, chapterWrites: [] }, job]) {
      mockJob(body);
      const { unmount } = renderModal();
      await screen.findAllByTestId("attempt");
      expect(screen.queryByText("チャプターの書き込みの履歴")).toBeNull();
      expect(screen.queryByTestId("chapter-write")).toBeNull();
      unmount();
    }
  });
});
