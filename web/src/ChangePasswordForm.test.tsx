import { MantineProvider } from "@mantine/core";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChangePasswordForm } from "./ChangePasswordForm";

function mockPut(status: number, body: unknown) {
  const sent: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url !== "/api/password" || init?.method !== "PUT") throw new Error(`想定外の呼び出し: ${init?.method} ${url}`);
      sent.push(JSON.parse(String(init.body)));
      return new Response(JSON.stringify(body), { status });
    }),
  );
  return sent;
}

const renderForm = () =>
  render(
    <MantineProvider>
      <ChangePasswordForm />
    </MantineProvider>,
  );

async function fill(current: string, password: string, again: string) {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText(/今のパスワード/), current);
  const [pw, confirm] = screen.getAllByLabelText(/新しいパスワード/);
  await user.type(pw, password);
  await user.type(confirm, again);
  await user.click(screen.getByRole("button", { name: "パスワードを変える" }));
}

afterEach(() => vi.unstubAllGlobals());

describe("ChangePasswordForm", () => {
  it("今のパスワードと新しいパスワードを送り、変えたと出す", async () => {
    const sent = mockPut(200, { changed: true });
    renderForm();
    await fill("old", "new", "new");
    expect(await screen.findByRole("status")).toHaveTextContent("パスワードを変えた");
    expect(sent).toEqual([{ current: "old", password: "new" }]);
  });

  it("今のパスワードが違えば（403）、その理由を出す", async () => {
    mockPut(403, { error: "今のパスワードが違う" });
    renderForm();
    await fill("bad", "new", "new");
    expect(await screen.findByRole("alert")).toHaveTextContent("今のパスワードが違う");
  });

  it("続けて間違えて受け付けを止めているとき（429）は、あと何秒待てばよいかを出す", async () => {
    mockPut(429, { error: "止めている", retry_after: 90 });
    renderForm();
    await fill("bad", "new", "new");
    expect(await screen.findByRole("alert")).toHaveTextContent("あと 90 秒待ってほしい");
  });

  it("通信に失敗したら、通信を確かめるよう出す", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    renderForm();
    await fill("old", "new", "new");
    expect(await screen.findByRole("alert")).toHaveTextContent("パスワードを変えられなかった。通信を確かめてほしい");
  });

  it("API が想定外のエラー（500）を返したら、その理由を出す", async () => {
    mockPut(500, { error: "内部エラー" });
    renderForm();
    await fill("old", "new", "new");
    expect(await screen.findByRole("alert")).toHaveTextContent("内部エラー");
  });

  it("新しいパスワードを 2 回とも同じに入れないと、送らない", async () => {
    const sent = mockPut(200, {});
    renderForm();
    await fill("old", "new", "other");
    expect(await screen.findByRole("alert")).toHaveTextContent("2 つの新しいパスワードが違う");
    expect(sent).toEqual([]);
  });
});
