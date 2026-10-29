import { MantineProvider } from "@mantine/core";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { parseTrayLink, TrayLinkPage } from "./TrayLinkPage";

const renderPage = (search: string) =>
  render(
    <MantineProvider>
      <TrayLinkPage search={search} />
    </MantineProvider>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("parseTrayLink", () => {
  it("port が 1024〜65535 の数だけを受け付ける", () => {
    expect(parseTrayLink("?port=1024&state=s&name=a")?.port).toBe(1024);
    expect(parseTrayLink("?port=65535&state=s")?.port).toBe(65535);
    for (const p of ["1023", "65536", "abc", "", "80", "1e4", "-1", "12345678"]) {
      expect(parseTrayLink(`?port=${p}&state=s`)).toBeNull();
    }
  });
  it("state が空なら受け付けない", () => {
    expect(parseTrayLink("?port=5000")).toBeNull();
    expect(parseTrayLink("?port=5000&state=")).toBeNull();
  });
});

describe("TrayLinkPage", () => {
  it("確かめのボタンを押すまでトークンを発行しない", () => {
    const f = vi.fn();
    vi.stubGlobal("fetch", f);
    renderPage("?port=5000&state=s&name=PC%5Cme");
    expect(screen.getByRole("button", { name: "このトレイと連携する" })).toBeTruthy();
    expect(f).not.toHaveBeenCalled();
  });

  it("押すと「トレイ（<name>）」のトークンを発行し、フォームの POST で token と state を 127.0.0.1 に送る", async () => {
    const f = vi.fn(
      async (_url: string, _init?: RequestInit) =>
        new Response(JSON.stringify({ id: 1, name: "x", createdAt: "", lastUsedAt: null, token: "rjk_abc" }), { status: 201 }),
    );
    vi.stubGlobal("fetch", f);
    let submitted: HTMLFormElement | null = null;
    vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(function (this: HTMLFormElement) {
      submitted = this;
    });
    renderPage("?port=5000&state=st1&name=PC%5Cme");
    await userEvent.click(screen.getByRole("button", { name: "このトレイと連携する" }));
    await vi.waitFor(() => expect(submitted).not.toBeNull());

    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/tokens");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(init?.body as string)).toEqual({ name: "トレイ（PC\\me）" });

    const form = submitted as unknown as HTMLFormElement;
    expect(form.method).toBe("post");
    expect(form.action).toBe("http://127.0.0.1:5000/callback");
    expect(Object.fromEntries(new FormData(form) as unknown as Iterable<[string, string]>)).toEqual({ token: "rjk_abc", state: "st1" });
  });

  it("port が範囲外なら、ボタンを出さず、何も送らない", () => {
    const f = vi.fn();
    vi.stubGlobal("fetch", f);
    renderPage("?port=80&state=s&name=a");
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
    expect(f).not.toHaveBeenCalled();
  });

  it("発行に失敗したら、送らずにエラーを出す", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "だめ" }), { status: 500 })));
    const submit = vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(() => {});
    renderPage("?port=5000&state=s&name=a");
    await userEvent.click(screen.getByRole("button", { name: "このトレイと連携する" }));
    expect((await screen.findByRole("alert")).textContent).toContain("だめ");
    expect(submit).not.toHaveBeenCalled();
  });
});
