// 番組表の状態のうち、ブラウザに覚えていないもの（放送日・検索の語・詳細を開いた放送回）を URL のクエリに載せる（ADR 0024。Shell を通す）。

import { MantineProvider } from "@mantine/core";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Shell } from "../Shell";
import type { GuideAiring } from "./api";
import { addDays, broadcastDayOf } from "./format";
import { airingParam } from "./GuidePage";

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
const tomorrow = addDays(today, 1);
const base = { stationId: "TBS", stationName: "TBSラジオ", performer: "", description: "", descriptionParts: [], notAvailable: false };
const morning: GuideAiring = {
  ...base,
  title: "朝の番組",
  start: `${today}T14:00:00+09:00`,
  end: `${today}T15:00:00+09:00`,
  state: "none",
};
const evening: GuideAiring = {
  ...base,
  title: "夜の番組",
  start: `${tomorrow}T20:00:00+09:00`,
  end: `${tomorrow}T21:00:00+09:00`,
  state: "none",
};
const guideOf = (day: string, airings: GuideAiring[]) => () => ({
  status: 200,
  body: { day, stations: [{ id: "TBS", name: "TBSラジオ", airings }] },
});
const searchKey = (q: string) => `GET /api/guide/search?${new URLSearchParams({ q })}`;

const routes: Record<string, Handler> = {
  [`GET /api/guide?day=${today}`]: guideOf(today, [morning]),
  [`GET /api/guide?day=${tomorrow}`]: guideOf(tomorrow, [evening]),
  [searchKey("夜")]: () => ({ status: 200, body: { q: "夜", airings: [evening], truncated: false } }),
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

describe("番組表の URL", () => {
  it("airingParam は局と、日本時間の開始時刻で放送回を指す", () => {
    expect(airingParam({ stationId: "TBS", start: "2026-10-23T01:30:00+09:00" })).toBe("TBS_20261023013000");
    expect(airingParam({ stationId: "TBS", start: "2026-10-22T16:30:00Z" })).toBe("TBS_20261023013000");
  });

  it("?day= の放送日を開き、放送日の切り替えは URL を置き換えて履歴に積まない", async () => {
    window.history.replaceState(null, "", `/?day=${tomorrow}`);
    const calls = mockApi(routes);
    const user = userEvent.setup();
    renderShell();
    const list = await screen.findByTestId("guide-list");
    expect(within(list).getByRole("button", { name: /夜の番組/ })).toBeInTheDocument();
    expect(calls).not.toContain(`GET /api/guide?day=${today}`);
    const before = window.history.length;

    // 今日の放送日は URL に載せない
    await user.click(screen.getByRole("button", { name: "前の日" }));
    await waitFor(() => expect(here()).toBe("/"));
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /朝の番組/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "次の日" }));
    await waitFor(() => expect(here()).toBe(`/?day=${tomorrow}`));
    expect(window.history.length).toBe(before);
  });

  it("前後 7 日の外の放送日は今日を開き、URL を直す", async () => {
    window.history.replaceState(null, "", "/?day=2000-01-01");
    mockApi(routes);
    renderShell();
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /朝の番組/ })).toBeInTheDocument();
    await waitFor(() => expect(here()).toBe("/"));
  });

  it("検索の語を URL の ?q= に載せ（履歴に積まない）、再読み込みしても同じ検索が開く", async () => {
    const calls = mockApi(routes);
    const user = userEvent.setup();
    const first = renderShell();
    await screen.findByTestId("guide-list");
    const before = window.history.length;
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "夜");
    await user.click(screen.getByRole("button", { name: "検索" }));
    expect(await screen.findByLabelText("検索結果")).toHaveTextContent("夜の番組");
    expect(here()).toBe(`/?${new URLSearchParams({ q: "夜" })}`);
    expect(window.history.length).toBe(before);

    first.unmount();
    calls.length = 0;
    renderShell();
    expect(await screen.findByLabelText("検索結果")).toHaveTextContent("夜の番組");
    expect(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" })).toHaveValue("夜");
    expect(calls).toContain(searchKey("夜"));

    await user.click(screen.getByRole("button", { name: "検索をやめる" }));
    await waitFor(() => expect(here()).toBe("/"));
  });

  it("詳細を開くと URL の ?airing= に載って履歴に積み、「戻る」で閉じ、「進む」で開き直す", async () => {
    mockApi(routes);
    const user = userEvent.setup();
    renderShell();
    const list = await screen.findByTestId("guide-list");
    const before = window.history.length;
    await user.click(within(list).getByRole("button", { name: /朝の番組/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "この回を録音" })).toBeInTheDocument();
    await waitFor(() => expect(here()).toBe(`/?airing=${airingParam(morning)}`));
    expect(window.history.length).toBe(before + 1);

    window.history.back();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(here()).toBe("/");

    window.history.forward();
    expect(await within(await screen.findByRole("dialog")).findByRole("button", { name: "この回を録音" })).toBeInTheDocument();
    expect(here()).toBe(`/?airing=${airingParam(morning)}`);
  });

  it("?airing= を直接開くと、その放送回の詳細が開く（検索の結果の放送回も）", async () => {
    window.history.replaceState(null, "", `/?airing=${airingParam(morning)}`);
    mockApi(routes);
    const first = renderShell();
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("朝の番組")).toBeInTheDocument();
    first.unmount();

    window.history.replaceState(null, "", `/?${new URLSearchParams({ q: "夜", airing: airingParam(evening) })}`);
    renderShell();
    expect(await screen.findByLabelText("検索結果")).toHaveTextContent("夜の番組");
    const again = await screen.findByRole("dialog");
    expect(within(again).getByText("夜の番組")).toBeInTheDocument();
  });

  it("?airing= を探す番組表を読めなければ、エラーを出して詳細を開かず、URL から外す", async () => {
    window.history.replaceState(null, "", `/?airing=${airingParam(morning)}`);
    const calls = mockApi({ ...routes, [`GET /api/guide?day=${today}`]: () => ({ status: 500, body: { error: "内部エラー" } }) });
    renderShell();
    expect(await screen.findByRole("alert")).toHaveTextContent("番組表を読めない: 内部エラー");
    await waitFor(() => expect(here()).toBe("/"));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls).toContain(`GET /api/guide?day=${today}`);
  });

  it("?airing= の放送回がその日に無ければ、詳細を開かずに番組表を出し、URL から外す", async () => {
    const missing = airingParam({ stationId: "TBS", start: `${today}T09:00:00+09:00` });
    window.history.replaceState(null, "", `/?airing=${missing}`);
    mockApi(routes);
    renderShell();
    expect(await within(await screen.findByTestId("guide-list")).findByRole("button", { name: /朝の番組/ })).toBeInTheDocument();
    await waitFor(() => expect(here()).toBe("/"));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
