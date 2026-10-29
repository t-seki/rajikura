import { MantineProvider } from "@mantine/core";
import { Notifications, notifications } from "@mantine/notifications";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GuideAiring } from "../guide/api";
import { KeywordsPage } from "./KeywordsPage";
import type { Keyword } from "./keywordsApi";

type Handler = (init?: RequestInit) => { status: number; body?: unknown };

function mockApi(routes: Record<string, Handler>) {
  const calls: { key: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push({ key, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = h(init);
      return new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

const renderPage = (props: Parameters<typeof KeywordsPage>[0] = {}) =>
  render(
    <MantineProvider env="test">
      <Notifications />
      <KeywordsPage {...props} />
    </MantineProvider>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
  notifications.clean();
});

const stations = [
  { id: "TBS", name: "TBSラジオ" },
  { id: "LFR", name: "ニッポン放送" },
];
const mukai: Keyword = { id: 1, words: "向井 ゲスト", exclude: "再放送", stations: ["TBS"], enabled: true, pending: 3 };
const other: Keyword = { id: 2, words: "朝", exclude: "", stations: [], enabled: false, pending: 0 };

const baseRoutes: Record<string, Handler> = { "GET /api/stations": () => ({ status: 200, body: stations }) };
const listRoute = (list: Keyword[]): Record<string, Handler> => ({ "GET /api/keywords": () => ({ status: 200, body: { keywords: list } }) });

function airing(over: Partial<GuideAiring> = {}): GuideAiring {
  return {
    stationId: "TBS",
    stationName: "TBSラジオ",
    start: "2026-10-20T13:00:00+09:00",
    end: "2026-10-20T14:00:00+09:00",
    title: "ゲスト回",
    performer: "",
    description: "",
    descriptionParts: [],
    state: "none",
    notAvailable: false,
    ...over,
  };
}
const previewPath = (words: string, exclude = "", st = "") => `GET /api/keywords/preview?${new URLSearchParams({ words, exclude, stations: st })}`;

describe("KeywordsPage", () => {
  it("一覧に、語・除外する語・局・有効／無効・録音を終えていない数を出す", async () => {
    mockApi({ ...baseRoutes, ...listRoute([mukai, other]) });
    renderPage();
    const items = await screen.findAllByTestId("keyword");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("向井 ゲスト")).toBeInTheDocument();
    expect(within(items[0]).getByText("除外: 再放送")).toBeInTheDocument();
    expect(within(items[0]).getByText("局: TBSラジオ")).toBeInTheDocument();
    expect(within(items[0]).getByText(/足した放送回: 3 件/)).toBeInTheDocument();
    expect(within(items[1]).getByText("局: エリアの全局")).toBeInTheDocument();
    expect(within(items[1]).getByText("無効")).toBeInTheDocument();
  });

  it("キーワードが無ければそう出す", async () => {
    mockApi({ ...baseRoutes, ...listRoute([]) });
    renderPage();
    expect(await screen.findByText("キーワードがまだ無い。")).toBeInTheDocument();
  });

  it("作るダイアログで、語を入れると当たるまだ始まっていない放送回が件数つきで出て、作れる", async () => {
    let list: Keyword[] = [];
    const calls = mockApi({
      ...baseRoutes,
      "GET /api/keywords": () => ({ status: 200, body: { keywords: list } }),
      [previewPath("向井")]: () => ({ status: 200, body: { airings: [airing(), airing({ start: "2026-10-21T01:00:00+09:00", end: "2026-10-21T02:00:00+09:00", title: "深夜の回" })], truncated: false } }),
      "POST /api/keywords": () => {
        list = [{ ...other, id: 5, words: "向井", enabled: true }];
        return { status: 201, body: list[0] };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "キーワードを作る" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: /^語/ }), "向井");
    expect(await within(dialog).findByText(/当たるまだ始まっていない放送回: 2 件/)).toBeInTheDocument();
    const rows = within(dialog).getAllByTestId("preview-airing");
    // 放送日の時刻（深夜 1 時は 25:00）
    expect(rows[1]).toHaveTextContent("25:00〜26:00");
    await user.click(within(dialog).getByRole("button", { name: "作る" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(calls.find((c) => c.key === "POST /api/keywords")?.body).toEqual({ words: "向井", exclude: "", stations: [], enabled: true });
    expect(await screen.findByText("向井")).toBeInTheDocument();
  });

  it("500 件を超えたら、絞るよう出す", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([]),
      [previewPath("朝")]: () => ({ status: 200, body: { airings: [airing()], truncated: true } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "キーワードを作る" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: /^語/ }), "朝");
    expect(await within(dialog).findByText(/500 件を超えたので、語を絞って/)).toBeInTheDocument();
  });

  it("番組表の検索から来た語を、作るダイアログの語の欄に入れて開く", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([]),
      [previewPath("ふらっと")]: () => ({ status: 200, body: { airings: [], truncated: false } }),
    });
    const handled = vi.fn();
    renderPage({ draftWords: "ふらっと", onDraftHandled: handled });
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("textbox", { name: /^語/ })).toHaveValue("ふらっと");
    expect(handled).toHaveBeenCalled();
    expect(await within(dialog).findByText(/当たるまだ始まっていない放送回: 0 件/)).toBeInTheDocument();
  });

  it("変えるダイアログは今の値で開き、保存で PUT する。局は複数選べる", async () => {
    const calls = mockApi({
      ...baseRoutes,
      ...listRoute([mukai]),
      [previewPath("向井 ゲスト", "再放送", "TBS")]: () => ({ status: 200, body: { airings: [], truncated: false } }),
      [previewPath("向井 ゲスト", "", "TBS")]: () => ({ status: 200, body: { airings: [], truncated: false } }),
      "PUT /api/keywords/1": () => ({ status: 200, body: mukai }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "向井 ゲスト を編集" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("textbox", { name: /^語/ })).toHaveValue("向井 ゲスト");
    expect(within(dialog).getByRole("textbox", { name: /^除外する語/ })).toHaveValue("再放送");
    await user.clear(within(dialog).getByRole("textbox", { name: /^除外する語/ }));
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key === "PUT /api/keywords/1")).toBe(true));
    expect(calls.find((c) => c.key === "PUT /api/keywords/1")?.body).toEqual({ words: "向井 ゲスト", exclude: "", stations: ["TBS"], enabled: true });
  });

  it("語が同じキーワードがあると、サーバーのエラーをダイアログに出す", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([]),
      [previewPath("朝")]: () => ({ status: 200, body: { airings: [], truncated: false } }),
      "POST /api/keywords": () => ({ status: 409, body: { error: "同じ語のキーワードがある" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "キーワードを作る" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: /^語/ }), "朝");
    await user.click(within(dialog).getByRole("button", { name: "作る" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("同じ語のキーワードがある");
  });

  it("有効／無効のスイッチで PUT し、消すと DELETE する", async () => {
    let list = [mukai];
    const calls = mockApi({
      ...baseRoutes,
      "GET /api/keywords": () => ({ status: 200, body: { keywords: list } }),
      "PUT /api/keywords/1": () => {
        list = [{ ...mukai, enabled: false }];
        return { status: 200, body: list[0] };
      },
      "DELETE /api/keywords/1": () => {
        list = [];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: "向井 ゲスト を有効にする" }));
    expect(await screen.findByText("無効")).toBeInTheDocument();
    expect(calls.find((c) => c.key === "PUT /api/keywords/1")?.body).toEqual({ words: "向井 ゲスト", exclude: "再放送", stations: ["TBS"], enabled: false });

    await user.click(screen.getByRole("button", { name: "向井 ゲスト を消す" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("足した 1 回の予約と、作った番組は残す");
    await user.click(within(dialog).getByRole("button", { name: "消す" }));
    expect(await screen.findByText("キーワードがまだ無い。")).toBeInTheDocument();
    expect(calls.some((c) => c.key === "DELETE /api/keywords/1")).toBe(true);
  });

  it("有効／無効を変えられなかったら、エラーを出し、スイッチは元のまま", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([mukai]),
      "PUT /api/keywords/1": () => ({ status: 500, body: { error: "保存できなかった" } }),
    });
    const user = userEvent.setup();
    renderPage();
    const sw = await screen.findByRole("switch", { name: "向井 ゲスト を有効にする" });
    await user.click(sw);
    expect(await screen.findByRole("alert")).toHaveTextContent("保存できなかった");
    expect(screen.getByRole("switch", { name: "向井 ゲスト を有効にする" })).toBeChecked();
  });

  it("消せなかったら、ダイアログを閉じてエラーを出し、キーワードは一覧に残る", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([mukai]),
      "DELETE /api/keywords/1": () => ({ status: 404, body: { error: "キーワードが無い" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "向井 ゲスト を消す" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "消す" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("キーワードが無い");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("keyword")).toBeInTheDocument();
  });

  it("足した放送回を、番組名・放送日の時刻と局・足した先の番組・状態つきで出し、番組を開ける", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([mukai]),
      "GET /api/keywords/1/airings": () => ({
        status: 200,
        body: {
          airings: [
            { stationId: "LFR", stationName: "ニッポン放送", start: "2026-10-21T01:00:00+09:00", end: "2026-10-21T02:00:00+09:00", title: "深夜の回", showName: "深夜の番組", state: "planned" },
            { stationId: "TBS", stationName: "TBSラジオ", start: "2026-10-18T13:00:00+09:00", end: "2026-10-18T14:00:00+09:00", title: "ゲスト回", showName: "ゲスト番組", state: "done" },
            { stationId: "TBS", stationName: "TBSラジオ", start: "2026-10-17T13:00:00+09:00", end: "2026-10-17T14:00:00+09:00", title: "外された回", showName: "ゲスト番組", state: "removed" },
          ],
        },
      }),
    });
    const onOpenShow = vi.fn();
    const user = userEvent.setup();
    renderPage({ onOpenShow });
    await user.click(await screen.findByRole("button", { name: "向井 ゲスト の足した放送回" }));
    const items = await screen.findAllByTestId("added-airing");
    expect(items).toHaveLength(3);
    expect(items[0]).toHaveTextContent("深夜の回");
    expect(items[0]).toHaveTextContent("25:00〜26:00 ニッポン放送");
    expect(within(items[0]).getByText("録音予定")).toBeInTheDocument();
    expect(within(items[1]).getByText("録音済み")).toBeInTheDocument();
    expect(within(items[2]).getByText("予約が外された")).toBeInTheDocument();
    await user.click(within(items[0]).getByRole("button", { name: "番組「深夜の番組」を開く" }));
    expect(onOpenShow).toHaveBeenCalledWith("深夜の番組");
  });

  it("足した放送回の録らない予定と録らなかったを出し分ける", async () => {
    mockApi({
      ...baseRoutes,
      ...listRoute([mukai]),
      "GET /api/keywords/1/airings": () => ({
        status: 200,
        body: {
          airings: [
            { stationId: "TBS", stationName: "TBSラジオ", start: "2026-10-21T13:00:00+09:00", end: "2026-10-21T14:00:00+09:00", title: "止めた回", showName: "ゲスト番組", state: "skip_planned" },
            { stationId: "TBS", stationName: "TBSラジオ", start: "2026-10-18T13:00:00+09:00", end: "2026-10-18T14:00:00+09:00", title: "録らなかった回", showName: "ゲスト番組", state: "skipped" },
          ],
        },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "向井 ゲスト の足した放送回" }));
    const items = await screen.findAllByTestId("added-airing");
    expect(within(items[0]).getByText("録らない予定")).toBeInTheDocument();
    expect(within(items[1]).getByText("録らなかった")).toBeInTheDocument();
  });

  it("一覧を読めなければエラーを出す", async () => {
    mockApi({ ...baseRoutes, "GET /api/keywords": () => ({ status: 500, body: { error: "内部エラー" } }) });
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("内部エラー");
  });
});
