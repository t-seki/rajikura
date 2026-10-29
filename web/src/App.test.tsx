import { MantineProvider } from "@mantine/core";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { broadcastDayOf } from "./guide/format";

type Handler = (init?: RequestInit) => { status: number; body?: unknown };

function mockApi(routes: Record<string, Handler>) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push(key);
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = h(init);
      return new Response(JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

// ログインした後の枠は、最初に番組表の画面を出す（今日の番組表を読む）。
const showsRoutes: Record<string, Handler> = {
  [`GET /api/guide?day=${broadcastDayOf(new Date())}`]: () => ({ status: 200, body: { stations: [] } }),
  "GET /api/shows": () => ({ status: 200, body: [] }),
  "GET /api/stations": () => ({ status: 200, body: [] }),
  "GET /api/notifications/status": () => ({ status: 200, body: { unread: 0, failed: 0, anomalies: [] } }),
};

const renderApp = () =>
  render(
    <MantineProvider>
      <App />
    </MantineProvider>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

describe("App", () => {
  it("未ログイン（401）ならログインの画面を出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 401 }) });
    renderApp();
    expect(await screen.findByLabelText(/パスワード/)).toBeInTheDocument();
  });

  it("/notifications を開くと通知の画面が出る", async () => {
    window.history.replaceState(null, "", "/notifications");
    mockApi({
      "GET /api/me": () => ({ status: 200 }),
      ...showsRoutes,
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [] } }),
      "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
    });
    renderApp();
    expect(await screen.findByText(/録音の結果・サービスの異常・テスト通知を新しい順に出す/)).toBeInTheDocument();
  });

  // トーストから開く URL の notification=<id>は、どの画面でも、その 1 件を既読にし、URL からそのキーだけを消す。
  it("URL に notification=<id> があれば、その 1 件を既読にして、そのキーだけを URL から消す（ほかのクエリは残す）", async () => {
    window.history.replaceState(null, "", "/keywords?notification=345&x=1");
    let unread = 2;
    const calls = mockApi({
      "GET /api/me": () => ({ status: 200 }),
      ...showsRoutes,
      "GET /api/notifications/status": () => ({ status: 200, body: { unread, failed: 0, anomalies: [] } }),
      "POST /api/notifications/345/read": () => {
        unread = 1;
        return { status: 204 };
      },
      "GET /api/keywords": () => ({ status: 200, body: { keywords: [] } }),
      "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
    });
    renderApp();
    await waitFor(() => expect(calls).toContain("POST /api/notifications/345/read"));
    expect(window.location.pathname + window.location.search).toBe("/keywords?x=1");
    expect(await screen.findByLabelText("未読の通知 1 件")).toBeInTheDocument();
    expect(calls.filter((c) => c === "POST /api/notifications/345/read")).toHaveLength(1);
  });

  it("notification の既読が失敗（404）しても、画面はふつうに出し、URL からキーを消し、console.warn だけ出す", async () => {
    window.history.replaceState(null, "", "/keywords?notification=999");
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const calls = mockApi({
      "GET /api/me": () => ({ status: 200 }),
      ...showsRoutes,
      "POST /api/notifications/999/read": () => ({ status: 404, body: { error: "通知が無い" } }),
      "GET /api/keywords": () => ({ status: 200, body: { keywords: [] } }),
      "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
    });
    renderApp();
    await waitFor(() => expect(warn).toHaveBeenCalled());
    expect(calls).toContain("POST /api/notifications/999/read");
    expect(window.location.pathname + window.location.search).toBe("/keywords");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "ナビゲーション" })).toBeInTheDocument();
    warn.mockRestore();
  });

  it("ログインしていなければ、ログインの後に notification を受ける（ログインの間もクエリは残る）", async () => {
    window.history.replaceState(null, "", "/keywords?notification=7");
    let loggedIn = false;
    const calls = mockApi({
      "GET /api/me": () => (loggedIn ? { status: 200 } : { status: 401 }),
      "POST /api/login": () => {
        loggedIn = true;
        return { status: 200, body: { state: "authenticated" } };
      },
      ...showsRoutes,
      "POST /api/notifications/7/read": () => ({ status: 204 }),
      "GET /api/keywords": () => ({ status: 200, body: { keywords: [] } }),
      "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "secret");
    await user.click(screen.getByRole("button", { name: /ログイン/ }));
    await waitFor(() => expect(calls).toContain("POST /api/notifications/7/read"));
    expect(window.location.pathname + window.location.search).toBe("/keywords");
  });

  it("/tray-link を開くと連携の画面が出る", async () => {
    window.history.replaceState(null, "", "/tray-link?port=5000&state=s&name=PC");
    mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes, "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }) });
    renderApp();
    expect(await screen.findByRole("button", { name: "このトレイと連携する" })).toBeInTheDocument();
  });

  it("ログインしていなければ、ログインの後に /tray-link の画面へ進む", async () => {
    window.history.replaceState(null, "", "/tray-link?port=5000&state=s&name=PC");
    mockApi({
      "GET /api/me": () => ({ status: 401 }),
      "POST /api/login": () => ({ status: 200, body: { state: "authenticated" } }),
      ...showsRoutes,
      "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
    });
    renderApp();
    await userEvent.type(await screen.findByLabelText(/パスワード/), "pw");
    await userEvent.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByRole("button", { name: "このトレイと連携する" })).toBeInTheDocument();
  });

  it("知らないパスは番組表を開き、URL を / に置き換える", async () => {
    window.history.replaceState(null, "", "/other?x=1");
    const before = window.history.length;
    mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes, "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }) });
    renderApp();
    await screen.findByRole("button", { name: "ログアウト" });
    expect(screen.queryByRole("button", { name: "このトレイと連携する" })).not.toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "番組表" })).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname + window.location.search).toBe("/"));
    expect(window.history.length).toBe(before);
  });

  it("ログインの画面に、飾りのアイコンと rajikura の文字を出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 401 }) });
    const { container } = renderApp();
    await screen.findByLabelText(/パスワード/);
    expect(screen.getByRole("heading", { name: "rajikura" })).toBeInTheDocument();
    const img = container.querySelector('img[src="/favicon.svg"]');
    expect(img).not.toBeNull();
    expect(img).toHaveAttribute("alt", "");
  });

  it("ログイン済みのヘッダーに、飾りのアイコンと rajikura の文字を出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes });
    const { container } = renderApp();
    await screen.findByRole("button", { name: "ログアウト" });
    expect(screen.getByRole("heading", { name: "rajikura" })).toBeInTheDocument();
    const img = container.querySelector('img[src="/favicon.svg"]');
    expect(img).not.toBeNull();
    expect(img).toHaveAttribute("alt", "");
  });

  it("パスワードが無い（503）なら、セットアップコードを入れてパスワードを決める画面だけを出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 503, body: { setup: true, locked: false } }) });
    renderApp();
    expect(await screen.findByLabelText(/セットアップコード/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "パスワードを決める" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "ログイン" })).not.toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });

  it("セットアップコードでパスワードを決めると、ログインした枠に進み、版を読む", async () => {
    let sent: unknown;
    let meCalls = 0;
    mockApi({
      "GET /api/me": () => {
        meCalls++;
        return meCalls === 1
          ? { status: 503, body: { setup: true, locked: false } }
          : { status: 200, body: { authenticated: true, version: "v1.2.3" } };
      },
      "POST /api/setup": (init) => {
        sent = JSON.parse(String(init?.body));
        return { status: 200, body: { authenticated: true } };
      },
      ...showsRoutes,
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/セットアップコード/), "ABCD-EFGH");
    const [pw, again] = screen.getAllByLabelText(/新しいパスワード/);
    await user.type(pw, "secret");
    await user.type(again, "secret");
    await user.click(screen.getByRole("button", { name: "パスワードを決める" }));
    expect(await screen.findByLabelText("rajikura の版")).toHaveTextContent("v1.2.3");
    expect(screen.getByRole("button", { name: "ログアウト" })).toBeInTheDocument();
    expect(sent).toEqual({ code: "ABCD-EFGH", password: "secret" });
  });

  it("新しいパスワードを 2 回とも同じに入れないと、送らない", async () => {
    const calls = mockApi({ "GET /api/me": () => ({ status: 503, body: { setup: true, locked: false } }) });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/セットアップコード/), "ABCD-EFGH");
    const [pw, again] = screen.getAllByLabelText(/新しいパスワード/);
    await user.type(pw, "secret");
    await user.type(again, "other");
    await user.click(screen.getByRole("button", { name: "パスワードを決める" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("2 つのパスワードが違う");
    expect(calls).not.toContain("POST /api/setup");
  });

  it("セットアップコードが違えば、あと何回かを出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 503, body: { setup: true, locked: false } }),
      "POST /api/setup": () => ({ status: 401, body: { error: "セットアップコードが違う", remaining: 3, locked: false } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/セットアップコード/), "WRONG");
    const [pw, again] = screen.getAllByLabelText(/新しいパスワード/);
    await user.type(pw, "secret");
    await user.type(again, "secret");
    await user.click(screen.getByRole("button", { name: "パスワードを決める" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("セットアップコードが違う（あと 3 回）");
  });

  it("5 回続けて間違えたら、起動し直すまで受け付けないと出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 503, body: { setup: true, locked: false } }),
      "POST /api/setup": () => ({ status: 401, body: { error: "セットアップコードが違う", remaining: 0, locked: true } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/セットアップコード/), "WRONG");
    const [pw, again] = screen.getAllByLabelText(/新しいパスワード/);
    await user.type(pw, "secret");
    await user.type(again, "secret");
    await user.click(screen.getByRole("button", { name: "パスワードを決める" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("起動し直すと");
    expect(screen.queryByLabelText(/セットアップコード/)).not.toBeInTheDocument();
  });

  it("パスワードを決めるときに通信に失敗したら、通信を確かめるよう出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 503, body: { setup: true, locked: false } }),
      "POST /api/setup": () => {
        throw new TypeError("Failed to fetch");
      },
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/セットアップコード/), "ABCD-EFGH");
    const [pw, again] = screen.getAllByLabelText(/新しいパスワード/);
    await user.type(pw, "secret");
    await user.type(again, "secret");
    await user.click(screen.getByRole("button", { name: "パスワードを決める" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("パスワードを決められなかった。通信を確かめてほしい");
    expect(screen.getByLabelText(/セットアップコード/)).toBeInTheDocument();
  });

  it("パスワードを決めるときに API が想定外のエラー（500）を返したら、決められなかったと出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 503, body: { setup: true, locked: false } }),
      "POST /api/setup": () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/セットアップコード/), "ABCD-EFGH");
    const [pw, again] = screen.getAllByLabelText(/新しいパスワード/);
    await user.type(pw, "secret");
    await user.type(again, "secret");
    await user.click(screen.getByRole("button", { name: "パスワードを決める" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("パスワードを決められなかった");
  });

  it("開いたときにもう締めていれば（locked）、入れる欄を出さない", async () => {
    mockApi({ "GET /api/me": () => ({ status: 503, body: { setup: true, locked: true } }) });
    renderApp();
    expect(await screen.findByRole("alert")).toHaveTextContent("5 回続けて間違えた");
    expect(screen.queryByLabelText(/セットアップコード/)).not.toBeInTheDocument();
  });

  it("ログイン済みなら枠（ナビゲーションとログアウト）を出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes });
    renderApp();
    expect(await screen.findByRole("button", { name: "ログアウト" })).toBeInTheDocument();
    // ナビは「番組表」「番組」「キーワード」「ジョブ」「通知」「設定」「API トークン」の順
    expect(screen.getByRole("navigation", { name: "ナビゲーション" }).textContent).toBe("番組表番組キーワードジョブ通知設定API トークン");
    // 最初に開くのは番組表
    expect(await screen.findByRole("heading", { name: "番組表" })).toBeInTheDocument();
  });

  describe("ヘッダーの「パスワードを変える」", () => {
    async function openAndFill(current: string, password: string, again: string) {
      const user = userEvent.setup();
      renderApp();
      await user.click(await screen.findByRole("button", { name: "パスワードを変える" }));
      const dialog = await screen.findByRole("dialog", { name: "パスワードを変える" });
      await user.type(within(dialog).getByLabelText(/今のパスワード/), current);
      const [pw, confirm] = within(dialog).getAllByLabelText(/新しいパスワード/);
      await user.type(pw, password);
      await user.type(confirm, again);
      await user.click(within(dialog).getByRole("button", { name: "パスワードを変える" }));
      return dialog;
    }

    it("ログアウトの横のボタンからダイアログを開いて、パスワードを変えられる", async () => {
      let sent: unknown;
      mockApi({
        "GET /api/me": () => ({ status: 200 }),
        ...showsRoutes,
        "PUT /api/password": (init) => {
          sent = JSON.parse(String(init?.body));
          return { status: 200, body: { changed: true } };
        },
      });
      const dialog = await openAndFill("old", "new", "new");
      expect(await within(dialog).findByRole("status")).toHaveTextContent("パスワードを変えた");
      expect(sent).toEqual({ current: "old", password: "new" });
      // ナビの項目にはしない
      expect(screen.getByRole("navigation", { name: "ナビゲーション" }).textContent).not.toContain("パスワード");
    });

    it("今のパスワードが違えば（403）、ダイアログに理由を出す", async () => {
      mockApi({
        "GET /api/me": () => ({ status: 200 }),
        ...showsRoutes,
        "PUT /api/password": () => ({ status: 403, body: { error: "今のパスワードが違う" } }),
      });
      const dialog = await openAndFill("bad", "new", "new");
      expect(await within(dialog).findByRole("alert")).toHaveTextContent("今のパスワードが違う");
    });

    it("通信に失敗したら、ダイアログに通信を確かめるよう出す", async () => {
      mockApi({
        "GET /api/me": () => ({ status: 200 }),
        ...showsRoutes,
        "PUT /api/password": () => {
          throw new TypeError("Failed to fetch");
        },
      });
      const dialog = await openAndFill("old", "new", "new");
      expect(await within(dialog).findByRole("alert")).toHaveTextContent("パスワードを変えられなかった。通信を確かめてほしい");
    });
  });

  it("ログイン済みなら、ナビに /api/me の版を出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 200, body: { authenticated: true, version: "v1.2.3" } }), ...showsRoutes });
    renderApp();
    expect(await screen.findByLabelText("rajikura の版")).toHaveTextContent("v1.2.3");
  });

  it("未ログインの画面には版を出さない", async () => {
    mockApi({ "GET /api/me": () => ({ status: 401, body: { version: "v1.2.3" } }) });
    renderApp();
    await screen.findByLabelText(/パスワード/);
    expect(screen.queryByText("v1.2.3")).not.toBeInTheDocument();
  });

  it("ナビで「番組」を開くと番組の一覧を出す", async () => {
    mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes });
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByText("番組"));
    expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
  });

  it("保存先が決まっていなければ「保存先を決めて」の帯を出し、押すと設定の画面を開く", async () => {
    const view = { settings: { library: "" }, secrets: {}, library_set: false, unreadable_secrets: ["jellyfin.api_key"], last_backup_at: null };
    mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes, "GET /api/settings": () => ({ status: 200, body: view }) });
    const user = userEvent.setup();
    renderApp();
    expect(await screen.findByText("保存先を決めて")).toBeInTheDocument();
    expect(screen.getByText("秘密を入れ直して")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "設定を開く" })[0]);
    expect(await screen.findByRole("region", { name: "保存先" })).toBeInTheDocument();
  });

  it("未読の通知があればナビの「通知」に数を出し、履歴を開くと既読にして数を消す", async () => {
    let unread = 3;
    const calls = mockApi({
      "GET /api/me": () => ({ status: 200 }),
      ...showsRoutes,
      "GET /api/notifications/status": () => ({ status: 200, body: { unread, failed: 0, anomalies: [] } }),
      "GET /api/notifications": () => ({
        status: 200,
        body: {
          notifications: [
            { id: 1, kind: "expired", message: "【録音できなかった】x", createdAt: "2026-10-15T03:00:00Z", failed: false, read: false },
          ],
        },
      }),
      "POST /api/notifications/read": () => {
        unread = 0;
        return { status: 200, body: { marked: 3 } };
      },
    });
    const user = userEvent.setup();
    renderApp();
    expect(await screen.findByLabelText("未読の通知 3 件")).toHaveTextContent("3");
    await user.click(screen.getByText("通知"));
    expect(await screen.findByText("【録音できなかった】x")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByLabelText(/未読の通知/)).not.toBeInTheDocument());
    expect(calls).toContain("POST /api/notifications/read");
  });

  it("まだ復旧していない異常と送れなかった通知を、どのページにも帯で出し、帯から履歴を開ける", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 200 }),
      ...showsRoutes,
      "GET /api/notifications/status": () => ({
        status: 200,
        body: {
          unread: 0,
          failed: 2,
          anomalies: [{ kind: "sync", message: "【番組表の同期の失敗】y", since: "2026-10-15T03:00:00Z" }],
        },
      }),
      "GET /api/notifications": () => ({ status: 200, body: { notifications: [] } }),
      "POST /api/notifications/read": () => ({ status: 200, body: { marked: 0 } }),
    });
    const user = userEvent.setup();
    renderApp();
    const banners = await screen.findByTestId("notification-banners");
    expect(banners).toHaveTextContent("【番組表の同期の失敗】y");
    expect(banners).toHaveTextContent("10/15 12:00 から");
    expect(banners).toHaveTextContent("2 件の通知を通知先に送れなかった");
    // ほかのページに移っても出る
    await user.click(screen.getByText("番組"));
    expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
    expect(screen.getByTestId("notification-banners")).toBeInTheDocument();
    // 帯のリンクで履歴を開く
    await user.click(screen.getByRole("button", { name: "通知の履歴を開く" }));
    expect(await screen.findByText("通知は無い。")).toBeInTheDocument();
    // 履歴を開いて既読にしても、帯は消えない
    expect(screen.getByTestId("notification-banners")).toBeInTheDocument();
  });

  it("帯の元が無ければ（異常も送れなかった通知も無い）帯を出さない", async () => {
    const calls = mockApi({ "GET /api/me": () => ({ status: 200 }), ...showsRoutes });
    renderApp();
    expect(await screen.findByRole("heading", { name: "番組表" })).toBeInTheDocument();
    await waitFor(() => expect(calls).toContain("GET /api/notifications/status"));
    expect(screen.queryByTestId("notification-banners")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/未読の通知/)).not.toBeInTheDocument();
  });

  it("ログインしてログアウトできる", async () => {
    let sent: unknown;
    const calls = mockApi({
      "GET /api/me": () => ({ status: 401 }),
      "POST /api/login": (init) => {
        sent = JSON.parse(String(init?.body));
        return { status: 200, body: { authenticated: true } };
      },
      "POST /api/logout": () => ({ status: 200 }),
      ...showsRoutes,
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "secret");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    await user.click(await screen.findByRole("button", { name: "ログアウト" }));
    expect(await screen.findByRole("button", { name: "ログイン" })).toBeInTheDocument();
    expect(sent).toEqual({ password: "secret" });
    expect(calls).toContain("POST /api/logout");
  });

  it("ログインの後は /api/me を読み直して、ナビに版を出す", async () => {
    let meCalls = 0;
    mockApi({
      "GET /api/me": () => {
        meCalls++;
        return meCalls === 1
          ? { status: 401 }
          : { status: 200, body: { authenticated: true, version: "v1.2.3" } };
      },
      "POST /api/login": () => ({ status: 200, body: { authenticated: true } }),
      ...showsRoutes,
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "secret");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByLabelText("rajikura の版")).toHaveTextContent("v1.2.3");
  });

  it("ログインの後の /api/me の読み直しが失敗しても、ナビは出て版だけが出ない", async () => {
    let meCalls = 0;
    mockApi({
      "GET /api/me": () => {
        meCalls++;
        return { status: meCalls === 1 ? 401 : 500 };
      },
      "POST /api/login": () => ({ status: 200, body: { authenticated: true } }),
      ...showsRoutes,
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "secret");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByRole("button", { name: "ログアウト" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "ナビゲーション" })).toBeInTheDocument();
    await waitFor(() => expect(meCalls).toBe(2));
    expect(screen.queryByLabelText("rajikura の版")).not.toBeInTheDocument();
  });

  it("ログアウトに失敗したら、ログイン済みのまま、エラーを出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 200 }),
      "POST /api/logout": () => ({ status: 500 }),
      ...showsRoutes,
    });
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByRole("button", { name: "ログアウト" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("ログアウトできなかった");
    expect(screen.getByRole("button", { name: "ログアウト" })).toBeInTheDocument();
  });

  it("ログインのときに 503 なら、パスワードを決める画面に移る", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 401 }),
      "POST /api/login": () => ({ status: 503, body: { setup: true, locked: false } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "x");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByLabelText(/セットアップコード/)).toBeInTheDocument();
  });

  it("続けて間違えて受け付けを止めているとき（429）は、あと何秒待てばよいかを出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 401 }),
      "POST /api/login": () => ({ status: 429, body: { error: "止めている", retry_after: 42 } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "secret");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("あと 42 秒待ってほしい");
    expect(screen.getByRole("button", { name: "ログイン" })).toBeInTheDocument();
  });

  it("パスワードが違えば（401）エラーを出す", async () => {
    mockApi({
      "GET /api/me": () => ({ status: 401 }),
      "POST /api/login": () => ({ status: 401, body: { error: "パスワードが違う" } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "bad");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("パスワードが違う");
  });
});

// 画面ごとの URL（ADR 0024）。ジョブの詳細は /jobs/{id}、番組の編集は /shows/{名前}。
describe("画面ごとの URL", () => {
  const job = {
    id: 5,
    showName: "番組A",
    day: "2026-10-11",
    part: 0,
    state: "done",
    attempts: 1,
    nextAttemptAt: "2026-10-12T05:00:00Z",
    deadline: "2026-10-18T05:00:00Z",
    retryable: false,
    settled: true,
    byPerson: false,
    live: false,
    skippable: false,
    recordable: false,
    outputPath: "",
    missingCount: 0,
    lastFailedStage: "",
    lastError: "",
    stations: [{ stationId: "TBS", start: "2026-10-11T16:00:00Z", end: "2026-10-11T18:00:00Z" }],
  };
  const jobDetail = { ...job, durationSeconds: 0, airings: [], attemptLog: [] };
  const show = { name: "朝の/番組", enabled: true, split: "none", check_title: true, weekly: [{ station: "TBS", weekday: 1, start: "07:00" }] };
  const enc = encodeURIComponent;
  const routes: Record<string, Handler> = {
    "GET /api/me": () => ({ status: 200 }),
    ...showsRoutes,
    "GET /api/shows": () => ({ status: 200, body: [show] }),
    [`GET /api/shows/${enc(show.name)}`]: () => ({ status: 200, body: { ...show, onces: [] } }),
    "GET /api/settings": () => ({ status: 200, body: { settings: {}, secrets: {} } }),
    "GET /api/jobs": () => ({ status: 200, body: { jobs: [job] } }),
    "GET /api/jobs/5": () => ({ status: 200, body: jobDetail }),
    "GET /api/jobs/999": () => ({ status: 404, body: { error: "ジョブが無い" } }),
  };
  const here = () => window.location.pathname + window.location.search;
  const nav = () => within(screen.getByRole("navigation", { name: "ナビゲーション" }));

  it("ナビで画面を切り替えると URL が変わり、「戻る」「進む」で前の画面に移る", async () => {
    mockApi(routes);
    const user = userEvent.setup();
    renderApp();
    await screen.findByRole("heading", { name: "番組表" });
    // ナビの項目は、新しいタブで開けるリンク
    expect(nav().getByRole("link", { name: "ジョブ" })).toHaveAttribute("href", "/jobs");
    await user.click(nav().getByRole("link", { name: "番組" }));
    expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
    expect(here()).toBe("/shows");

    window.history.back();
    expect(await screen.findByRole("heading", { name: "番組表" })).toBeInTheDocument();
    expect(here()).toBe("/");

    window.history.forward();
    expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
    expect(here()).toBe("/shows");
  });

  it("/jobs/{id} を直接開くとジョブの詳細が開き、再読み込みしても同じ詳細が開く", async () => {
    window.history.replaceState(null, "", "/jobs/5");
    mockApi(routes);
    const first = renderApp();
    expect(await screen.findByRole("dialog", { name: "番組A 2026-10-11" })).toBeInTheDocument();
    first.unmount();
    renderApp();
    expect(await screen.findByRole("dialog", { name: "番組A 2026-10-11" })).toBeInTheDocument();
    expect(here()).toBe("/jobs/5");
  });

  it("ジョブの一覧で詳細を開くと URL が /jobs/{id} になり、「戻る」で詳細を閉じる", async () => {
    window.history.replaceState(null, "", "/jobs");
    mockApi(routes);
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    expect(await screen.findByRole("dialog", { name: "番組A 2026-10-11" })).toBeInTheDocument();
    expect(here()).toBe("/jobs/5");

    window.history.back();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(here()).toBe("/jobs");
    expect(screen.getByRole("button", { name: "番組Aの詳細" })).toBeInTheDocument();
  });

  it("一覧から開いた詳細を閉じると、開く前の履歴に戻る（同じ URL の履歴を積まない）", async () => {
    window.history.replaceState(null, "", "/jobs");
    mockApi(routes);
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await screen.findByRole("dialog", { name: "番組A 2026-10-11" });
    const pushed = window.history.length;
    await user.keyboard("{Escape}");
    await waitFor(() => expect(here()).toBe("/jobs"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(window.history.length).toBe(pushed);
    // 「進む」で詳細を開き直せる（戻ったので、閉じた詳細が次の履歴に残っている）
    window.history.forward();
    expect(await screen.findByRole("dialog", { name: "番組A 2026-10-11" })).toBeInTheDocument();
  });

  it("無いジョブ・数でない id の URL は、ジョブの一覧を開いて URL を /jobs に置き換える", async () => {
    for (const path of ["/jobs/999", "/jobs/abc"]) {
      window.history.replaceState(null, "", path);
      const before = window.history.length;
      mockApi(routes);
      const r = renderApp();
      await waitFor(() => expect(here()).toBe("/jobs"));
      expect(await screen.findByRole("button", { name: "番組Aの詳細" })).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(window.history.length).toBe(before);
      r.unmount();
    }
  });

  it("一覧から開いた詳細のジョブが無い（404）ときは、戻らずに URL を /jobs に置き換える", async () => {
    // 一覧の履歴に印を付けておき、戻った（印のある履歴に移った）のか、置き換えた（印の無い履歴）のかを見分ける
    window.history.replaceState({ marker: "list" }, "", "/jobs");
    mockApi({ ...routes, "GET /api/jobs/5": () => ({ status: 404, body: { error: "ジョブが無い" } }) });
    const user = userEvent.setup();
    renderApp();
    const before = window.history.length;
    await user.click(await screen.findByRole("button", { name: "番組Aの詳細" }));
    await waitFor(() => expect(here()).toBe("/jobs"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // 積んだ /jobs/5 の履歴を /jobs に置き換えた（戻っていない）
    expect(window.history.length).toBe(before + 1);
    expect(window.history.state).toBeNull();
  });

  it("無いジョブの /jobs/{id}?notification=N を開くと、一覧に置き換わっても、既読の POST は 1 回で、URL から notification が消える", async () => {
    window.history.replaceState(null, "", "/jobs/999?notification=42");
    const calls = mockApi({ ...routes, "POST /api/notifications/42/read": () => ({ status: 204 }) });
    renderApp();
    await waitFor(() => expect(here()).toBe("/jobs"));
    await waitFor(() => expect(calls).toContain("POST /api/notifications/42/read"));
    expect(calls.filter((c) => c === "POST /api/notifications/42/read")).toHaveLength(1);
    expect(here()).toBe("/jobs");
  });

  it("/jobs/{id} のジョブを 404 でなく読めない（5xx）ときは、詳細にエラーを出し、URL はそのまま", async () => {
    window.history.replaceState(null, "", "/jobs/5");
    mockApi({ ...routes, "GET /api/jobs/5": () => ({ status: 500, body: { error: "内部エラー" } }) });
    renderApp();
    const dialog = await screen.findByRole("dialog", { name: "ジョブ" });
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("内部エラー");
    // 少し待っても、一覧の URL に置き換えない
    await new Promise((r) => setTimeout(r, 100));
    expect(here()).toBe("/jobs/5");
    expect(screen.getByRole("dialog", { name: "ジョブ" })).toBeInTheDocument();
  });

  it("/shows/{名前} で番組の一覧を読めないときは、エラーを出して編集を開かず、URL はそのまま", async () => {
    window.history.replaceState(null, "", `/shows/${enc(show.name)}`);
    mockApi({ ...routes, "GET /api/shows": () => ({ status: 500, body: { error: "内部エラー" } }) });
    renderApp();
    expect(await screen.findByRole("alert")).toHaveTextContent("番組の一覧を読めない");
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByRole("dialog", { name: "番組を編集する" })).toBeNull();
    expect(here()).toBe(`/shows/${enc(show.name)}`);
  });

  it("/shows/{名前} を直接開くと番組の編集が開く（名前の / もエンコードして渡す）", async () => {
    window.history.replaceState(null, "", `/shows/${enc(show.name)}`);
    mockApi(routes);
    renderApp();
    const dialog = await screen.findByRole("dialog", { name: "番組を編集する" });
    expect(within(dialog).getByRole("textbox", { name: /番組の名前/ })).toHaveValue(show.name);
  });

  it("番組の一覧で編集を開くと URL が /shows/{名前} になり、「戻る」で編集を閉じる", async () => {
    window.history.replaceState(null, "", "/shows");
    mockApi(routes);
    const user = userEvent.setup();
    renderApp();
    await user.click(await screen.findByRole("button", { name: `${show.name} を編集` }));
    expect(await screen.findByRole("dialog", { name: "番組を編集する" })).toBeInTheDocument();
    expect(here()).toBe(`/shows/${enc(show.name)}`);

    window.history.back();
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "番組を編集する" })).toBeNull());
    expect(here()).toBe("/shows");
  });

  it("無い番組の URL は、番組の一覧に見つからないと出し、URL を /shows に置き換える", async () => {
    window.history.replaceState(null, "", `/shows/${enc("無い番組")}`);
    const before = window.history.length;
    mockApi(routes);
    renderApp();
    expect(await screen.findByRole("alert")).toHaveTextContent("番組「無い番組」が見つからない");
    await waitFor(() => expect(here()).toBe("/shows"));
    expect(screen.queryByRole("dialog", { name: "番組を編集する" })).toBeNull();
    expect(window.history.length).toBe(before);
  });

  // 通知が指す画面へのリンク。link は server が組み立てる（番組の名前はパスの 1 区切りとしてエスケープ済み）。
  const notificationRoutes: Record<string, Handler> = {
    ...routes,
    "GET /api/notifications": () => ({
      status: 200,
      body: {
        notifications: [
          { id: 3, kind: "expired", message: "【録音できなかった】番組A", createdAt: "2026-10-15T03:00:00Z", failed: false, read: false, link: "/jobs/5" },
          { id: 2, kind: "unmatched_day", message: "【当たらない日】朝の/番組", createdAt: "2026-10-15T02:00:00Z", failed: false, read: false, link: `/shows/${enc(show.name)}` },
          { id: 1, kind: "keyword_limit", message: "【キーワードの上限】", createdAt: "2026-10-15T01:00:00Z", failed: false, read: false, link: "/keywords" },
        ],
      },
    }),
    "POST /api/notifications/read": () => ({ status: 200, body: { marked: 3 } }),
  };

  it("通知の画面で「ジョブを開く」を押すとジョブの詳細に移り（履歴に積む）、「戻る」で通知の画面に戻る", async () => {
    window.history.replaceState(null, "", "/notifications");
    mockApi(notificationRoutes);
    const user = userEvent.setup();
    renderApp();
    const items = await screen.findAllByTestId("notification");
    expect(within(items[0]).getByRole("link", { name: "ジョブを開く" })).toHaveAttribute("href", "/jobs/5");
    expect(within(items[1]).getByRole("link", { name: "番組を開く" })).toHaveAttribute("href", `/shows/${enc(show.name)}`);
    expect(within(items[2]).getByRole("link", { name: "キーワードを開く" })).toHaveAttribute("href", "/keywords");

    await user.click(within(items[0]).getByRole("link", { name: "ジョブを開く" }));
    expect(await screen.findByRole("dialog", { name: "番組A 2026-10-11" })).toBeInTheDocument();
    expect(here()).toBe("/jobs/5");

    window.history.back();
    expect(await screen.findByText(/録音の結果・サービスの異常・テスト通知を新しい順に出す/)).toBeInTheDocument();
    expect(here()).toBe("/notifications");
  });

  it("通知の画面で「番組を開く」を押すと、その番組の編集に移る", async () => {
    window.history.replaceState(null, "", "/notifications");
    mockApi(notificationRoutes);
    const user = userEvent.setup();
    renderApp();
    const items = await screen.findAllByTestId("notification");
    await user.click(within(items[1]).getByRole("link", { name: "番組を開く" }));
    const dialog = await screen.findByRole("dialog", { name: "番組を編集する" });
    expect(within(dialog).getByRole("textbox", { name: /番組の名前/ })).toHaveValue(show.name);
    expect(here()).toBe(`/shows/${enc(show.name)}`);
  });

  it("ログインしていない状態で深い URL を開くと、ログインの後にその画面が開く", async () => {
    window.history.replaceState(null, "", "/jobs/5");
    mockApi({
      ...routes,
      "GET /api/me": () => ({ status: 401 }),
      "POST /api/login": () => ({ status: 200, body: { state: "authenticated" } }),
    });
    const user = userEvent.setup();
    renderApp();
    await user.type(await screen.findByLabelText(/パスワード/), "pw");
    await user.click(screen.getByRole("button", { name: "ログイン" }));
    expect(await screen.findByRole("dialog", { name: "番組A 2026-10-11" })).toBeInTheDocument();
    expect(here()).toBe("/jobs/5");
  });
});
