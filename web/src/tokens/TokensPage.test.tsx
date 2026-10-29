import { MantineProvider } from "@mantine/core";
import { Notifications, notifications } from "@mantine/notifications";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TokensPage } from "./TokensPage";
import type { ApiToken } from "./tokensApi";

const used: ApiToken = { id: 2, name: "手元の CLI", createdAt: "2026-10-12T01:00:00Z", lastUsedAt: "2026-10-13T03:30:00Z" };
const fresh: ApiToken = { id: 1, name: "AI", createdAt: "2026-10-11T01:00:00Z", lastUsedAt: null };

function mockApi(routes: Record<string, (init?: RequestInit) => { status: number; body?: unknown }>) {
  const calls: { key: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push({ key, init });
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = h(init);
      return new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

const renderPage = () =>
  render(
    <MantineProvider>
      <Notifications />
      <TokensPage />
    </MantineProvider>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
  notifications.clean();
});

describe("TokensPage", () => {
  it("一覧に名前・発行日時・最後に使った日時を出す（使っていなければそう出す）", async () => {
    mockApi({ "GET /api/tokens": () => ({ status: 200, body: { tokens: [used, fresh] } }) });
    renderPage();
    const items = await screen.findAllByTestId("token");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("手元の CLI")).toBeInTheDocument();
    expect(within(items[0]).getByText(/発行 10\/12 10:00/)).toBeInTheDocument();
    expect(within(items[0]).getByText(/最後に使った日時 10\/13 12:30/)).toBeInTheDocument();
    expect(within(items[1]).getByText(/まだ使っていない/)).toBeInTheDocument();
  });

  it("発行するとトークンそのものを 1 度だけ出す", async () => {
    let list: ApiToken[] = [];
    const calls = mockApi({
      "GET /api/tokens": () => ({ status: 200, body: { tokens: list } }),
      "POST /api/tokens": () => {
        list = [fresh];
        return { status: 201, body: { ...fresh, token: "rjk_secret" } };
      },
    });
    renderPage();
    expect(await screen.findByText("発行した API トークンは無い。")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("名前"), "AI");
    await userEvent.click(screen.getByRole("button", { name: "発行" }));
    expect(await screen.findByTestId("new-token")).toHaveTextContent("rjk_secret");
    expect(JSON.parse(calls.find((c) => c.key === "POST /api/tokens")!.init!.body as string)).toEqual({ name: "AI" });
    expect(await screen.findAllByTestId("token")).toHaveLength(1);
    // 一覧にはトークンそのものを出さない
    expect(within(screen.getByTestId("token")).queryByText(/rjk_secret/)).toBeNull();
    // 閉じたら二度と出ない
    await userEvent.click(screen.getByRole("button", { name: "閉じる" }));
    expect(screen.queryByTestId("new-token")).toBeNull();
  });

  it("名前が空なら発行できない", async () => {
    mockApi({ "GET /api/tokens": () => ({ status: 200, body: { tokens: [] } }) });
    renderPage();
    await screen.findByText("発行した API トークンは無い。");
    expect(screen.getByRole("button", { name: "発行" })).toBeDisabled();
  });

  it("失効は確認してから行い、一覧を読み直す", async () => {
    let list = [used];
    const calls = mockApi({
      "GET /api/tokens": () => ({ status: 200, body: { tokens: list } }),
      "DELETE /api/tokens/2": () => {
        list = [];
        return { status: 204 };
      },
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "手元の CLIを失効" }));
    expect(calls.some((c) => c.key.startsWith("DELETE"))).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "手元の CLIを失効" }));
    expect(await screen.findByText("発行した API トークンは無い。")).toBeInTheDocument();
    expect(calls.some((c) => c.key === "DELETE /api/tokens/2")).toBe(true);
    expect(await screen.findByText("API トークン「手元の CLI」を失効した")).toBeInTheDocument();
    confirm.mockRestore();
  });

  it.each([
    [404, "トークンが無い"],
    [500, "内部エラー"],
  ])("失効が %i で断られたらエラーを出し、一覧は残す", async (status, msg) => {
    mockApi({
      "GET /api/tokens": () => ({ status: 200, body: { tokens: [used] } }),
      "DELETE /api/tokens/2": () => ({ status, body: { error: msg } }),
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "手元の CLIを失効" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(msg);
    expect(screen.getAllByTestId("token")).toHaveLength(1);
    confirm.mockRestore();
  });

  it("失効は成功し読み直しだけ失敗したら、トーストで知らせ、読み直しの失敗を区別して出す", async () => {
    let reads = 0;
    mockApi({
      "GET /api/tokens": () => (++reads === 1 ? { status: 200, body: { tokens: [used] } } : { status: 500, body: { error: "内部エラー" } }),
      "DELETE /api/tokens/2": () => ({ status: 204 }),
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "手元の CLIを失効" }));
    expect(await screen.findByText("API トークン「手元の CLI」を失効した")).toBeInTheDocument();
    expect(await screen.findByText("失効したが、一覧を読み直せなかった: 内部エラー")).toBeInTheDocument();
    confirm.mockRestore();
  });

  it("一覧を読めなかったらエラーを出す", async () => {
    mockApi({ "GET /api/tokens": () => ({ status: 500, body: { error: "内部エラー" } }) });
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("内部エラー");
    expect(screen.queryByTestId("token")).toBeNull();
  });

  it("発行に失敗したらエラーを出す", async () => {
    mockApi({
      "GET /api/tokens": () => ({ status: 200, body: { tokens: [] } }),
      "POST /api/tokens": () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    renderPage();
    await screen.findByText("発行した API トークンは無い。");
    await userEvent.type(screen.getByLabelText("名前"), "x");
    await userEvent.click(screen.getByRole("button", { name: "発行" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("内部エラー");
  });
});
