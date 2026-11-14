import { MantineProvider } from "@mantine/core";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
    expect(within(rows[0]).getByText("10/11(日) 11:40 ・ チャプターの取り直し ・ 書き込めなかった（ファイルは前のまま）")).toBeInTheDocument();
    expect(within(rows[0]).getByText("保存先にファイルが無い")).toBeInTheDocument();
    expect(within(rows[1]).getByText("10/11(日) 12:10 ・ チャプターの取り直し ・ チャプターを取れなかったので書かなかった")).toBeInTheDocument();
    expect(within(rows[2]).getByText("10/11(日) 12:40 ・ チャプターの入れ直し ・ 12 件を書き込んだ")).toBeInTheDocument();
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

describe("JobDetailModal の「保存先を移す」", () => {
  const movable: JobDetail = { ...job, showName: "古い名前", outputPath: "/lib/TBS/古い名前/古い名前 2026-10-11.m4a", movable: true };

  function mockMove(requests: { key: string; body: string | null }[], answer?: Response) {
    let requested = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const key = `${init?.method ?? "GET"} ${url}`;
        if (key === "GET /api/jobs/1") return new Response(JSON.stringify({ ...movable, moveRequested: requested }), { status: 200 });
        if (key === "GET /api/shows") {
          return new Response(JSON.stringify([{ name: "今の番組" }, { name: "別の番組" }]), { status: 200 });
        }
        if (key === "POST /api/jobs/1/move") {
          requests.push({ key, body: typeof init?.body === "string" ? init.body : null });
          if (answer) return answer;
          requested = true;
          return new Response(JSON.stringify({ ...movable, moveRequested: true }), { status: 200 });
        }
        throw new Error(`想定外の呼び出し: ${key}`);
      }),
    );
  }

  it("受け付けるジョブでは、押すと POST /api/jobs/{id}/move で今の番組への移しを頼み、読み直した詳細に待っていることを出す", async () => {
    const requests: { key: string; body: string | null }[] = [];
    mockMove(requests);
    const onChanged = vi.fn();
    render(
      <MantineProvider>
        <JobDetailModal id={1} onClose={() => {}} onChanged={onChanged} />
      </MantineProvider>,
    );
    await userEvent.click(await screen.findByRole("button", { name: "保存先を移す" }));
    expect(await screen.findByText("保存先の移しを待っている。")).toBeInTheDocument();
    // 今の番組のままなら、番組を渡さない。
    expect(requests).toEqual([{ key: "POST /api/jobs/1/move", body: null }]);
    expect(onChanged).toHaveBeenCalled();
  });

  it("移し先にほかの番組を選ぶと、その番組を body の show で渡す", async () => {
    const requests: { key: string; body: string | null }[] = [];
    mockMove(requests);
    renderModal();
    const select = await screen.findByRole("combobox", { name: "移し先の番組" });
    expect(select).toHaveValue("古い名前");
    await userEvent.click(select);
    await userEvent.click(await screen.findByRole("option", { name: "今の番組", hidden: true }));
    await userEvent.click(screen.getByRole("button", { name: "保存先を移す" }));
    await screen.findByText("保存先の移しを待っている。");
    expect(requests).toEqual([{ key: "POST /api/jobs/1/move", body: JSON.stringify({ show: "今の番組" }) }]);
  });

  it("受け付けないジョブ（保存先が無い・録音中・古い serve）では出さず、API が断ったら理由を出す", async () => {
    for (const body of [job, { ...movable, state: "running" as const, movable: false }]) {
      mockJob(body);
      const { unmount } = renderModal();
      await screen.findAllByTestId("attempt");
      expect(screen.queryByRole("button", { name: "保存先を移す" })).toBeNull();
      unmount();
    }
    mockMove([], new Response(JSON.stringify({ error: "録音中のジョブは、保存先を移せない（録音が終わってから頼む）" }), { status: 409 }));
    renderModal();
    await userEvent.click(await screen.findByRole("button", { name: "保存先を移す" }));
    expect(await screen.findByText("録音中のジョブは、保存先を移せない（録音が終わってから頼む）")).toBeInTheDocument();
  });

  it("保存先の移しの履歴を、時刻・結果・移す前と移し先・理由で、ほかの履歴とは別の欄に出す。無ければ欄を出さない", async () => {
    mockJob({
      ...job,
      outputMoves: [
        { at: "2026-10-11T02:40:00Z", from: "/lib/a/a 2026-10-11.m4a", to: "/lib/b/b 2026-10-11.m4a", result: "target_exists", error: "" },
        { at: "2026-10-11T03:10:00Z", from: "/lib/a.m4a", to: "", result: "not_show_path", error: "" },
        { at: "2026-10-11T03:40:00Z", from: "/lib/a/a 2026-10-11.m4a", to: "/lib/b/b 2026-10-11.m4a", result: "failed", error: "書けない" },
        { at: "2026-10-11T04:10:00Z", from: "/lib/a/a 2026-10-11.m4a", to: "/lib/b/b 2026-10-11.m4a", result: "moved", error: "" },
        { at: "2026-10-11T04:40:00Z", from: "/lib/b/b 2026-10-11.m4a", to: "/lib/b/b 2026-10-11.m4a", result: "album_rewritten", error: "" },
      ],
    });
    const { unmount } = renderModal();
    const rows = await screen.findAllByTestId("output-move");
    expect(screen.getByText("保存先の移しの履歴")).toBeInTheDocument();
    expect(rows).toHaveLength(5);
    expect(within(rows[0]).getByText("10/11(日) 11:40 ・ 移し先に同じ名前のファイルがあるので移さなかった")).toBeInTheDocument();
    expect(within(rows[0]).getByText("/lib/a/a 2026-10-11.m4a → /lib/b/b 2026-10-11.m4a")).toBeInTheDocument();
    expect(within(rows[1]).getByText("10/11(日) 12:10 ・ 保存先が番組の名前の形でないので移さなかった")).toBeInTheDocument();
    expect(within(rows[1]).getByText("/lib/a.m4a")).toBeInTheDocument();
    expect(within(rows[2]).getByText("書けない")).toBeInTheDocument();
    expect(within(rows[3]).getByText("10/11(日) 13:10 ・ 移した")).toBeInTheDocument();
    expect(within(rows[4]).getByText("10/11(日) 13:40 ・ 保存先は同じなので、アルバムの名前だけ書き換えた")).toBeInTheDocument();
    // 移す前と移し先が同じ行は、パスを 1 回だけ出す。
    expect(within(rows[4]).getByText("/lib/b/b 2026-10-11.m4a")).toBeInTheDocument();
    expect(rows[4].textContent).not.toContain("→");
    expect(within(rows[3]).getByText("/lib/a/a 2026-10-11.m4a → /lib/b/b 2026-10-11.m4a")).toBeInTheDocument();
    expect(screen.getAllByTestId("attempt")).toHaveLength(1);
    unmount();
    for (const body of [{ ...job, outputMoves: [] }, job]) {
      mockJob(body);
      const r = renderModal();
      await screen.findAllByTestId("attempt");
      expect(screen.queryByText("保存先の移しの履歴")).toBeNull();
      r.unmount();
    }
  });
});

describe("JobDetailModal の「チャプターを入れ直す」", () => {
  it("受け付けるジョブでは、押すと POST /api/jobs/{id}/chapters で頼み、読み直した詳細に待っていることを出す", async () => {
    let requested = false;
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const key = `${init?.method ?? "GET"} ${url}`;
        calls.push(key);
        if (key === "GET /api/jobs/1") {
          return new Response(JSON.stringify({ ...job, chaptersRedoable: true, chaptersRedoRequested: requested }), { status: 200 });
        }
        if (key === "POST /api/jobs/1/chapters") {
          requested = true;
          return new Response(JSON.stringify({ ...job, chaptersRedoable: true, chaptersRedoRequested: true }), { status: 200 });
        }
        throw new Error(`想定外の呼び出し: ${key}`);
      }),
    );
    const onChanged = vi.fn();
    render(
      <MantineProvider>
        <JobDetailModal id={1} onClose={() => {}} onChanged={onChanged} />
      </MantineProvider>,
    );
    // チャプターが入ったジョブにも出す（ファイルのチャプターを置き換える）。
    expect(await screen.findByText("チャプター: 入った")).toBeInTheDocument();
    expect(screen.queryByText("チャプターの入れ直しを待っている。")).toBeNull();
    await userEvent.click(await screen.findByRole("button", { name: "チャプターを入れ直す" }));
    expect(await screen.findByText("チャプターの入れ直しを待っている。")).toBeInTheDocument();
    expect(calls).toContain("POST /api/jobs/1/chapters");
    expect(onChanged).toHaveBeenCalled();
  });

  it("受け付けないジョブ（ライブ録音・録音済みでない・古い serve）では出さず、API が断ったら理由を出す", async () => {
    for (const body of [{ ...job, live: true, chaptersRedoable: false }, { ...job, state: "retrying" as const }]) {
      mockJob(body);
      const { unmount } = renderModal();
      await screen.findAllByTestId("attempt");
      expect(screen.queryByRole("button", { name: "チャプターを入れ直す" })).toBeNull();
      unmount();
    }
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const key = `${init?.method ?? "GET"} ${url}`;
        if (key === "GET /api/jobs/1") return new Response(JSON.stringify({ ...job, chaptersRedoable: true }), { status: 200 });
        if (key === "POST /api/jobs/1/chapters") {
          return new Response(JSON.stringify({ error: "ライブ録音のジョブにはチャプターを入れ直せない" }), { status: 409 });
        }
        throw new Error(`想定外の呼び出し: ${key}`);
      }),
    );
    renderModal();
    await userEvent.click(await screen.findByRole("button", { name: "チャプターを入れ直す" }));
    expect(await screen.findByText("ライブ録音のジョブにはチャプターを入れ直せない")).toBeInTheDocument();
  });
});
