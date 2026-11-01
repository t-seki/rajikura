// 番組表で選べる過去の範囲。会員種別の録音期限（GET /api/guide/areas の pastDays。7 または 30）に合わせる（Shell を通す）。

import { MantineProvider } from "@mantine/core";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Shell } from "../Shell";
import type { GuideAreas } from "./api";
import { addDays, broadcastDayOf } from "./format";

type Handler = () => { status: number; body?: unknown };

function mockApi(pastDays: number) {
  const calls: string[] = [];
  const routes: Record<string, Handler> = {
    "GET /api/guide/areas": () => ({
      status: 200,
      body: { own: "JP13", switchable: false, pastDays, areas: [{ id: "JP13", name: "東京都" }] } satisfies GuideAreas,
    }),
    "GET /api/shows": () => ({ status: 200, body: [] }),
    "GET /api/stations": () => ({ status: 200, body: [] }),
    "GET /api/notifications/status": () => ({ status: 200, body: { unread: 0, failed: 0, anomalies: [] } }),
    "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      calls.push(`${method} ${url}`);
      const guide = /^\/api\/guide\?day=([\d-]+)$/.exec(url);
      if (method === "GET" && guide) return new Response(JSON.stringify({ day: guide[1], stations: [] }), { status: 200 });
      const h = routes[`${method} ${url}`];
      if (!h) throw new Error(`想定外の呼び出し: ${method} ${url}`);
      const { status, body } = h();
      return new Response(JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

const today = broadcastDayOf(new Date());

const renderShell = () =>
  render(
    <MantineProvider>
      <Shell onLogout={async () => {}} version="v0.0.0" />
    </MantineProvider>,
  );

/** 放送日の選択肢の数（開いて数える）。 */
async function optionCount() {
  await userEvent.setup().click(await screen.findByLabelText("放送日", { selector: "input" }));
  return screen.getAllByRole("option", { hidden: true }).length;
}

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
});
afterEach(() => vi.unstubAllGlobals());

describe("番組表で選べる過去の範囲", () => {
  it("タイムフリー30・ダブルプランのとき、30 日前の放送日まで選べる", async () => {
    mockApi(30);
    renderShell();
    await screen.findByText("この放送日の番組表は空。");
    // 会員種別が分かると選択肢が増える。開く前に待つため、数え直す
    await vi.waitFor(async () => expect(await optionCount()).toBe(30 + 7 + 1));
  });

  it("非会員のとき、選べるのは 7 日前まで", async () => {
    mockApi(7);
    renderShell();
    await screen.findByText("この放送日の番組表は空。");
    expect(await optionCount()).toBe(7 + 7 + 1);
  });

  it("30 日前の放送日を URL で開ける（会員種別が分かってから読む）", async () => {
    const old = addDays(today, -30);
    window.history.replaceState(null, "", `/?day=${old}`);
    const calls = mockApi(30);
    renderShell();
    await screen.findByText("この放送日の番組表は空。");
    expect(calls).toContain(`GET /api/guide?day=${old}`);
  });

  it("7 日より前の放送日を、非会員が URL で開いたら今日にする（範囲の外の番組表は読まない）", async () => {
    const old = addDays(today, -30);
    window.history.replaceState(null, "", `/?day=${old}`);
    const calls = mockApi(7);
    renderShell();
    await screen.findByText("この放送日の番組表は空。");
    expect(calls).toContain(`GET /api/guide?day=${today}`);
    expect(calls).not.toContain(`GET /api/guide?day=${old}`);
  });
});
