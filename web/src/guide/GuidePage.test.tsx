import { MantineProvider } from "@mantine/core";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Show } from "../shows/api";
import { OnceConflict, type GuideAiring, type GuideStation } from "./api";
import {
  addDays,
  airingDayRange,
  broadcastClock,
  broadcastDayOf,
  broadcastRange,
  dayLabel,
  titleKey,
  trimTrailingMark,
  weekdayOf,
} from "./format";
import { matchRanges } from "./highlight";
import { GuidePage } from "./GuidePage";
import { autoGroup, conflictMessage } from "./reserve";

type Reply = { status: number; body?: unknown };
type Handler = (init?: RequestInit) => Reply | Promise<Reply>;

function mockApi(routes: Record<string, Handler>) {
  const calls: { key: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? "GET"} ${url}`;
      calls.push({ key, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      const h = routes[key];
      if (!h) throw new Error(`想定外の呼び出し: ${key}`);
      const { status, body } = await h(init);
      return new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status });
    }),
  );
  return calls;
}

/** 画面の幅（wide なら PC 幅）と、タッチの操作か（touch なら `(pointer: coarse)` に当たる）。 */
function setWide(wide: boolean, touch = false) {
  window.matchMedia = ((query: string) => ({
    matches: (wide && query.includes("min-width")) || (touch && query.includes("pointer: coarse")),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
}

const today = broadcastDayOf(new Date());
const at = (day: string, hhmm: string) => `${day}T${hhmm}:00+09:00`;

function airing(over: Partial<GuideAiring> & Pick<GuideAiring, "title" | "start" | "end">): GuideAiring {
  return {
    stationId: "TBS",
    stationName: "TBSラジオ",
    performer: "",
    description: "",
    descriptionParts: [],
    state: "none",
    notAvailable: false,
    ...over,
  };
}

const flat1 = airing({
  title: "パンサー向井のふらっと(1)",
  start: at(today, "08:30"),
  end: at(today, "09:00"),
  performer: "向井慧",
  description: "朝の番組",
  descriptionParts: [{ text: "朝の番組" }],
});
const linked = airing({
  title: "リンクの番組",
  start: at(today, "16:00"),
  end: at(today, "17:00"),
  description: "詳細はこちら と a@example.com まで",
  descriptionParts: [
    { text: "詳細は" },
    { text: "こちら", href: "https://example.com/a?x=1&y=2" },
    { text: " と " },
    { text: "a@example.com", href: "mailto:a@example.com" },
    { text: " まで" },
  ],
});
const flat2 = airing({ title: "パンサー向井のふらっと(2)", start: at(today, "09:00"), end: at(today, "09:30") });
const plannedA = airing({
  title: "番組R",
  start: at(today, "10:00"),
  end: at(today, "11:00"),
  state: "planned",
  showName: "番組Rの番組",
});
const doneA = airing({ title: "番組D", start: at(today, "11:00"), end: at(today, "12:00"), state: "done" });
const missedA = airing({ title: "番組M", start: at(today, "12:00"), end: at(today, "13:00"), state: "missed" });
const ngA = airing({ title: "番組N", start: at(today, "13:00"), end: at(today, "14:00"), notAvailable: true });
// 同じ名前の番組がある放送回と、同じ名前の番組が無効な放送回
const morning = airing({ title: "朝の番組", start: at(today, "14:00"), end: at(today, "15:00") });
const stopped = airing({ title: "無効な番組", start: at(today, "15:00"), end: at(today, "16:00") });
const lfr = airing({
  title: "オールナイト",
  stationId: "LFR",
  stationName: "ニッポン放送",
  start: at(today, "13:00"),
  end: at(today, "14:00"),
});

const stations: GuideStation[] = [
  { id: "TBS", name: "TBSラジオ", airings: [flat1, flat2, plannedA, doneA, missedA, ngA, morning, stopped] },
  { id: "LFR", name: "ニッポン放送", airings: [lfr] },
];

const guideKey = (day: string) => `GET /api/guide?day=${day}`;
const dayRoute: Record<string, Handler> = { [guideKey(today)]: () => ({ status: 200, body: { day: today, stations } }) };

const existingShow: Show = {
  name: "朝の番組",
  enabled: true,
  split: "none",
  check_title: true,
  weekly: [{ station: "TBS", weekday: 1, start: "07:00" }],
};
const disabledShow: Show = { ...existingShow, name: "無効な番組", enabled: false, weekly: [] };
const showsRoute: Record<string, Handler> = {
  "GET /api/shows": () => ({ status: 200, body: [existingShow, disabledShow] }),
};

const enc = encodeURIComponent;
const showKey = (method: string, name: string) => `${method} /api/shows/${enc(name)}`;
const onces = (name: string) => `POST /api/shows/${enc(name)}/onces`;
const created = (id: number, a: GuideAiring) => ({ id, stationId: a.stationId, start: a.start, missing: false });

const renderPage = (onOpenShow?: (name: string) => void, onCreateKeyword?: (words: string) => void) =>
  render(
    <MantineProvider env="test">
      <GuidePage onOpenShow={onOpenShow} onCreateKeyword={onCreateKeyword} />
    </MantineProvider>,
  );

beforeEach(() => {
  window.localStorage.clear();
  setWide(false);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("format", () => {
  it("放送日は 5 時で区切る", () => {
    expect(broadcastDayOf("2026-10-12T04:59:00+09:00")).toBe("2026-10-11");
    expect(broadcastDayOf("2026-10-12T05:00:00+09:00")).toBe("2026-10-12");
    expect(addDays("2026-10-31", 1)).toBe("2026-11-01");
  });

  it("放送日に添える時刻は放送日の時刻で、終わりは開始と同じ放送日を基準にする", () => {
    const cases: { start: string; end: string; want: string }[] = [
      { start: "2026-10-15T21:30:00+09:00", end: "2026-10-15T22:00:00+09:00", want: "21:30〜22:00" },
      { start: "2026-10-15T23:00:00+09:00", end: "2026-10-16T01:00:00+09:00", want: "23:00〜25:00" },
      { start: "2026-10-16T01:00:00+09:00", end: "2026-10-16T03:00:00+09:00", want: "25:00〜27:00" },
      { start: "2026-10-16T04:30:00+09:00", end: "2026-10-16T05:30:00+09:00", want: "28:30〜29:30" },
      { start: "2026-10-16T05:00:00+09:00", end: "2026-10-16T06:00:00+09:00", want: "05:00〜06:00" },
    ];
    for (const c of cases) expect(broadcastRange(c), c.start).toBe(c.want);
    expect(broadcastClock("2026-10-16T01:00:00+09:00", "2026-10-15")).toBe("25:00");
    // 番組表の詳細も、暦の日付ではなく開始の放送日を添えた放送日の時刻で書く（ADR 0012）。
    expect(airingDayRange({ start: "2026-10-15T23:00:00+09:00", end: "2026-10-16T01:00:00+09:00" })).toBe("10/15(木) 23:00〜25:00");
    expect(airingDayRange({ start: "2026-10-16T01:00:00+09:00", end: "2026-10-16T03:00:00+09:00" })).toBe("10/15(木) 25:00〜27:00");
    expect(airingDayRange({ start: "2026-10-21T03:40:00+09:00", end: "2026-10-21T04:00:00+09:00" })).toBe("10/20(火) 27:40〜28:00");
  });

  it("番組名の末尾の印を落とす", () => {
    expect(trimTrailingMark("ふらっと(2)")).toBe("ふらっと");
    expect(trimTrailingMark("ふらっと　（２）")).toBe("ふらっと");
    expect(trimTrailingMark("ふらっと 8時～9時")).toBe("ふらっと");
    expect(trimTrailingMark("ふらっと 12時～12時55分")).toBe("ふらっと");
    expect(trimTrailingMark("ふらっと 12時30分～13時")).toBe("ふらっと");
    expect(trimTrailingMark("ふらっと　１２時３０分〜１２時５５分")).toBe("ふらっと");
    expect(titleKey("ふらっと(1)")).toBe(titleKey("ふらっと (2)"));
  });
});

describe("GuidePage の番組表", () => {
  it("スマホ幅では局を選んだ時刻順のリスト、選んだ局は覚える", async () => {
    mockApi(dayRoute);
    const user = userEvent.setup();
    renderPage();
    const list = await screen.findByTestId("guide-list");
    expect(within(list).getAllByRole("button")).toHaveLength(8);
    expect(screen.queryByTestId("guide-grid")).toBeNull();

    await user.click(screen.getByRole("combobox", { name: "局" }));
    await user.click(await screen.findByRole("option", { name: "ニッポン放送", hidden: true }));
    expect(within(await screen.findByTestId("guide-list")).getAllByRole("button")).toHaveLength(1);
    expect(window.localStorage.getItem("rajikura.guide.station")).toBe("LFR");
  });

  it("覚えた局があれば、それを先に出す", async () => {
    window.localStorage.setItem("rajikura.guide.station", "LFR");
    mockApi(dayRoute);
    renderPage();
    const list = await screen.findByTestId("guide-list");
    expect(within(list).getByText("オールナイト")).toBeInTheDocument();
  });

  it("PC 幅では局 × 時間のグリッド", async () => {
    setWide(true);
    mockApi(dayRoute);
    renderPage();
    const grid = await screen.findByTestId("guide-grid");
    expect(screen.queryByTestId("guide-list")).toBeNull();
    expect(within(grid).getByText("ニッポン放送")).toBeInTheDocument();
    // 放送回 9 つと、局の見出しの局名と × が 2 つずつ
    expect(within(grid).getAllByRole("button")).toHaveLength(13);
  });

  it("放送日を切り替えると、その日の番組表を読み直す", async () => {
    const yesterday = addDays(today, -1);
    const calls = mockApi({
      ...dayRoute,
      [guideKey(yesterday)]: () => ({ status: 200, body: { day: yesterday, stations: [] } }),
    });
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId("guide-list");
    await user.click(screen.getByRole("button", { name: "前の日" }));
    expect(await screen.findByText("この放送日の番組表は空。")).toBeInTheDocument();
    await waitFor(() => expect(calls.map((c) => c.key)).toContain(guideKey(yesterday)));
  });

  it("4 つの状態と対象外が、文字で見分けられる", async () => {
    mockApi(dayRoute);
    renderPage();
    const list = await screen.findByTestId("guide-list");
    const name = (t: string) => within(list).getByRole("button", { name: new RegExp(t) }).getAttribute("aria-label");
    expect(name("ふらっと\\(1\\)")).toContain("予約なし");
    expect(name("番組R")).toContain("録音予定");
    expect(name("番組D")).toContain("録音済み");
    expect(name("番組M")).toContain("録音できなかった");
    expect(name("番組N")).toContain("ライブで録る");
    expect(within(list).getAllByText("ライブで録る")).toHaveLength(1);
  });

  it("グリッドは前置きの文字を出さず、印・枠の形・aria-label・title で状態が分かる", async () => {
    setWide(true);
    mockApi(dayRoute);
    renderPage();
    const grid = await screen.findByTestId("guide-grid");
    const cell = (t: RegExp) => within(grid).getByRole("button", { name: t });
    const r = cell(/番組R/);
    expect(r).toHaveTextContent("●");
    expect(r.getAttribute("aria-label")).toContain("録音予定");
    expect(r).not.toHaveTextContent("[録音予定]");
    expect(cell(/番組D/)).toHaveTextContent("✓");
    const m = cell(/番組M/);
    expect(m).toHaveTextContent("!");
    expect(m).not.toHaveTextContent("[録音できなかった]");
    expect(m.style.border).toContain("dashed");
    expect(cell(/番組D/).style.border).toContain("solid");
    expect(cell(/番組D/).style.border).not.toContain("dashed");
    const n = cell(/番組N/);
    expect(n).toHaveTextContent("◎");
    expect(n).not.toHaveTextContent("[ライブで録る]");
    expect(n.getAttribute("title")).toContain("ライブで録る");
    const none = cell(/ふらっと\(1\)/);
    expect(none).not.toHaveTextContent(/[●✓!◎]/);
    expect(none.getAttribute("aria-label")).toContain("予約なし");
  });

  it("グリッドの枠は番組名を 1 行で省略し、title に番組名・時刻・状態を全部出す", async () => {
    setWide(true);
    mockApi(dayRoute);
    renderPage();
    const grid = await screen.findByTestId("guide-grid");
    const cell = within(grid).getByRole("button", { name: /番組R/ });
    expect(cell.getAttribute("title")).toBe("番組R 10:00〜11:00 録音予定");
    expect(cell.querySelector("p")).toHaveStyle({ whiteSpace: "nowrap", textOverflow: "ellipsis" });
  });

  it("スマホのリストと凡例にも、状態の印と対象外の印が出る", async () => {
    mockApi(dayRoute);
    renderPage();
    const list = await screen.findByTestId("guide-list");
    const row = (t: RegExp) => within(list).getByRole("button", { name: t });
    expect(row(/番組R/)).toHaveTextContent("●録音予定");
    expect(row(/番組D/)).toHaveTextContent("✓録音済み");
    expect(row(/番組M/)).toHaveTextContent("!録音できなかった");
    expect(row(/番組N/)).toHaveTextContent("◎ライブで録る");
    const legend = screen.getByLabelText("凡例");
    expect(legend).toHaveTextContent("●録音予定");
    expect(legend).toHaveTextContent("✓録音済み");
    expect(legend).toHaveTextContent("!録音できなかった");
    expect(legend).toHaveTextContent("◎ライブで録る");
  });

  it("リスト・グリッド・局の表の時刻は、その放送回の放送日の時刻で出す（深夜は 24 時より後）", async () => {
    const next = addDays(today, 1);
    const late = airing({ title: "番組深", stationId: "LFR", stationName: "ニッポン放送", start: at(next, "01:00"), end: at(next, "03:00") });
    const dawn = airing({ title: "番組暁", stationId: "LFR", stationName: "ニッポン放送", start: at(next, "04:30"), end: at(next, "05:30") });
    const night = airing({ title: "番組夜", stationId: "LFR", stationName: "ニッポン放送", start: at(today, "23:00"), end: at(next, "01:00") });
    const lateStations: GuideStation[] = [{ id: "LFR", name: "ニッポン放送", airings: [night, late, dawn] }];
    mockApi({ [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: lateStations } }) });
    renderPage();
    const list = await screen.findByTestId("guide-list");
    const row = (t: RegExp) => within(list).getByRole("button", { name: t });
    expect(row(/番組夜/)).toHaveTextContent("23:00〜25:00");
    expect(row(/番組深/)).toHaveTextContent("25:00〜27:00");
    expect(row(/番組暁/)).toHaveTextContent("28:30〜29:30");
    expect(row(/番組深/).getAttribute("aria-label")).toContain("番組深 25:00〜27:00");
    cleanup();

    setWide(true);
    renderPage();
    const grid = await screen.findByTestId("guide-grid");
    const cell = within(grid).getByRole("button", { name: /番組深/ });
    expect(cell).toHaveTextContent("25:00 番組深");
    expect(cell.getAttribute("title")).toBe("番組深 25:00〜27:00 予約なし");
    cleanup();

    window.localStorage.setItem("rajikura.guide.view", "station");
    window.localStorage.setItem("rajikura.guide.station", "LFR");
    renderPage();
    const table = await screen.findByTestId("guide-station-table");
    const cells = within(within(table).getByRole("button", { name: /番組暁/ }).closest("tr")!).getAllByRole("cell");
    expect(cells[0]).toHaveTextContent("28:30〜29:30");
  });

  it("詳細の見出しは、放送回の放送日と放送日の時刻で出す（深夜は暦の翌日にしない）", async () => {
    const next = addDays(today, 1);
    const late = airing({ title: "番組深", stationId: "LFR", stationName: "ニッポン放送", start: at(next, "03:40"), end: at(next, "04:00") });
    const lateStations: GuideStation[] = [{ id: "LFR", name: "ニッポン放送", airings: [late] }];
    mockApi({ ...showsRoute, [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: lateStations } }) });
    const user = userEvent.setup();
    renderPage();
    const list = await screen.findByTestId("guide-list");
    await user.click(within(list).getByRole("button", { name: /番組深/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(`ニッポン放送 ${dayLabel(today)} 27:40〜28:00`)).toBeInTheDocument();
    expect(within(dialog).queryByText(/03:40/)).toBeNull();
  });

  it("番組表を読めなければエラーを出す", async () => {
    mockApi({ [guideKey(today)]: () => ({ status: 502, body: { error: "radiko から番組表を取れない" } }) });
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("radiko から番組表を取れない");
  });
});

describe("GuidePage の検索", () => {
  const q = `GET /api/guide/search?${new URLSearchParams({ q: "ふらっと" })}`;

  it("検索結果から放送回の詳細を開ける", async () => {
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [q]: () => ({ status: 200, body: { q: "ふらっと", airings: [flat1, flat2], truncated: false } }),
    });
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId("guide-list");
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "ふらっと");
    await user.click(screen.getByRole("button", { name: "検索" }));
    const results = await screen.findByLabelText("検索結果");
    expect(screen.queryByText(/500 件を超えた/)).toBeNull();
    await user.click(within(results).getAllByRole("button").filter((b) => b.textContent !== "このキーワードで録音")[0]);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("出演: 向井慧")).toBeInTheDocument();
    expect(within(dialog).getByText("朝の番組")).toBeInTheDocument();
    expect(within(dialog).getByText("予約なし")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "この回を録音" })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "毎週録音" })).toBeInTheDocument();
  });

  it("検索結果の「このキーワードで録音」は、検索の語を渡す", async () => {
    mockApi({ ...dayRoute, [q]: () => ({ status: 200, body: { q: "ふらっと", airings: [flat1], truncated: false } }) });
    const onCreateKeyword = vi.fn();
    const user = userEvent.setup();
    renderPage(undefined, onCreateKeyword);
    await screen.findByTestId("guide-list");
    expect(screen.queryByRole("button", { name: "このキーワードで録音" })).not.toBeInTheDocument();
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "ふらっと");
    await user.click(screen.getByRole("button", { name: "検索" }));
    await user.click(await screen.findByRole("button", { name: "このキーワードで録音" }));
    expect(onCreateKeyword).toHaveBeenCalledWith("ふらっと");
  });

  it("truncated のときは、絞るよう知らせる", async () => {
    mockApi({ ...dayRoute, [q]: () => ({ status: 200, body: { q: "ふらっと", airings: [flat1], truncated: true } }) });
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId("guide-list");
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "ふらっと");
    await user.click(screen.getByRole("button", { name: "検索" }));
    expect(await screen.findByText("500 件を超えたので、キーワードを絞ってください。")).toBeInTheDocument();
  });
});

describe("GuidePage の検索の結果の見せ方", () => {
  const q = `GET /api/guide/search?${new URLSearchParams({ q: "向井" })}`;
  const tomorrow = addDays(today, 1);
  // 見出しは放送回の放送日（5 時区切り）。翌日の 01:00 の放送回は今日の放送日に入り、04:59 まで今日、05:00 から翌日
  const dawn = airing({
    title: "朝の向井",
    stationId: "LFR",
    stationName: "ニッポン放送",
    start: at(addDays(today, 1), "05:00"),
    end: at(addDays(today, 1), "06:00"),
  });
  const lateNight = airing({
    title: "深夜の向井",
    start: at(addDays(today, 1), "01:00"),
    end: at(addDays(today, 1), "02:00"),
    performer: "出演者あ",
  });

  it("放送日の見出しで区切り、行の時刻を放送日の時刻で出し、出演者を出し、当たった語を強調する", async () => {
    mockApi({
      ...dayRoute,
      [q]: () => ({ status: 200, body: { q: "向井", airings: [flat1, flat2, lateNight, dawn], truncated: false } }),
    });
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId("guide-list");
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "向井");
    await user.click(screen.getByRole("button", { name: "検索" }));
    const results = await screen.findByLabelText("検索結果");

    const headings = within(results).getAllByRole("heading");
    expect(headings.map((h) => h.textContent)).toEqual([dayLabel(today), dayLabel(tomorrow)]);
    const first = within(results).getByRole("group", { name: dayLabel(today) });
    expect(within(first).getAllByRole("button")).toHaveLength(3);
    // 深夜の放送回は今日の放送日の見出しの下に、放送日の時刻で出す（行の aria-label も放送日と放送日の時刻）
    const late = within(first).getByRole("button", { name: /深夜の向井/ });
    expect(late).toHaveTextContent("25:00〜26:00 TBSラジオ");
    expect(late.getAttribute("aria-label")).toBe(`TBSラジオ ${dayLabel(today)} 深夜の向井 25:00〜26:00 予約なし`);
    const second = within(results).getByRole("group", { name: dayLabel(tomorrow) });
    expect(second).toHaveTextContent("05:00〜06:00 ニッポン放送");
    expect(second).not.toHaveTextContent("深夜の向井");

    // 出演者と強調
    const row = within(first).getAllByRole("button")[0];
    expect(row).toHaveTextContent("08:30〜09:00 TBSラジオ");
    expect(row).toHaveTextContent("向井慧");
    expect(Array.from(row.querySelectorAll("mark")).map((m) => m.textContent)).toEqual(["向井", "向井"]);
  });

  it("強調は全角・半角と大文字・小文字を同じに見る", () => {
    expect(matchRanges("ＡＢＣ abc", "b")).toEqual([
      [1, 2],
      [5, 6],
    ]);
    expect(matchRanges("パンサー向井", "")).toEqual([]);
    // 空白（全角も）で区切った語ごとに付け、重なる範囲は 1 つにまとめる
    expect(matchRanges("向井慧 パンサー向井", "向井　パンサー")).toEqual([
      [0, 2],
      [4, 10],
    ]);
    expect(matchRanges("abcd", "ab bc")).toEqual([[0, 3]]);
    expect(matchRanges("abc", " 　")).toEqual([]);
    // 前後の文字と合成される列（半角カナ + 半角の濁点、「か」+ 結合濁点）
    expect(matchRanges("ﾊﾟﾝｻｰ", "パンサー")).toEqual([[0, 5]]);
    expect(matchRanges("あがい", "が")).toEqual([[1, 3]]);
  });
});

describe("GuidePage の局の絞り込み", () => {
  const KEY = "rajikura.guide.hiddenStations";
  const grid = () => screen.findByTestId("guide-grid");

  async function openPicker(user: User) {
    await user.click(await screen.findByRole("button", { name: /表示する局/ }));
    const dialog = await screen.findByRole("dialog");
    // 遷移は env="test" で切ってある。切らない jsdom では開いても display: none のままだった（遷移の都合。aria-hidden は付かない）
    await waitFor(() => expect(dialog).toBeVisible());
    return dialog;
  }

  it("× で隠し、一覧で戻せる。ボタンに数が出て、隠した局を覚える", async () => {
    setWide(true);
    mockApi(dayRoute);
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByRole("button", { name: "表示する局 2 / 2" })).toBeInTheDocument();

    await user.click(within(await grid()).getByRole("button", { name: "ニッポン放送を隠す" }));
    expect(within(await grid()).queryByText("ニッポン放送")).toBeNull();
    expect(screen.getByRole("button", { name: "表示する局 1 / 2" })).toBeInTheDocument();
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["LFR"]);

    const picker = await openPicker(user);
    expect(within(picker).getByLabelText("ニッポン放送")).not.toBeChecked();
    await user.click(within(picker).getByLabelText("ニッポン放送"));
    expect(within(await grid()).getByText("ニッポン放送")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "表示する局 2 / 2" })).toBeInTheDocument();
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual([]);

    await user.click(within(picker).getByRole("button", { name: "すべて外す" }));
    expect(screen.getByRole("button", { name: "表示する局 0 / 2" })).toBeInTheDocument();
    expect(screen.queryByTestId("guide-grid")).toBeNull();
    await user.click(within(picker).getByRole("button", { name: "すべて選ぶ" }));
    expect(screen.getByRole("button", { name: "表示する局 2 / 2" })).toBeInTheDocument();
  });

  it("壊れた覚えは無視して全局を出し、文字列でない要素だけ捨てる", async () => {
    for (const bad of ["{壊れた", '{"a":1}', "42", "null"]) {
      window.localStorage.setItem(KEY, bad);
      setWide(true);
      mockApi(dayRoute);
      const { unmount } = renderPage();
      expect(await screen.findByRole("button", { name: "表示する局 2 / 2" })).toBeInTheDocument();
      unmount();
      vi.unstubAllGlobals();
    }
    window.localStorage.setItem(KEY, JSON.stringify([1, null, "LFR", { a: 1 }]));
    mockApi(dayRoute);
    renderPage();
    expect(await screen.findByRole("button", { name: "表示する局 1 / 2" })).toBeInTheDocument();
  });

  it("「隠した局も表示」の間に隠し直しても、同じ局を重ねて覚えない", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["LFR"]));
    setWide(true);
    mockApi(dayRoute);
    const user = userEvent.setup();
    renderPage();
    await grid();
    await user.click(screen.getByRole("switch", { name: "隠した局も表示" }));
    await user.click(within(await grid()).getByRole("button", { name: "ニッポン放送を隠す" }));
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["LFR"]);
  });

  it("詳細を開く・局を隠す・検索欄に打つでは、グリッドのスクロールの位置を変えない", async () => {
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    g.scrollTop = 123;
    await user.click(within(g).getByRole("button", { name: /番組R/ }));
    await screen.findByRole("dialog");
    expect(g.scrollTop).toBe(123);
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "ab");
    await user.click(within(g).getByRole("button", { name: "ニッポン放送を隠す" }));
    expect(g.scrollTop).toBe(123);
  });

  it("覚えた隠した局は読み直しても隠れ、新しい局は出る", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["LFR", "GONE"]));
    setWide(true);
    mockApi(dayRoute);
    renderPage();
    const g = await grid();
    expect(within(g).getByText("TBSラジオ")).toBeInTheDocument();
    expect(within(g).queryByText("ニッポン放送")).toBeNull();
    expect(screen.getByRole("button", { name: "表示する局 1 / 2" })).toBeInTheDocument();
  });

  it("localStorage が使えなくても動く", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    setWide(true);
    mockApi(dayRoute);
    const user = userEvent.setup();
    renderPage();
    await user.click(within(await grid()).getByRole("button", { name: "ニッポン放送を隠す" }));
    expect(within(await grid()).queryByText("ニッポン放送")).toBeNull();
    vi.restoreAllMocks();
  });

  it("「隠した局も表示」の間は全局を出し、切ると隠した局は戻らずに隠れる", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["LFR"]));
    setWide(true);
    mockApi(dayRoute);
    const user = userEvent.setup();
    renderPage();
    await grid();
    const sw = screen.getByRole("switch", { name: "隠した局も表示" });
    await user.click(sw);
    expect(within(await grid()).getByText("ニッポン放送")).toBeInTheDocument();
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["LFR"]);
    await user.click(sw);
    expect(within(await grid()).queryByText("ニッポン放送")).toBeNull();
  });

  it("スマホ幅の局の選び方は隠した局に影響されない", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["LFR"]));
    mockApi(dayRoute);
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId("guide-list");
    expect(screen.queryByRole("button", { name: /表示する局/ })).toBeNull();
    await user.click(screen.getByRole("combobox", { name: "局" }));
    expect(await screen.findByRole("option", { name: "ニッポン放送", hidden: true })).toBeInTheDocument();
  });

  it("検索の結果は隠した局も含めて出す", async () => {
    window.localStorage.setItem(KEY, JSON.stringify(["LFR"]));
    setWide(true);
    const qq = `GET /api/guide/search?${new URLSearchParams({ q: "オール" })}`;
    mockApi({ ...dayRoute, [qq]: () => ({ status: 200, body: { q: "オール", airings: [lfr], truncated: false } }) });
    const user = userEvent.setup();
    renderPage();
    await grid();
    await user.type(screen.getByRole("textbox", { name: "番組名・出演者・説明で探す" }), "オール");
    await user.click(screen.getByRole("button", { name: "検索" }));
    expect(await within(await screen.findByLabelText("検索結果")).findByText(/ニッポン放送/)).toBeInTheDocument();
  });
});

type User = ReturnType<typeof userEvent.setup>;

async function openDetail(user: User, title: RegExp) {
  const list = await screen.findByTestId("guide-list");
  await user.click(within(list).getByRole("button", { name: title }));
  return await screen.findByRole("dialog");
}

/** 押せるようになる（番組の一覧とパートを読み終える）のを待ってから押す。 */
async function press(user: User, dialog: HTMLElement, name: string) {
  const button = await within(dialog).findByRole("button", { name });
  await waitFor(() => expect(button).not.toHaveAttribute("data-loading"));
  await user.click(button);
}

const band = () => screen.findByRole("status", { name: "知らせ" });

/** 番組D の番組を「この回を録音」で新しく作った直後の形。 */
const madeD: Show = { name: "番組D", enabled: true, split: "none", check_title: true, weekly: [] };

/** 外から応答を返せる Promise。 */
function gate() {
  let open: () => void = () => {};
  const wait = new Promise<void>((r) => (open = r));
  return { wait, open };
}

describe("GuidePage の詳細の出し方", () => {
  it("PC 幅では右の欄に出し、番組表を隠さない（重ねる幕が無く、番組表から次の放送回を選べる）", async () => {
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const grid = await screen.findByTestId("guide-grid");
    await user.click(within(grid).getByRole("button", { name: /ふらっと\(1\)/ }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("パンサー向井のふらっと(1)");
    expect(dialog).not.toHaveAttribute("data-full-screen");
    expect(document.querySelector(".mantine-Drawer-overlay")).toBeNull();
    expect(document.querySelector(".mantine-Drawer-root")).not.toBeNull();
    // 開いたまま、番組表の別の放送回を選べる
    await user.click(within(grid).getByRole("button", { name: /番組D/ }));
    await waitFor(() => expect(screen.getByRole("dialog")).toHaveTextContent("番組D"));
  });

  it("説明の中のリンクは新しいタブで開くリンクとして描き、HTML としては描かない", async () => {
    const withLinked = { [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ ...stations[0], airings: [linked] }] } }) };
    mockApi({ ...withLinked, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /リンクの番組/);
    const a = within(dialog).getByRole("link", { name: "こちら" });
    expect(a).toHaveAttribute("href", "https://example.com/a?x=1&y=2");
    expect(a).toHaveAttribute("target", "_blank");
    expect(a).toHaveAttribute("rel", "noopener noreferrer");
    expect(within(dialog).getByRole("link", { name: "a@example.com" })).toHaveAttribute("href", "mailto:a@example.com");
    expect(within(dialog).getAllByRole("link")).toHaveLength(2);
    expect(dialog).toHaveTextContent("詳細はこちら と a@example.com まで");
  });

  it("スマホ幅では画面いっぱいに出す", async () => {
    mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    expect(dialog).toHaveAttribute("data-full-screen");
    expect(document.querySelector(".mantine-Drawer-root")).toBeNull();
  });

  it("タイムフリーの対象外の放送回には「ライブで録る」を出し、「録音できない」は出さず、「この回を録音」「毎週録音」を出す", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(at(today, "10:30"))); // 放送回「番組N」（13:00〜14:00）の放送の前
    mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組N/);
    expect(within(dialog).getByText("ライブで録る")).toBeInTheDocument();
    expect(dialog).not.toHaveTextContent("録音できない");
    expect(within(dialog).getByText(/放送中にライブで録る/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "この回を録音" })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "毎週録音" })).toBeInTheDocument();
  });

  it("録音期限（放送の終了の 5 分後）を過ぎたタイムフリーの対象外の放送回には、「放送中にライブで録る」と「この回を録音」を出さない", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(at(today, "14:05"))); // 放送回「番組N」（13:00〜14:00）の録音期限ちょうど
    mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組N/);
    expect(within(dialog).getByText(/録音期限（放送の終了の 5 分後）を過ぎたので/)).toBeInTheDocument();
    expect(dialog).not.toHaveTextContent("放送中にライブで録る");
    expect(within(dialog).queryByRole("button", { name: "この回を録音" })).toBeNull();
    expect(within(dialog).getByRole("button", { name: "毎週録音" })).toBeInTheDocument();
  });
});

describe("GuidePage の「この回を録音」", () => {
  it("同じ名前の番組があれば、そこにすぐ足して帯で知らせ、番組表の状態を取り直す", async () => {
    let booked = false;
    const calls = mockApi({
      [guideKey(today)]: () => ({
        status: 200,
        body: {
          day: today,
          stations: booked
            ? [{ ...stations[0], airings: [{ ...morning, state: "planned", showName: "朝の番組" }] }]
            : stations,
        },
      }),
      ...showsRoute,
      [onces("朝の番組")]: () => {
        booked = true;
        return { status: 201, body: { onces: [created(7, morning)] } };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");

    expect(await band()).toHaveTextContent("この回を録音します。番組「朝の番組」に足しました");
    expect(calls.find((c) => c.key === onces("朝の番組"))?.body).toEqual({
      airings: [{ station: "TBS", start: morning.start }],
    });
    expect(calls.some((c) => c.key === "POST /api/shows")).toBe(false);
    // 状態を取り直した。スマホ幅では詳細を閉じて帯を見せる
    expect(calls.filter((c) => c.key === guideKey(today))).toHaveLength(2);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("button", { name: /録音予定/ })).toBeInTheDocument();
  });

  it("同じ名前の番組が無ければ、その名前で新しく作って足す（パートが 1 つなら分割番組でない）", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("番組D")]: () => ({ status: 201, body: { onces: [created(8, doneA)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組D/);
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("この回を録音します。番組「番組D」を新しく作りました");
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).toMatchObject({
      name: "番組D",
      enabled: true,
      split: "none",
      weekly: [],
    });
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).not.toHaveProperty("join");
  });

  it("タイムフリーの対象外の放送回も、1 回の予約で足せる", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(at(today, "10:30"))); // 放送回「番組N」（13:00〜14:00）の放送の前
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("番組N")]: () => ({ status: 201, body: { onces: [created(10, ngA)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組N/);
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("この回を録音します。番組「番組N」を新しく作りました");
    expect(calls.find((c) => c.key === onces("番組N"))?.body).toEqual({ airings: [{ station: "TBS", start: ngA.start }] });
  });

  it("自動の組は、タイムフリーの対象外のパートも含めてまとめる", () => {
    const ng2 = { ...flat2, notAvailable: true };
    const before = new Date(at(today, "08:00")).getTime();
    expect(autoGroup(flat1, [flat1, ng2, ngA], before).map((a) => a.start)).toEqual([flat1.start, flat2.start]);
  });

  it("自動の組から、録音期限（放送の終了の 5 分後）を過ぎたタイムフリーの対象外のパートを除く", () => {
    const ng1 = { ...flat1, notAvailable: true }; // 08:30〜09:00
    const ng2 = { ...flat2, notAvailable: true }; // 09:00〜09:30
    const ms = (hhmm: string) => new Date(at(today, hhmm)).getTime();
    expect(autoGroup(ng2, [ng1, ng2], ms("09:05")).map((a) => a.start)).toEqual([flat2.start]);
    expect(autoGroup(ng2, [ng1, ng2], ms("09:04")).map((a) => a.start)).toEqual([flat1.start, flat2.start]);
    // 対象内のパートは除かない
    expect(autoGroup(ng2, [flat1, ng2], ms("09:05")).map((a) => a.start)).toEqual([flat1.start, flat2.start]);
  });

  it("分割番組の後ろのパートの「この回を録音」は、録音期限を過ぎたタイムフリーの対象外のパートを送らない", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(at(today, "10:30")));
    const live1 = airing({ title: "ライブの番組(1)", start: at(today, "08:00"), end: at(today, "09:00"), notAvailable: true });
    const live2 = airing({ title: "ライブの番組(2)", start: at(today, "11:00"), end: at(today, "12:00"), notAvailable: true });
    const liveDay = { [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ ...stations[0], airings: [live1, live2] }] } }) };
    const calls = mockApi({
      ...liveDay,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("ライブの番組")]: () => ({ status: 201, body: { onces: [created(11, live2)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ライブの番組\(2\)/);
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("この回を録音します。番組「ライブの番組」を新しく作りました");
    expect(calls.find((c) => c.key === onces("ライブの番組"))?.body).toEqual({ airings: [{ station: "TBS", start: live2.start }] });
  });

  it("分割のパートはまとめて足し、新しく作る番組を分割番組でつなぐ番組にする", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("パンサー向井のふらっと")]: () => ({ status: 201, body: { onces: [] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("この回（2 パート）を録音します。番組「パンサー向井のふらっと」を新しく作りました");
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).toMatchObject({ split: "join" });
    expect(calls.find((c) => c.key === onces("パンサー向井のふらっと"))?.body).toEqual({
      airings: [
        { station: "TBS", start: flat1.start },
        { station: "TBS", start: flat2.start },
      ],
    });
  });

  it("足す先の番組が無効な番組なら、すぐには足さず、折りたたみを開いて知らせる", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("無効な番組")]: () => ({ status: 201, body: { onces: [created(9, stopped)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /無効な番組/);
    await press(user, dialog, "この回を録音");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    expect(within(settings).getByText(/番組「無効な番組」は無効なので、すぐには録音しなかった/)).toBeInTheDocument();
    expect(within(settings).getByRole("combobox", { name: "どの番組に足すか" })).toHaveValue("無効な番組");
    expect(calls.some((c) => c.key.startsWith("POST "))).toBe(false);
    // 知らせを見たうえでもう一度押せば、無効な番組に足す
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("番組「無効な番組」に足しました");
  });

  it("無効な番組の知らせは、足す先を選び直したら消える", async () => {
    mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /無効な番組/);
    await press(user, dialog, "この回を録音");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    expect(within(settings).getByText(/すぐには録音しなかった/)).toBeInTheDocument();
    await user.click(within(settings).getByRole("combobox", { name: "どの番組に足すか" }));
    await user.click(await screen.findByRole("option", { name: "朝の番組", hidden: true }));
    expect(within(settings).queryByText(/すぐには録音しなかった/)).toBeNull();
  });

  it("番組の名前は NFKC で揃えて比べる（全角の名前の番組にも足す）", async () => {
    const abc = airing({ title: "ABC(1)", start: at(today, "16:00"), end: at(today, "17:00") });
    const wide: Show = { ...existingShow, name: "ＡＢＣ" };
    const calls = mockApi({
      [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [abc] }] } }),
      "GET /api/shows": () => ({ status: 200, body: [existingShow, wide] }),
      [onces("ＡＢＣ")]: () => ({ status: 201, body: { onces: [created(3, abc)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ABC/);
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("番組「ＡＢＣ」に足しました");
    expect(calls.some((c) => c.key === "POST /api/shows")).toBe(false);
  });
});

describe("GuidePage の「設定を変えて予約」", () => {
  it("選択肢は既存の番組（各 1 回）と「新しい番組を作る」で、初めは選んだ放送回の番組名から決まる名前の足す先の番組。新しく作るときだけ名前が出る", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(10, flat1)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "設定を変えて予約");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    const select = within(settings).getByRole("combobox", { name: "どの番組に足すか" });
    expect(select).toHaveValue("新しい番組を作る");
    expect(within(settings).getByRole("textbox", { name: /番組の名前/ })).toHaveValue("パンサー向井のふらっと");
    // 「パートを 1 つのファイルにつなぐ」は折りたたみの外に出し、パートをまとめるかのチェックは無い
    expect(within(settings).queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeNull();
    expect(within(dialog).getByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeChecked();
    expect(within(dialog).queryByRole("checkbox", { name: /まとめて録音/ })).toBeNull();

    await user.click(select);
    await waitFor(() => expect(select).toHaveAttribute("aria-controls"));
    const listbox = document.getElementById(select.getAttribute("aria-controls")!)!;
    const options = within(listbox)
      .getAllByRole("option", { hidden: true })
      .map((o) => o.textContent);
    expect(options).toEqual(["朝の番組", "無効な番組", "新しい番組を作る"]);
    await user.click(screen.getByRole("option", { name: "朝の番組", hidden: true }));
    // 既存の番組を選んだら、名前の入力は出さず、その番組の設定と予約を表示する
    expect(within(settings).queryByRole("textbox", { name: /番組の名前/ })).toBeNull();
    expect(within(settings).getByText("分割番組の設定: 分割番組でない")).toBeInTheDocument();
    expect(within(settings).getByText(/予約: TBSラジオ 月 07:00/)).toBeInTheDocument();
    // 分割番組でない既存の番組に 2 つ足すので、つなぐかを出す（足す先の番組に合わせて文を変える）
    expect(within(dialog).getByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeChecked();
    expect(within(dialog).getByText(/番組「朝の番組」の分割番組の設定を「つなぐ」に変える/)).toBeInTheDocument();

    // 詳細のチェックで 2 つ目を外すと、この放送回だけを足す（1 つになるので、つなぐかは出さない）
    await user.click(within(within(dialog).getByRole("group", { name: "選んだ放送回" })).getByRole("checkbox", { name: /ふらっと\(2\)/ }));
    expect(within(dialog).queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeNull();
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("この回を録音します。番組「朝の番組」に足しました");
    expect(calls.find((c) => c.key === onces("朝の番組"))?.body).toEqual({
      airings: [{ station: "TBS", start: flat1.start }],
    });
  });

  it("新しく作るときは、直した名前とつなぐの設定で作る", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("ふらっと")]: () => ({ status: 201, body: { onces: [] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "設定を変えて予約");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    const name = within(settings).getByRole("textbox", { name: /番組の名前/ });
    await user.clear(name);
    await user.type(name, "ふらっと");
    await user.click(within(dialog).getByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" }));
    await press(user, dialog, "この回を録音");
    expect(await band()).toHaveTextContent("番組「ふらっと」を新しく作りました");
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).toMatchObject({ name: "ふらっと", split: "separate" });
  });

  it("新しく作るとき、「この回だけにする」を押せば分割番組でない番組にする（「この回を録音」も「毎週録音」も）", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("パンサー向井のふらっと")]: () => ({ status: 201, body: { onces: [] } }),
    });
    const user = userEvent.setup();
    renderPage();
    for (const kind of ["この回を録音", "毎週録音"]) {
      const dialog = await openDetail(user, /ふらっと\(1\)/);
      await press(user, dialog, "この回だけにする");
      expect(within(dialog).queryByRole("group", { name: "選んだ放送回" })).toBeNull();
      expect(within(dialog).queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeNull();
      await press(user, dialog, kind);
      await band();
      const posts = calls.filter((c) => c.key === "POST /api/shows");
      expect(posts.at(-1)?.body).toMatchObject({ name: "パンサー向井のふらっと", split: "none" });
      await user.click(screen.getByRole("button", { name: "知らせを閉じる" }));
    }
    expect(calls.filter((c) => c.key === "POST /api/shows").map((c) => (c.body as Show).weekly.length)).toEqual([0, 1]);
  });
});

describe("GuidePage の 409 と失敗", () => {
  it("409 の文の時刻は、その放送回の放送日の時刻で出す", () => {
    const late = new OnceConflict("断った", "already", "深夜の番組", { station: "LFR", start: "2026-10-16T01:00:00+09:00" });
    expect(conflictMessage(late)).toBe("この番組「深夜の番組」ですでに録音予定（25:00 LFR）");
    const day = new OnceConflict("断った", "other_show", "別の番組", { station: "TBS", start: "2026-10-16T08:30:00+09:00" });
    expect(conflictMessage(day)).toBe("ほかの番組「別の番組」がすでにこの放送回を当てている（08:30 TBS）");
  });

  it.each([
    [{ reason: "other_show", showName: "別の番組" }, /ほかの番組「別の番組」がすでに/],
    [{ reason: "already", showName: "朝の番組" }, /この番組「朝の番組」ですでに録音予定/],
  ])("409 の理由を出す %j", async (extra, want) => {
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({
        status: 409,
        body: { error: "断った", ...extra, airing: { station: "TBS", start: morning.start } },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(want);
  });

  const newShowRoutes = (once: Handler, del?: Handler) => ({
    ...dayRoute,
    ...showsRoute,
    "POST /api/shows": () => ({ status: 201, body: {} }),
    [onces("番組D")]: once,
    ...(del ? { [showKey("DELETE", "番組D")]: del } : {}),
  });
  const reserveNew = async () => {
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組D/);
    await press(user, dialog, "この回を録音");
    return dialog;
  };

  it("新しい番組を作ったあとに 409 で断られたら、作った番組を消す", async () => {
    const calls = mockApi(
      newShowRoutes(
        () => ({ status: 409, body: { error: "断った", reason: "other_show", showName: "朝の番組" } }),
        () => ({ status: 204 }),
      ),
    );
    const dialog = await reserveNew();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("ほかの番組「朝の番組」");
    expect(calls.map((c) => c.key)).toContain(showKey("DELETE", "番組D"));
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
  });

  it("競合で断られた 409（reason・airing なし）は、サーバーの文を出す", async () => {
    mockApi(newShowRoutes(() => ({ status: 409, body: { error: "保存先に断られた" } }), () => ({ status: 204 })));
    const dialog = await reserveNew();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("保存先に断られた");
  });

  it("通信の失敗など結果が分からないときは、作った番組を消さず、確かめるよう知らせる", async () => {
    const calls = mockApi(
      newShowRoutes(() => {
        throw new Error("通信できない");
      }),
    );
    const dialog = await reserveNew();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "番組「番組D」は作ったが、1 回の予約を足せたか分からない。番組の画面で確かめてほしい",
    );
    expect(calls.some((c) => c.key.startsWith("DELETE "))).toBe(false);
  });

  it("5xx も結果が分からないので、消さない", async () => {
    const calls = mockApi(newShowRoutes(() => ({ status: 500, body: { error: "内部エラー" } })));
    const dialog = await reserveNew();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("足せたか分からない");
    expect(calls.some((c) => c.key.startsWith("DELETE "))).toBe(false);
  });

  it("番組を消すのに失敗したら、番組が残ったことを知らせる", async () => {
    mockApi(
      newShowRoutes(
        () => ({ status: 400, body: { error: "番組表にその放送回が無い" } }),
        () => ({ status: 500, body: { error: "消せない" } }),
      ),
    );
    const dialog = await reserveNew();
    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent("番組表にその放送回が無い");
    expect(alert).toHaveTextContent("作った番組「番組D」を消せなかった");
  });
});

describe("GuidePage の「毎週録音」", () => {
  const weekday = weekdayOf(today);

  it("同じ名前の番組が無ければ、その毎週の予約を持つ番組を新しく作る", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^番組D/);
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent("毎週録音します。番組「番組D」を新しく作りました");
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).toEqual({
      name: "番組D",
      enabled: true,
      split: "none",
      check_title: true,
      weekly: [{ station: "TBS", weekday, start: "11:00", from: today }],
    });
  });

  it("パートが 2 つ以上なら、全パートの毎週の予約を持つ、分割番組でつなぐ番組を新しく作る", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(2\)/);
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent("毎週（2 パート）録音します。番組「パンサー向井のふらっと」を新しく作りました");
    expect(calls.find((c) => c.key === "POST /api/shows")?.body).toEqual({
      name: "パンサー向井のふらっと",
      enabled: true,
      split: "join",
      check_title: true,
      weekly: [
        { station: "TBS", weekday, start: "08:30", from: today },
        { station: "TBS", weekday, start: "09:00", from: today },
      ],
    });
  });

  it("パートが 2 つ以上なら、既存の番組にも全パートの毎週の予約を足し（もうあるものは足さない）、つなぐがオンなら同じ PUT で分割番組でつなぐ番組にする", async () => {
    const flatExisting: Show = {
      ...existingShow,
      name: "パンサー向井のふらっと",
      weekly: [{ station: "TBS", weekday, start: "08:30" }],
    };
    const calls = mockApi({
      ...dayRoute,
      "GET /api/shows": () => ({ status: 200, body: [existingShow, flatExisting] }),
      [showKey("GET", flatExisting.name)]: () => ({ status: 200, body: { ...flatExisting, onces: [] } }),
      [showKey("PUT", flatExisting.name)]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent(
      "毎週（2 パート）録音します。番組「パンサー向井のふらっと」に足しました（パートを 1 つのファイルにつなぐ設定にしました）",
    );
    const puts = calls.filter((c) => c.key === showKey("PUT", flatExisting.name));
    expect(puts).toHaveLength(1);
    const put = puts[0].body as Show;
    expect(put.split).toBe("join");
    expect(put.weekly).toEqual([flatExisting.weekly[0], { station: "TBS", weekday, start: "09:00", from: today }]);
  });

  it("全パートの「毎週」がもうある番組には足さない", async () => {
    const flatExisting: Show = {
      ...existingShow,
      name: "パンサー向井のふらっと",
      split: "join",
      weekly: [
        { station: "TBS", weekday, start: "08:30" },
        { station: "TBS", weekday, start: "09:00" },
      ],
    };
    const calls = mockApi({
      ...dayRoute,
      "GET /api/shows": () => ({ status: 200, body: [flatExisting] }),
      [showKey("GET", flatExisting.name)]: () => ({ status: 200, body: flatExisting }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "毎週録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("同じ「毎週」がもうある");
    expect(calls.some((c) => c.key.startsWith("PUT "))).toBe(false);
  });

  it("詳細のチェックで外した放送回の「毎週」は足さない", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: { ...existingShow, onces: [] } }),
      [showKey("PUT", "朝の番組")]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(2\)/);
    await press(user, dialog, "設定を変えて予約");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    await user.click(within(settings).getByRole("combobox", { name: "どの番組に足すか" }));
    await user.click(await screen.findByRole("option", { name: "朝の番組", hidden: true }));
    await user.click(within(within(dialog).getByRole("group", { name: "選んだ放送回" })).getByRole("checkbox", { name: /ふらっと\(1\)/ }));
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent("毎週録音します。番組「朝の番組」に足しました");
    const put = calls.find((c) => c.key === showKey("PUT", "朝の番組"))?.body as Show;
    expect(put.weekly).toEqual([existingShow.weekly[0], { station: "TBS", weekday, start: "09:00", from: today }]);
  });

  it("同じ名前の番組があれば、読み直した番組に毎週の予約を足す", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: { ...existingShow, onces: [] } }),
      [showKey("PUT", "朝の番組")]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent("毎週録音します。番組「朝の番組」に足しました");
    const put = calls.find((c) => c.key === showKey("PUT", "朝の番組"))?.body as Show;
    expect(put.name).toBe("朝の番組");
    expect(put.weekly).toEqual([existingShow.weekly[0], { station: "TBS", weekday, start: "14:00", from: today }]);
  });

  it("同じ「毎週」がもうある番組には足さない", async () => {
    const dup: Show = { ...existingShow, weekly: [{ station: "TBS", weekday, start: "14:00" }] };
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: dup }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("同じ「毎週」がもうある");
    expect(calls.some((c) => c.key.startsWith("PUT "))).toBe(false);
  });

  it("既にある同じ「毎週」の開始日が、この放送回の放送日より前なら、足さない", async () => {
    const dup: Show = { ...existingShow, weekly: [{ station: "TBS", weekday, start: "14:00", from: addDays(today, -7) }] };
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: dup }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("同じ「毎週」がもうある");
    expect(calls.some((c) => c.key.startsWith("PUT "))).toBe(false);
  });

  it("既にある同じ「毎週」の開始日がこの放送回の放送日より後なら、開始日を今回の放送日にした「毎週」を足す", async () => {
    const later: Show = { ...existingShow, weekly: [{ station: "TBS", weekday, start: "14:00", from: addDays(today, 7) }] };
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: later }),
      [showKey("PUT", "朝の番組")]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent("毎週録音します。番組「朝の番組」に足しました");
    const put = calls.find((c) => c.key === showKey("PUT", "朝の番組"))?.body as Show;
    expect(put.weekly).toEqual([...later.weekly, { station: "TBS", weekday, start: "14:00", from: today }]);
  });

  it("既にある同じ「毎週」が終了日を持てば、全部は当てないので、終了日の無い「毎週」を足す", async () => {
    const ending: Show = { ...existingShow, weekly: [{ station: "TBS", weekday, start: "14:00", until: addDays(today, 7) }] };
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: ending }),
      [showKey("PUT", "朝の番組")]: () => ({ status: 200, body: {} }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    expect(await band()).toHaveTextContent("毎週録音します。番組「朝の番組」に足しました");
    const put = calls.find((c) => c.key === showKey("PUT", "朝の番組"))?.body as Show;
    expect(put.weekly).toEqual([...ending.weekly, { station: "TBS", weekday, start: "14:00", from: today }]);
  });

  it.each([
    ["読み直せない", { GET: 500 }, "番組を読めない"],
    ["保存できない", { PUT: 500 }, "番組を保存できない"],
  ])("既存の番組を%sときは、詳細に理由を出す", async (_, fail, want) => {
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () =>
        "GET" in fail ? { status: 500, body: { error: "内部エラー" } } : { status: 200, body: existingShow },
      [showKey("PUT", "朝の番組")]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(want);
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
  });

  it("足す先の番組が無効な番組なら、すぐには足さず、折りたたみを開いて知らせる", async () => {
    const calls = mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /無効な番組/);
    await press(user, dialog, "毎週録音");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    expect(within(settings).getByText(/無効なので、すぐには録音しなかった/)).toBeInTheDocument();
    expect(calls.some((c) => c.key.startsWith("PUT ") || c.key.startsWith("POST "))).toBe(false);
  });
});

describe("GuidePage の帯と取り消し", () => {
  it("新しく作った番組は、取り消すと消え、番組表の状態が戻る", async () => {
    let booked = false;
    const calls = mockApi({
      [guideKey(today)]: () => ({
        status: 200,
        body: {
          day: today,
          stations: booked ? [{ ...stations[0], airings: [{ ...doneA, state: "planned", showName: "番組D" }] }] : stations,
        },
      }),
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("番組D")]: () => {
        booked = true;
        return { status: 201, body: { onces: [created(8, doneA)] } };
      },
      [showKey("GET", "番組D")]: () => ({ status: 200, body: { ...madeD, onces: [created(8, doneA)] } }),
      [showKey("DELETE", "番組D")]: () => {
        booked = false;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組D/);
    await press(user, dialog, "この回を録音");
    await band();
    expect(await screen.findByRole("button", { name: /番組D.*録音予定/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await band()).toHaveTextContent("取り消しました（番組「番組D」を消しました）");
    expect(calls.map((c) => c.key)).toContain(showKey("DELETE", "番組D"));
    // 今の操作で作ったものだけを消す（1 回の予約を個別には消さない、ほかの番組に触らない）
    expect(calls.filter((c) => c.key.startsWith("DELETE "))).toHaveLength(1);
    expect(await screen.findByRole("button", { name: /番組D.*録音済み/ })).toBeInTheDocument();
    // 取り消した後は、もう取り消せない。消した番組は開けない
    expect(screen.queryByRole("button", { name: "取り消す" })).toBeNull();
    expect(screen.queryByRole("button", { name: "番組を開く" })).toBeNull();
  });

  it("既存の番組に足したこの回は、足した 1 回の予約だけを消す", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(7, morning)] } }),
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: { ...existingShow, onces: [created(7, morning)] } }),
      [`DELETE /api/shows/${enc("朝の番組")}/onces/7`]: () => ({ status: 204 }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await band()).toHaveTextContent("取り消しました");
    expect(calls.filter((c) => c.key.startsWith("DELETE ")).map((c) => c.key)).toEqual([
      `DELETE /api/shows/${enc("朝の番組")}/onces/7`,
    ]);
    expect(calls.filter((c) => c.key === guideKey(today))).toHaveLength(3);
  });

  it("足した毎週の予約は、足す前の番組の形に戻す", async () => {
    const weekday = weekdayOf(today);
    let current: Show = existingShow;
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: { ...current, onces: [] } }),
      [showKey("PUT", "朝の番組")]: (init) => {
        current = JSON.parse(String(init?.body)) as Show;
        return { status: 200, body: current };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    await band();
    expect(current.weekly).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await band()).toHaveTextContent("取り消しました（番組「朝の番組」から「毎週」を外しました）");
    const puts = calls.filter((c) => c.key === showKey("PUT", "朝の番組"));
    expect(puts).toHaveLength(2);
    expect(puts[1].body).toEqual({
      name: "朝の番組",
      enabled: true,
      split: "none",
      check_title: true,
      weekly: existingShow.weekly,
    });
    expect(current.weekly).not.toContainEqual({ station: "TBS", weekday, start: "14:00", from: today });
  });

  it("新しく作った番組が、その後に別に変えられていたら、消さずに知らせる", async () => {
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("番組D")]: () => ({ status: 201, body: { onces: [created(8, doneA)] } }),
      // 別の画面で毎週の予約が足された
      [showKey("GET", "番組D")]: () => ({
        status: 200,
        body: { ...madeD, weekly: [{ station: "TBS", weekday: 1, start: "11:00" }], onces: [created(8, doneA)] },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組D/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await screen.findByText(/番組「番組D」がその後に変わったので、取り消さなかった/)).toBeInTheDocument();
    expect(calls.some((c) => c.key.startsWith("DELETE "))).toBe(false);
  });

  it("毎週録音で新しく作った番組は、作った直後の形なら取り消すと消える", async () => {
    const weekday = weekdayOf(today);
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [showKey("GET", "パンサー向井のふらっと")]: () => ({
        status: 200,
        body: {
          name: "パンサー向井のふらっと",
          enabled: true,
          split: "join",
          check_title: true,
          // 比べるのは局・曜日・開始時刻・開始日（空と省略は同じ）だけ。ほかのキーが付いていても同じ形とみなす
          weekly: [
            { station: "TBS", weekday, start: "08:30", from: today, extra: "x" },
            { station: "TBS", weekday, start: "09:00", from: today },
          ],
        },
      }),
      [showKey("DELETE", "パンサー向井のふらっと")]: () => ({ status: 204 }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "毎週録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await band()).toHaveTextContent("取り消しました（番組「パンサー向井のふらっと」を消しました）");
    expect(calls.map((c) => c.key)).toContain(showKey("DELETE", "パンサー向井のふらっと"));
  });

  it("作った番組を消すのが断られたら、帯に理由を出し、押し直せる", async () => {
    mockApi({
      ...dayRoute,
      ...showsRoute,
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("番組D")]: () => ({ status: 201, body: { onces: [created(8, doneA)] } }),
      [showKey("GET", "番組D")]: () => ({ status: 200, body: { ...madeD, onces: [created(8, doneA)] } }),
      [showKey("DELETE", "番組D")]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組D/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await screen.findByText(/番組を消せない: 内部エラー/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "取り消す" })).toBeInTheDocument();
  });

  it("複数のパートの取り消しが途中で失敗しても、押し直せば残りだけを消す", async () => {
    const remaining = new Set([10, 11]);
    let failOnce = true;
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(10, flat1), created(11, flat2)] } }),
      [showKey("GET", "朝の番組")]: () => ({
        status: 200,
        body: { ...existingShow, onces: [...remaining].map((id) => created(id, id === 10 ? flat1 : flat2)) },
      }),
      [`DELETE /api/shows/${enc("朝の番組")}/onces/10`]: () => {
        remaining.delete(10);
        return { status: 204 };
      },
      [`DELETE /api/shows/${enc("朝の番組")}/onces/11`]: () => {
        if (failOnce) {
          failOnce = false;
          return { status: 500, body: { error: "内部エラー" } };
        }
        remaining.delete(11);
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /ふらっと\(1\)/);
    await press(user, dialog, "設定を変えて予約");
    const settings = await within(dialog).findByLabelText("設定を変えて予約");
    await user.click(within(settings).getByRole("combobox", { name: "どの番組に足すか" }));
    await user.click(await screen.findByRole("option", { name: "朝の番組", hidden: true }));
    // つながない（分割番組の設定を変えない）
    await user.click(within(dialog).getByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" }));
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await screen.findByText(/1 回の予約を 1 件外せなかった/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await band()).toHaveTextContent("取り消しました（番組「朝の番組」からこの回（2 パート）の録音を外しました）");
    const deletes = calls.filter((c) => c.key.startsWith("DELETE ")).map((c) => c.key.split("/").pop());
    // 10 は 1 回だけ消し、11 は 2 回目で消えた
    expect(deletes).toEqual(["10", "11", "11"]);
  });

  it("取り消しの応答を待つ間に帯を閉じたら、結果で帯を出し直さない", async () => {
    const g = gate();
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(7, morning)] } }),
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: { ...existingShow, onces: [created(7, morning)] } }),
      [`DELETE /api/shows/${enc("朝の番組")}/onces/7`]: async () => {
        await g.wait;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    await user.click(screen.getByRole("button", { name: "知らせを閉じる" }));
    g.open();
    // 取り消しの後の番組表の読み直し（3 回目）を待ってから見る
    await waitFor(() => expect(screen.queryByText(/取り消しました/)).toBeNull());
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
  });

  it("取り消しの応答を待つ間に次の予約をしたら、新しい帯を上書きしない", async () => {
    const g = gate();
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(7, morning)] } }),
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: { ...existingShow, onces: [created(7, morning)] } }),
      [`DELETE /api/shows/${enc("朝の番組")}/onces/7`]: async () => {
        await g.wait;
        return { status: 500, body: { error: "内部エラー" } };
      },
      "POST /api/shows": () => ({ status: 201, body: {} }),
      [onces("番組D")]: () => ({ status: 201, body: { onces: [created(8, doneA)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    let dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    dialog = await openDetail(user, /番組D/);
    await press(user, dialog, "この回を録音");
    await waitFor(async () => expect(await band()).toHaveTextContent("番組「番組D」を新しく作りました"));
    g.open();
    await new Promise((r) => setTimeout(r, 50));
    const now = await band();
    expect(now).toHaveTextContent("番組「番組D」を新しく作りました");
    expect(now).not.toHaveTextContent("外せなかった");
    expect(within(now).getByRole("button", { name: "取り消す" })).not.toHaveAttribute("data-loading");
  });

  it("足した後に番組が別に変えられていたら、戻さずに知らせる", async () => {
    let current: Show = existingShow;
    const calls = mockApi({
      ...dayRoute,
      ...showsRoute,
      [showKey("GET", "朝の番組")]: () => ({ status: 200, body: current }),
      [showKey("PUT", "朝の番組")]: (init) => {
        current = JSON.parse(String(init?.body)) as Show;
        return { status: 200, body: current };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "毎週録音");
    await band();
    // 別の画面で変えられた
    current = { ...current, split: "join" };
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await screen.findByText(/その後に変わったので、取り消さなかった/)).toBeInTheDocument();
    expect(calls.filter((c) => c.key === showKey("PUT", "朝の番組"))).toHaveLength(1);
  });

  it("帯を閉じた後は取り消せない", async () => {
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(7, morning)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "知らせを閉じる" }));
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
    expect(screen.queryByRole("button", { name: "取り消す" })).toBeNull();
  });

  it("「番組を開く」は、その番組の名前で呼ぶ", async () => {
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [onces("朝の番組")]: () => ({ status: 201, body: { onces: [created(7, morning)] } }),
    });
    const opened = vi.fn();
    const user = userEvent.setup();
    renderPage(opened);
    const dialog = await openDetail(user, /^朝の番組/);
    await press(user, dialog, "この回を録音");
    await band();
    await user.click(screen.getByRole("button", { name: "番組を開く" }));
    expect(opened).toHaveBeenCalledWith("朝の番組");
  });
});

describe("GuidePage の録音予定の放送回", () => {
  it("1 回の予約で当たっている回は「この回の録音をやめる」で 1 回の予約を消し、取り消すと足し直す", async () => {
    const calls = mockApi({
      ...dayRoute,
      [showKey("GET", "番組Rの番組")]: () => ({
        status: 200,
        body: { ...existingShow, name: "番組Rの番組", onces: [created(5, plannedA)] },
      }),
      [`DELETE /api/shows/${enc("番組Rの番組")}/onces/5`]: () => ({ status: 204 }),
      [onces("番組Rの番組")]: () => ({ status: 201, body: { onces: [created(6, plannedA)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組R/);
    expect(within(dialog).queryByRole("button", { name: "この回を録音" })).toBeNull();
    expect(within(dialog).queryByRole("button", { name: "番組の設定を開く" })).toBeNull();
    await press(user, dialog, "この回の録音をやめる");
    expect(await band()).toHaveTextContent("この回の録音をやめました（番組「番組Rの番組」）");
    expect(calls.map((c) => c.key)).toContain(`DELETE /api/shows/${enc("番組Rの番組")}/onces/5`);

    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await band()).toHaveTextContent("取り消しました");
    expect(calls.find((c) => c.key === onces("番組Rの番組"))?.body).toEqual({
      airings: [{ station: "TBS", start: plannedA.start }],
    });
  });

  it("「この回の録音をやめる」に失敗したら、詳細に理由を出す", async () => {
    mockApi({
      ...dayRoute,
      [showKey("GET", "番組Rの番組")]: () => ({ status: 200, body: { ...existingShow, name: "番組Rの番組", onces: [created(5, plannedA)] } }),
      [`DELETE /api/shows/${enc("番組Rの番組")}/onces/5`]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組R/);
    await press(user, dialog, "この回の録音をやめる");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("1 回の予約を消せない: 内部エラー");
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
  });

  it("やめたのを取り消すときに足し直しが 409 で断られたら、帯に理由を出す", async () => {
    mockApi({
      ...dayRoute,
      [showKey("GET", "番組Rの番組")]: () => ({ status: 200, body: { ...existingShow, name: "番組Rの番組", onces: [created(5, plannedA)] } }),
      [`DELETE /api/shows/${enc("番組Rの番組")}/onces/5`]: () => ({ status: 204 }),
      [onces("番組Rの番組")]: () => ({
        status: 409,
        body: { error: "断った", reason: "other_show", showName: "別の番組", airing: { station: "TBS", start: plannedA.start } },
      }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /番組R/);
    await press(user, dialog, "この回の録音をやめる");
    await band();
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(await screen.findByText(/ほかの番組「別の番組」がすでにこの放送回を当てている/)).toBeInTheDocument();
  });

  it("当たっている番組を読めなければ、理由と「番組の設定を開く」を出す", async () => {
    const opened = vi.fn();
    mockApi({
      ...dayRoute,
      [showKey("GET", "番組Rの番組")]: () => ({ status: 500, body: { error: "内部エラー" } }),
    });
    const user = userEvent.setup();
    renderPage(opened);
    const dialog = await openDetail(user, /番組R/);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("番組を読めない: 内部エラー");
    await user.click(within(dialog).getByRole("button", { name: "番組の設定を開く" }));
    expect(opened).toHaveBeenCalledWith("番組Rの番組");
    expect(within(dialog).queryByRole("button", { name: "この回を録音" })).toBeNull();
  });

  it("録音予定なのに当たっている番組の名前が無い回には、番組のボタンを出さない", async () => {
    const noName = airing({ title: "名無し", start: at(today, "16:00"), end: at(today, "17:00"), state: "planned" });
    mockApi({
      [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [noName] }] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /名無し/);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("当たっている番組が分からない");
    expect(within(dialog).queryByRole("button", { name: "この回を録音" })).toBeNull();
    expect(within(dialog).queryByRole("button", { name: "毎週録音" })).toBeNull();
    expect(within(dialog).queryByRole("button", { name: "番組の設定を開く" })).toBeNull();
  });

  it("予約で当たっている回は「番組の設定を開く」だけを出し", async () => {
    mockApi({
      ...dayRoute,
      [showKey("GET", "番組Rの番組")]: () => ({ status: 200, body: { ...existingShow, name: "番組Rの番組", onces: [] } }),
    });
    const opened = vi.fn();
    const user = userEvent.setup();
    renderPage(opened);
    const dialog = await openDetail(user, /番組R/);
    await press(user, dialog, "番組の設定を開く");
    expect(opened).toHaveBeenCalledWith("番組Rの番組");
    expect(within(dialog).queryByRole("button", { name: "この回の録音をやめる" })).toBeNull();
    expect(within(dialog).queryByRole("button", { name: "毎週録音" })).toBeNull();
  });
});

describe("GuidePage のグリッドのドラッグ", () => {
  async function setup() {
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    const grid = await screen.findByTestId("guide-grid");
    return { grid, cell: within(grid).getByRole("button", { name: /番組R/ }) };
  }

  it("5px 以上動かしたらドラッグで送り、離したときに詳細を開かない。つかむ形のカーソルにする", async () => {
    const { grid, cell } = await setup();
    expect(grid).toHaveStyle({ cursor: "grab", userSelect: "none" });
    grid.scrollLeft = 100;
    grid.scrollTop = 100;
    fireEvent.mouseDown(cell, { button: 0, clientX: 50, clientY: 50 });
    fireEvent.mouseMove(window, { clientX: 60, clientY: 70, buttons: 1 });
    expect(grid).toHaveStyle({ cursor: "grabbing" });
    expect(grid.scrollLeft).toBe(90);
    expect(grid.scrollTop).toBe(80);
    fireEvent.mouseUp(window, { clientX: 60, clientY: 70 });
    fireEvent.click(cell);
    expect(grid).toHaveStyle({ cursor: "grab" });
    expect(screen.queryByRole("dialog")).toBeNull();
    // ドラッグのあとの、次の普通の押し方は効く
    fireEvent.mouseDown(cell, { button: 0, clientX: 50, clientY: 50 });
    fireEvent.mouseUp(window, { clientX: 50, clientY: 50 });
    fireEvent.click(cell);
    expect(await screen.findByRole("dialog")).toHaveTextContent("番組R");
  });

  it("動きが 5px 未満なら押したことにして詳細を開く", async () => {
    const { grid, cell } = await setup();
    grid.scrollTop = 100;
    fireEvent.mouseDown(cell, { button: 0, clientX: 50, clientY: 50 });
    fireEvent.mouseMove(window, { clientX: 52, clientY: 53, buttons: 1 });
    fireEvent.mouseUp(window, { clientX: 52, clientY: 53 });
    fireEvent.click(cell);
    expect(grid.scrollTop).toBe(100);
    expect(await screen.findByRole("dialog")).toHaveTextContent("番組R");
  });

  it("左ボタン以外ではドラッグしない", async () => {
    const { grid, cell } = await setup();
    grid.scrollTop = 100;
    fireEvent.mouseDown(cell, { button: 2, clientX: 50, clientY: 50 });
    fireEvent.mouseMove(window, { clientX: 50, clientY: 90, buttons: 1 });
    fireEvent.mouseUp(window);
    expect(grid.scrollTop).toBe(100);
  });

  it("スクロールバーの上の押下ではドラッグを始めない", async () => {
    const { grid, cell } = await setup();
    // jsdom にはレイアウトが無いので、枠の内側を 200 x 100 とする。x = 250 は縦のスクロールバーの上
    Object.defineProperty(grid, "clientWidth", { value: 200, configurable: true });
    Object.defineProperty(grid, "clientHeight", { value: 100, configurable: true });
    grid.scrollTop = 100;
    fireEvent.mouseDown(grid, { button: 0, clientX: 250, clientY: 50 });
    fireEvent.mouseMove(window, { clientX: 250, clientY: 90, buttons: 1 });
    expect(grid.scrollTop).toBe(100);
    expect(grid).toHaveStyle({ cursor: "grab" });
    fireEvent.mouseUp(window);
    fireEvent.click(cell);
    expect(await screen.findByRole("dialog")).toHaveTextContent("番組R");
    // 内側の押下は効く
    fireEvent.mouseDown(cell, { button: 0, clientX: 50, clientY: 50 });
    fireEvent.mouseMove(window, { clientX: 50, clientY: 20, buttons: 1 });
    expect(grid.scrollTop).toBe(130);
    fireEvent.mouseUp(window);
  });

  it("mouseup を取りこぼしても、ボタンが離れた mousemove でドラッグを終える", async () => {
    const { grid, cell } = await setup();
    grid.scrollTop = 100;
    fireEvent.mouseDown(cell, { button: 0, clientX: 50, clientY: 50 });
    fireEvent.mouseMove(window, { clientX: 50, clientY: 70, buttons: 1 });
    expect(grid.scrollTop).toBe(80);
    fireEvent.mouseMove(window, { clientX: 50, clientY: 90, buttons: 0 });
    expect(grid).toHaveStyle({ cursor: "grab" });
    fireEvent.mouseMove(window, { clientX: 50, clientY: 120, buttons: 0 });
    expect(grid.scrollTop).toBe(80);
  });

  it("ドラッグの途中で外れても、window のリスナーを残さない", async () => {
    const remove = vi.spyOn(window, "removeEventListener");
    const { cell } = await setup();
    fireEvent.mouseDown(cell, { button: 0, clientX: 50, clientY: 50 });
    cleanup();
    const removed = remove.mock.calls.map((c) => c[0]);
    expect(removed).toContain("mousemove");
    expect(removed).toContain("mouseup");
    remove.mockRestore();
  });
});

describe("GuidePage の 1 局の表示", () => {
  const VIEW = "rajikura.guide.view";
  const STATION = "rajikura.guide.station";

  async function pc() {
    setWide(true);
    const calls = mockApi({ ...dayRoute, ...showsRoute });
    const user = userEvent.setup();
    renderPage();
    return { calls, user };
  }

  it("局名を押すと、その局の 1 局の表示になり、選んだ局と表示を覚える", async () => {
    const { user } = await pc();
    const grid = await screen.findByTestId("guide-grid");
    await user.click(within(grid).getByRole("button", { name: "ニッポン放送だけを表示" }));
    const table = await screen.findByTestId("guide-station-table");
    expect(screen.queryByTestId("guide-grid")).toBeNull();
    expect(within(table).getByText("オールナイト")).toBeInTheDocument();
    expect(within(table).queryByText("番組R")).toBeNull();
    expect(window.localStorage.getItem(STATION)).toBe("LFR");
    expect(window.localStorage.getItem(VIEW)).toBe("station");
  });

  it("× は局を隠すだけで、1 局の表示に移らない", async () => {
    const { user } = await pc();
    const grid = await screen.findByTestId("guide-grid");
    await user.click(within(grid).getByRole("button", { name: "ニッポン放送を隠す" }));
    expect(await screen.findByTestId("guide-grid")).toBeInTheDocument();
    expect(screen.queryByTestId("guide-station-table")).toBeNull();
    expect(window.localStorage.getItem(VIEW)).toBeNull();
  });

  it("表は時刻・番組名・出演者・状態の列で、長い文字は 1 行で切り、対象外の印も出す", async () => {
    window.localStorage.setItem(VIEW, "station");
    await pc();
    const table = await screen.findByTestId("guide-station-table");
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["時刻", "番組名", "出演者", "状態"]);
    const row = within(table).getByRole("button", { name: /ふらっと\(1\)/ }).closest("tr")!;
    const cells = within(row).getAllByRole("cell");
    expect(cells[0]).toHaveTextContent("08:30〜09:00");
    expect(cells[2]).toHaveTextContent("向井慧");
    expect(cells[1]).toHaveStyle({ whiteSpace: "nowrap", textOverflow: "ellipsis" });
    expect(cells[2]).toHaveStyle({ whiteSpace: "nowrap", textOverflow: "ellipsis" });
    expect(within(table).getByText("録音予定")).toBeInTheDocument();
    expect(within(table).getByText("録音済み")).toBeInTheDocument();
    expect(within(table).getAllByText("ライブで録る")).toHaveLength(1);
  });

  it("行を押すと右の欄に詳細を開き、表は隠れない。出演者の欄を押しても開く", async () => {
    const { user } = await pc();
    await screen.findByTestId("guide-grid");
    await user.click(screen.getByRole("radio", { name: "1 局" }));
    const table = await screen.findByTestId("guide-station-table");
    const row = within(table).getByRole("button", { name: /ふらっと\(1\)/ }).closest("tr")!;
    await user.click(within(row).getByText("向井慧"));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("パンサー向井のふらっと(1)");
    expect(document.querySelector(".mantine-Drawer-overlay")).toBeNull();
    expect(table).toBeVisible();
    await user.click(within(table).getByRole("button", { name: /番組D/ }));
    await waitFor(() => expect(screen.getByRole("dialog")).toHaveTextContent("番組D"));
  });

  it("「全局 / 1 局」を切り替えられ、読み直しても同じ表示と局で開く", async () => {
    const { user } = await pc();
    await screen.findByTestId("guide-grid");
    await user.click(screen.getByRole("radio", { name: "1 局" }));
    await user.click(screen.getByRole("combobox", { name: "局" }));
    await user.click(await screen.findByRole("option", { name: "ニッポン放送", hidden: true }));
    expect(within(await screen.findByTestId("guide-station-table")).getByText("オールナイト")).toBeInTheDocument();
    expect(window.localStorage.getItem(VIEW)).toBe("station");
    cleanup();

    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    expect(within(await screen.findByTestId("guide-station-table")).getByText("オールナイト")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "全局" }));
    expect(await screen.findByTestId("guide-grid")).toBeInTheDocument();
    expect(window.localStorage.getItem(VIEW)).toBe("all");
  });

  it("局を選ぶ欄に、隠した局も出る", async () => {
    window.localStorage.setItem("rajikura.guide.hiddenStations", JSON.stringify(["LFR"]));
    window.localStorage.setItem(VIEW, "station");
    const { user } = await pc();
    await screen.findByTestId("guide-station-table");
    await user.click(screen.getByRole("combobox", { name: "局" }));
    expect(await screen.findByRole("option", { name: "ニッポン放送", hidden: true })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "TBSラジオ", hidden: true })).toBeInTheDocument();
  });

  it("壊れた値や localStorage が使えないときは全局で動く", async () => {
    window.localStorage.setItem(VIEW, "bogus");
    await pc();
    expect(await screen.findByTestId("guide-grid")).toBeInTheDocument();
    cleanup();

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    const { user } = await pc();
    await user.click(await screen.findByRole("radio", { name: "1 局" }));
    expect(await screen.findByTestId("guide-station-table")).toBeInTheDocument();
    vi.restoreAllMocks();
  });

  it("スマホ幅では切り替えを出さず、リストのまま", async () => {
    window.localStorage.setItem(VIEW, "station");
    mockApi(dayRoute);
    renderPage();
    expect(await screen.findByTestId("guide-list")).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: "1 局" })).toBeNull();
    expect(screen.queryByTestId("guide-station-table")).toBeNull();
  });

  describe("今の時刻まで送る", () => {
    afterEach(() => {
      vi.useRealTimers();
      vi.restoreAllMocks();
    });

    // 行 i の上端を 80 * i、表の枠の上端を 0 とみなす
    function fakeLayout() {
      vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
        const row = this.closest("tr");
        const i = row ? Array.from(row.parentElement!.children).indexOf(row) : 0;
        return { top: row ? 80 * i : 0 } as DOMRect;
      });
    }

    it("今日を開いたら、今放送中の行を上の方に出し、詳細を開いても送り直さない", async () => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date(at(today, "10:30")));
      fakeLayout();
      window.localStorage.setItem(VIEW, "station");
      const { user } = await pc();
      const table = await screen.findByTestId("guide-station-table");
      // 10:00〜11:00 の放送回（番組R）は 3 行目（添字 2）
      expect(table.scrollTop).toBe(80 * 2 - 40);
      table.scrollTop = 7;
      await user.click(within(table).getByRole("button", { name: /番組D/ }));
      await screen.findByRole("dialog");
      expect(table.scrollTop).toBe(7);
    });

    it("今日でなければ送らない", async () => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date(at(today, "10:30")));
      fakeLayout();
      const yesterday = addDays(today, -1);
      const yesterdayAiring = airing({ title: "昨日の番組", start: at(yesterday, "10:00"), end: at(yesterday, "11:00") });
      const yesterdayStation: GuideStation = { id: "TBS", name: "TBSラジオ", airings: [yesterdayAiring] };
      setWide(true);
      window.localStorage.setItem(VIEW, "station");
      mockApi({
        ...dayRoute,
        ...showsRoute,
        [guideKey(yesterday)]: () => ({ status: 200, body: { day: yesterday, stations: [yesterdayStation] } }),
      });
      const user = userEvent.setup();
      renderPage();
      await screen.findByTestId("guide-station-table");
      await user.click(screen.getByRole("button", { name: "前の日" }));
      // 前の日の表に代わった（前の日だけにある放送回が出た）あとで、送っていないことを見る
      expect(await screen.findByRole("button", { name: /昨日の番組/ })).toBeInTheDocument();
      expect(screen.getByTestId("guide-station-table").scrollTop).toBe(0);
    });
  });
});

describe("GuidePage の録らなかったの放送回", () => {
  const skippedA = airing({
    title: "特番",
    start: at(today, "18:00"),
    end: at(today, "19:00"),
    state: "skipped",
    showName: "朝の番組",
    jobId: 42,
    skipBy: "title",
  });
  const route: Record<string, Handler> = {
    [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [skippedA] }] } }),
  };

  it("状態と「録る」を出し、押すとそのジョブを録るに戻す（録らなかったは手動再試行になる）", async () => {
    const calls = mockApi({ ...route, "POST /api/jobs/42/record": () => ({ status: 200, body: { id: 42, state: "retrying" } }) });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /特番/);
    expect(within(dialog).getByText(/番組表の番組名「特番」が番組「朝の番組」の名前で始まらないので、録らなかった/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "この回を録音" })).toBeNull();
    await press(user, dialog, "録る");
    expect(await band()).toHaveTextContent("この回を録音します。番組「朝の番組」の録らなかった回を再試行にしました");
    expect(calls.map((c) => c.key)).toContain("POST /api/jobs/42/record");
  });

  it("「録る」を断られたら、詳細に理由を出す", async () => {
    mockApi({
      ...route,
      "POST /api/jobs/42/record": () => ({ status: 409, body: { error: "録音期限を過ぎているので再試行できない" } }),
    });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /特番/);
    await press(user, dialog, "録る");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("録音期限を過ぎているので再試行できない");
  });
});

describe("GuidePage の録らない予定の放送回", () => {
  const planned = (over: Partial<GuideAiring> = {}) =>
    airing({
      title: "特番",
      start: at(today, "18:00"),
      end: at(today, "19:00"),
      state: "skip_planned",
      showName: "朝の番組",
      jobId: 43,
      skipBy: "title",
      ...over,
    });
  const routeOf = (a: GuideAiring): Record<string, Handler> => ({
    [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [a] }] } }),
  });

  it("録らない予定と、番組名で止めたことを出し、「録る」で録音予定に戻す", async () => {
    const calls = mockApi({ ...routeOf(planned()), "POST /api/jobs/43/record": () => ({ status: 200, body: { id: 43, state: "waiting" } }) });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /特番/);
    expect(within(dialog).getByText("録らない予定")).toBeInTheDocument();
    expect(within(dialog).getByText(/番組表の番組名「特番」が番組「朝の番組」の名前で始まらないので、録らない予定にした/)).toBeInTheDocument();
    await press(user, dialog, "録る");
    expect(await band()).toHaveTextContent("この回を録音します。番組「朝の番組」の録らない予定の回を録音予定に戻しました");
    expect(calls.map((c) => c.key)).toContain("POST /api/jobs/43/record");
  });

  it("人が止めた回は、人が決めたことを出す", async () => {
    mockApi(routeOf(planned({ skipBy: "person" })));
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /特番/);
    expect(within(dialog).getByText(/人が録らないと決めたので、録らない予定にした/)).toBeInTheDocument();
  });

  it("ジョブが無ければ、予約なしと同じく「この回を録音」を出す", async () => {
    mockApi({ ...routeOf(planned({ jobId: undefined })), "GET /api/shows": () => ({ status: 200, body: [] }) });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /特番/);
    expect(await within(dialog).findByRole("button", { name: "この回を録音" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "録る" })).toBeNull();
  });
});

describe("GuidePage の「録らない」", () => {
  const plannedA = (over: Partial<GuideAiring> = {}) =>
    airing({
      title: "朝の番組",
      start: at(today, "18:00"),
      end: at(today, "19:00"),
      state: "planned",
      showName: "朝の番組",
      jobId: 44,
      skippable: true,
      ...over,
    });
  const routeOf = (a: GuideAiring): Record<string, Handler> => ({
    [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: [{ id: "TBS", name: "TBSラジオ", airings: [a] }] } }),
    [showKey("GET", "朝の番組")]: () => ({
      status: 200,
      body: { name: "朝の番組", split: "none", check_title: true, enabled: true, weekly: [], onces: [] },
    }),
  });

  it("録音予定のジョブに「録らない」を出し、押すとそのジョブを録らないにする", async () => {
    const calls = mockApi({ ...routeOf(plannedA()), "POST /api/jobs/44/skip": () => ({ status: 200, body: { id: 44, state: "skipped" } }) });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /朝の番組/);
    await press(user, dialog, "録らない");
    expect(await band()).toHaveTextContent("この回は録音しません。番組「朝の番組」の録音予定の回を録らないにしました");
    expect(calls.map((c) => c.key)).toContain("POST /api/jobs/44/skip");
  });

  it("「録らない」を断られたら、詳細に理由を出す", async () => {
    mockApi({ ...routeOf(plannedA()), "POST /api/jobs/44/skip": () => ({ status: 409, body: { error: "このジョブの状態では受け付けられない" } }) });
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /朝の番組/);
    await press(user, dialog, "録らない");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("このジョブの状態では受け付けられない");
  });

  it("ジョブが無い・試行のある録音予定には「録らない」を出さない", async () => {
    mockApi(routeOf(plannedA({ jobId: undefined, skippable: false })));
    const user = userEvent.setup();
    renderPage();
    const dialog = await openDetail(user, /朝の番組/);
    expect(within(dialog).queryByRole("button", { name: "録らない" })).toBeNull();
  });
});

describe("GuidePage の今の時刻の線と放送中の印", () => {
  const VIEW = "rajikura.guide.view";
  const tomorrow = addDays(today, 1);

  function freeze(iso: string) {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date(iso));
  }

  it("PC 幅のグリッドは、今日の今の位置に線と札（放送日の時刻）を出す", async () => {
    freeze(at(today, "14:32"));
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    await screen.findByTestId("guide-grid");
    expect(screen.getByTestId("guide-now-label")).toHaveTextContent("14:32");
    const line = screen.getByTestId("guide-now-line");
    expect(line.style.pointerEvents).toBe("none");
    // 5:00 から 9 時間 32 分
    expect(line.style.top).toBe(`${32 + (9 * 60 + 32) * 1.6 - 1}px`);
  });

  it("0:00〜4:59 は札を 24:00〜28:59 で書く", async () => {
    freeze(at(tomorrow, "01:32"));
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    await screen.findByTestId("guide-grid");
    expect(screen.getByTestId("guide-now-label")).toHaveTextContent("25:32");
  });

  it("今日でない放送日には線を出さない", async () => {
    freeze(at(today, "14:32"));
    setWide(true);
    const yesterday = addDays(today, -1);
    mockApi({
      ...dayRoute,
      ...showsRoute,
      [guideKey(yesterday)]: () => ({ status: 200, body: { day: yesterday, stations } }),
    });
    const user = userEvent.setup();
    renderPage();
    await screen.findByTestId("guide-now-line");
    await user.click(screen.getByRole("button", { name: "前の日" }));
    await waitFor(() => expect(screen.queryByTestId("guide-now-line")).toBeNull());
  });

  it("1 分ほどごとに線を動かし、5:00 を越えたら線を消す", async () => {
    freeze(at(tomorrow, "04:58"));
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    await screen.findByTestId("guide-grid");
    expect(screen.getByTestId("guide-now-label")).toHaveTextContent("28:58");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60 * 1000);
    });
    expect(screen.getByTestId("guide-now-label")).toHaveTextContent("28:59");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60 * 1000);
    });
    expect(screen.queryByTestId("guide-now-line")).toBeNull();
  });

  it("タブが表に戻ったら、すぐ読み直す", async () => {
    freeze(at(today, "14:32"));
    setWide(true);
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    await screen.findByTestId("guide-grid");
    vi.setSystemTime(new Date(at(today, "14:40")));
    expect(screen.getByTestId("guide-now-label")).toHaveTextContent("14:32");
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(screen.getByTestId("guide-now-label")).toHaveTextContent("14:40");
  });

  it("狭い幅のリストは、放送中の回の行に印を付ける（線は引かない）", async () => {
    freeze(at(today, "10:30"));
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    const list = await screen.findByTestId("guide-list");
    expect(screen.queryByTestId("guide-now-line")).toBeNull();
    expect(within(list).getByRole("button", { name: /番組R .*放送中/ })).toBeInTheDocument();
    expect(within(list).getAllByRole("button", { name: /放送中/ })).toHaveLength(1);
    expect(within(list).getByText("放送中")).toBeInTheDocument();
  });

  it("番組の間が空いていて当たる回が無ければ、リストに印を付けない", async () => {
    freeze(at(today, "07:00"));
    mockApi({ ...dayRoute, ...showsRoute });
    renderPage();
    const list = await screen.findByTestId("guide-list");
    expect(within(list).queryAllByRole("button", { name: /放送中/ })).toHaveLength(0);
  });

  it("1 局の表示は、放送中の回の行に印を付け、5:00 を越えたら消す", async () => {
    freeze(at(tomorrow, "04:59"));
    // 放送日 today の 28:30〜29:30 の回
    const late = airing({ title: "深夜の番組", start: at(tomorrow, "04:30"), end: at(tomorrow, "05:30") });
    const lateStations: GuideStation[] = [{ id: "TBS", name: "TBSラジオ", airings: [late] }];
    setWide(true);
    window.localStorage.setItem(VIEW, "station");
    mockApi({
      [guideKey(today)]: () => ({ status: 200, body: { day: today, stations: lateStations } }),
      ...showsRoute,
    });
    renderPage();
    const table = await screen.findByTestId("guide-station-table");
    expect(within(table).getByRole("button", { name: /深夜の番組 .*放送中/ })).toBeInTheDocument();
    expect(within(table).getByText("放送中")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60 * 1000);
    });
    expect(within(table).queryByText("放送中")).toBeNull();
    expect(within(table).getByRole("button", { name: /深夜の番組/ }).getAttribute("aria-label")).not.toContain("放送中");
  });
});
