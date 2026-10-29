// 番組表の「番組を開く」「番組の設定を開く」で、番組の画面に切り替えてその番組の編集を開く（Shell を通す）。

import { MantineProvider } from "@mantine/core";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Shell } from "../Shell";
import type { Show } from "../shows/api";
import type { GuideAiring } from "./api";
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
const base = { stationId: "TBS", stationName: "TBSラジオ", performer: "", description: "", descriptionParts: [], notAvailable: false };
const morning: GuideAiring = {
  ...base,
  title: "朝の番組",
  start: `${today}T14:00:00+09:00`,
  end: `${today}T15:00:00+09:00`,
  state: "none",
};
const withWeekly: GuideAiring = {
  ...base,
  title: "番組R",
  start: `${today}T10:00:00+09:00`,
  end: `${today}T11:00:00+09:00`,
  state: "planned",
  showName: "朝の番組",
};
const show: Show = {
  name: "朝の番組",
  enabled: true,
  split: "none",
  check_title: true,
  weekly: [{ station: "TBS", weekday: 1, start: "07:00" }],
};
const enc = encodeURIComponent;

const routes: Record<string, Handler> = {
  [`GET /api/guide?day=${today}`]: () => ({
    status: 200,
    body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [withWeekly, morning] }] },
  }),
  "GET /api/shows": () => ({ status: 200, body: [show] }),
  "GET /api/stations": () => ({ status: 200, body: [{ id: "TBS", name: "TBSラジオ" }] }),
  [`GET /api/shows/${enc("朝の番組")}`]: () => ({ status: 200, body: { ...show, onces: [] } }),
  [`POST /api/shows/${enc("朝の番組")}/onces`]: () => ({ status: 201, body: { onces: [] } }),
  // 番組R をもう当てている分割番組でない番組に朝の放送回を足すので、「パートを 1 つのファイルにつなぐ」（既定でオン）で split を変える
  [`PUT /api/shows/${enc("朝の番組")}`]: () => ({ status: 200, body: { ...show, split: "join" } }),
};

const renderShell = () =>
  render(
    <MantineProvider>
      <Shell onLogout={async () => {}} version="v0.0.0" />
    </MantineProvider>,
  );

async function openAiring(user: ReturnType<typeof userEvent.setup>, name: RegExp) {
  const list = await screen.findByTestId("guide-list");
  await user.click(within(list).getByRole("button", { name }));
  return await screen.findByRole("dialog");
}

async function expectEditing(name: string) {
  expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
  const dialog = await screen.findByRole("dialog", { name: "番組を編集する" });
  expect(within(dialog).getByRole("textbox", { name: /番組の名前/ })).toHaveValue(name);
}

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
});
afterEach(() => vi.unstubAllGlobals());

describe("番組表から番組の画面を開く", () => {
  it("帯の「番組を開く」で、番組の画面のその番組の編集を開く", async () => {
    mockApi(routes);
    const user = userEvent.setup();
    renderShell();
    const dialog = await openAiring(user, /^朝の番組/);
    const button = await within(dialog).findByRole("button", { name: "この回を録音" });
    await waitFor(() => expect(button).not.toHaveAttribute("data-loading"));
    await user.click(button);
    await user.click(await screen.findByRole("button", { name: "番組を開く" }));
    await expectEditing("朝の番組");

    // 一度開いたら覚えない（ナビで開き直しても、編集は開かない）
    await user.click(within(screen.getByRole("dialog", { name: "番組を編集する" })).getByRole("button", { name: "やめる" }));
    await user.click(screen.getByText("番組表"));
    await screen.findByTestId("guide-list");
    await user.click(screen.getByText("番組"));
    expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
    await screen.findByText("朝の番組");
    // 編集が開くなら一覧を読んだ直後に開くので、少し待ってから見る
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByRole("dialog", { name: "番組を編集する" })).toBeNull();
  });

  it("予約で当たっている回の「番組の設定を開く」で、その番組の編集を開く", async () => {
    mockApi(routes);
    const user = userEvent.setup();
    renderShell();
    const dialog = await openAiring(user, /番組R/);
    await user.click(await within(dialog).findByRole("button", { name: "番組の設定を開く" }));
    await expectEditing("朝の番組");
    // 番組の編集の URL へ移る
    expect(window.location.pathname).toBe(`/shows/${enc("朝の番組")}`);
  });

  it("開く番組が一覧に無ければ、見つからないと出し、編集は開かない", async () => {
    mockApi({ ...routes, "GET /api/shows": () => ({ status: 200, body: [] }) });
    const user = userEvent.setup();
    renderShell();
    const dialog = await openAiring(user, /番組R/);
    await user.click(await within(dialog).findByRole("button", { name: "番組の設定を開く" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("番組「朝の番組」が見つからない");
    expect(screen.queryByRole("dialog", { name: "番組を編集する" })).toBeNull();
  });

  it("番組の一覧を読めなかったときも、開く番組を片付ける（後で開き直しても前の番組を開かない）", async () => {
    let failed = false;
    mockApi({
      ...routes,
      "GET /api/shows": () => {
        if (!failed) {
          failed = true;
          return { status: 500, body: { error: "内部エラー" } };
        }
        return { status: 200, body: [show] };
      },
    });
    const user = userEvent.setup();
    renderShell();
    const dialog = await openAiring(user, /番組R/);
    await user.click(await within(dialog).findByRole("button", { name: "番組の設定を開く" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("番組の一覧を読めない");

    await user.click(screen.getByText("番組表"));
    await screen.findByTestId("guide-list");
    await user.click(screen.getByText("番組"));
    await screen.findByText("朝の番組");
    // 編集が開くなら一覧を読んだ直後に開くので、少し待ってから見る
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByRole("dialog", { name: "番組を編集する" })).toBeNull();
  });
});
