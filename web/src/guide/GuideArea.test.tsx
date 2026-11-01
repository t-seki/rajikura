// 番組表のエリアの切り替え。会員種別がエリアフリー・ダブルプランのときだけ出し、選んだエリアを URL の ?area= に載せる（ADR 0024。Shell を通す）。

import { MantineProvider } from "@mantine/core";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Shell } from "../Shell";
import type { GuideAiring, GuideAreas } from "./api";
import { broadcastDayOf } from "./format";

type Handler = () => { status: number; body?: unknown };

function mockApi(routes: Record<string, Handler>) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push(key);
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = h();
      return new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

const today = broadcastDayOf(new Date());
const base = { performer: "", description: "", descriptionParts: [], notAvailable: false, state: "none" as const };
const airing = (stationId: string, stationName: string, title: string): GuideAiring => ({
  ...base,
  stationId,
  stationName,
  title,
  start: `${today}T14:00:00+09:00`,
  end: `${today}T15:00:00+09:00`,
});
const guideOf = (id: string, name: string, title: string) => () => ({
  status: 200,
  body: { day: today, stations: [{ id, name, airings: [airing(id, name, title)] }] },
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

const common: Record<string, Handler> = {
  [`GET /api/guide?day=${today}`]: guideOf("TBS", "TBSラジオ", "東京の番組"),
  [`GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`]: guideOf("ABC", "ABCラジオ", "大阪の番組"),
  [`GET /api/guide/search?${new URLSearchParams({ q: "番組" })}`]: () => ({
    status: 200,
    body: { q: "番組", airings: [airing("TBS", "TBSラジオ", "東京の番組")], truncated: false },
  }),
  [`GET /api/guide/search?${new URLSearchParams({ q: "番組", area: "JP27" })}`]: () => ({
    status: 200,
    body: { q: "番組", airings: [airing("ABC", "ABCラジオ", "大阪の番組")], truncated: false },
  }),
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

describe("番組表のエリアの切り替え", () => {
  it("エリアフリーのときだけエリアの切り替えを出す", async () => {
    mockApi({ ...common, "GET /api/guide/areas": areasOf(false) });
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    expect(screen.queryByLabelText("エリア", { selector: "input" })).not.toBeInTheDocument();
  });

  it("選んだエリアの番組表を読み、URL の ?area= に載せ（自分のエリアに戻すと外す）、履歴に積まない", async () => {
    const calls = mockApi({ ...common, "GET /api/guide/areas": areasOf(true) });
    const user = userEvent.setup();
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    const select = await screen.findByLabelText("エリア", { selector: "input" });
    expect(select).toHaveValue("東京都（自分のエリア）");
    const before = window.history.length;

    await user.click(select);
    await user.click(screen.getByRole("option", { name: "大阪府", hidden: true }));
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /大阪の番組/ })).toBeInTheDocument();
    expect(calls).toContain(`GET /api/guide?${new URLSearchParams({ day: today, area: "JP27" })}`);
    await waitFor(() => expect(here()).toBe("/?area=JP27"));

    await user.click(screen.getByLabelText("エリア", { selector: "input" }));
    await user.click(screen.getByRole("option", { name: "東京都（自分のエリア）", hidden: true }));
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ })).toBeInTheDocument();
    await waitFor(() => expect(here()).toBe("/"));
    expect(window.history.length).toBe(before);
  });

  it("?area= のエリアを開く（自分のエリアの番組表は読まない）", async () => {
    window.history.replaceState(null, "", "/?area=JP27");
    const calls = mockApi({ ...common, "GET /api/guide/areas": areasOf(true) });
    renderShell();
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /大阪の番組/ })).toBeInTheDocument();
    expect(calls).not.toContain(`GET /api/guide?day=${today}`);
    expect(await screen.findByLabelText("エリア", { selector: "input" })).toHaveValue("大阪府");
    expect(here()).toBe("/?area=JP27");
  });

  it("切り替えられない会員種別で ?area= を開いたら、自分のエリアを開いて URL を直す", async () => {
    window.history.replaceState(null, "", "/?area=JP27");
    const calls = mockApi({ ...common, "GET /api/guide/areas": areasOf(false) });
    renderShell();
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ })).toBeInTheDocument();
    expect(calls.some((c) => c.includes("area=JP27") && c.startsWith("GET /api/guide?"))).toBe(false);
    await waitFor(() => expect(here()).toBe("/"));
  });

  it("エリアの一覧を読めなければ、切り替えを出さず自分のエリアを開く", async () => {
    window.history.replaceState(null, "", "/?area=JP27");
    mockApi({ ...common, "GET /api/guide/areas": () => ({ status: 502, body: { error: "x" } }) });
    renderShell();
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ })).toBeInTheDocument();
    expect(screen.queryByLabelText("エリア", { selector: "input" })).not.toBeInTheDocument();
    await waitFor(() => expect(here()).toBe("/"));
  });

  it("ほかのエリアを選んでいる間は、検索結果の「このキーワードで録音」を出さない", async () => {
    mockApi({ ...common, "GET /api/guide/areas": areasOf(true) });
    const user = userEvent.setup();
    renderShell();
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /東京の番組/ });
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
    await user.click(screen.getByRole("button", { name: "検索" }));
    expect(await screen.findByRole("button", { name: "このキーワードで録音" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "検索をやめる" }));

    await user.click(await screen.findByLabelText("エリア", { selector: "input" }));
    await user.click(screen.getByRole("option", { name: "大阪府", hidden: true }));
    await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /大阪の番組/ });
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "番組");
    await user.click(screen.getByRole("button", { name: "検索" }));
    await waitFor(() => expect(screen.getByLabelText("検索結果")).toHaveTextContent("大阪の番組"));
    expect(screen.queryByRole("button", { name: "このキーワードで録音" })).not.toBeInTheDocument();
  });
});
