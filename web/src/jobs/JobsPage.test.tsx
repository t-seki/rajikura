import { MantineProvider } from "@mantine/core";
import { Notifications, notifications } from "@mantine/notifications";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { JobDetail, JobSummary } from "./jobsApi";
import { JobsPage } from "./JobsPage";
import { jobsPollInterval } from "./JobDetailModal";

const base: JobSummary = {
  id: 1,
  showName: "番組A",
  day: "2026-10-11",
  part: 0,
  state: "retrying",
  attempts: 2,
  nextAttemptAt: "2026-10-12T05:00:00Z",
  deadline: "2026-10-18T05:00:00Z",
  retryable: true,
  settled: false,
  byPerson: false,
  live: false,
  skippable: false,
  recordable: false,
  rerecordable: false,
  outputPath: "",
  missingCount: 0,
  lastFailedStage: "",
  lastError: "",
  stations: [{ stationId: "TBS", start: "2026-10-11T16:00:00Z", end: "2026-10-11T18:00:00Z" }],
};

const detail = (over: Partial<JobDetail> = {}): JobDetail => ({
  ...base,
  outputPath: "/library/番組A/2026-10-11.m4a",
  durationSeconds: 5400,
  airings: [
    { stationId: "TBS", start: "2026-10-11T01:00:00Z", end: "2026-10-11T02:00:00Z", title: "番組A #1", performer: "", description: "", missing: false },
    { stationId: "TBS", start: "2026-10-11T02:00:00Z", end: "2026-10-11T03:00:00Z", title: "番組A #2", performer: "", description: "", missing: true },
  ],
  attemptLog: [
    { startedAt: "2026-10-12T01:00:00Z", endedAt: "2026-10-12T01:01:00Z", failedStage: "fetch", error: "boom", durationSeconds: 0 },
  ],
  ...over,
});

function mockApi(routes: Record<string, () => { status: number; body?: unknown }>) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push(key);
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = h();
      return new Response(JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

const renderPage = () =>
  render(
    <MantineProvider>
      <Notifications />
      <JobsPage />
    </MantineProvider>,
  );

function setWide(wide: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: wide && query.includes("min-width"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
}

const stations = () => ({ status: 200, body: [{ id: "TBS", name: "TBSラジオ" }] });
const names = (items: HTMLElement[]) => items.map((i) => within(i).getAllByText(/^番組./)[0].textContent);

afterEach(() => {
  vi.unstubAllGlobals();
  notifications.clean();
  setWide(false);
});

describe("JobsPage", () => {
  it("一覧に番組・放送日・状態・試行回数を出し、パートも出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base, { ...base, id: 2, showName: "番組B", part: 2, state: "done", retryable: false }] } }),
    });
    renderPage();
    const items = await screen.findAllByTestId("job");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("番組A")).toBeInTheDocument();
    expect(within(items[0]).getByText(/放送日 2026-10-11/)).toBeInTheDocument();
    expect(within(items[0]).getByText("録音予定（失敗・再試行待ち）")).toBeInTheDocument();
    expect(within(items[0]).getByText(/試行 2 回/)).toBeInTheDocument();
    expect(within(items[1]).getByText(/パート 2/)).toBeInTheDocument();
  });

  it("放送日が 120 日前以降のジョブだけを表示していることを書く", async () => {
    mockApi({ "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }) });
    renderPage();
    await screen.findAllByTestId("job");
    expect(screen.getByText("放送日が 120 日前以降のジョブだけを表示している。")).toBeInTheDocument();
  });

  it("状態の文字は録音の言葉で、再試行待ちなどを見分けられる", async () => {
    mockApi({ "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }) });
    renderPage();
    await screen.findAllByTestId("job");
    await userEvent.click(screen.getByPlaceholderText("すべて"));
    for (const label of [
      "録音予定",
      "録音中",
      "録音予定（失敗・再試行待ち）",
      "録音済み",
      "録音できなかった（期限切れ）",
    ]) {
      expect(await screen.findByRole("option", { name: label, hidden: true })).toBeInTheDocument();
    }
  });

  it("状態で絞り込むと、state を付けて読み直す", async () => {
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs?state=expired": () => ({ status: 200, body: { jobs: [] } }),
    });
    renderPage();
    await screen.findAllByTestId("job");
    await userEvent.click(screen.getByPlaceholderText("すべて"));
    await userEvent.click(await screen.findByText("録音できなかった（期限切れ）"));
    expect(await screen.findByText("該当するジョブは無い。")).toBeInTheDocument();
    expect(calls).toContain("GET /api/jobs?state=expired");
  });

  it("詳細に放送回（欠落）・保存先・長さ・試行の履歴を出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByText(/\/library\/番組A\/2026-10-11\.m4a/)).toBeInTheDocument();
    expect(screen.getByText("長さ: 1時間30分")).toBeInTheDocument();
    expect(screen.getByText("番組A #1")).toBeInTheDocument();
    expect(screen.getByText("番組A #2（欠落）")).toBeInTheDocument();
    expect(screen.getByText(/失敗（入手）/)).toBeInTheDocument();
    expect(screen.queryByText(/fetch/)).toBeNull();
    expect(screen.getByText("boom")).toBeInTheDocument();
  });

  it("詳細の放送回は放送日の時刻（時刻だけ）で出し、録音期限は暦の日時のまま出す", async () => {
    // 放送日 2026-10-20 の 10/21 03:40〜04:00（日本時間）の放送回。録音期限は終了の 5 分後。
    const late = { ...base, day: "2026-10-20", deadline: "2026-10-20T19:05:00Z" };
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [late] } }),
      "GET /api/jobs/1": () => ({
        status: 200,
        body: detail({
          ...late,
          airings: [
            { stationId: "TBS", start: "2026-10-20T18:40:00Z", end: "2026-10-20T19:00:00Z", title: "番組A #1", performer: "", description: "", missing: false },
          ],
        }),
      }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    const airing = await screen.findByTestId("airing");
    expect(within(airing).getByText("TBS ・ 27:40〜28:00")).toBeInTheDocument();
    expect(within(airing).queryByText(/03:40/)).toBeNull();
    expect(within(screen.getByRole("dialog")).getByText("録音期限 10/21 04:05")).toBeInTheDocument();
  });

  it("詳細にチャプターの値（入った・待っている・入らなかった）を出し、値が無ければ出さない", async () => {
    for (const [chapters, label] of [
      ["added", "入った"],
      ["waiting", "待っている"],
      ["not_added", "入らなかった"],
    ] as const) {
      mockApi({
        "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
        "GET /api/jobs/1": () => ({ status: 200, body: detail({ chapters }) }),
      });
      const { unmount } = renderPage();
      expect(screen.queryByText(/チャプター:/)).toBeNull();
      await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
      expect(await screen.findByText(`チャプター: ${label}`)).toBeInTheDocument();
      unmount();
    }
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText("長さ: 1時間30分");
    expect(screen.queryByText(/チャプター:/)).toBeNull();
  });

  it("長さの記録が無ければ「記録なし」と出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ durationSeconds: 0 }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByText("長さ: 記録なし")).toBeInTheDocument();
  });

  it("ライブ録音の保存を自動でやり直している間は、その打ち切りを 1 行出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ saveRetryUntil: "2026-10-12T04:00:00Z" }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByText("録れた分を保存できなかったので、保存を自動でやり直している（10/12 13:00 まで）。")).toBeInTheDocument();
    expect(screen.queryByText(/まで残している/)).toBeNull();
  });

  it("保存できなかったライブ録音の録れた分を残している間は、残す期限と手動再試行で保存からやり直せることを 1 行出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "expired" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "expired", keepUntil: "2026-11-11T04:00:00Z" }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(
      await screen.findByText("録れた分を 11/11 13:00 まで残している。保存先などの原因を直してから「手動で再試行」を押すと、保存からやり直す。"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "手動で再試行" })).toBeInTheDocument();
    expect(screen.queryByText(/自動でやり直している/)).toBeNull();
  });

  it("保存のやり直しも録れた分を残すことも無ければ、その行を出さない", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText(/保存先/);
    expect(screen.queryByText(/自動でやり直している/)).toBeNull();
    expect(screen.queryByText(/まで残している/)).toBeNull();
  });

  it("対象外の状態ではボタンを出さない", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "done" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "done", retryable: false }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText(/保存先/);
    expect(screen.queryByRole("button", { name: "手動で再試行" })).not.toBeInTheDocument();
  });

  it("手動で再試行すると POST して読み直す", async () => {
    let state: JobSummary["state"] = "expired";
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state, retryable: state === "expired" }) }),
      "POST /api/jobs/1/retry": () => {
        state = "retrying";
        return { status: 200, body: { ...base, state } };
      },
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "手動で再試行" }));
    await vi.waitFor(() => expect(screen.queryByRole("button", { name: "手動で再試行" })).not.toBeInTheDocument());
    expect(calls).toContain("POST /api/jobs/1/retry");
  });

  it("録り直せない録音済みのジョブには「録り直す」を出さない", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "done" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "done", retryable: false, rerecordable: false }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText(/保存先/);
    expect(screen.queryByRole("button", { name: "録り直す" })).not.toBeInTheDocument();
  });

  it("保存先があるジョブの詳細に、録音の URL を指す audio を出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "done" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "done", retryable: false }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    const audio = await screen.findByTestId("job-audio");
    expect(audio).toHaveAttribute("src", "/api/jobs/1/audio");
    expect(audio).toHaveAttribute("preload", "none");
    expect(audio).toHaveAttribute("controls");
  });

  it("保存先が空のジョブの詳細には audio を出さない", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ outputPath: "" }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText(/保存先/);
    expect(screen.queryByTestId("job-audio")).not.toBeInTheDocument();
  });

  it("audio が error になったら、同じ URL を Range 付きで読み直して API の理由を出す", async () => {
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "done" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "done", retryable: false }) }),
      "GET /api/jobs/1/audio": () => ({ status: 403, body: { error: "今の保存先（library）の外にあるので出さない" } }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    const audio = await screen.findByTestId("job-audio");
    expect(calls).not.toContain("GET /api/jobs/1/audio");
    fireEvent.error(audio);
    expect(await screen.findByText("今の保存先（library）の外にあるので出さない")).toBeInTheDocument();
    const fetchMock = vi.mocked(fetch);
    const init = fetchMock.mock.calls.find(([u]) => u === "/api/jobs/1/audio")?.[1];
    expect((init?.headers as Record<string, string>).Range).toBe("bytes=0-0");
  });

  it("理由が取れなければ「再生できない」だけを出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "done" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "done", retryable: false }) }),
      "GET /api/jobs/1/audio": () => ({ status: 206 }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    fireEvent.error(await screen.findByTestId("job-audio"));
    expect(await screen.findByText("再生できない")).toBeInTheDocument();
  });

  it("「録り直す」は確かめの画面を経てから POST し、取りやめたら呼ばない", async () => {
    let state: JobSummary["state"] = "done";
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state, retryable: false, rerecordable: state === "done" }) }),
      "POST /api/jobs/1/rerecord": () => {
        state = "retrying";
        return { status: 200, body: { ...base, state } };
      },
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "録り直す" }));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0][0]).toContain("上書き");
    expect(calls).not.toContain("POST /api/jobs/1/rerecord");
    await userEvent.click(screen.getByRole("button", { name: "録り直す" }));
    await vi.waitFor(() => expect(screen.queryByRole("button", { name: "録り直す" })).not.toBeInTheDocument());
    expect(calls).toContain("POST /api/jobs/1/rerecord");
    expect(confirm).toHaveBeenCalledTimes(2);
    confirm.mockRestore();
  });

  it("「録り直す」を API が断ったら理由を出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state: "done" }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state: "done", retryable: false, rerecordable: true }) }),
      "POST /api/jobs/1/rerecord": () => ({ status: 409, body: { error: "録音期限を過ぎているので録り直せない" } }),
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "録り直す" }));
    expect(await screen.findByText("録音期限を過ぎているので録り直せない")).toBeInTheDocument();
    confirm.mockRestore();
  });

  it("API が拒んだら理由を出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
      "POST /api/jobs/1/retry": () => ({ status: 409, body: { error: "録音期限を過ぎているので再試行できない" } }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "手動で再試行" }));
    expect(await screen.findByText("録音期限を過ぎているので再試行できない")).toBeInTheDocument();
  });

  it("スマホのカードに局と時刻・失敗の理由を出し、これからを放送日の古い順、済んだものを新しい順に並べる", async () => {
    const j = (id: number, name: string, day: string, state: JobSummary["state"], over: Partial<JobSummary> = {}): JobSummary => ({
      ...base, id, showName: name, day, state, ...over,
    });
    mockApi({
      "GET /api/stations": stations,
      "GET /api/jobs": () => ({
        status: 200,
        body: {
          jobs: [
            j(1, "番組先", "2026-10-20", "waiting"),
            j(2, "番組済新", "2026-10-12", "done"),
            j(3, "番組済旧", "2026-10-09", "expired"),
            j(4, "番組近", "2026-10-13", "retrying", {
              lastFailedStage: "fetch",
              lastError: "boom",
              stations: [{ stationId: "TBS", start: "2026-10-13T16:00:00Z", end: "2026-10-13T18:00:00Z" }],
            }),
          ],
        },
      }),
    });
    renderPage();
    const items = await screen.findAllByTestId("job");
    expect(names(items)).toEqual(["番組近", "番組先", "番組済新", "番組済旧"]);
    expect(within(items[0]).getByText("入手で失敗: boom")).toBeInTheDocument();
    expect(await within(items[0]).findByText("TBSラジオ 25:00〜27:00")).toBeInTheDocument();
  });

  it("PC では表に出し、見出しで 昇順 → 降順 → 既定 と並べ替える", async () => {
    setWide(true);
    const j = (id: number, name: string, day: string, state: JobSummary["state"]): JobSummary => ({
      ...base, id, showName: name, day, state,
    });
    mockApi({
      "GET /api/stations": stations,
      "GET /api/jobs": () => ({
        status: 200,
        body: { jobs: [j(1, "番組先", "2026-10-20", "waiting"), j(2, "番組済", "2026-10-12", "done"), j(3, "番組近", "2026-10-13", "retrying")] },
      }),
    });
    renderPage();
    expect(names(await screen.findAllByTestId("job"))).toEqual(["番組近", "番組先", "番組済"]);
    expect(screen.getByRole("columnheader", { name: "局と時刻" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "放送日" }));
    expect(names(screen.getAllByTestId("job"))).toEqual(["番組済", "番組近", "番組先"]);
    await userEvent.click(screen.getByRole("button", { name: /放送日/ }));
    expect(names(screen.getAllByTestId("job"))).toEqual(["番組先", "番組近", "番組済"]);
    await userEvent.click(screen.getByRole("button", { name: /放送日/ }));
    expect(names(screen.getAllByTestId("job"))).toEqual(["番組近", "番組先", "番組済"]);
  });

  it("同じ放送日の中を、最初の放送回の開始時刻の順に並べる（既定は これから が古い順・済んだものが新しい順、開始が同じなら API の並び）", async () => {
    setWide(true);
    const at = (start: string) => [{ stationId: "TBS", start, end: start }];
    const j = (id: number, name: string, day: string, state: JobSummary["state"], start: string): JobSummary => ({
      ...base, id, showName: name, day, state, stations: at(start),
    });
    mockApi({
      "GET /api/stations": stations,
      "GET /api/jobs": () => ({
        status: 200,
        body: {
          jobs: [
            j(1, "番組ナ", "2026-10-15", "waiting", "2026-10-15T16:00:00Z"),
            j(2, "番組ハ", "2026-10-15", "waiting", "2026-10-15T15:00:00Z"),
            j(3, "番組サ", "2026-10-15", "waiting", "2026-10-15T12:30:00Z"),
            j(4, "番組日", "2026-10-15", "retrying", "2026-10-15T15:00:00Z"),
            j(5, "番組翌", "2026-10-16", "waiting", "2026-10-16T02:30:00Z"),
            j(6, "番組済朝", "2026-10-12", "done", "2026-10-12T15:00:00Z"),
            j(7, "番組済夜", "2026-10-12", "done", "2026-10-12T12:30:00Z"),
            j(8, "番組済前", "2026-10-11", "expired", "2026-10-11T15:00:00Z"),
          ],
        },
      }),
    });
    renderPage();
    expect(names(await screen.findAllByTestId("job"))).toEqual(["番組サ", "番組ハ", "番組日", "番組ナ", "番組翌", "番組済朝", "番組済夜", "番組済前"]);
    await userEvent.click(screen.getByRole("button", { name: "放送日" }));
    expect(names(screen.getAllByTestId("job"))).toEqual(["番組済前", "番組済夜", "番組済朝", "番組サ", "番組ハ", "番組日", "番組ナ", "番組翌"]);
    await userEvent.click(screen.getByRole("button", { name: /放送日/ }));
    expect(names(screen.getAllByTestId("job"))).toEqual(["番組翌", "番組ナ", "番組ハ", "番組日", "番組サ", "番組済朝", "番組済夜", "番組済前"]);
  });

  it("PC の表で、複数の局は 1 つのセルに縦に並べ、失敗の理由は切り、行を押すと詳細を開く", async () => {
    setWide(true);
    const long = "x".repeat(100);
    mockApi({
      "GET /api/stations": stations,
      "GET /api/jobs": () => ({
        status: 200,
        body: {
          jobs: [
            {
              ...base,
              lastFailedStage: "verify",
              lastError: long,
              stations: [
                { stationId: "TBS", start: "2026-10-11T16:00:00Z", end: "2026-10-11T18:00:00Z" },
                { stationId: "QRR", start: "2026-10-11T18:00:00Z", end: "2026-10-11T20:00:00Z" },
              ],
            },
          ],
        },
      }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
    });
    renderPage();
    const row = await screen.findByTestId("job");
    expect(await within(row).findByText("TBSラジオ 25:00〜27:00")).toBeInTheDocument();
    expect(within(row).getByText("QRR 27:00〜29:00")).toBeInTheDocument();
    expect(within(row).getByText(`検証で失敗: ${"x".repeat(60)}…`)).toBeInTheDocument();
    await userEvent.click(row);
    expect(await screen.findByText(/保存先/)).toBeInTheDocument();
  });

  it("局名が引けない（API が失敗）ときは局 ID を出す", async () => {
    mockApi({
      "GET /api/stations": () => ({ status: 503, body: { error: "番組表が無い" } }),
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
    });
    renderPage();
    const item = await screen.findByTestId("job");
    expect(within(item).getByText("TBS 25:00〜27:00")).toBeInTheDocument();
  });

  it("局と時刻は、ジョブの放送日を基準にした放送日の時刻で出し、次の試行は暦の日時のまま出す", async () => {
    const job = (id: number, name: string, stationId: string, start: string, end: string): JobSummary => ({
      ...base,
      id,
      showName: name,
      day: "2026-10-15",
      state: "waiting",
      nextAttemptAt: "2026-10-15T16:04:00Z",
      stations: [{ stationId, start, end }],
    });
    mockApi({
      "GET /api/stations": () => ({ status: 200, body: [{ id: "LFR", name: "ニッポン放送" }, { id: "QRR", name: "文化放送" }] }),
      "GET /api/jobs": () => ({
        status: 200,
        body: {
          jobs: [
            job(1, "番組文", "QRR", "2026-10-15T12:30:00Z", "2026-10-15T13:00:00Z"),
            job(2, "番組ニ", "LFR", "2026-10-15T16:00:00Z", "2026-10-15T18:00:00Z"),
          ],
        },
      }),
    });
    renderPage();
    const items = await screen.findAllByTestId("job");
    expect(await within(items[0]).findByText("文化放送 21:30〜22:00")).toBeInTheDocument();
    expect(within(items[1]).getByText("ニッポン放送 25:00〜27:00")).toBeInTheDocument();
    expect(within(items[1]).getByText(/次の試行 10\/16 01:04/)).toBeInTheDocument();
  });

  it("手動再試行が成功したらトーストで知らせる", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
      "POST /api/jobs/1/retry": () => ({ status: 200, body: base }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "手動で再試行" }));
    expect(await screen.findByText("再試行を依頼した")).toBeInTheDocument();
  });
});

describe("JobsPage のライブ録音", () => {
  const liveSentence = "ライブ録音のジョブ（タイムフリーの対象外の放送回を含むので、放送中に録る。放送の時刻に rajikura が動いている必要がある）。";
  const jobs = () => ({
    status: 200,
    body: {
      jobs: [
        { ...base, id: 1, showName: "番組ライブ", state: "waiting", live: true },
        { ...base, id: 2, showName: "番組タイムフリー", state: "waiting", live: false },
        { ...base, id: 3, showName: "番組ライブ済", day: "2026-10-10", state: "done", retryable: false, live: true },
      ],
    },
  });
  const itemOf = (items: HTMLElement[], name: string) => items.find((i) => within(i).queryByText(name) !== null)!;

  it("スマホのカードで、live のジョブには「◎ライブ録音」を出し、live でないジョブには出さない", async () => {
    mockApi({ "GET /api/stations": stations, "GET /api/jobs": jobs });
    renderPage();
    const items = await screen.findAllByTestId("job");
    expect(within(itemOf(items, "番組ライブ")).getByText("◎ライブ録音")).toBeInTheDocument();
    expect(within(itemOf(items, "番組ライブ済")).getByText("◎ライブ録音")).toBeInTheDocument();
    expect(within(itemOf(items, "番組タイムフリー")).queryByText("◎ライブ録音")).toBeNull();
  });

  it("PC の表で、live のジョブの状態のセルに「◎ライブ録音」を出し、live でないジョブには出さない", async () => {
    setWide(true);
    mockApi({ "GET /api/stations": stations, "GET /api/jobs": jobs });
    renderPage();
    const items = await screen.findAllByTestId("job");
    expect(items[0].tagName).toBe("TR");
    const live = itemOf(items, "番組ライブ");
    const stateCell = within(live).getByText("◎ライブ録音").closest("td");
    expect(stateCell).not.toBeNull();
    expect(within(stateCell!).getByText("録音予定")).toBeInTheDocument();
    expect(within(itemOf(items, "番組ライブ済")).getByText("◎ライブ録音")).toBeInTheDocument();
    expect(within(itemOf(items, "番組タイムフリー")).queryByText("◎ライブ録音")).toBeNull();
  });

  it("live のジョブの詳細に、放送中に録ることと rajikura が動いている必要があることを 1 文出す", async () => {
    mockApi({
      "GET /api/stations": stations,
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, live: true }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ live: true }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByText(liveSentence)).toBeInTheDocument();
  });

  it("live でないジョブの詳細には、その 1 文を出さない", async () => {
    mockApi({
      "GET /api/stations": stations,
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail() }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText(/保存先/);
    expect(screen.queryByText(liveSentence)).toBeNull();
  });
});

describe("JobsPage の録らない予定・録らなかった（ADR 0019）", () => {
  const waiting: JobSummary = { ...base, state: "waiting", attempts: 0, retryable: false, skippable: true };
  const planned: JobSummary = { ...base, state: "skipped", attempts: 0, retryable: false, recordable: true };

  it("確定の前の skipped は録らない予定、確定した skipped は録らなかったと出し、録らない予定はこれからに置く", async () => {
    mockApi({
      "GET /api/jobs": () => ({
        status: 200,
        body: {
          jobs: [
            { ...base, id: 1, showName: "番組A", day: "2026-10-12", state: "skipped", settled: true, retryable: true },
            { ...planned, id: 2, showName: "番組B", day: "2026-10-13" },
            { ...base, id: 3, showName: "番組C", day: "2026-10-10", state: "done", retryable: false },
          ],
        },
      }),
    });
    renderPage();
    const items = await screen.findAllByTestId("job");
    expect(names(items)).toEqual(["番組B", "番組A", "番組C"]);
    expect(within(items[0]).getByText("録らない予定")).toBeInTheDocument();
    expect(within(items[1]).getByText("録らなかった")).toBeInTheDocument();
  });

  it("録音予定のジョブを「録らない」にすると POST して知らせる", async () => {
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [waiting] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ ...waiting }) }),
      "POST /api/jobs/1/skip": () => ({ status: 200, body: planned }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "録らない" }));
    expect(screen.queryByRole("button", { name: "録る" })).not.toBeInTheDocument();
    expect(await screen.findByText("録らないにした")).toBeInTheDocument();
    expect(calls).toContain("POST /api/jobs/1/skip");
  });

  it("録らない予定のジョブに、誰がなぜ止めたかと「録る」を出し、押すと録音予定に戻す", async () => {
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [planned] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ ...planned }) }),
      "POST /api/jobs/1/record": () => ({ status: 200, body: waiting }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(
      await screen.findByText("番組表の番組名「番組A #1」「番組A #2」が番組「番組A」の名前で始まらないので、録らない予定にした。"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "手動で再試行" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "録らない" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "録る" }));
    expect(await screen.findByText("録音予定に戻した")).toBeInTheDocument();
    expect(calls).toContain("POST /api/jobs/1/record");
  });

  it("人が止めたジョブは、人が決めたことを出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [planned] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ ...planned, byPerson: true }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByText("人が録らないと決めたので、録らない予定にした。")).toBeInTheDocument();
  });

  it("「録らない」を API が拒んだら理由を出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [waiting] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ ...waiting }) }),
      "POST /api/jobs/1/skip": () => ({ status: 409, body: { error: "このジョブの状態では受け付けられない" } }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "録らない" }));
    expect(await screen.findByText("このジョブの状態では受け付けられない")).toBeInTheDocument();
  });
});

// 読み直しは setInterval で動く。fake timers で setInterval を偽物にすると Mantine の Modal が開かなくなるので、
// 読み直しの間隔（jobsPollInterval）の setInterval だけを横取りして、tick() で「間隔が 1 回過ぎた」ことにする。
describe("JobsPage の読み直し", () => {
  let visibility = "visible";
  let ticks: Map<number, () => void>;
  let intervals: number[];
  const setVisibility = (v: "visible" | "hidden") => {
    visibility = v;
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
  };
  const tick = async () => {
    await act(async () => {
      for (const fn of [...ticks.values()]) fn();
    });
  };
  const count = (calls: string[], key: string) => calls.filter((c) => c === key).length;

  beforeEach(() => {
    visibility = "visible";
    ticks = new Map();
    intervals = [];
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
    const realSet = window.setInterval.bind(window);
    const realClear = window.clearInterval.bind(window);
    let next = 1_000_000;
    vi.spyOn(window, "setInterval").mockImplementation(((fn: () => void, ms?: number, ...args: unknown[]) => {
      if (ms !== jobsPollInterval) return realSet(fn, ms, ...args);
      intervals.push(ms);
      ticks.set(++next, fn);
      return next;
    }) as typeof window.setInterval);
    vi.spyOn(window, "clearInterval").mockImplementation(((id?: number) => {
      if (id !== undefined && ticks.delete(id)) return;
      realClear(id);
    }) as typeof window.clearInterval);
  });
  afterEach(() => {
    vi.restoreAllMocks();
    delete (document as { visibilityState?: unknown }).visibilityState;
  });

  it("画面が見えている間だけ決まった間隔（10 秒）で読み直し、裏にある間は読み直さず、表に戻ったらすぐ読み直す", async () => {
    const calls = mockApi({ "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }) });
    renderPage();
    await screen.findAllByTestId("job");
    expect(jobsPollInterval).toBe(10_000);
    expect(intervals).toEqual([10_000]);
    expect(count(calls, "GET /api/jobs")).toBe(1);

    await tick();
    expect(count(calls, "GET /api/jobs")).toBe(2);

    setVisibility("hidden");
    expect(ticks.size).toBe(0);
    await tick();
    expect(count(calls, "GET /api/jobs")).toBe(2);

    setVisibility("visible");
    await vi.waitFor(() => expect(count(calls, "GET /api/jobs")).toBe(3));
    expect(ticks.size).toBe(1);
    await tick();
    expect(count(calls, "GET /api/jobs")).toBe(4);
  });

  it("一覧の読み直しで行が入れ替わり、ローディングは出さない", async () => {
    let state: JobSummary["state"] = "waiting";
    mockApi({ "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state }] } }) });
    renderPage();
    const row = await screen.findByTestId("job");
    expect(within(row).getByText("録音予定")).toBeInTheDocument();
    state = "done";
    await tick();
    expect(within(await screen.findByTestId("job")).getByText("録音済み")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  it("一覧の読み直しが失敗したら Alert を出して前の行を残し、次の成功で Alert を消す", async () => {
    let fail = false;
    mockApi({
      "GET /api/jobs": () => (fail ? { status: 500, body: { error: "読めない" } } : { status: 200, body: { jobs: [base] } }),
    });
    renderPage();
    await screen.findAllByTestId("job");
    fail = true;
    await tick();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getAllByTestId("job")).toHaveLength(1);
    fail = false;
    await tick();
    await vi.waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(screen.getAllByTestId("job")).toHaveLength(1);
  });

  it("開いている詳細も読み直して新しい状態にし、裏にある間は読み直さない", async () => {
    let state: JobSummary["state"] = "waiting";
    const calls = mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...base, state }] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail({ state, retryable: false }) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByText(/保存先/);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("録音予定")).toBeInTheDocument();

    setVisibility("hidden");
    state = "done";
    await tick();
    expect(count(calls, "GET /api/jobs/1")).toBe(1);
    expect(within(dialog).getByText("録音予定")).toBeInTheDocument();

    setVisibility("visible");
    expect(await within(dialog).findByText("録音済み")).toBeInTheDocument();
  });

  it("詳細の読み直しが失敗しても、表示もエラーも変えない", async () => {
    let fail = false;
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => (fail ? { status: 500, body: { error: "読めない" } } : { status: 200, body: detail() }),
      "POST /api/jobs/1/retry": () => ({ status: 409, body: { error: "録音期限を過ぎているので再試行できない" } }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "手動で再試行" }));
    await screen.findByText("録音期限を過ぎているので再試行できない");
    fail = true;
    await tick();
    expect(screen.getByText("録音期限を過ぎているので再試行できない")).toBeInTheDocument();
    expect(screen.queryByText("読めない")).toBeNull();
    expect(screen.getByText(/保存先/)).toBeInTheDocument();
    expect(screen.getAllByRole("alert")).toHaveLength(1);
  });

  it("操作の最中は詳細を読み直さない", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const key = `${init?.method ?? "GET"} ${url}`;
        calls.push(key);
        if (key === "POST /api/jobs/1/retry") await gate;
        const body = key === "GET /api/jobs" ? { jobs: [base] } : key === "GET /api/jobs/1" ? detail() : {};
        return new Response(JSON.stringify(body), { status: 200 });
      }),
    );
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await userEvent.click(await screen.findByRole("button", { name: "手動で再試行" }));
    await vi.waitFor(() => expect(calls).toContain("POST /api/jobs/1/retry"));
    const before = count(calls, "GET /api/jobs/1");
    await tick();
    expect(count(calls, "GET /api/jobs/1")).toBe(before);
    release();
    await vi.waitFor(() => expect(count(calls, "GET /api/jobs/1")).toBeGreaterThan(before));
  });
  it("読み直しが飛んでいる間に絞り込みを変えたら、古い絞り込みの応答は捨てる", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    let plain = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url === "/api/jobs") {
          plain++;
          if (plain === 2) await gate;
          return new Response(JSON.stringify({ jobs: [base] }), { status: 200 });
        }
        if (url === "/api/jobs?state=expired") return new Response(JSON.stringify({ jobs: [] }), { status: 200 });
        if (url === "/api/stations") return new Response("[]", { status: 200 });
        throw new Error(`想定外の呼び出し: ${url}`);
      }),
    );
    renderPage();
    await screen.findAllByTestId("job");
    await tick(); // 絞り込み無しの読み直しが飛んだまま止まる
    await userEvent.click(screen.getByPlaceholderText("すべて"));
    await userEvent.click(await screen.findByText("録音できなかった（期限切れ）"));
    expect(await screen.findByText("該当するジョブは無い。")).toBeInTheDocument();
    release();
    await act(async () => {
      await gate;
    });
    expect(screen.getByText("該当するジョブは無い。")).toBeInTheDocument();
    expect(screen.queryByTestId("job")).toBeNull();
  });

  it("最初の詳細の読み込みが失敗してエラーが出ていても、読み直しが成功したらエラーを消して詳細を出す", async () => {
    let fail = true;
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [base] } }),
      "GET /api/jobs/1": () => (fail ? { status: 500, body: { error: "読めない" } } : { status: 200, body: detail() }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByText("読めない")).toBeInTheDocument();
    fail = false;
    await tick();
    expect(await screen.findByText(/保存先/)).toBeInTheDocument();
    expect(screen.queryByText("読めない")).toBeNull();
  });
});

describe("JobsPage の進捗", () => {
  const running: JobSummary = {
    ...base,
    state: "running",
    retryable: false,
    progress: { stage: "fetch", fetchedSeconds: 2520, totalSeconds: 3600 },
  };

  for (const wide of [true, false]) {
    it(`一覧の録音中の行に、状態の横に進捗を文字だけで出し、プログレスバーは置かない（${wide ? "PC の表" : "スマホのカード"}）`, async () => {
      setWide(wide);
      mockApi({ "GET /api/stations": stations, "GET /api/jobs": () => ({ status: 200, body: { jobs: [running] } }) });
      renderPage();
      const row = await screen.findByTestId("job");
      expect(within(row).getByText("録音中")).toBeInTheDocument();
      expect(within(row).getByTestId("job-progress")).toHaveTextContent("入手中 42分 / 60分（70%）");
      expect(screen.queryByRole("progressbar")).toBeNull();
    });
  }

  it("一覧の検証・保存の段階は段階だけを出し、進捗の無い録音中の行は今どおり「録音中」だけを出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({
        status: 200,
        body: {
          jobs: [
            { ...running, id: 1, showName: "番組A", progress: { stage: "verify" } },
            { ...running, id: 2, showName: "番組B", progress: { stage: "save" } },
            { ...running, id: 3, showName: "番組C", progress: undefined },
          ],
        },
      }),
    });
    renderPage();
    const items = await screen.findAllByTestId("job");
    const byName = (n: string) => items.find((i) => within(i).queryByText(n) !== null)!;
    expect(within(byName("番組A")).getByTestId("job-progress").textContent).toBe("検証中");
    expect(within(byName("番組B")).getByTestId("job-progress").textContent).toBe("保存中");
    expect(within(byName("番組C")).queryByTestId("job-progress")).toBeNull();
    expect(within(byName("番組C")).getByText("録音中")).toBeInTheDocument();
  });

  it("一覧の録音中でない行には、進捗が付いていても出さない", async () => {
    mockApi({ "GET /api/jobs": () => ({ status: 200, body: { jobs: [{ ...running, state: "done" }] } }) });
    renderPage();
    const row = await screen.findByTestId("job");
    expect(within(row).queryByTestId("job-progress")).toBeNull();
  });

  it("詳細には、入手の間は進捗の文字とプログレスバーを出す", async () => {
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [running] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail(running) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("入手中 42分 / 60分（70%）")).toBeInTheDocument();
    expect(within(dialog).getByRole("progressbar", { name: "入手の進捗" })).toHaveAttribute("aria-valuenow", "70");
  });

  it("詳細の検証の段階は文字だけを出し、プログレスバーは出さない", async () => {
    const verifying = { ...running, progress: { stage: "verify" as const } };
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [verifying] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail(verifying) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("検証中")).toBeInTheDocument();
    expect(within(dialog).queryByRole("progressbar")).toBeNull();
  });

  it("詳細の進捗の無い録音中のジョブには、進捗を出さない", async () => {
    const plain = { ...running, progress: undefined };
    mockApi({
      "GET /api/jobs": () => ({ status: 200, body: { jobs: [plain] } }),
      "GET /api/jobs/1": () => ({ status: 200, body: detail(plain) }),
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/保存先/)).toBeInTheDocument();
    expect(within(dialog).queryByTestId("job-progress")).toBeNull();
    expect(within(dialog).queryByRole("progressbar")).toBeNull();
  });
});

describe("JobsPage のチャプターの印", () => {
  const jobs = () => ({
    status: 200,
    body: {
      jobs: [
        { ...base, id: 1, showName: "番組待ち", state: "done", retryable: false, chapters: "waiting" },
        { ...base, id: 2, showName: "番組なし", state: "done", retryable: false, chapters: "not_added" },
        { ...base, id: 3, showName: "番組入った", state: "done", retryable: false, chapters: "added" },
        { ...base, id: 4, showName: "番組値なし", state: "done", retryable: false },
      ],
    },
  });
  const itemOf = (items: HTMLElement[], name: string) => items.find((i) => within(i).queryByText(name) !== null)!;

  for (const wide of [true, false]) {
    it(`waiting に「チャプター待ち」、not_added に「チャプターなし」を出し、added と値なしには出さない（${wide ? "PC の表" : "スマホのカード"}）`, async () => {
      setWide(wide);
      mockApi({ "GET /api/stations": stations, "GET /api/jobs": jobs });
      renderPage();
      const items = await screen.findAllByTestId("job");
      expect(items).toHaveLength(4);
      const waiting = within(itemOf(items, "番組待ち"));
      expect(waiting.getByText("チャプター待ち")).toBeInTheDocument();
      expect(waiting.queryByText("チャプターなし")).toBeNull();
      const none = within(itemOf(items, "番組なし"));
      expect(none.getByText("チャプターなし")).toBeInTheDocument();
      expect(none.queryByText("チャプター待ち")).toBeNull();
      for (const name of ["番組入った", "番組値なし"]) {
        const row = within(itemOf(items, name));
        expect(row.queryByText("チャプター待ち")).toBeNull();
        expect(row.queryByText("チャプターなし")).toBeNull();
      }
    });
  }
});
