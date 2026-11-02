// 番組表の全国の検索（用語集。ADR 0035）。会員種別がエリアフリー・ダブルプランのときだけ「自分のエリア」「全国」の切り替えを出し、
// 全国を選んだら URL の ?scope=all に載せる（ADR 0024。Shell を通す）。

import { MantineProvider } from "@mantine/core";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Shell } from "../Shell";
import type { GuideAiring, GuideAreas, SearchResult } from "./api";
import { broadcastDayOf } from "./format";
import { NATIONWIDE_LOADING } from "./GuidePage";

type Handler = () => { status: number; body?: unknown } | Promise<{ status: number; body?: unknown }>;

function mockApi(routes: Record<string, Handler>) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push(key);
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = await h();
      return new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

const today = broadcastDayOf(new Date());
const base = { performer: "", description: "", descriptionParts: [], notAvailable: false, timeFree30Excluded: false, state: "none" as const };
const airing = (stationId: string, stationName: string, title: string, stationAreaName?: string): GuideAiring => ({
  ...base,
  stationId,
  stationName,
  title,
  stationAreaName,
  start: `${today}T14:00:00+09:00`,
  end: `${today}T15:00:00+09:00`,
});
const areasOf = (switchable: boolean): Handler => () => ({
  status: 200,
  body: {
    own: "JP13",
    switchable,
    pastDays: 7,
    areas: [
      { id: "JP13", name: "東京都" },
      { id: "JP27", name: "大阪府" },
    ],
  } satisfies GuideAreas,
});

const ownSearch = `GET /api/guide/search?${new URLSearchParams({ q: "番組" })}`;
const allSearch = `GET /api/guide/search?${new URLSearchParams({ q: "番組", scope: "all" })}`;
const allResult = (failedStations = 0): SearchResult => ({
  q: "番組",
  scope: "all",
  failedStations,
  truncated: false,
  airings: [airing("TBS", "TBSラジオ", "ネットの番組"), airing("MBS", "MBSラジオ", "ネットの番組", "大阪府")],
});

const common: Record<string, Handler> = {
  [`GET /api/guide?day=${today}`]: () => ({
    status: 200,
    body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [airing("TBS", "TBSラジオ", "東京の番組")] }] },
  }),
  [ownSearch]: () => ({ status: 200, body: { q: "番組", airings: [airing("TBS", "TBSラジオ", "東京の番組")], truncated: false } }),
  [allSearch]: () => ({ status: 200, body: allResult() }),
  "GET /api/shows": () => ({ status: 200, body: [] }),
  "GET /api/stations": () => ({ status: 200, body: [{ id: "TBS", name: "TBSラジオ" }] }),
  "GET /api/notifications/status": () => ({ status: 200, body: { unread: 0, failed: 0, anomalies: [] } }),
  "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
};

const renderShell = () =>
  render(
    <MantineProvider>
      <Shell onLogout={async () => {}} version="v0.0.0" />
    </MantineProvider>,
  );

const here = () => window.location.pathname + window.location.search;

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
});
afterEach(() => vi.unstubAllGlobals());

describe("番組表の全国の検索", () => {
  it("非会員・タイムフリー30（切り替えられない会員種別）では、検索の対象の切り替えを出さず、自分のエリアを探す", async () => {
    const calls = mockApi({ ...common, "GET /api/guide/areas": areasOf(false) });
    const user = userEvent.setup();
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    expect(screen.queryByRole("radio", { name: "全国" })).not.toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
    await user.click(screen.getByRole("button", { name: "検索" }));
    await waitFor(() => expect(screen.getByLabelText("検索結果")).toHaveTextContent("東京の番組"));
    expect(calls).toContain(ownSearch);
    expect(calls).not.toContain(allSearch);
  });

  it("全国を選ぶと全国の局から探し、URL の ?scope=all に載せる。局名にエリアの名前を添え、「このキーワードで録音」を出さない", async () => {
    let release = () => {};
    const waiting = new Promise<void>((r) => (release = r));
    const calls = mockApi({
      ...common,
      "GET /api/guide/areas": areasOf(true),
      [allSearch]: async () => {
        await waiting;
        return { status: 200, body: allResult() };
      },
    });
    const user = userEvent.setup();
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    // 既定は自分のエリア
    expect(await screen.findByRole("radio", { name: "自分のエリア" })).toBeChecked();
    await user.click(screen.getByText("全国"));
    await waitFor(() => expect(here()).toBe("/?scope=all"));

    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
    await user.click(screen.getByRole("button", { name: "検索" }));
    // 取り終えるまでは、待っていることを出す
    expect(await screen.findByText(NATIONWIDE_LOADING)).toBeInTheDocument();
    release();
    const results = await screen.findByLabelText("検索結果");
    await waitFor(() => expect(results).toHaveTextContent("MBSラジオ（大阪府）"));
    // 自分のエリアの局にはエリアの名前を添えない
    expect(results).not.toHaveTextContent("TBSラジオ（");
    expect(screen.queryByText(NATIONWIDE_LOADING)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "このキーワードで録音" })).not.toBeInTheDocument();
    expect(calls).toContain(allSearch);
    await waitFor(() => expect(here()).toBe(`/?${new URLSearchParams({ q: "番組", scope: "all" })}`));

    // 自分のエリアに戻すと、流している検索を自分のエリアで流し直し、URL から外す
    await user.click(screen.getByText("自分のエリア"));
    await waitFor(() => expect(screen.getByLabelText("検索結果")).toHaveTextContent("東京の番組"));
    expect(screen.getByRole("button", { name: "このキーワードで録音" })).toBeInTheDocument();
    await waitFor(() => expect(here()).toBe(`/?${new URLSearchParams({ q: "番組" })}`));
  });

  it("全国の検索を待つ間に自分のエリアへ戻したら、全国の遅い応答が後から返っても自分のエリアの結果を残す", async () => {
    let release = () => {};
    const waiting = new Promise<void>((r) => (release = r));
    let allDone = false;
    mockApi({
      ...common,
      "GET /api/guide/areas": areasOf(true),
      [allSearch]: async () => {
        await waiting;
        allDone = true;
        return { status: 200, body: allResult() };
      },
    });
    const user = userEvent.setup();
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    await user.click(await screen.findByText("全国"));
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
    await user.click(screen.getByRole("button", { name: "検索" }));
    expect(await screen.findByText(NATIONWIDE_LOADING)).toBeInTheDocument();

    // 待つ間に自分のエリアへ戻すと、自分のエリアで流し直す
    await user.click(screen.getByText("自分のエリア"));
    await waitFor(() => expect(screen.getByLabelText("検索結果")).toHaveTextContent("東京の番組"));

    // 全国の遅い応答が後から返っても、捨てる
    release();
    await waitFor(() => expect(allDone).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    const results = screen.getByLabelText("検索結果");
    expect(results).toHaveTextContent("東京の番組");
    expect(results).not.toHaveTextContent("ネットの番組");
    expect(screen.getByRole("button", { name: "このキーワードで録音" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "検索" })).not.toHaveAttribute("data-loading");
  });

  it("検索中に「検索をやめる」を押したら、遅い応答が後から返っても結果を出さない", async () => {
    let release = () => {};
    const waiting = new Promise<void>((r) => (release = r));
    let allDone = false;
    mockApi({
      ...common,
      "GET /api/guide/areas": areasOf(true),
      [allSearch]: async () => {
        await waiting;
        allDone = true;
        return { status: 200, body: allResult() };
      },
    });
    const user = userEvent.setup();
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    // 自分のエリアで探した後、全国に切り替えて流し直す（全国の応答は待たせる）
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
    await user.click(screen.getByRole("button", { name: "検索" }));
    await waitFor(() => expect(screen.getByLabelText("検索結果")).toHaveTextContent("東京の番組"));
    await user.click(screen.getByText("全国"));
    expect(await screen.findByText(NATIONWIDE_LOADING)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "検索をやめる" }));
    expect(screen.queryByLabelText("検索結果")).not.toBeInTheDocument();
    expect(screen.queryByText(NATIONWIDE_LOADING)).not.toBeInTheDocument();

    release();
    await waitFor(() => expect(allDone).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByLabelText("検索結果")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" })).toHaveValue("");
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
  });

  it("番組表を一部取れなかった局があれば、その数を出す", async () => {
    window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", scope: "all" })}`);
    mockApi({ ...common, "GET /api/guide/areas": areasOf(true), [allSearch]: () => ({ status: 200, body: allResult(2) }) });
    renderShell();
    expect(await screen.findByText("2 局の番組表を一部取れなかった。")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "全国" })).toBeChecked();
  });

  it("選べない会員種別で ?scope=all を開いたら、自分のエリアで探して URL を直す", async () => {
    window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", scope: "all" })}`);
    const calls = mockApi({ ...common, "GET /api/guide/areas": areasOf(false) });
    renderShell();
    await waitFor(() => expect(screen.getByLabelText("検索結果")).toHaveTextContent("東京の番組"));
    expect(calls).not.toContain(allSearch);
    await waitFor(() => expect(here()).toBe(`/?${new URLSearchParams({ q: "番組" })}`));
  });
  // 全国の検索の結果から開いた分割番組の詳細は、局のエリアの番組表からほかのパートを探す（#53）。
  describe("分割番組のほかのパート", () => {
    const part = (stationId: string, stationName: string, hour: number, stationAreaId?: string): GuideAiring => ({
      ...base,
      stationId,
      stationName,
      title: `ネットの番組（${hour}時台）`,
      stationAreaId,
      start: `${today}T${hour}:00:00+09:00`,
      end: `${today}T${hour + 1}:00:00+09:00`,
    });
    const guideOf = (airings: GuideAiring[]): Handler => () => ({
      status: 200,
      body: { day: today, stations: [{ id: airings[0].stationId, name: airings[0].stationName, airings }] },
    });
    const partsLabel = "パート 2 件（自動で選びました）";

    it("自分のエリアの外の局の分割番組は、stationAreaId のエリアの番組表から組む", async () => {
      window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", scope: "all" })}`);
      const mbs = [part("MBS", "MBSラジオ", 14, "JP27"), part("MBS", "MBSラジオ", 15, "JP27")];
      const calls = mockApi({
        ...common,
        "GET /api/guide/areas": areasOf(true),
        [allSearch]: () => ({ status: 200, body: { ...allResult(), airings: [mbs[0]] } }),
        [`GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`]: guideOf(mbs),
      });
      const user = userEvent.setup();
      renderShell();
      await user.click(await within(await screen.findByLabelText("検索結果")).findByRole("button", { name: /14時台/ }));
      const dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText(partsLabel)).toBeInTheDocument();
      expect(calls).toContain(`GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`);
    });

    it("ほかのエリアを選んだまま自分のエリアの局（stationAreaId なし）の分割番組を開くと、area を付けない番組表から組む", async () => {
      window.history.replaceState(null, "", `/?${new URLSearchParams({ area: "JP27" })}`);
      const tbs = [part("TBS", "TBSラジオ", 14), part("TBS", "TBSラジオ", 15)];
      const calls = mockApi({
        ...common,
        "GET /api/guide/areas": areasOf(true),
        [`GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`]: guideOf([part("MBS", "MBSラジオ", 10, "JP27")]),
        [`GET /api/guide?day=${today}`]: guideOf(tbs),
        [allSearch]: () => ({ status: 200, body: { ...allResult(), airings: [tbs[0]] } }),
      });
      const user = userEvent.setup();
      renderShell();
      await user.click(await screen.findByText("全国"));
      await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
      await user.click(screen.getByRole("button", { name: "検索" }));
      await user.click(await within(await screen.findByLabelText("検索結果")).findByRole("button", { name: /14時台/ }));
      const dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText(partsLabel)).toBeInTheDocument();
      expect(calls).toContain(`GET /api/guide?day=${today}`);
    });

    it("選んだエリアの検索から開いた詳細は、今どおり選んでいるエリアの番組表から組む", async () => {
      window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", area: "JP27" })}`);
      const mbs = [part("MBS", "MBSラジオ", 14), part("MBS", "MBSラジオ", 15)];
      const areaGuide = `GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`;
      const calls = mockApi({
        ...common,
        "GET /api/guide/areas": areasOf(true),
        [areaGuide]: guideOf(mbs),
        [`GET /api/guide/search?${new URLSearchParams({ q: "番組", area: "JP27" })}`]: () => ({
          status: 200,
          body: { q: "番組", airings: [mbs[0]], truncated: false },
        }),
      });
      const user = userEvent.setup();
      renderShell();
      await user.click(await within(await screen.findByLabelText("検索結果")).findByRole("button", { name: /14時台/ }));
      const dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText(partsLabel)).toBeInTheDocument();
      expect(calls.filter((c) => c === areaGuide).length).toBeGreaterThan(0);
      expect(calls).not.toContain(`GET /api/guide?day=${today}`);
    });
  });

  // 詳細のパネルが「もう当てている放送回」を数える番組表も、パート探しと同じエリアにする（#57）。
  describe("詳細のパネルの番組表のエリア", () => {
    const mbsAt = (hour: number, extra: Partial<GuideAiring>): GuideAiring => ({
      ...base,
      stationId: "MBS",
      stationName: "MBSラジオ",
      title: "ネットの番組",
      stationAreaId: "JP27",
      start: `${today}T${hour}:00:00+09:00`,
      end: `${today}T${hour + 1}:00:00+09:00`,
      ...extra,
    });
    const mbsGuide = `GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`;
    const guideOf = (airings: GuideAiring[]): Handler => () => ({
      status: 200,
      body: { day: today, stations: [{ id: "MBS", name: "MBSラジオ", airings }] },
    });
    const morningShow = { name: "朝の番組", enabled: true, split: "none", check_title: true, weekly: [] };
    const join = "パートを 1 つのファイルにつなぐ";

    it("全国の検索の結果から開いたら、足す先の番組がもう当てている放送回も局のエリアの番組表で数えてつなぐを出す", async () => {
      window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", scope: "all" })}`);
      const target = mbsAt(14, {});
      // 局のエリアの番組表で、同じ放送日の別の放送回に番組「朝の番組」が当たっている（選んでいるエリアの番組表に MBS は無い）
      const other = mbsAt(10, { title: "別の題", showName: "朝の番組" });
      const calls = mockApi({
        ...common,
        "GET /api/guide/areas": areasOf(true),
        "GET /api/shows": () => ({ status: 200, body: [morningShow] }),
        [allSearch]: () => ({ status: 200, body: { ...allResult(), airings: [target] } }),
        [mbsGuide]: guideOf([other, target]),
      });
      const user = userEvent.setup();
      renderShell();
      await user.click(await within(await screen.findByLabelText("検索結果")).findByRole("button", { name: /ネットの番組/ }));
      const dialog = await screen.findByRole("dialog");
      await user.click(await within(dialog).findByRole("button", { name: "設定を変えて予約" }));
      const settings = await within(dialog).findByLabelText("設定を変えて予約");
      await user.click(within(settings).getByRole("combobox", { name: "どの番組に足すか" }));
      await user.click(await screen.findByRole("option", { name: "朝の番組", hidden: true }));
      expect(await within(dialog).findByRole("checkbox", { name: join })).toBeInTheDocument();
      expect(calls).toContain(mbsGuide);
    });

    it("全国の検索以外から開いたら、選んでいるエリアの番組表で数える（局のエリアの番組表は引かない）", async () => {
      window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", area: "JP27" })}`);
      const target = mbsAt(14, { stationAreaId: undefined });
      const calls = mockApi({
        ...common,
        "GET /api/guide/areas": areasOf(true),
        "GET /api/shows": () => ({ status: 200, body: [morningShow] }),
        [mbsGuide]: guideOf([mbsAt(10, { stationAreaId: undefined, title: "別の題", showName: "朝の番組" }), target]),
        [`GET /api/guide/search?${new URLSearchParams({ q: "番組", area: "JP27" })}`]: () => ({
          status: 200,
          body: { q: "番組", airings: [target], truncated: false },
        }),
      });
      const user = userEvent.setup();
      renderShell();
      await user.click(await within(await screen.findByLabelText("検索結果")).findByRole("button", { name: /ネットの番組/ }));
      const dialog = await screen.findByRole("dialog");
      await user.click(await within(dialog).findByRole("button", { name: "設定を変えて予約" }));
      const settings = await within(dialog).findByLabelText("設定を変えて予約");
      await user.click(within(settings).getByRole("combobox", { name: "どの番組に足すか" }));
      await user.click(await screen.findByRole("option", { name: "朝の番組", hidden: true }));
      expect(await within(dialog).findByRole("checkbox", { name: join })).toBeInTheDocument();
      expect(calls).not.toContain(`GET /api/guide?day=${today}`);
    });

    it("全国の検索の結果から選べない放送回（録音済み）を開いても、パネルは局のエリアの番組表を引く", async () => {
      window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "番組", scope: "all" })}`);
      const done = mbsAt(14, { state: "done" });
      const calls = mockApi({
        ...common,
        "GET /api/guide/areas": areasOf(true),
        [allSearch]: () => ({ status: 200, body: { ...allResult(), airings: [done] } }),
        [mbsGuide]: guideOf([done]),
      });
      const user = userEvent.setup();
      renderShell();
      await user.click(await within(await screen.findByLabelText("検索結果")).findByRole("button", { name: /ネットの番組/ }));
      await screen.findByRole("dialog");
      await waitFor(() => expect(calls).toContain(mbsGuide));
      expect(calls).not.toContain(`GET /api/guide?day=${today}&area=JP13`);
    });
  });
});
