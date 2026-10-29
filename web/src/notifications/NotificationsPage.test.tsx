import { MantineProvider } from "@mantine/core";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NotificationsPage } from "./NotificationsPage";
import type { AppNotification } from "./notificationsApi";

const failed: AppNotification = {
  id: 2,
  kind: "expired",
  message: "【録音できなかった】番組A・放送日 2026-10-14\n番組名の食い違い: …",
  createdAt: "2026-10-15T03:00:00Z",
  failed: true,
  read: false,
};
const sent: AppNotification = {
  id: 1,
  kind: "recorded",
  message: "【録音済み】番組B",
  createdAt: "2026-10-14T03:00:00Z",
  failed: false,
  read: true,
};

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

const renderPage = (onRead?: () => void) =>
  render(
    <MantineProvider>
      <NotificationsPage onRead={onRead} />
    </MantineProvider>,
  );

afterEach(() => vi.unstubAllGlobals());

describe("NotificationsPage", () => {
  it("通知を新しい順に、種類・本文・作った時刻・送れなかった印・未読の印付きで出し、開いたら既読にする", async () => {
    const calls = mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [failed, sent] } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 1 } }),
    });
    const onRead = vi.fn();
    renderPage(onRead);
    const items = await screen.findAllByTestId("notification");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("録音できなかった")).toBeInTheDocument();
    expect(within(items[0]).getByText("送れなかった")).toBeInTheDocument();
    expect(within(items[0]).getByText("未読")).toBeInTheDocument();
    expect(within(items[0]).getByText("10/15 12:00")).toBeInTheDocument();
    expect(within(items[0]).getByText(/番組名の食い違い/)).toBeInTheDocument();
    expect(within(items[1]).getByText("録音済み")).toBeInTheDocument();
    expect(within(items[1]).queryByText("送れなかった")).not.toBeInTheDocument();
    expect(within(items[1]).queryByText("未読")).not.toBeInTheDocument();
    await waitFor(() => expect(onRead).toHaveBeenCalledTimes(1));
    expect(calls.filter((c) => c !== "GET /api/notifications/status")).toEqual(["GET /api/notifications", "POST /api/notifications/read"]);
  });

  it("キーワードの出来事の 2 種類を、それぞれの言葉で出す", async () => {
    mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({
        status: 200,
        body: {
          notifications: [
            { ...sent, id: 4, kind: "keyword_limit", message: "【キーワードの上限】向井", read: true },
            { ...sent, id: 3, kind: "keyword_added", message: "【キーワードで録音予定】向井", read: true },
          ],
        },
      }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
    });
    renderPage();
    const items = await screen.findAllByTestId("notification");
    expect(within(items[0]).getByText("キーワードの上限で止めた")).toBeInTheDocument();
    expect(within(items[1]).getByText("キーワードで録音予定にした")).toBeInTheDocument();
  });

  it("番組の出来事（当たらない日）を、その言葉で出す", async () => {
    mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({
        status: 200,
        body: { notifications: [{ ...sent, id: 5, kind: "unmatched_day", message: "【当たらない日】番組「チャリで30分」", read: true }] },
      }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
    });
    renderPage();
    const items = await screen.findAllByTestId("notification");
    expect(within(items[0]).getByText("当たらない日")).toBeInTheDocument();
  });

  it("スリープから起こせない・再スキャンの失敗の異常を、それぞれの言葉で出す", async () => {
    mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({
        status: 200,
        body: {
          notifications: [
            { ...sent, id: 7, kind: "rescan", message: "【再スキャンの失敗】ライブラリの再スキャンを頼めなかった。", read: true },
            { ...sent, id: 6, kind: "wake", message: "【スリープから起こせない】ライブ録音の前にスリープ中の PC を起こせない見込み。", read: true },
          ],
        },
      }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
    });
    renderPage();
    const items = await screen.findAllByTestId("notification");
    expect(within(items[0]).getByText("再スキャンの失敗")).toBeInTheDocument();
    expect(within(items[1]).getByText("スリープから起こせない")).toBeInTheDocument();
  });

  it("通知が無ければそう出す", async () => {
    mockApi({
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [] } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
    });
    renderPage();
    expect(await screen.findByText("通知は無い。")).toBeInTheDocument();
  });

  it("一覧を読めなければエラーを出し、既読にしない", async () => {
    const calls = mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const onRead = vi.fn();
    renderPage(onRead);
    expect(await screen.findByRole("alert")).toHaveTextContent("内部エラー");
    expect(calls).not.toContain("POST /api/notifications/read");
    expect(onRead).not.toHaveBeenCalled();
  });

  it("既読にできなければ、一覧は出したままエラーを出す", async () => {
    mockApi({
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [sent] } }),
      "POST /api/notifications/read": () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const onRead = vi.fn();
    renderPage(onRead);
    expect(await screen.findByRole("alert")).toHaveTextContent("通知を既読にできなかった");
    expect(screen.getAllByTestId("notification")).toHaveLength(1);
    expect(onRead).not.toHaveBeenCalled();
  });

  it("link のある通知に、指す画面を開くリンクを置く。ふつうに押すと onOpenLink を呼び、Ctrl+クリックはブラウザに任せる", async () => {
    mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({
        status: 200,
        body: {
          notifications: [
            { ...failed, id: 5, link: "/jobs/12" },
            { ...sent, id: 4, kind: "keyword_added", link: "/shows/a%2Fb" },
            { ...sent, id: 3, kind: "keyword_limit", link: "/keywords" },
            { ...sent, id: 2, kind: "sync" },
          ],
        },
      }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 1 } }),
    });
    const onOpenLink = vi.fn();
    const user = userEvent.setup();
    render(
      <MantineProvider>
        <NotificationsPage onOpenLink={onOpenLink} />
      </MantineProvider>,
    );
    const items = await screen.findAllByTestId("notification");
    const links = items.map((it) => within(it).queryAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")]));
    expect(links).toEqual([
      [["ジョブを開く", "/jobs/12"]],
      [["番組を開く", "/shows/a%2Fb"]],
      [["キーワードを開く", "/keywords"]],
      [],
    ]);
    await user.click(within(items[0]).getByRole("link", { name: "ジョブを開く" }));
    expect(onOpenLink).toHaveBeenCalledWith("/jobs/12");
    onOpenLink.mockClear();
    // jsdom はリンクで別の文書へ移れないので、Ctrl+クリックで onOpenLink を呼ばないことだけを確かめる
    // （ブラウザの既定の動き（別タブ）は document で止め、jsdom の「Not implemented」を出さない）
    const stop = (e: Event) => e.preventDefault();
    document.addEventListener("click", stop);
    try {
      await user.keyboard("{Control>}");
      await user.click(within(items[1]).getByRole("link", { name: "番組を開く" }));
      await user.keyboard("{/Control}");
    } finally {
      document.removeEventListener("click", stop);
    }
    expect(onOpenLink).not.toHaveBeenCalled();
  });
});

const statusRoute = (channel: string) => () => ({ status: 200, body: { unread: 0, failed: 0, anomalies: [], channel } });

const testRow: AppNotification = {
  id: 3,
  kind: "test",
  message: "【テスト】rajikura からのテスト通知。",
  createdAt: "2026-10-15T04:00:00Z",
  failed: false,
  read: true,
};

describe("テスト通知のカード", () => {
  it("通知先を出し、送れたら時刻と確かめる促しを出して、履歴と帯を読み直す（テスト通知が先頭に出る）", async () => {
    let list = [sent];
    let release: () => void = () => {};
    const calls = mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: list } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
      "POST /api/notifications/test": () => {
        list = [testRow, sent];
        return { status: 200, body: { sent: true, sentAt: "2026-10-15T04:00:00Z", notification: testRow } };
      },
    });
    // 送っている間の表示を見るため、POST /api/notifications/test の応答を止めておく。
    const f = globalThis.fetch as unknown as (url: string, init?: RequestInit) => Promise<Response>;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url === "/api/notifications/test") await new Promise<void>((r) => (release = r));
        return f(url, init);
      }),
    );
    const onRead = vi.fn();
    const user = userEvent.setup();
    renderPage(onRead);
    const card = await screen.findByTestId("test-notification");
    expect(card).toHaveTextContent("通知先（discord）に、テストの通知を 1 通送る。");
    await waitFor(() => expect(onRead).toHaveBeenCalledTimes(1));
    await user.click(within(card).getByRole("button", { name: "テスト通知を送る" }));
    const busy = await within(card).findByRole("button", { name: "送っている…" });
    expect(busy).toBeDisabled();
    release();
    expect(await within(card).findByText("送れた（10/15 13:00）。通知先に届いたかを確かめて。")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "テスト通知を送る" })).toBeEnabled();
    await waitFor(() => expect(screen.getAllByTestId("notification")).toHaveLength(2));
    const items = screen.getAllByTestId("notification");
    expect(within(items[0]).getByText("テスト")).toBeInTheDocument();
    expect(within(items[0]).queryByText("未読")).not.toBeInTheDocument();
    expect(onRead).toHaveBeenCalledTimes(2);
    expect(calls.filter((c) => c === "POST /api/notifications/test")).toHaveLength(1);
  });

  it("送れなければ通知先のエラーを出す", async () => {
    const failedRow = { ...testRow, failed: true };
    mockApi({
      "GET /api/notifications/status": statusRoute("slack"),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [] } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
      "POST /api/notifications/test": () => ({
        status: 200,
        body: { sent: false, error: "slack: 状態 404 が返った", notification: failedRow },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    const card = await screen.findByTestId("test-notification");
    await user.click(within(card).getByRole("button", { name: "テスト通知を送る" }));
    expect(await within(card).findByRole("alert")).toHaveTextContent("送れなかった: slack: 状態 404 が返った");
  });

  it.each([
    [409, "テスト通知を送る口がつながっていない"],
    [500, "内部エラー"],
  ])("API が %i を返したら、そのエラーを出し、履歴は読み直さない", async (status, error) => {
    const calls = mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [] } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
      "POST /api/notifications/test": () => ({ status, body: { error } }),
    });
    const user = userEvent.setup();
    renderPage();
    const card = await screen.findByTestId("test-notification");
    await user.click(within(card).getByRole("button", { name: "テスト通知を送る" }));
    expect(await within(card).findByRole("alert")).toHaveTextContent(`送れなかった: ${error}`);
    expect(within(card).getByRole("button", { name: "テスト通知を送る" })).toBeEnabled();
    expect(calls.filter((c) => c === "GET /api/notifications")).toHaveLength(1);
  });

  it("例外のメッセージが空でも、「送れなかった: 」だけにしない", async () => {
    mockApi({
      "GET /api/notifications/status": statusRoute("discord"),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [] } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
    });
    const f = globalThis.fetch as unknown as (url: string, init?: RequestInit) => Promise<Response>;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url === "/api/notifications/test") throw new Error("");
        return f(url, init);
      }),
    );
    const user = userEvent.setup();
    renderPage();
    const card = await screen.findByTestId("test-notification");
    await user.click(within(card).getByRole("button", { name: "テスト通知を送る" }));
    expect(await within(card).findByRole("alert")).toHaveTextContent("送れなかった: 原因は分からない");
  });

  it("通知先が none でもボタンを出し、押すと履歴に入れたと出して、履歴と帯を読み直す", async () => {
    let list: AppNotification[] = [];
    const calls = mockApi({
      "GET /api/notifications/status": statusRoute("none"),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: list } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
      "POST /api/notifications/test": () => {
        list = [testRow];
        return { status: 200, body: { sent: false, noChannel: true, notification: testRow } };
      },
    });
    const onRead = vi.fn();
    const user = userEvent.setup();
    renderPage(onRead);
    const card = await screen.findByTestId("test-notification");
    expect(card).toHaveTextContent('通知先が選ばれていない（notify.channel = "none"）。通知は Web UI の履歴にだけ残る。');
    await waitFor(() => expect(onRead).toHaveBeenCalledTimes(1));
    await user.click(within(card).getByRole("button", { name: "テスト通知を送る" }));
    expect(await within(card).findByRole("status")).toHaveTextContent("履歴に入れた。トレイには 1 分以内に届く。");
    expect(within(card).queryByRole("alert")).not.toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "テスト通知を送る" })).toBeEnabled();
    await waitFor(() => expect(screen.getAllByTestId("notification")).toHaveLength(1));
    expect(within(screen.getAllByTestId("notification")[0]).getByText("テスト")).toBeInTheDocument();
    expect(onRead).toHaveBeenCalledTimes(2);
    expect(calls.filter((c) => c === "POST /api/notifications/test")).toHaveLength(1);
  });
});
