import { MantineProvider } from "@mantine/core";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Notifications, notifications } from "@mantine/notifications";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Show, SplitMode, UnmatchedDay } from "./api";
import {
  fromBroadcastClock,
  groupSummary,
  groupWeekly,
  nextAiringLabel,
  toBroadcastClock,
  ungroupWeekly,
  unmatchedDayLines,
  weeklySummaries,
} from "./format";
import { ShowsPage } from "./ShowsPage";

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

const renderPage = () =>
  render(
    <MantineProvider>
      <Notifications />
      <ShowsPage />
    </MantineProvider>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
  notifications.clean();
});

const flat: Show = {
  name: "パンサー向井のふらっと",
  enabled: true,
  split: "join",
  check_title: true,
  weekly: [
    { station: "TBS", weekday: 1, start: "08:30" },
    { station: "TBS", weekday: 1, start: "10:00" },
  ],
  next_airing: "2026-10-12T08:30:00+09:00",
};
// 当たらない日の例（10/19（月）17:40 の枠に放送回が無い）と、その時刻を含む放送回も番組名の合う放送回も無い深夜の枠。
const unmatched: UnmatchedDay = {
  day: "2026-10-19",
  station: "LFR",
  start: "2026-10-19T17:40:00+09:00",
  covering: { title: "辛坊治郎 ズーム そこまで言うか！", start: "2026-10-19T15:30:00+09:00", end: "2026-10-19T18:00:00+09:00" },
  titled: [{ title: "チャリで30分", start: "2026-10-19T19:00:00+09:00", end: "2026-10-19T19:30:00+09:00" }],
};
const unmatchedLate: UnmatchedDay = { day: "2026-10-19", station: "LFR", start: "2026-10-20T01:00:00+09:00", covering: null, titled: [] };
const stopped: Show = { name: "無効な番組", enabled: false, split: "none", check_title: true, weekly: [] };
const stations = [{ id: "TBS", name: "TBSラジオ" }];

describe("format", () => {
  it("毎週の予約の要約は、まとまりごとに曜日を「・」でつなぐ", () => {
    expect(groupSummary({ station: "TBS", weekdays: [1, 2, 3, 4], start: "08:30" }, stations)).toBe("TBSラジオ 月・火・水・木 08:30");
    expect(groupSummary({ station: "TBS", weekdays: [1], start: "10:00" })).toBe("TBS 月 10:00");
    expect(groupSummary({ station: "LFR", weekdays: [6], start: "03:00", from: "2026-10-20" }, [], "2026-10-18")).toBe("LFR 土 27:00（2026-10-20 から）");
    expect(groupSummary({ station: "LFR", weekdays: [6], start: "03:00", from: "" })).toBe("LFR 土 27:00");
  });

  it("開始日が今日の放送日より前なら「（… から）」を付けない。当日と後は付ける。開始日なしは変わらない", () => {
    const g = { station: "LFR", weekdays: [6], start: "03:00" };
    const today = "2026-10-18";
    expect(groupSummary({ ...g, from: "2026-10-17" }, [], today)).toBe("LFR 土 27:00");
    expect(groupSummary({ ...g, from: "2026-10-18" }, [], today)).toBe("LFR 土 27:00（2026-10-18 から）");
    expect(groupSummary({ ...g, from: "2026-10-19" }, [], today)).toBe("LFR 土 27:00（2026-10-19 から）");
    expect(groupSummary({ ...g, from: "" }, [], today)).toBe("LFR 土 27:00");
    expect(groupSummary(g, [], today)).toBe("LFR 土 27:00");
    expect(weeklySummaries([{ station: "LFR", weekday: 6, start: "03:00", from: "2026-10-17" }], [], today)).toEqual(["LFR 土 27:00"]);
  });

  it("終了日があれば、今日の放送日と比べて「（… まで）」「（… から … まで）」「（… で終了）」を付ける", () => {
    const g = { station: "LFR", weekdays: [6], start: "03:00" };
    const today = "2026-10-18";
    // 開始日が無いか今日より前、終了日が無い: 付けない。開始日が今日以降、終了日が無い: （… から）
    expect(groupSummary(g, [], today)).toBe("LFR 土 27:00");
    expect(groupSummary({ ...g, from: "2026-10-17" }, [], today)).toBe("LFR 土 27:00");
    expect(groupSummary({ ...g, from: "2026-10-20" }, [], today)).toBe("LFR 土 27:00（2026-10-20 から）");
    // 開始日が無いか今日より前、終了日が今日以降: （… まで）。終了日の当日も「まで」
    expect(groupSummary({ ...g, until: "2026-12-26" }, [], today)).toBe("LFR 土 27:00（2026-12-26 まで）");
    expect(groupSummary({ ...g, from: "2026-10-17", until: "2026-12-26" }, [], today)).toBe("LFR 土 27:00（2026-12-26 まで）");
    expect(groupSummary({ ...g, until: "2026-10-18" }, [], today)).toBe("LFR 土 27:00（2026-10-18 まで）");
    // 開始日と終了日が今日以降: （… から … まで）
    expect(groupSummary({ ...g, from: "2026-10-20", until: "2026-12-26" }, [], today)).toBe("LFR 土 27:00（2026-10-20 から 2026-12-26 まで）");
    expect(groupSummary({ ...g, from: "2026-10-18", until: "2026-10-18" }, [], today)).toBe("LFR 土 27:00（2026-10-18 から 2026-10-18 まで）");
    // 終了日が今日より前: 開始日によらず（… で終了）
    expect(groupSummary({ ...g, until: "2026-10-17" }, [], today)).toBe("LFR 土 27:00（2026-10-17 で終了）");
    expect(groupSummary({ ...g, from: "2026-10-01", until: "2026-10-17" }, [], today)).toBe("LFR 土 27:00（2026-10-17 で終了）");
    expect(weeklySummaries([{ station: "LFR", weekday: 6, start: "03:00", until: "2026-10-17" }], [], today)).toEqual(["LFR 土 27:00（2026-10-17 で終了）"]);
  });

  it("毎週の予約の要約の時刻は放送日の時刻で出す（API の 00:00〜04:59 は 24:00〜28:59）", () => {
    const names = [{ id: "LFR", name: "ニッポン放送" }];
    expect(weeklySummaries([{ station: "LFR", weekday: 6, start: "01:00" }], names)).toEqual(["ニッポン放送 土 25:00"]);
    expect(weeklySummaries([{ station: "LFR", weekday: 6, start: "04:59" }], names)).toEqual(["ニッポン放送 土 28:59"]);
    expect(weeklySummaries([{ station: "LFR", weekday: 6, start: "05:00" }], names)).toEqual(["ニッポン放送 土 05:00"]);
  });

  it("API の開始時刻と放送日の時刻を行き来する", () => {
    const cases: [string, string][] = [
      ["00:00", "24:00"],
      ["01:00", "25:00"],
      ["04:59", "28:59"],
      ["05:00", "05:00"],
      ["23:59", "23:59"],
    ];
    for (const [api, shown] of cases) {
      expect(toBroadcastClock(api), api).toBe(shown);
      expect(fromBroadcastClock(shown), shown).toEqual({ ok: true, start: api });
    }
  });

  it("放送日の時刻として 05:00〜28:59 の HH:MM だけを受け付け、00:00〜04:59 は前の曜日で書くべき値として断る", () => {
    for (const early of ["00:00", "01:00", "04:59"]) expect(fromBroadcastClock(early), early).toEqual({ ok: false, reason: "early" });
    for (const bad of ["29:00", "08:60", "8:30", "", "ab:cd"]) expect(fromBroadcastClock(bad), bad).toEqual({ ok: false, reason: "invalid" });
  });

  const row = (station: string, weekday: number, start: string, from?: string) => ({ station, weekday, start, ...(from ? { from } : {}) });

  it("同じ局・開始時刻・開始日の毎週の予約を 1 つにまとめ、まとまりを いちばん早い曜日（月曜始まり）・開始時刻・放送局の順に並べる", () => {
    const names = [{ id: "TBS", name: "TBSラジオ" }, { id: "LFR", name: "ニッポン放送" }];
    const cases: { name: string; weekly: Show["weekly"]; want: string[] }[] = [
      {
        name: "帯番組",
        weekly: [1, 2, 3, 4].map((d) => row("TBS", d, "08:30")).concat([1, 2, 3, 4].map((d) => row("TBS", d, "10:00"))),
        want: ["TBSラジオ 月・火・水・木 08:30", "TBSラジオ 月・火・水・木 10:00"],
      },
      { name: "1 曜日だけ", weekly: [row("LFR", 0, "01:00")], want: ["ニッポン放送 日 25:00"] },
      {
        name: "同じ曜日の中は放送日の時刻で比べる（深夜は最後）",
        weekly: [row("TBS", 1, "01:00"), row("TBS", 1, "22:00"), row("TBS", 1, "08:30")],
        want: ["TBSラジオ 月 08:30", "TBSラジオ 月 22:00", "TBSラジオ 月 25:00"],
      },
      {
        name: "開始日が違えば別",
        weekly: [row("TBS", 1, "08:30"), row("TBS", 2, "08:30", "2026-10-20")],
        want: ["TBSラジオ 月 08:30", "TBSラジオ 火 08:30（2026-10-20 から）"],
      },
      {
        name: "終了日が違えば別",
        weekly: [row("TBS", 1, "08:30"), { ...row("TBS", 2, "08:30"), until: "2026-12-26" }, { ...row("TBS", 3, "08:30"), until: "2026-12-26" }],
        want: ["TBSラジオ 月 08:30", "TBSラジオ 火・水 08:30（2026-12-26 まで）"],
      },
      { name: "局が違えば別。並びは放送局の順", weekly: [row("TBS", 1, "08:30"), row("QRR", 1, "08:30")], want: ["QRR 月 08:30", "TBSラジオ 月 08:30"] },
      {
        name: "並びで続いていなくてもまとめる。曜日は月曜始まり",
        weekly: [row("TBS", 0, "01:00"), row("TBS", 1, "10:00"), row("TBS", 6, "01:00"), row("TBS", 1, "08:30")],
        want: ["TBSラジオ 月 08:30", "TBSラジオ 月 10:00", "TBSラジオ 土・日 25:00"],
      },
      { name: "同じ曜日が重なるものは別", weekly: [row("TBS", 1, "08:30"), row("TBS", 1, "08:30")], want: ["TBSラジオ 月 08:30", "TBSラジオ 月 08:30"] },
    ];
    for (const c of cases) {
      const copy = c.weekly.map((r) => ({ ...r }));
      expect(weeklySummaries(c.weekly, names, "2026-10-18"), c.name).toEqual(c.want);
      expect(c.weekly, `${c.name}（元の配列は変えない）`).toEqual(copy);
    }
  });

  it("まとまりを曜日ごとの行に戻すと、まとまりの順に、中の曜日の順になる", () => {
    const groups = groupWeekly([row("TBS", 2, "08:30"), row("TBS", 1, "08:30"), row("TBS", 0, "10:00", "2026-10-20")]);
    expect(ungroupWeekly(groups)).toEqual([
      row("TBS", 1, "08:30"),
      row("TBS", 2, "08:30"),
      row("TBS", 0, "10:00", "2026-10-20"),
    ]);
  });

  it("当たらない日は、枠とその時刻を含む放送回の 1 行と、番組名の合う放送回の 1 行にする（時刻は放送日の時刻）", () => {
    expect(unmatchedDayLines(unmatched)).toEqual([
      "10/19（月）17:40 は放送回が無い。その時刻は「辛坊治郎 ズーム そこまで言うか！」（15:30〜18:00）",
      "番組名の合う放送回: 19:00〜19:30",
    ]);
    expect(unmatchedDayLines(unmatchedLate)).toEqual(["10/19（月）25:00 は放送回が無い。その時刻を含む放送回も番組表に無い", "番組名の合う放送回は無い"]);
  });

  it("次の放送予定（日本時間）。無ければ、7 日先まで番組表に無いと出す", () => {
    expect(nextAiringLabel("2026-10-11T23:30:00Z")).toBe("10/12(月) 08:30");
    // 放送回の開始なので、深夜は暦の翌日ではなく放送日と放送日の時刻で書く（ADR 0012）。
    expect(nextAiringLabel("2026-10-21T03:40:00+09:00")).toBe("10/20(火) 27:40");
    expect(nextAiringLabel(undefined)).toBe("7 日先まで番組表に無い");
    expect(nextAiringLabel("2026-10-11T23:30:00Z", false)).toMatch(/^無効/);
  });
});

describe("ShowsPage", () => {
  it("一覧に名前・有効／無効・毎週の予約の要約・次の放送予定を出す", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat, stopped] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
    });
    renderPage();
    const row = (await screen.findByText("パンサー向井のふらっと")).closest("tr")!;
    expect(within(row).getByText("TBSラジオ 月 08:30")).toBeInTheDocument();
    expect(within(row).getByText("TBSラジオ 月 10:00")).toBeInTheDocument();
    expect(within(row).getByText(/10\/12.*08:30/)).toBeInTheDocument();
    expect(within(row).getByRole("switch")).toBeChecked();
    const row2 = screen.getByText("無効な番組").closest("tr")!;
    expect(within(row2).getByText("無効")).toBeInTheDocument();
    expect(within(row2).getByText(/^無効（番組表の同期で当てない）/)).toBeInTheDocument();
    expect(within(row2).queryByText("7 日先まで番組表に無い")).not.toBeInTheDocument();
    expect(within(row2).getByRole("switch")).not.toBeChecked();
  });

  it("当たらない日を次の放送予定の下に放送日の順に全部出す（次の放送予定があってもなくても）。無効な番組には出さない", async () => {
    const withNext: Show = { ...flat, unmatched_days: [unmatched, unmatchedLate] };
    const noNext: Show = { ...flat, name: "チャリで30分", next_airing: undefined, unmatched_days: [unmatched] };
    const off: Show = { ...stopped, unmatched_days: [unmatched] };
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [withNext, noNext, off] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
    });
    renderPage();
    const row = (await screen.findByText(withNext.name)).closest("tr")!;
    const cell = within(row).getByText(/10\/12.*08:30/).closest("td")!;
    expect(cell.textContent).toMatch(/10\/12.*08:30.*10\/19（月）17:40 は放送回が無い.*19:00〜19:30.*10\/19（月）25:00 は放送回が無い/);
    const row2 = screen.getByText(noNext.name).closest("tr")!;
    expect(within(row2).getByText("7 日先まで番組表に無い")).toBeInTheDocument();
    expect(within(row2).getByText(/^10\/19（月）17:40 は放送回が無い。その時刻は「辛坊治郎/)).toBeInTheDocument();
    const row3 = screen.getByText(off.name).closest("tr")!;
    expect(within(row3).queryByText(/放送回が無い/)).not.toBeInTheDocument();
  });

  it("一覧は保存された順にかかわらず並べて出し、編集のフォームは開いたときに 1 回だけ並べる（分割番組の設定で変わらない）", async () => {
    const mk = (name: string, split: SplitMode): Show => ({
      ...flat,
      name,
      split,
      weekly: [
        { station: "TBS", weekday: 1, start: "08:30" },
        { station: "TBS", weekday: 2, start: "08:30" },
        { station: "TBS", weekday: 1, start: "10:00" },
        { station: "TBS", weekday: 0, start: "01:00" },
      ],
    });
    const a = mk("連結あり", "join");
    const b = mk("連結なし", "none");
    const expected = ["TBSラジオ 月・火 08:30", "TBSラジオ 月 10:00", "TBSラジオ 日 25:00"];
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [a, b] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(a.name)}`]: () => ({ status: 200, body: a }),
    });
    const user = userEvent.setup();
    renderPage();
    for (const r of [a, b]) {
      const row = (await screen.findByText(r.name)).closest("tr")!;
      const shown = within(row)
        .getAllByText(/^TBSラジオ /)
        .map((e) => e.textContent);
      expect(shown).toEqual(expected);
    }
    await user.click(await screen.findByRole("button", { name: `${a.name} を編集` }));
    const first = await screen.findByLabelText("予約 1");
    // 1 回の予約の欄の下に、足し方をいつも出す（1 回の予約が無くても、あっても）。
    expect(screen.getByText("番組表の「この回を録音」で足す。録音済みになると消える。")).toBeInTheDocument();
    const starts = () =>
      [1, 2, 3].map(
        (n) => (within(screen.getByLabelText(`予約 ${n}`)).getByLabelText(/開始（HH:MM）/) as HTMLInputElement).value,
      );
    // 開始時刻の欄は放送日の時刻で出す（API の 01:00 は 25:00）。
    expect(starts()).toEqual(["08:30", "10:00", "25:00"]);
    // 編集中は並べ直さない: 1 行目の時刻を変えても行は動かない。
    fireEvent.change(within(first).getByLabelText(/開始（HH:MM）/), { target: { value: "23:00" } });
    expect(starts()).toEqual(["23:00", "10:00", "25:00"]);
    // 1 枚目のカードは月・火の 2 曜日を、曜日ボタンの押した状態で出す。
    const pressed = within(first)
      .getAllByRole("button", { pressed: true })
      .map((b) => b.textContent);
    expect(pressed).toEqual(["月", "火"]);
  });

  it("有効／無効を切り替える", async () => {
    let enabled = false;
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [{ ...stopped, enabled }] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "PUT /api/shows/%E7%84%A1%E5%8A%B9%E3%81%AA%E7%95%AA%E7%B5%84/enabled": () => {
        enabled = true;
        return { status: 200, body: { enabled: true } };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: "無効な番組 を有効にする" }));
    await waitFor(() => expect(screen.getByRole("switch")).toBeChecked());
    expect(calls.find((c) => c.key.endsWith("/enabled"))?.body).toEqual({ enabled: true });
    // 有効にするときは確かめず、起きることをトーストで知らせる
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(calls.some((c) => c.key.startsWith("GET /api/jobs"))).toBe(false);
    expect(await screen.findByText(/有効にした。番組表を展開して、今日から先の録音予定のジョブを作る/)).toBeInTheDocument();
  });

  const waitingJob = (showName: string, id: number) => ({ id, showName, state: "waiting" });
  const flatEnabledPath = `/api/shows/${encodeURIComponent(flat.name)}/enabled`;

  it("無効にするときは確かめの Modal に消えるジョブの件数を出し、取消すと変わらない", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "GET /api/jobs?state=waiting": () => ({
        status: 200,
        body: { jobs: [waitingJob(flat.name, 1), waitingJob(flat.name, 2), waitingJob("別の番組", 3)] },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: `${flat.name} を有効にする` }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(`番組「${flat.name}」を無効にする。録音予定のジョブ 2 件が消える`);
    await user.click(within(dialog).getByRole("button", { name: "やめる" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(calls.some((c) => c.key.endsWith("/enabled"))).toBe(false);
    expect(screen.getByRole("switch")).toBeChecked();
  });

  it("確かめて無効にすると、消える件数をトーストで知らせる。0 件ならそう出す", async () => {
    let enabled = true;
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [{ ...flat, enabled }] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "GET /api/jobs?state=waiting": () => ({ status: 200, body: { jobs: [] } }),
      [`PUT ${flatEnabledPath}`]: () => {
        enabled = false;
        return { status: 200, body: { enabled: false } };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: `${flat.name} を有効にする` }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("消える録音予定のジョブは 0 件");
    await user.click(within(dialog).getByRole("button", { name: "無効にする" }));
    expect(await screen.findByText("無効にした。録音予定のジョブ 0 件を消す")).toBeInTheDocument();
    expect(calls.find((c) => c.key === `PUT ${flatEnabledPath}`)?.body).toEqual({ enabled: false });
    await waitFor(() => expect(screen.getByRole("switch")).not.toBeChecked());
  });

  it("件数を取れなくても、確かめて無効にできる（件数は出さない）", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "GET /api/jobs?state=waiting": () => ({ status: 500, body: { error: "内部エラー" } }),
      [`PUT ${flatEnabledPath}`]: () => ({ status: 200, body: { enabled: false } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: `${flat.name} を有効にする` }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("録音予定のジョブが消える");
    expect(dialog.textContent).not.toMatch(/\d+ 件/);
    await user.click(within(dialog).getByRole("button", { name: "無効にする" }));
    expect(await screen.findByText("無効にした。録音予定のジョブを消す")).toBeInTheDocument();
    expect(calls.some((c) => c.key === `PUT ${flatEnabledPath}`)).toBe(true);
  });

  it("無効にするのが拒まれたら、Alert を出しトーストは出さない", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "GET /api/jobs?state=waiting": () => ({ status: 200, body: { jobs: [] } }),
      [`PUT ${flatEnabledPath}`]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: `${flat.name} を有効にする` }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "無効にする" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("有効／無効を変えられない: 内部エラー");
    expect(screen.queryByText(/無効にした。/)).not.toBeInTheDocument();
  });

  it("件数を取っている間は、スイッチ・編集・削除を押せず、取得は 1 回で、押した番組の確かめだけが出る", async () => {
    const other: Show = { ...flat, name: "別の番組" };
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat, other] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "GET /api/jobs?state=waiting": () => ({ status: 200, body: { jobs: [waitingJob(flat.name, 1)] } }),
    });
    // 件数の取得だけ遅らせる
    const base = globalThis.fetch;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/jobs")) await gate;
      return base(url, init);
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: `${flat.name} を有効にする` }));
    await waitFor(() => expect(screen.getByRole("switch", { name: "別の番組 を有効にする" })).toBeDisabled());
    await user.click(screen.getByRole("switch", { name: "別の番組 を有効にする" }));
    await user.click(screen.getByRole("switch", { name: `${flat.name} を有効にする` }));
    expect(screen.getByRole("button", { name: `${flat.name} を編集` })).toBeDisabled();
    expect(screen.getByRole("button", { name: `${flat.name} を消す` })).toBeDisabled();
    release();
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(`番組「${flat.name}」を無効にする。録音予定のジョブ 1 件が消える`);
    expect(calls.filter((c) => c.key.startsWith("GET /api/jobs"))).toHaveLength(1);
  });

  const names = () =>
    screen
      .getAllByRole("row")
      .slice(1)
      .map((r) => r.querySelector("td")?.textContent?.replace(/\s*無効$/, "").trim());
  const mk = (name: string, next?: string, enabled = true): Show => ({ ...stopped, name, enabled, next_airing: next });

  it("既定は次の放送予定の近い順で、無い番組は下（名前順）。見出しで昇順→降順→既定と並べ替わり、無い番組は下のまま", async () => {
    mockApi({
      "GET /api/shows": () => ({
        status: 200,
        body: [
          mk("い-なし"),
          mk("う-遅い", "2026-10-20T08:00:00+09:00"),
          mk("あ-無効", "2026-10-12T08:00:00+09:00", false),
          mk("え-早い", "2026-10-15T08:00:00+09:00"),
          mk("あ-なし"),
        ],
      }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
    });
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("え-早い");
    expect(names()).toEqual(["え-早い", "う-遅い", "あ-なし", "あ-無効", "い-なし"]);

    const nextTh = () => screen.getByRole("columnheader", { name: /次の放送予定/ });
    await user.click(within(nextTh()).getByRole("button"));
    expect(nextTh()).toHaveAttribute("aria-sort", "ascending");
    expect(names()).toEqual(["え-早い", "う-遅い", "あ-なし", "あ-無効", "い-なし"]);
    await user.click(within(nextTh()).getByRole("button"));
    expect(nextTh()).toHaveAttribute("aria-sort", "descending");
    expect(names()).toEqual(["う-遅い", "え-早い", "あ-なし", "あ-無効", "い-なし"]);
    await user.click(within(nextTh()).getByRole("button"));
    expect(nextTh()).toHaveAttribute("aria-sort", "none");
    expect(names()).toEqual(["え-早い", "う-遅い", "あ-なし", "あ-無効", "い-なし"]);

    const nameTh = () => screen.getByRole("columnheader", { name: /名前/ });
    await user.click(within(nameTh()).getByRole("button"));
    expect(names()).toEqual(["あ-なし", "あ-無効", "い-なし", "う-遅い", "え-早い"]);
    await user.click(within(nameTh()).getByRole("button"));
    expect(names()).toEqual(["え-早い", "う-遅い", "い-なし", "あ-無効", "あ-なし"]);
  });

  it("保存と削除の成功をトーストで知らせる", async () => {
    let list: Show[] = [flat];
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: list }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
      [`DELETE /api/shows/${encodeURIComponent(flat.name)}`]: () => {
        list = [];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    await screen.findByLabelText(/番組の名前/);
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText(`番組「${flat.name}」を保存した`)).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を消す` }));
    await user.click(await screen.findByRole("button", { name: "消す" }));
    expect(await screen.findByText(`番組「${flat.name}」を消した`)).toBeInTheDocument();
  });

  it("分割番組のチェックがオンのときだけ「パートを 1 つのファイルにつなぐ」を出し、2 つのチェックから split を決める", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const saved = async () => {
      await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
      await screen.findByLabelText(/番組の名前/);
      return {
        split: () => screen.getByRole("checkbox", { name: "分割番組（1 回の放送が番組表で複数のパートに分かれて載る）" }),
        join: () => screen.queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" }),
        save: async () => {
          const n = calls.filter((c) => c.key.startsWith("PUT ")).length;
          await user.click(screen.getByRole("button", { name: "保存" }));
          await waitFor(() => expect(calls.filter((c) => c.key.startsWith("PUT ")).length).toBe(n + 1));
          return (calls.filter((c) => c.key.startsWith("PUT ")).pop()!.body as Show).split;
        },
      };
    };
    // 分割番組でつなぐ番組は、2 つともオン。子を外すと "separate"。
    let form = await saved();
    expect(form.split()).toBeChecked();
    expect(form.join()).toBeChecked();
    expect(screen.getByText("オフなら、パートごとに -1・-2 のファイルにする")).toBeInTheDocument();
    await user.click(form.join()!);
    expect(await form.save()).toBe("separate");
    // 親を外すと子は消え、"none"。
    form = await saved();
    await user.click(form.split());
    expect(form.join()).toBeNull();
    expect(await form.save()).toBe("none");
    // 親を入れ直すと、子は既定のオンで出て "join"。
    form = await saved();
    await user.click(form.split());
    await user.click(form.split());
    expect(form.join()).toBeChecked();
    expect(await form.save()).toBe("join");
    // 古いキーの join は送らない。
    expect(calls.filter((c) => c.key.startsWith("PUT ")).every((c) => !("join" in (c.body as object)))).toBe(true);
  });

  it("作成フォームの言葉は画面の言葉で、英語のキー名を出さない", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
    });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByRole("heading", { name: "番組の一覧" })).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "番組を作る" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(/番組の名前（保存先のフォルダ名になる）/)).toBeInTheDocument();
    expect(within(dialog).getByRole("checkbox", { name: "分割番組（1 回の放送が番組表で複数のパートに分かれて載る）" })).not.toBeChecked();
    expect(
      within(dialog).getByText("同じ日のパートを 1 回の放送として扱う。オフなら、同じ日に 2 回当たっても別々のファイルにする"),
    ).toBeInTheDocument();
    // 「パートを 1 つのファイルにつなぐ」は、分割番組のときだけ出す。
    expect(within(dialog).queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeNull();
    expect(within(dialog).getByRole("checkbox", { name: "番組名が変わった回は録らない" })).toBeInTheDocument();
    expect(within(dialog).getByText("録音の通知にも番組名の食い違いを添える。毎週の予約だけに当たる回が対象")).toBeInTheDocument();
    expect(within(dialog).getByText("毎週の予約")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "毎週の予約を足す" })).toBeInTheDocument();
    expect(within(dialog).queryByText(/開始日（放送日）を入れると/)).toBeNull();
    expect(dialog.textContent).not.toMatch(/join|split|check_title|ルール|単発予約/);
  });

  it("番組を作る。局・曜日・開始時刻から名前の欄を埋める", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      "GET /api/show-name?station=TBS&weekday=1&start=08%3A30": () => ({
        status: 200,
        body: { found: true, day: "2026-10-12", name: "パンサー向井のふらっと", title: "パンサー向井のふらっと（１）" },
      }),
      "GET /api/show-name?station=TBS&weekday=1&start=09%3A00": () => ({
        status: 200,
        body: { found: false, day: "2026-10-12" },
      }),
      "POST /api/shows": () => ({ status: 201, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "番組を作る" }));
    await user.click(await screen.findByRole("button", { name: "毎週の予約を足す" }));
    const weekly = screen.getByLabelText("予約 1");
    // 足せる予約は「毎週の予約」だけで、種類を選ぶ欄と「番組名で探す」は無い。
    expect(screen.queryByRole("button", { name: "「番組名で探す」を足す" })).toBeNull();
    expect(within(weekly).queryByRole("combobox", { name: "種類" })).toBeNull();
    await user.click(within(weekly).getByRole("combobox", { name: "局" }));
    await user.click(await screen.findByRole("option", { name: "TBSラジオ（TBS）", hidden: true }));
    // 足したカードは曜日を 1 つも選んでいない。
    expect(within(weekly).queryAllByRole("button", { pressed: true })).toHaveLength(0);
    await user.click(within(weekly).getByRole("button", { name: "月" }));

    // 見つからなければそう出し、名前は空のまま。
    await user.type(within(weekly).getByLabelText(/開始（HH:MM）/), "09:00");
    await user.click(within(weekly).getByRole("button", { name: "番組表から名前を入れる" }));
    expect(await within(weekly).findByRole("status")).toHaveTextContent("09:00 の放送回が無い");
    expect(screen.getByLabelText(/番組の名前/)).toHaveValue("");

    await user.clear(within(weekly).getByLabelText(/開始（HH:MM）/));
    await user.type(within(weekly).getByLabelText(/開始（HH:MM）/), "08:30");
    await user.click(within(weekly).getByRole("button", { name: "番組表から名前を入れる" }));
    await waitFor(() => expect(screen.getByLabelText(/番組の名前/)).toHaveValue("パンサー向井のふらっと"));

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key === "POST /api/shows")).toBe(true));
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).toEqual({
      name: "パンサー向井のふらっと",
      enabled: true,
      split: "none",
      check_title: true,
      weekly: [{ station: "TBS", weekday: 1, start: "08:30" }],
    });
  });

  it("番組表から名前を入れるときは、開始の放送日の時刻を API の値に戻して探す", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [] }),
      "GET /api/stations": () => ({ status: 200, body: [{ id: "LFR", name: "ニッポン放送" }] }),
      "GET /api/show-name?station=LFR&weekday=6&start=01%3A00": () => ({
        status: 200,
        body: { found: true, day: "2026-10-17", name: "オードリーのオールナイトニッポン", title: "オードリーのオールナイトニッポン" },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "番組を作る" }));
    await user.click(await screen.findByRole("button", { name: "毎週の予約を足す" }));
    const weekly = screen.getByLabelText("予約 1");
    await user.click(within(weekly).getByRole("combobox", { name: "局" }));
    await user.click(await screen.findByRole("option", { name: "ニッポン放送（LFR）", hidden: true }));
    await user.click(within(weekly).getByRole("button", { name: "土" }));
    await user.type(within(weekly).getByLabelText(/開始（HH:MM）/), "01:00");
    await user.click(within(weekly).getByRole("button", { name: "番組表から名前を入れる" }));
    expect(await within(weekly).findByRole("status")).toHaveTextContent("前の曜日の 24:00〜28:59 で入れる");
    expect(calls.some((c) => c.key.startsWith("GET /api/show-name"))).toBe(false);
    await user.clear(within(weekly).getByLabelText(/開始（HH:MM）/));
    await user.type(within(weekly).getByLabelText(/開始（HH:MM）/), "25:00");
    await user.click(within(weekly).getByRole("button", { name: "番組表から名前を入れる" }));
    await waitFor(() => expect(screen.getByLabelText(/番組の名前/)).toHaveValue("オードリーのオールナイトニッポン"));
  });

  it("名前を変えると、録音済みでないジョブが付け替わることと、保存済みのファイルが古いフォルダに残ることを知らせ、古い名前に PUT する", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [{ ...flat, unmatched_days: [unmatched] }] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const name = await screen.findByLabelText(/番組の名前/);
    expect(screen.queryByText(/古い名前のフォルダに残る/)).not.toBeInTheDocument();
    await user.clear(name);
    await user.type(name, "ふらっと（新）");
    expect(screen.getByText(/古い名前のフォルダに残る/)).toBeInTheDocument();
    expect(screen.getByText(/録音済みでないジョブ.*新しい名前に付け替わって残る/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(true));
    const sent = calls.find((c) => c.key.startsWith("PUT"))?.body as Show;
    expect(sent.name).toBe("ふらっと（新）");
    expect(sent.weekly).toEqual(flat.weekly);
    expect(sent).not.toHaveProperty("next_airing");
    expect(sent).not.toHaveProperty("unmatched_days");
  });

  it("毎週の予約の開始日を、書き換えも空にもできる", async () => {
    const withFrom: Show = {
      ...flat,
      weekly: [
        { station: "TBS", weekday: 1, start: "08:30", from: "2099-10-12" },
        { station: "TBS", weekday: 1, start: "10:00", from: "2099-10-12" },
      ],
    };
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [withFrom] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: withFrom }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findAllByText(/（2099-10-12 から）/)).toHaveLength(2);
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const first = await screen.findByLabelText("予約 1");
    const second = screen.getByLabelText("予約 2");
    expect(within(first).getByLabelText(/開始日/)).toHaveValue("2099-10-12");
    fireEvent.change(within(first).getByLabelText(/開始日/), { target: { value: "2099-10-19" } });
    fireEvent.change(within(second).getByLabelText(/開始日/), { target: { value: "" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(true));
    const sent = calls.find((c) => c.key.startsWith("PUT"))?.body as Show;
    expect(sent.weekly[0].from).toBe("2099-10-19");
    expect(sent.weekly[1].from ?? "").toBe("");
  });

  it("毎週の予約の終了日を書け、空にもでき、要約に出る", async () => {
    const withUntil: Show = {
      ...flat,
      weekly: [
        { station: "TBS", weekday: 1, start: "08:30", until: "2099-12-28" },
        { station: "TBS", weekday: 1, start: "10:00" },
      ],
    };
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [withUntil] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: withUntil }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText(/（2099-12-28 まで）/)).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const first = await screen.findByLabelText("予約 1");
    const second = screen.getByLabelText("予約 2");
    expect(within(first).getByLabelText(/終了日/)).toHaveValue("2099-12-28");
    fireEvent.change(within(first).getByLabelText(/終了日/), { target: { value: "" } });
    fireEvent.change(within(second).getByLabelText(/終了日/), { target: { value: "2099-12-21" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(true));
    const sent = calls.find((c) => c.key.startsWith("PUT"))?.body as Show;
    expect(sent.weekly[0].until ?? "").toBe("");
    expect(sent.weekly[1].until).toBe("2099-12-21");
  });

  it("終了日が開始日より前のカードは、保存せずエラーを出す。同じ日なら保存する", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const first = await screen.findByLabelText("予約 1");
    fireEvent.change(within(first).getByLabelText(/開始日/), { target: { value: "2099-10-19" } });
    fireEvent.change(within(first).getByLabelText(/終了日/), { target: { value: "2099-10-18" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await within(first).findByText("終了日は開始日と同じ日か、それより後にする")).toBeInTheDocument();
    expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(false);
    // 同じ日に直せば保存する。
    fireEvent.change(within(first).getByLabelText(/終了日/), { target: { value: "2099-10-19" } });
    expect(within(first).queryByText("終了日は開始日と同じ日か、それより後にする")).toBeNull();
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(true));
    const sent = calls.find((c) => c.key.startsWith("PUT"))?.body as Show;
    expect(sent.weekly[0]).toMatchObject({ from: "2099-10-19", until: "2099-10-19" });
  });

  it("保存すると、カードを曜日ごとの行に戻して送る（カードの順に、中の曜日の順）", async () => {
    const band: Show = {
      ...flat,
      weekly: [3, 1, 2].map((d) => ({ station: "TBS", weekday: d, start: "08:30" })).concat([{ station: "TBS", weekday: 0, start: "01:00" }]),
    };
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [band] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: band }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const first = await screen.findByLabelText("予約 1");
    await user.click(within(first).getByRole("button", { name: "水" }));
    await user.click(within(first).getByRole("button", { name: "金" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(true));
    const sent = calls.find((c) => c.key.startsWith("PUT"))?.body as Show;
    expect(sent.weekly).toEqual([
      { station: "TBS", weekday: 1, start: "08:30" },
      { station: "TBS", weekday: 2, start: "08:30" },
      { station: "TBS", weekday: 5, start: "08:30" },
      { station: "TBS", weekday: 0, start: "01:00" },
    ]);
  });

  it("開始に 24:00〜28:59 を入れて保存すると、API には 00:00〜04:59 で送る", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const second = await screen.findByLabelText("予約 2");
    fireEvent.change(within(second).getByLabelText(/開始（HH:MM）/), { target: { value: "25:00" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(true));
    const sent = calls.find((c) => c.key.startsWith("PUT"))?.body as Show;
    expect(sent.weekly).toEqual([
      { station: "TBS", weekday: 1, start: "08:30" },
      { station: "TBS", weekday: 1, start: "01:00" },
    ]);
  });

  it("開始に 00:00〜04:59 を入れると、保存せず、前の曜日の 24:00〜28:59 で入れるよう欄に出す", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    // 案内の文は、曜日が放送日の曜日で、開始が 05:00〜28:59 であることを書く。
    expect(
      await screen.findByText("曜日は 5 時で区切った放送日の曜日で、開始は 05:00〜28:59（深夜 1 時の放送は前の曜日の 25:00）。"),
    ).toBeInTheDocument();
    const second = screen.getByLabelText("予約 2");
    const start = within(second).getByLabelText(/開始（HH:MM）/);
    fireEvent.change(start, { target: { value: "01:00" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(
      await within(second).findByText("深夜 0〜4 時台の放送は、前の曜日の 24:00〜28:59 で入れる（例: 月曜の深夜 1 時 → 日 25:00）"),
    ).toBeInTheDocument();
    expect(start).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(false);
    // 29:00 のように 05:00〜28:59 の外も保存しない。
    fireEvent.change(start, { target: { value: "29:00" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await within(second).findByText("開始は 05:00〜28:59 の HH:MM で入れてほしい")).toBeInTheDocument();
    expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(false);
  });

  it("曜日を 1 つも選んでいないカードがあれば、保存せずそのカードにエラーを出す", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    await screen.findByLabelText("予約 1");
    await user.click(screen.getByRole("button", { name: "毎週の予約を足す" }));
    const added = screen.getByLabelText("予約 3");
    fireEvent.change(within(added).getByLabelText(/開始（HH:MM）/), { target: { value: "12:00" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await within(added).findByRole("alert")).toHaveTextContent("曜日を 1 つ以上選んで");
    expect(within(screen.getByLabelText("予約 1")).queryByRole("alert")).toBeNull();
    expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(false);
  });

  it("同じ局・開始時刻・開始日のカードどうしで曜日が重なれば、保存せずエラーを出す", async () => {
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: flat }),
      [`PUT /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    // 2 枚目（月 10:00）の時刻を 1 枚目（月 08:30）と同じにすると、月が重なる。
    const second = await screen.findByLabelText("予約 2");
    fireEvent.change(within(second).getByLabelText(/開始（HH:MM）/), { target: { value: "08:30" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await within(second).findByRole("alert")).toHaveTextContent("同じ局・曜日・開始の予約が重なっている");
    expect(calls.some((c) => c.key.startsWith("PUT"))).toBe(false);
  });

  it("保存に失敗したら、フォームにエラーを出す", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [] }),
      "GET /api/stations": () => ({ status: 502, body: { error: "radiko から局の一覧を取れない" } }),
      "POST /api/shows": () => ({ status: 409, body: { error: "同じ名前の番組がもうある" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "番組を作る" }));
    await user.type(await screen.findByLabelText(/番組の名前/), "x");
    // 局の一覧を取れなければ、局 ID を手で打つ。
    await user.click(screen.getByRole("button", { name: "毎週の予約を足す" }));
    await user.type(screen.getByLabelText(/局 ID/), "TBS");
    await user.type(screen.getByLabelText(/開始（HH:MM）/), "08:30");
    await user.click(screen.getByRole("button", { name: "月" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("同じ名前の番組がもうある");
  });

  it("有効／無効の切り替えが拒まれたら、エラーを出し、スイッチは元のまま", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [stopped] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`PUT /api/shows/${encodeURIComponent(stopped.name)}/enabled`]: () => ({
        status: 404,
        body: { error: "番組が無い" },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("switch", { name: "無効な番組 を有効にする" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("有効／無効を変えられない: 番組が無い");
    expect(screen.getByRole("switch")).not.toBeChecked();
  });

  it("削除が拒まれたら、エラーを出し、一覧に残す", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`DELETE /api/shows/${encodeURIComponent(flat.name)}`]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を消す` }));
    await user.click(await screen.findByRole("button", { name: "消す" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("番組を消せない: 内部エラー");
    expect(screen.getByText(flat.name)).toBeInTheDocument();
  });

  it("削除は確認を挟む", async () => {
    let list = [flat];
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: list }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`DELETE /api/shows/${encodeURIComponent(flat.name)}`]: () => {
        list = [];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を消す` }));
    expect(calls.some((c) => c.key.startsWith("DELETE"))).toBe(false);
    await user.click(await screen.findByRole("button", { name: "消す" }));
    expect(await screen.findByText("番組がまだ無い。")).toBeInTheDocument();
    expect(calls.some((c) => c.key.startsWith("DELETE"))).toBe(true);
  });

  const flatPath = `/api/shows/${encodeURIComponent(flat.name)}`;
  const once = {
    id: 7,
    stationId: "TBS",
    stationName: "TBSラジオ",
    start: "2026-10-07T13:00:00+09:00",
    end: "2026-10-07T14:00:00+09:00",
    title: "すっぴん",
    missing: false,
  };
  const gone = { id: 8, stationId: "TBS", start: "2026-10-08T13:00:00+09:00", missing: true };

  it("番組の編集画面に 1 回の予約の一覧が出て、消せる。番組表に無いものには印が付く", async () => {
    let onces = [once, gone];
    const calls = mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${flatPath}`]: () => ({ status: 200, body: { ...flat, onces } }),
      [`DELETE ${flatPath}/onces/7`]: () => {
        onces = [gone];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const list = await screen.findByLabelText("1 回の予約");
    expect(await within(list).findByText(/10\/7.*水.*13:00 TBSラジオ すっぴん/)).toBeInTheDocument();
    expect(within(list).getAllByText("番組表に無い")).toHaveLength(1);
    // 有効な番組には、当たらないという知らせを出さない。
    expect(within(list).queryByRole("status")).not.toBeInTheDocument();

    await user.click(within(list).getByRole("button", { name: /^1 回の予約 10\/7.*13:00 TBSラジオ を消す$/ }));
    await waitFor(() => expect(within(list).queryByText(/すっぴん/)).not.toBeInTheDocument());
    expect(calls.some((c) => c.key === `DELETE ${flatPath}/onces/7`)).toBe(true);
    expect(within(list).getByText(/10\/8/)).toBeInTheDocument();
  });

  it("1 回の予約の開始は、放送日と放送日の時刻で出す（深夜は暦の翌日にしない）", async () => {
    const late = { ...once, start: "2026-10-21T03:40:00+09:00", end: "2026-10-21T04:00:00+09:00", title: "深夜の番組" };
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${flatPath}`]: () => ({ status: 200, body: { ...flat, onces: [late] } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const list = await screen.findByLabelText("1 回の予約");
    expect(await within(list).findByText(/^10\/20\(火\) 27:40 TBSラジオ 深夜の番組/)).toBeInTheDocument();
    expect(within(list).queryByText(/03:40/)).toBeNull();
  });

  it("キーワードが足した 1 回の予約には、「キーワード『○○』が足した」の印が付く。人が足したものには付かない", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${flatPath}`]: () => ({
        status: 200,
        body: { ...flat, onces: [{ ...once, keywordId: 3, keywordWords: "向井 ゲスト" }, { ...gone, id: 9, missing: false }] },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const list = await screen.findByLabelText("1 回の予約");
    expect(await within(list).findAllByText("キーワード『向井 ゲスト』が足した")).toHaveLength(1);
  });

  it("予約の無い番組（毎週も 1 回も無い）は分けて畳み、開くと出る。1 回の予約だけある番組は一覧に残る", async () => {
    const empty: Show = { ...stopped, name: "空の番組", enabled: true };
    const onlyOnce: Show = { ...stopped, name: "1回だけの番組", enabled: true };
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat, empty, onlyOnce] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(empty.name)}`]: () => ({ status: 200, body: empty }),
      [`GET /api/shows/${encodeURIComponent(onlyOnce.name)}`]: () => ({ status: 200, body: { ...onlyOnce, onces: [once] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const toggle = await screen.findByRole("button", { name: /予約の無い番組（1 件）/ });
    expect(screen.getByText(flat.name)).toBeInTheDocument();
    expect(screen.getByText(onlyOnce.name)).toBeInTheDocument();
    expect(screen.queryByText(empty.name)).not.toBeInTheDocument();
    await user.click(toggle);
    expect(await screen.findByText(empty.name)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /予約の無い番組（1 件）/ }));
    expect(screen.queryByText(empty.name)).not.toBeInTheDocument();
  });

  it("1 回の予約を読めなかった番組は、畳まずに一覧に残す", async () => {
    const empty: Show = { ...stopped, name: "空の番組", enabled: true };
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [empty] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(empty.name)}`]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    renderPage();
    expect(await screen.findByText(empty.name)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /予約の無い番組/ })).not.toBeInTheDocument();
  });

  it("予約の無い番組だけのとき、行の無い表は出さず、見出しの下に畳む", async () => {
    const empty: Show = { ...stopped, name: "空の番組", enabled: true };
    const user = userEvent.setup();
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [empty] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET /api/shows/${encodeURIComponent(empty.name)}`]: () => ({ status: 200, body: empty }),
    });
    renderPage();
    const toggle = await screen.findByRole("button", { name: /予約の無い番組（1 件）/ });
    expect(screen.queryAllByRole("table")).toHaveLength(0);
    await user.click(toggle);
    expect(await screen.findAllByRole("table")).toHaveLength(1);
  });

  it("編集ダイアログで 1 回の予約を消すだけでも、閉じると畳み方と件数を今の状態に合わせる", async () => {
    const solo: Show = { ...stopped, name: "1回だけの番組", enabled: true };
    const path = `/api/shows/${encodeURIComponent(solo.name)}`;
    let onces = [once];
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [solo] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${path}`]: () => ({ status: 200, body: { ...solo, onces } }),
      [`DELETE ${path}/onces/7`]: () => {
        onces = [];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("1 回の予約 1 件")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: `${solo.name} を編集` }));
    const list = await screen.findByLabelText("1 回の予約");
    await user.click(await within(list).findByRole("button", { name: /を消す$/ }));
    await waitFor(() => expect(within(list).queryByText(/すっぴん/)).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "やめる" }));
    expect(await screen.findByRole("button", { name: /予約の無い番組（1 件）/ })).toBeInTheDocument();
    expect(screen.queryByText("1 回の予約 1 件")).not.toBeInTheDocument();
  });

  it("無効な番組には、1 回の予約も当たらないことを知らせる", async () => {
    const path = `/api/shows/${encodeURIComponent(stopped.name)}`;
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [stopped] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${path}`]: () => ({ status: 200, body: { ...stopped, onces: [once] } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${stopped.name} を編集` }));
    const list = await screen.findByLabelText("1 回の予約");
    expect(await within(list).findByRole("status")).toHaveTextContent("1 回の予約も放送回に当たらない");
  });

  it("1 回の予約が無ければそう出す。読めなければエラーを出す", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat, stopped] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${flatPath}`]: () => ({ status: 200, body: flat }),
      [`GET /api/shows/${encodeURIComponent(stopped.name)}`]: () => ({ status: 502, body: { error: "取れない" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    expect(await screen.findByText("1 回の予約は無い。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "やめる" }));
    await user.click(await screen.findByRole("button", { name: `${stopped.name} を編集` }));
    expect(await screen.findByText(/番組を読めない: 取れない/)).toBeInTheDocument();
  });

  it("1 回の予約を消せなければ、エラーを出して一覧に残す", async () => {
    mockApi({
      "GET /api/shows": () => ({ status: 200, body: [flat] }),
      "GET /api/stations": () => ({ status: 200, body: stations }),
      [`GET ${flatPath}`]: () => ({ status: 200, body: { ...flat, onces: [once] } }),
      [`DELETE ${flatPath}/onces/7`]: () => ({ status: 404, body: { error: "1 回の予約が無い" } }),
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: `${flat.name} を編集` }));
    const list = await screen.findByLabelText("1 回の予約");
    await user.click(await within(list).findByRole("button", { name: /を消す$/ }));
    expect(await within(list).findByRole("alert")).toHaveTextContent("1 回の予約を消せない: 1 回の予約が無い");
    expect(within(list).getByText(/すっぴん/)).toBeInTheDocument();
  });
});
