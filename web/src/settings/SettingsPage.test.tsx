import { MantineProvider } from "@mantine/core";
import { Notifications, notifications } from "@mantine/notifications";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SettingsBanners } from "./SettingsBanners";
import { SettingsPage } from "./SettingsPage";
import type { SettingsView } from "./settingsApi";

const base: SettingsView = {
  settings: {
    library: "/library",
    preset: "jellyfin",
    truncate_artist: true,
    membership: "free",
    max_attempts_free: 10,
    keyword_max_pending: 10,
    muxer: "ffmpeg",
    ffmpeg: "",
    ffprobe: "",
    chapters: true,
    backup_dir: "/backups",
    backup_keep: 7,
    radiko: { program_cache_mb: 64 },
    notify: { channel: "discord", success: true, smtp: { host: "", port: 0, username: "", from: "", to: [] } },
    jellyfin: { url: "", library: "" },
  },
  secrets: {
    "notify.discord_webhook": { set: true, unreadable: false },
    "notify.slack_webhook": { set: false, unreadable: false },
    "notify.smtp.password": { set: false, unreadable: false },
    "jellyfin.api_key": { set: false, unreadable: false },
  },
  library_set: true,
  unreadable_secrets: [],
  last_backup_at: null,
};

interface Call {
  method: string;
  body: unknown;
}

// GET は view を返し、PATCH は handler（既定は 200 で view をそのまま返す）に任せる。
function mockApi(
  view: SettingsView,
  patch?: (body: unknown) => { status: number; body: unknown },
  folders?: (path: string) => { status: number; body: unknown },
) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      if (folders && url.startsWith("/api/folders?path=")) {
        const path = decodeURIComponent(url.slice("/api/folders?path=".length));
        calls.push({ method: "GET folders", body: path });
        const r = folders(path);
        return new Response(JSON.stringify(r.body), { status: r.status });
      }
      if (url !== "/api/settings") throw new Error(`想定外の呼び出し: ${method} ${url}`);
      const body = init?.body ? JSON.parse(init.body as string) : undefined;
      calls.push({ method, body });
      if (method === "GET") return new Response(JSON.stringify(view), { status: 200 });
      const r = patch ? patch(body) : { status: 200, body: view };
      return new Response(JSON.stringify(r.body), { status: r.status });
    }),
  );
  return calls;
}

const renderPage = (onChanged = vi.fn()) => {
  render(
    <MantineProvider>
      <Notifications />
      <SettingsPage onChanged={onChanged} />
    </MantineProvider>,
  );
  return onChanged;
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  notifications.clean();
});

describe("SettingsPage", () => {
  it("保存先・通知・Jellyfin との連携・バックアップ・詳しい設定の 5 つの区切りを出す", async () => {
    mockApi(base);
    renderPage();
    expect(await screen.findByRole("region", { name: "保存先" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "通知" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Jellyfin との連携" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "バックアップ" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "詳しい設定" })).toBeInTheDocument();
    // 会員種別とプリセットは画面に出さない
    expect(screen.queryByText(/会員種別|プリセット/)).toBeNull();
  });

  it("保存先を変えて保存すると、library だけを送る", async () => {
    const calls = mockApi(base);
    const onChanged = renderPage();
    const input = await screen.findByLabelText("保存先のフォルダ");
    expect(input).toHaveValue("/library");
    await userEvent.clear(input);
    await userEvent.type(input, "/music");
    await userEvent.click(within(screen.getByRole("region", { name: "保存先" })).getByRole("button", { name: "保存" }));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ library: "/music" });
    expect(onChanged).toHaveBeenCalled();
    expect(await screen.findByText("保存先を保存した")).toBeInTheDocument();
  });

  it("保存に失敗したら、400 の理由を画面に出す", async () => {
    mockApi(base, () => ({ status: 400, body: { error: "保存先に書けない: /nope" } }));
    renderPage();
    await screen.findByLabelText("保存先のフォルダ");
    const region = screen.getByRole("region", { name: "保存先" });
    await userEvent.click(within(region).getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("保存先に書けない: /nope");
  });

  it("秘密の欄は設定済みと出し、空のまま保存したら送らない。入れたときだけ送る", async () => {
    const calls = mockApi(base);
    renderPage();
    const field = await screen.findByLabelText("Discord の Webhook の URL");
    expect(field).toHaveValue("");
    expect(screen.getByText("設定済み")).toBeInTheDocument();
    const region = screen.getByRole("region", { name: "通知" });
    const save = () => region.querySelectorAll("button")[region.querySelectorAll("button").length - 1] as HTMLElement;
    await userEvent.click(save());
    expect(calls.filter((c) => c.method === "PATCH")[0].body).toEqual({ notify: { channel: "discord", success: true } });
    await userEvent.type(field, "https://discord.example/hook");
    await userEvent.click(save());
    expect(calls.filter((c) => c.method === "PATCH")[1].body).toEqual({
      notify: { channel: "discord", success: true, discord_webhook: "https://discord.example/hook" },
    });
  });

  it("今の通知先が使っている秘密を消すときは、確かめで通知先を「なし」にすると伝え、通知先も一緒に送る（確かめに答えたときだけ）", async () => {
    const calls = mockApi(base);
    renderPage();
    const del = await screen.findByRole("button", { name: "Discord の Webhook の URLを消す" });
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    await userEvent.click(del);
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    expect(confirm.mock.calls[0][0]).toContain("通知先を「なし」にして");
    confirm.mockReturnValueOnce(true);
    await userEvent.click(del);
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ notify: { channel: "none", discord_webhook: "" } });
  });

  it("SMTP のパスワードも、メールが今の通知先なら、通知先を「なし」にして消す", async () => {
    const calls = mockApi({
      ...base,
      settings: { ...base.settings, notify: { channel: "email", success: true, smtp: { host: "h", port: 587, username: "u", from: "a@example.com", to: ["b@example.com"] } } },
      secrets: { ...base.secrets, "notify.smtp.password": { set: true, unreadable: false } },
    });
    renderPage();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(await screen.findByRole("button", { name: "SMTP のパスワードを消す" }));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ notify: { channel: "none", smtp: { password: "" } } });
  });

  it("今の通知先が使っていない秘密は、秘密だけを送って消す", async () => {
    const calls = mockApi({ ...base, secrets: { ...base.secrets, "notify.slack_webhook": { set: true, unreadable: false } } });
    renderPage();
    // 通知先の選び直しは保存していないので、保存してある通知先（Discord）は変わらない
    await userEvent.click(await screen.findByRole("combobox", { name: "通知先" }));
    await userEvent.click(await screen.findByRole("option", { name: "Slack", hidden: true }));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(await screen.findByRole("button", { name: "Slack の Webhook の URLを消す" }));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ notify: { slack_webhook: "" } });
  });

  it("保存してある Jellyfin の連携の API キーを消すときは、連携をやめて、url・library・api_key を全部空にして送る", async () => {
    const calls = mockApi({
      ...base,
      settings: { ...base.settings, jellyfin: { url: "http://jf:8096", library: "ラジオ" } },
      secrets: { ...base.secrets, "jellyfin.api_key": { set: true, unreadable: false } },
    });
    renderPage();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(await screen.findByRole("button", { name: "Jellyfin の API キーを消す" }));
    expect(confirm.mock.calls[0][0]).toContain("Jellyfin との連携をやめて");
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ jellyfin: { url: "", library: "", api_key: "" } });
  });

  it("連携を使っていないときの Jellyfin の API キーは、キーだけを送って消す", async () => {
    const calls = mockApi({ ...base, secrets: { ...base.secrets, "jellyfin.api_key": { set: true, unreadable: false } } });
    renderPage();
    await userEvent.click(await screen.findByRole("switch", { name: "Jellyfin と連携する" }));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: "Jellyfin の API キーを消す" }));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ jellyfin: { api_key: "" } });
  });

  it("「消す」の後の保存が断られたら、その理由を出す", async () => {
    mockApi(base, () => ({ status: 400, body: { error: "消せない" } }));
    renderPage();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(await screen.findByRole("button", { name: "Discord の Webhook の URLを消す" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("消せない");
  });

  it("保存の通信が失敗したら、通信を確かめてほしいと出す", async () => {
    const calls = mockApi(base);
    renderPage();
    await screen.findByLabelText("保存先のフォルダ");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("network");
      }),
    );
    void calls;
    await userEvent.click(within(screen.getByRole("region", { name: "保存先" })).getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("保存先を保存できなかった。通信を確かめてほしい");
  });

  it("最初の設定を読めなかったら、その理由を出す", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "読めない" }), { status: 500 })));
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("読めない");
  });

  it("復号できない秘密は、そのことを欄に出す", async () => {
    mockApi({
      ...base,
      secrets: { ...base.secrets, "notify.discord_webhook": { set: true, unreadable: true } },
      unreadable_secrets: ["notify.discord_webhook"],
    });
    renderPage();
    expect(await screen.findByText("復号できないので、未設定として扱っている")).toBeInTheDocument();
  });

  it("通知先をメールにすると SMTP の欄を出し、送るときに宛先を分ける", async () => {
    const calls = mockApi({ ...base, settings: { ...base.settings, notify: { channel: "email", success: false, smtp: { host: "h", port: 587, username: "u", from: "a@example.com", to: ["b@example.com", "c@example.com"] } } } });
    renderPage();
    expect(await screen.findByLabelText("SMTP のホスト")).toHaveValue("h");
    expect(screen.getByLabelText("宛先のアドレス")).toHaveValue("b@example.com\nc@example.com");
    const region = screen.getByRole("region", { name: "通知" });
    const buttons = region.querySelectorAll("button");
    await userEvent.click(buttons[buttons.length - 1]);
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({
      notify: { channel: "email", success: false, smtp: { host: "h", port: 587, username: "u", from: "a@example.com", to: ["b@example.com", "c@example.com"] } },
    });
  });

  it("Jellyfin は使うと決めたときだけ欄を開く。使わないと決めて保存すると、連携の値を全部空にする", async () => {
    const calls = mockApi({ ...base, settings: { ...base.settings, jellyfin: { url: "http://jf:8096", library: "ラジオ" } }, secrets: { ...base.secrets, "jellyfin.api_key": { set: true, unreadable: false } } });
    renderPage();
    expect(await screen.findByLabelText("Jellyfin の URL")).toHaveValue("http://jf:8096");
    await userEvent.click(screen.getByRole("switch", { name: "Jellyfin と連携する" }));
    expect(screen.queryByLabelText("Jellyfin の URL")).toBeNull();
    const region = screen.getByRole("region", { name: "Jellyfin との連携" });
    await userEvent.click(region.querySelector("button")!);
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ jellyfin: { url: "", library: "", api_key: "" } });
  });

  it("Jellyfin の欄は、使わない設定では閉じている", async () => {
    mockApi(base);
    renderPage();
    await screen.findByRole("region", { name: "Jellyfin との連携" });
    expect(screen.queryByLabelText("Jellyfin の URL")).toBeNull();
    await userEvent.click(screen.getByRole("switch", { name: "Jellyfin と連携する" }));
    expect(screen.getByLabelText("Jellyfin の URL")).toBeInTheDocument();
  });

  it("詳しい設定で、チャプターを入れるか（chapters）を切り替えて送れる", async () => {
    const calls = mockApi(base);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "詳しい設定" }));
    // Collapse は開き始めるまで中身を隠している（React の Activity）ので、見えるようになるのを待ってから押す。
    // Switch の label は説明の文も含むので、前方一致で探す。
    const sw = await screen.findByLabelText(/^タイムフリーの録音にチャプターを入れる/);
    await waitFor(() => expect(sw).toBeVisible());
    expect(sw).toBeChecked();
    await userEvent.click(sw);
    const region = screen.getByRole("region", { name: "詳しい設定" });
    await userEvent.click(region.querySelector("button")!);
    expect(calls.find((c) => c.method === "PATCH")!.body).toMatchObject({ chapters: false });
  });

  it("詳しい設定は畳んであり、開くと keyword_max_pending などを送れる。番組表のキャッシュは再起動が要ると出す", async () => {
    const calls = mockApi(base);
    renderPage();
    const toggle = await screen.findByRole("button", { name: "詳しい設定" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("保存しても、serve を再起動するまで効かない")).toBeInTheDocument();
    const kw = screen.getByLabelText("キーワードごとの、録音を終えていない放送回の上限");
    // Collapse は開き始めるまで中身を隠している（React の Activity）ので、見えるようになるのを待ってから打つ。
    await waitFor(() => expect(kw).toBeVisible());
    await userEvent.clear(kw);
    await userEvent.type(kw, "5");
    const region = screen.getByRole("region", { name: "詳しい設定" });
    await userEvent.click(region.querySelector("button")!);
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({
      muxer: "ffmpeg",
      ffmpeg: "",
      ffprobe: "",
      truncate_artist: true,
      chapters: true,
      max_attempts_free: 10,
      keyword_max_pending: 5,
      radiko: { program_cache_mb: 64 },
    });
  });
});

// GET /api/settings は base を返し、GET /api/backup は backup（既定は 200 の写し）に任せる。
function mockBackupApi(backup?: () => Response) {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      urls.push(url);
      if (url === "/api/settings") return new Response(JSON.stringify(base), { status: 200 });
      if (url === "/api/backup") {
        return backup
          ? backup()
          : new Response("SQLite format 3\0", {
              status: 200,
              headers: { "Content-Type": "application/vnd.sqlite3", "Content-Disposition": 'attachment; filename="rajikura-20261018-120405.db"' },
            });
      }
      throw new Error(`想定外の呼び出し: ${url}`);
    }),
  );
  return urls;
}

// URL.createObjectURL と、リンクの click（ブラウザに保存させる）を差し替え、保存させたファイルの名前と中身を集める。
function captureDownloads() {
  const saved: { name: string; blob: Blob }[] = [];
  const blobs = new Map<string, Blob>();
  class TestURL extends URL {
    static createObjectURL(b: Blob) {
      const u = `blob:test/${blobs.size}`;
      blobs.set(u, b);
      return u;
    }
    static revokeObjectURL() {}
  }
  vi.stubGlobal("URL", TestURL);
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    saved.push({ name: this.download, blob: blobs.get(this.getAttribute("href") ?? "")! });
  });
  return saved;
}

describe("SettingsPage のバックアップ", () => {
  it("「バックアップ」の欄に「ダウンロード」と、鍵を含まない旨を出す", async () => {
    mockBackupApi();
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    expect(region).toHaveTextContent("この PC にファイルとして保存する。秘密の鍵は含まない");
    expect(screen.getByRole("button", { name: "ダウンロード" })).toBeInTheDocument();
  });

  it("「ダウンロード」を押すと、GET /api/backup の写しを答えの名前でブラウザに保存させ、トーストで知らせる", async () => {
    const urls = mockBackupApi();
    const saved = captureDownloads();
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "ダウンロード" }));
    expect(await screen.findByText("バックアップ（rajikura-20261018-120405.db）をダウンロードした")).toBeInTheDocument();
    expect(urls).toContain("/api/backup");
    expect(saved).toHaveLength(1);
    expect(saved[0].name).toBe("rajikura-20261018-120405.db");
    expect(await saved[0].blob.text()).toBe("SQLite format 3\0");
  });

  it("API が断ったら、その理由を欄に出し、保存させない", async () => {
    mockBackupApi(() => new Response(JSON.stringify({ error: "内部エラー" }), { status: 500 }));
    const saved = captureDownloads();
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "ダウンロード" }));
    const region = screen.getByRole("region", { name: "バックアップ" });
    expect(await within(region).findByRole("alert")).toHaveTextContent("内部エラー");
    expect(saved).toHaveLength(0);
  });

  it("通信に失敗したら、通信を確かめてほしいと出す", async () => {
    mockBackupApi(() => {
      throw new TypeError("Failed to fetch");
    });
    captureDownloads();
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "ダウンロード" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("バックアップをダウンロードできなかった。通信を確かめてほしい");
  });
});

describe("SettingsPage の自動のバックアップ", () => {
  it("取る先と残す数を出し、最後に自動で取れた日時を出す（取れていなければ、まだ取れていないと出す）", async () => {
    mockApi({ ...base, last_backup_at: "2026-10-18T03:05:00Z" });
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    expect(within(region).getByLabelText("自動のバックアップを取る先のフォルダ")).toHaveValue("/backups");
    expect(within(region).getByLabelText("残す数")).toHaveValue("7");
    expect(region).toHaveTextContent("最後に自動で取れた日時: 10/18 12:05");
  });

  it("まだ取れていなければ「まだ取れていない」と出す", async () => {
    mockApi(base);
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    expect(region).toHaveTextContent("最後に自動で取れた日時: まだ取れていない");
  });

  it("取る先と残す数を変えて保存すると、backup_dir と backup_keep を送る", async () => {
    const calls = mockApi(base);
    const onChanged = renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    const dir = within(region).getByLabelText("自動のバックアップを取る先のフォルダ");
    await userEvent.clear(dir);
    await userEvent.type(dir, "/mnt/backups");
    const keep = within(region).getByLabelText("残す数");
    await userEvent.clear(keep);
    await userEvent.type(keep, "3");
    await userEvent.click(within(region).getByRole("button", { name: "保存" }));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ backup_dir: "/mnt/backups", backup_keep: 3 });
    expect(onChanged).toHaveBeenCalled();
    expect(await screen.findByText("バックアップの設定を保存した")).toBeInTheDocument();
  });

  it("取る先を空にして保存すると、空の backup_dir を送る（自動では取らない）", async () => {
    const calls = mockApi(base);
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    await userEvent.clear(within(region).getByLabelText("自動のバックアップを取る先のフォルダ"));
    await userEvent.click(within(region).getByRole("button", { name: "保存" }));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ backup_dir: "", backup_keep: 7 });
  });

  it("保存に失敗したら、400 の理由を欄に出す", async () => {
    mockApi(base, () => ({ status: 400, body: { error: "バックアップの取る先 /nope に書けない" } }));
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    await userEvent.click(within(region).getByRole("button", { name: "保存" }));
    expect(await within(region).findByRole("alert")).toHaveTextContent("バックアップの取る先 /nope に書けない");
  });

  it("保存の通信が失敗したら、通信を確かめてほしいと出す", async () => {
    mockApi(base);
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("network");
      }),
    );
    await userEvent.click(within(region).getByRole("button", { name: "保存" }));
    expect(await within(region).findByRole("alert")).toHaveTextContent("バックアップの設定を保存できなかった。通信を確かめてほしい");
  });
});

describe("SettingsBanners", () => {
  it("バックアップの取る先（backup_dir）が空なら「自動のバックアップが止まっている」を出し、設定を開ける", async () => {
    const open = vi.fn();
    render(
      <MantineProvider>
        <SettingsBanners view={{ ...base, settings: { ...base.settings, backup_dir: "" } }} onOpenSettings={open} />
      </MantineProvider>,
    );
    expect(screen.getByText("自動のバックアップが止まっている")).toBeInTheDocument();
    expect(screen.queryByText("保存先を決めて")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "設定を開く" }));
    expect(open).toHaveBeenCalled();
  });


  it("保存先が決まっていて、復号できない秘密も無ければ、何も出さない", () => {
    const { container } = render(
      <MantineProvider>
        <SettingsBanners view={base} onOpenSettings={() => {}} />
      </MantineProvider>,
    );
    expect(container.querySelector('[data-testid="settings-banners"]')).toBeNull();
  });

  it("保存先が無ければ「保存先を決めて」、復号できない秘密があれば「秘密を入れ直して」を出し、設定を開ける", async () => {
    const open = vi.fn();
    render(
      <MantineProvider>
        <SettingsBanners view={{ ...base, library_set: false, unreadable_secrets: ["jellyfin.api_key"] }} onOpenSettings={open} />
      </MantineProvider>,
    );
    expect(screen.getByText("保存先を決めて")).toBeInTheDocument();
    expect(screen.getByText("秘密を入れ直して")).toBeInTheDocument();
    expect(screen.getByText(/Jellyfin の API キー/)).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("button", { name: "設定を開く" })[0]);
    expect(open).toHaveBeenCalled();
  });
});

describe("SettingsPage のフォルダを選ぶダイアログ", () => {
  const ok = (path: string, parent: string, names: string[]) => ({
    status: 200,
    body: { path, parent, folders: names.map((n) => ({ name: n, path: `${path}/${n}` })) },
  });
  const folderCalls = (calls: Call[]) => calls.filter((c) => c.method === "GET folders").map((c) => c.body);

  it("選んだパスを欄に入れ、保存はしない", async () => {
    const calls = mockApi(base, undefined, (p) => (p === "/library" ? ok("/library", "/", ["jazz", "news"]) : ok(p, "/library", [])));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "保存先のフォルダを選ぶ" }));
    await userEvent.click(await screen.findByRole("button", { name: "news" }));
    await waitFor(() => expect(screen.getByLabelText("場所")).toHaveValue("/library/news"));
    await userEvent.click(screen.getByRole("button", { name: "選ぶ" }));
    await waitFor(() => expect(screen.getByLabelText("保存先のフォルダ")).toHaveValue("/library/news"));
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });

  it("欄の値が無いフォルダなら、在る先祖を開き、理由を出す", async () => {
    const calls = mockApi(base, undefined, (p) =>
      p === "/library" ? ok("/library", "/", ["a"]) : { status: 400, body: { error: `フォルダを開けない: ${p}` } },
    );
    renderPage();
    const input = await screen.findByLabelText("保存先のフォルダ");
    await userEvent.clear(input);
    await userEvent.type(input, "/library/new/deeper");
    await userEvent.click(screen.getByRole("button", { name: "保存先のフォルダを選ぶ" }));
    await waitFor(() => expect(screen.getByLabelText("場所")).toHaveValue("/library"));
    expect(await screen.findByRole("alert")).toHaveTextContent("フォルダを開けない: /library/new/deeper");
    expect(folderCalls(calls)).toEqual(["/library/new/deeper", "/library/new", "/library"]);
  });

  it("UNC の共有の下の値は、400 を繰り返さずに根の一覧を開く", async () => {
    const unc = String.raw`\\server\share\new`;
    const calls = mockApi(base, undefined, (p) =>
      p === "" ? ok("", "", ["C:"]) : { status: 400, body: { error: `開けない: ${p}` } },
    );
    renderPage();
    const input = await screen.findByLabelText("保存先のフォルダ");
    await userEvent.clear(input);
    await userEvent.click(input);
    await userEvent.paste(unc);
    await userEvent.click(screen.getByRole("button", { name: "保存先のフォルダを選ぶ" }));
    expect(await screen.findByRole("button", { name: "C:" })).toBeInTheDocument();
    expect(folderCalls(calls)).toEqual([unc, String.raw`\\server\share`, ""]);
  });

  const netFail = "フォルダを読めなかった。通信を確かめてほしい";

  it("開いたときに通信が失敗したら、通信を確かめてほしいと出す", async () => {
    mockApi(base, undefined, () => {
      throw new TypeError("network");
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "保存先のフォルダを選ぶ" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(netFail);
  });

  it("移動・上へ・一覧のフォルダで通信が失敗したら、通信を確かめてほしいと出し、一覧は前のまま残る", async () => {
    let down = false;
    mockApi(base, undefined, (p) => {
      if (down) throw new TypeError("network");
      return ok(p, "/", ["jazz"]);
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "保存先のフォルダを選ぶ" }));
    const jazz = await screen.findByRole("button", { name: "jazz" });
    down = true;
    await userEvent.click(jazz);
    expect(await screen.findByRole("alert")).toHaveTextContent(netFail);
    expect(screen.getByRole("button", { name: "jazz" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "上へ" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(netFail);
    await userEvent.click(screen.getByRole("button", { name: "移動" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(netFail);
    expect(screen.getByRole("button", { name: "jazz" })).toBeInTheDocument();
  });

  it("根の一覧まで 400 なら、最初に開けなかった理由を出す", async () => {
    mockApi(base, undefined, (p) => ({ status: 400, body: { error: `開けない: ${p}` } }));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "保存先のフォルダを選ぶ" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("開けない: /library");
  });

  it("欄が空で根の一覧が 400 なら、その理由を出す", async () => {
    mockApi({ ...base, settings: { ...base.settings, backup_dir: "" } }, undefined, () => ({
      status: 400,
      body: { error: "根の一覧を読めない" },
    }));
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    await userEvent.click(within(region).getByRole("button", { name: "自動のバックアップを取る先のフォルダを選ぶ" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("根の一覧を読めない");
  });

  it("移動で打った先が 400 なら、理由を出し、一覧は前のまま残る", async () => {
    mockApi(base, undefined, (p) => (p === "/nope" ? { status: 400, body: { error: "フォルダを開けない: /nope" } } : ok(p, "/", ["jazz"])));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "保存先のフォルダを選ぶ" }));
    await screen.findByRole("button", { name: "jazz" });
    const where = screen.getByLabelText("場所");
    await userEvent.clear(where);
    await userEvent.type(where, "/nope");
    await userEvent.click(screen.getByRole("button", { name: "移動" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("フォルダを開けない: /nope");
    expect(screen.getByRole("button", { name: "jazz" })).toBeInTheDocument();
  });

  it("欄が空なら根の一覧を開く", async () => {
    const calls = mockApi({ ...base, settings: { ...base.settings, backup_dir: "" } }, undefined, () => ok("", "", ["x"]));
    renderPage();
    const region = await screen.findByRole("region", { name: "バックアップ" });
    await userEvent.click(within(region).getByRole("button", { name: "自動のバックアップを取る先のフォルダを選ぶ" }));
    expect(await screen.findByRole("button", { name: "x" })).toBeInTheDocument();
    expect(folderCalls(calls)).toEqual([""]);
  });
});
