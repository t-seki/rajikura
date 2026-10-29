// 番組名の末尾の印、自動の組、共通の頭の名前、番組表の選択、既存の番組のつなぐ設定。
import { MantineProvider } from "@mantine/core";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Show } from "../shows/api";
import type { GuideAiring, GuideStation } from "./api";
import { broadcastDayOf, hasTrailingMark, trimTrailingMark } from "./format";
import { GuidePage } from "./GuidePage";
import { autoGroup, groupName } from "./reserve";

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

function setMedia(wide: boolean, touch = false) {
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
    stationId: "LFR",
    stationName: "ニッポン放送",
    performer: "",
    description: "",
    descriptionParts: [],
    state: "none",
    notAvailable: false,
    ...over,
  };
}

/** 番組名の末尾の印の例。internal/domain/title_test.go の trailingMarkCases と同じ例にする。 */
const trailingMarkCases: { title: string; want: string; mark: boolean }[] = [
  // 括弧付きの N時台（全角・半角）
  { title: "ナイツ ザ・ラジオショー（13時台）", want: "ナイツ ザ・ラジオショー", mark: true },
  { title: "ナイツ ザ・ラジオショー(１４時台)", want: "ナイツ ザ・ラジオショー", mark: true },
  // 括弧付きの PARTN（大文字・小文字・全角、数字との間の空白）
  { title: "STEP ONE(PART1)", want: "STEP ONE", mark: true },
  { title: "STEP ONE （ｐａｒｔ　２）", want: "STEP ONE", mark: true },
  { title: "STEP ONE(Part 3)", want: "STEP ONE", mark: true },
  // 括弧の無い PartN（前に空白があるときだけ）
  { title: "垣花正 あなたとハッピー！ Part2", want: "垣花正 あなたとハッピー！", mark: true },
  { title: "Good Luck！Morning！ Part1", want: "Good Luck！Morning！", mark: true },
  { title: "番組 PART 2", want: "番組", mark: true },
  { title: "番組　ｐａｒｔ２", want: "番組", mark: true },
  // Hour.N
  { title: "AWAKE Hour.1", want: "AWAKE", mark: true },
  { title: "AWAKE　Ｈｏｕｒ．２", want: "AWAKE", mark: true },
  // 丸数字
  { title: "HAPPYパンチ！　①", want: "HAPPYパンチ！", mark: true },
  { title: "HAPPYパンチ！ ⑳", want: "HAPPYパンチ！", mark: true },
  // 括弧の無い N時台。前に曜日、後ろに日付が付いてもよい
  { title: "ふんわり　火曜ＭＣは木村祐一さん　８時台（１０／２０）", want: "ふんわり　火曜ＭＣは木村祐一さん", mark: true },
  { title: "ふんわり「おひる、こんなんどう？スペシャル」　９時台", want: "ふんわり「おひる、こんなんどう？スペシャル」", mark: true },
  { title: "ふんわり「おひる、こんなんどう？スペシャル」　１０時台", want: "ふんわり「おひる、こんなんどう？スペシャル」", mark: true },
  { title: "ふんわり　まいにちリクエスト♪　火曜１１時台（１０／２０）", want: "ふんわり　まいにちリクエスト♪", mark: true },
  { title: "番組 8時台 (10/20)", want: "番組", mark: true },
  // 前からある形
  { title: "パンサー向井の#ふらっと (1)", want: "パンサー向井の#ふらっと", mark: true },
  { title: "ふらっと １２時３０分〜１２時５５分", want: "ふらっと", mark: true },
  // 印にしないもの
  { title: "番組(10/20)", want: "番組(10/20)", mark: false },
  { title: "番組 （１０／２０）", want: "番組 （１０／２０）", mark: false },
  { title: "XPart2", want: "XPart2", mark: false },
  { title: "番組Part2", want: "番組Part2", mark: false },
  { title: "Part2の番組", want: "Part2の番組", mark: false },
  { title: "番組 Part2 スペシャル", want: "番組 Part2 スペシャル", mark: false },
  { title: "13時台の番組", want: "13時台の番組", mark: false },
  { title: "番組（13時台）スペシャル", want: "番組（13時台）スペシャル", mark: false },
  { title: "HAPPYパンチ！①", want: "HAPPYパンチ！①", mark: false },
  { title: "AWAKEHour.1", want: "AWAKEHour.1", mark: false },
  { title: "番組 2", want: "番組 2", mark: false },
  { title: "SCHOOL OF LOCK!", want: "SCHOOL OF LOCK!", mark: false },
  { title: "垣花正 あなたとハッピー！", want: "垣花正 あなたとハッピー！", mark: false },
];

describe("番組名の末尾の印", () => {
  it.each(trailingMarkCases)("$title", ({ title, want, mark }) => {
    expect(trimTrailingMark(title)).toBe(want);
    expect(hasTrailingMark(title)).toBe(mark);
  });
});

// internal/domain/keyword_test.go の TestKeywordPartsGroupShapes と同じ例。
const d20 = "2026-10-20";
const lfrDay = [
  airing({ title: "垣花正 あなたとハッピー！", start: at(d20, "08:00"), end: at(d20, "10:16") }),
  airing({ title: "ニュース", start: at(d20, "10:16"), end: at(d20, "10:21") }),
  airing({ title: "垣花正 あなたとハッピー！ Part2", start: at(d20, "10:21"), end: at(d20, "11:00") }),
  airing({ title: "ナイツ ザ・ラジオショー（13時台）", start: at(d20, "13:00"), end: at(d20, "14:00") }),
  airing({ title: "ナイツ ザ・ラジオショー（14時台）", start: at(d20, "14:00"), end: at(d20, "15:00") }),
  airing({ title: "ナイツ ザ・ラジオショー（15時台）", start: at(d20, "15:00"), end: at(d20, "15:30") }),
];
const fmtDay = [
  airing({ stationId: "FMT", title: "TOKYO FM NEWS", start: at(d20, "21:55"), end: at(d20, "22:00") }),
  airing({ stationId: "FMT", title: "SCHOOL OF LOCK!", start: at(d20, "22:00"), end: at(d20, "23:00") }),
  airing({ stationId: "FMT", title: "SCHOOL OF LOCK!", start: at(d20, "23:00"), end: at(d20, "23:55") }),
  airing({ stationId: "FMT", title: "TOKYO FM NEWS", start: at(d20, "23:55"), end: at(d20, "24:00").replace("T24:00", "T23:59") }),
];
const tbsDay = [
  airing({ stationId: "TBS", title: "パンサー向井のふらっと (1)", start: at(d20, "08:30"), end: at(d20, "09:00") }),
  airing({ stationId: "TBS", title: "ニュース", start: at(d20, "09:00"), end: at(d20, "09:05") }),
  airing({ stationId: "TBS", title: "パンサー向井のふらっと (2)", start: at(d20, "09:05"), end: at(d20, "11:00") }),
];
const qrrDay = [
  airing({ stationId: "QRR", title: "ふんわり　火曜ＭＣは木村祐一さん　８時台（１０／２０）", start: at(d20, "08:00"), end: at(d20, "09:00") }),
  airing({ stationId: "QRR", title: "ふんわり「おひる、こんなんどう？スペシャル」　９時台", start: at(d20, "09:00"), end: at(d20, "10:00") }),
  airing({ stationId: "QRR", title: "ふんわり「おひる、こんなんどう？スペシャル」　１０時台", start: at(d20, "10:00"), end: at(d20, "11:00") }),
  airing({ stationId: "QRR", title: "ふんわり　まいにちリクエスト♪　火曜１１時台（１０／２０）", start: at(d20, "11:00"), end: at(d20, "12:00") }),
];
const hhmm = (as: GuideAiring[]) => as.map((a) => a.start.slice(11, 16));
const before = new Date(at("2026-10-19", "12:00")).getTime();

describe("自動の組", () => {
  it.each([
    ["ナイツ（どのパートにも括弧付きの N時台）", lfrDay[4], lfrDay, ["13:00", "14:00", "15:00"]],
    ["垣花（印の無い 1 つ目から見ても Part2 と組になる。間のニュースは入らない）", lfrDay[0], lfrDay, ["08:00", "10:21"]],
    ["垣花（Part2 から）", lfrDay[2], lfrDay, ["08:00", "10:21"]],
    ["ふらっと (1) と (2)（間のニュースは入らない）", tbsDay[0], tbsDay, ["08:30", "09:05"]],
    ["SCHOOL OF LOCK!（印が無い放送回だけなので 1 件）", fmtDay[1], fmtDay, ["22:00"]],
    ["TOKYO FM NEWS（1 日に何度もある。1 件）", fmtDay[0], fmtDay, ["21:55"]],
    ["ふんわり（副題が時間帯ごとに違うので 1 件）", qrrDay[0], qrrDay, ["08:00"]],
    ["ふんわり（副題が同じ 9時台と 10時台は組になる）", qrrDay[1], qrrDay, ["09:00", "10:00"]],
  ] as const)("%s", (_, a, day, want) => {
    expect(hhmm(autoGroup(a, [...day], before))).toEqual(want);
  });
});

describe("選んだ放送回から決まる番組の名前", () => {
  it("1 件なら末尾の印を落とした名前、2 件以上なら共通の頭（4 文字未満なら先頭の放送回の名前）", () => {
    expect(groupName([lfrDay[3]])).toBe("ナイツ ザ・ラジオショー");
    expect(groupName(qrrDay)).toBe("ふんわり");
    expect(groupName(lfrDay.slice(3))).toBe("ナイツ ザ・ラジオショー");
    expect(groupName([tbsDay[2], tbsDay[0]])).toBe("パンサー向井のふらっと");
    expect(groupName([fmtDay[1], fmtDay[2]])).toBe("SCHOOL OF LOCK!");
    // 共通の頭が 4 文字未満（「AB」）なら、先頭（開始の順）の放送回の名前
    const x = airing({ title: "AB朝(1)", start: at(d20, "06:00"), end: at(d20, "07:00") });
    const y = airing({ title: "AB夜", start: at(d20, "05:00"), end: at(d20, "06:00") });
    expect(groupName([x, y])).toBe("AB夜");
  });
});

// --- 画面 ---

const n13 = airing({ title: "ナイツ ザ・ラジオショー（13時台）", start: at(today, "13:00"), end: at(today, "14:00") });
const n14 = airing({ title: "ナイツ ザ・ラジオショー（14時台）", start: at(today, "14:00"), end: at(today, "15:00") });
const n15 = airing({ title: "ナイツ ザ・ラジオショー（15時台）", start: at(today, "15:00"), end: at(today, "15:30") });
const other = airing({ title: "別の番組", start: at(today, "16:00"), end: at(today, "17:00") });
const planned = airing({
  title: "録音予定の番組",
  start: at(today, "15:30"),
  end: at(today, "16:00"),
  state: "planned",
  showName: "別の番組",
});
const tbs1 = airing({ stationId: "TBS", stationName: "TBSラジオ", title: "TBSの番組", start: at(today, "13:00"), end: at(today, "14:00") });

const stationsOf = (airings: GuideAiring[]): GuideStation[] => [
  { id: "LFR", name: "ニッポン放送", airings },
  { id: "TBS", name: "TBSラジオ", airings: [tbs1] },
];
const guideKey = `GET /api/guide?day=${today}`;
const enc = encodeURIComponent;
const showKey = (method: string, name: string) => `${method} /api/shows/${enc(name)}`;
const onces = (name: string) => `POST /api/shows/${enc(name)}/onces`;
const created = (id: number, a: GuideAiring) => ({ id, stationId: a.stationId, start: a.start, missing: false });

function routes(airings: GuideAiring[], shows: Show[] = []): Record<string, Handler> {
  return {
    [guideKey]: () => ({ status: 200, body: { day: today, stations: stationsOf(airings) } }),
    "GET /api/shows": () => ({ status: 200, body: shows }),
  };
}

const renderPage = () =>
  render(
    <MantineProvider env="test">
      <GuidePage />
    </MantineProvider>,
  );

const grid = () => screen.findByTestId("guide-grid");
const cell = (g: HTMLElement, name: RegExp) => within(g).getByRole("button", { name });
const selectedTitles = (g: HTMLElement) =>
  Array.from(g.querySelectorAll("[data-selected]")).map((e) => e.getAttribute("title")?.split(" ")[0] + (e.getAttribute("title")?.match(/（\d+時台）/)?.[0] ?? ""));
const partsGroup = (dialog: HTMLElement) => within(dialog).getByRole("group", { name: "選んだ放送回" });

async function press(user: ReturnType<typeof userEvent.setup>, root: HTMLElement, name: string) {
  const button = await within(root).findByRole("button", { name });
  await waitFor(() => expect(button).not.toHaveAttribute("data-loading"));
  await user.click(button);
}

beforeEach(() => {
  window.localStorage.clear();
  setMedia(true);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("番組表の選択", () => {
  it("普通のクリックで自動の組（予約なしの放送回）が選ばれ、番組表に枠が付き、詳細に「パート N 件（自動で選びました）」が並ぶ", async () => {
    mockApi(routes([n13, n14, n15, other]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /14時台/));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(g.querySelectorAll("[data-selected]")).toHaveLength(3));
    expect(cell(g, /13時台/)).toHaveAttribute("aria-pressed", "true");
    expect(cell(g, /別の番組/)).not.toHaveAttribute("aria-pressed");
    expect(within(dialog).getByText("パート 3 件（自動で選びました）")).toBeInTheDocument();
    expect(within(partsGroup(dialog)).getAllByRole("checkbox").every((c) => (c as HTMLInputElement).checked)).toBe(true);
    // 名前の既定は共通の頭
    await press(user, dialog, "設定を変えて予約");
    expect(within(await within(dialog).findByLabelText("設定を変えて予約")).getByRole("textbox", { name: /番組の名前/ })).toHaveValue(
      "ナイツ ザ・ラジオショー",
    );
  });

  it("予約済みの放送回は自動の組から除く。予約済みの放送回を普通に開いたら選択に入れず、番組の操作を出す", async () => {
    const r14 = { ...n14, state: "planned" as const, showName: "別の番組" };
    mockApi({
      ...routes([n13, r14, n15]),
      [showKey("GET", "別の番組")]: () => ({ status: 200, body: { name: "別の番組", enabled: true, split: "none", check_title: true, weekly: [], onces: [] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /13時台/));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(within(dialog).getByText("パート 2 件（自動で選びました）")).toBeInTheDocument());
    expect(cell(g, /14時台/)).not.toHaveAttribute("aria-pressed");
    await user.click(cell(g, /14時台/));
    expect(await within(screen.getByRole("dialog")).findByRole("button", { name: "番組の設定を開く" })).toBeInTheDocument();
    expect(g.querySelectorAll("[data-selected]")).toHaveLength(0);
  });

  it("Ctrl+Click（⌘+Click）で足す・外す。予約済みの放送回は選べず、初めの 1 度だけ知らせる", async () => {
    mockApi(routes([n13, n14, n15, planned, other]));
    renderPage();
    const g = await grid();
    fireEvent.click(cell(g, /別の番組/), { ctrlKey: true });
    fireEvent.click(cell(g, /13時台/), { metaKey: true });
    expect(g.querySelectorAll("[data-selected]")).toHaveLength(2);
    // 2 件以上で詳細を閉じているので、画面の下に帯を出す
    const bar = screen.getByRole("region", { name: "選択中の放送回" });
    expect(bar).toHaveTextContent("2 件を選択中（13:00〜17:00）");
    fireEvent.click(cell(g, /別の番組/), { ctrlKey: true });
    expect(g.querySelectorAll("[data-selected]")).toHaveLength(1);
    expect(screen.queryByRole("region", { name: "選択中の放送回" })).toBeNull();
    fireEvent.click(cell(g, /録音予定の番組/), { ctrlKey: true });
    expect(screen.getByRole("status", { name: "選択の知らせ" })).toHaveTextContent("予約済みの回は選べない");
    expect(cell(g, /録音予定の番組/)).not.toHaveAttribute("aria-pressed");
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "選択の知らせを閉じる" }));
    fireEvent.click(cell(g, /録音予定の番組/), { ctrlKey: true });
    expect(screen.queryByRole("status", { name: "選択の知らせ" })).toBeNull();
  });

  it("Shift+Click で前に選んだ放送回からの範囲を足し、予約済みの放送回は飛ばす", async () => {
    mockApi(routes([n13, n14, n15, planned, other]));
    renderPage();
    const g = await grid();
    fireEvent.click(cell(g, /13時台/), { ctrlKey: true });
    fireEvent.click(cell(g, /別の番組/), { shiftKey: true });
    expect(g.querySelectorAll("[data-selected]")).toHaveLength(4);
    expect(cell(g, /録音予定の番組/)).not.toHaveAttribute("aria-pressed");
    expect(screen.getByRole("status", { name: "選択の知らせ" })).toHaveTextContent("予約済みの回は選べない");
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toHaveTextContent("4 件を選択中");
  });

  it("違う局の放送回を Ctrl/Shift+Click したら、その放送回から選び直す", async () => {
    mockApi(routes([n13, n14, n15]));
    renderPage();
    const g = await grid();
    fireEvent.click(cell(g, /13時台/), { ctrlKey: true });
    fireEvent.click(cell(g, /14時台/), { ctrlKey: true });
    fireEvent.click(cell(g, /TBSの番組/), { shiftKey: true });
    expect(g.querySelectorAll("[data-selected]")).toHaveLength(1);
    expect(cell(g, /TBSの番組/)).toHaveAttribute("aria-pressed", "true");
  });

  it("詳細のチェックを外すと番組表の枠も消え、「この回だけにする」で押した放送回だけが残る。全部外して押すと何も足さない", async () => {
    const calls = mockApi(routes([n13, n14, n15]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /14時台/));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(within(dialog).getByText("パート 3 件（自動で選びました）")).toBeInTheDocument());
    await user.click(within(partsGroup(dialog)).getByRole("checkbox", { name: /15時台/ }));
    expect(cell(g, /15時台/)).not.toHaveAttribute("aria-pressed");
    expect(within(dialog).getByText("パート 2 件")).toBeInTheDocument();
    // 外した回も並びに残し、足し直せる
    await user.click(within(partsGroup(dialog)).getByRole("checkbox", { name: /15時台/ }));
    expect(cell(g, /15時台/)).toHaveAttribute("aria-pressed", "true");
    for (const name of [/13時台/, /14時台/, /15時台/]) await user.click(within(partsGroup(dialog)).getByRole("checkbox", { name }));
    await press(user, dialog, "この回を録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("選んだ回が無い");
    expect(calls.some((c) => c.key.startsWith("POST "))).toBe(false);

    // 開き直して「この回だけにする」
    await user.click(cell(g, /13時台/));
    const again = screen.getByRole("dialog");
    await press(user, again, "この回だけにする");
    expect(within(again).queryByRole("group", { name: "選んだ放送回" })).toBeNull();
    expect(selectedTitles(g)).toEqual(["ナイツ（13時台）"]);
  });

  it("詳細を閉じると選択の帯を出し、「この N 件を予約」で選んだ放送回の詳細を開き、「選択を解除」で選択を捨てる", async () => {
    mockApi(routes([n13, n14, n15]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /13時台/));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(within(dialog).getByText("パート 3 件（自動で選びました）")).toBeInTheDocument());
    await user.click(dialog.querySelector<HTMLButtonElement>(".mantine-Drawer-close")!);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const bar = screen.getByRole("region", { name: "選択中の放送回" });
    expect(bar).toHaveTextContent("3 件を選択中（13:00〜15:30）");
    await user.click(within(bar).getByRole("button", { name: "この 3 件を予約" }));
    const reopened = await screen.findByRole("dialog");
    expect(within(reopened).getByText("パート 3 件")).toBeInTheDocument();
    await user.click(reopened.querySelector<HTMLButtonElement>(".mantine-Drawer-close")!);
    await user.click(within(screen.getByRole("region", { name: "選択中の放送回" })).getByRole("button", { name: "選択を解除" }));
    expect(screen.queryByRole("region", { name: "選択中の放送回" })).toBeNull();
    expect(g.querySelectorAll("[data-selected]")).toHaveLength(0);
  });

  it("タッチの操作では 1 件の詳細に「ほかの回も選ぶ」を出し、押すと以後のタップで足す・外す。PC では出さない", async () => {
    setMedia(true, true);
    mockApi(routes([n13, n14, n15, other]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /別の番組/));
    const dialog = await screen.findByRole("dialog");
    await press(user, dialog, "ほかの回も選ぶ");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toHaveTextContent("1 件を選択中");
    await user.click(cell(g, /13時台/));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toHaveTextContent("2 件を選択中");
    await user.click(cell(g, /13時台/));
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toHaveTextContent("1 件を選択中");
  });

  it("PC では「ほかの回も選ぶ」を出さない", async () => {
    mockApi(routes([other]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /別の番組/));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("button", { name: "この回を録音" });
    expect(within(dialog).queryByRole("button", { name: "ほかの回も選ぶ" })).toBeNull();
  });
});

describe("番組表の、分割番組でない既存の番組のつなぐ設定", () => {
  const existing: Show = { name: "ナイツ ザ・ラジオショー", enabled: true, split: "none", check_title: true, weekly: [] };
  const shape = (r: Show) => ({ name: r.name, enabled: r.enabled, split: r.split, check_title: r.check_title, weekly: r.weekly });

  it("この回を録音: もう当てている放送回と合わせて同じ放送日に 2 つ以上になるならチェックを出し、オンなら split を join にしてから足し、取り消しで戻す", async () => {
    // 13時台はもうこの番組が当てている（1 回の予約）。14時台を足すと 2 つになる
    const r13 = { ...n13, state: "planned" as const, showName: existing.name };
    let current: Show = existing;
    let ids: number[] = [];
    const calls = mockApi({
      ...routes([r13, n14], [existing]),
      [showKey("GET", existing.name)]: () => ({ status: 200, body: { ...current, onces: ids.map((id) => created(id, n14)) } }),
      [showKey("PUT", existing.name)]: (init) => {
        current = JSON.parse(String(init?.body));
        return { status: 200, body: current };
      },
      [onces(existing.name)]: () => {
        ids = [21];
        return { status: 201, body: { onces: [created(21, n14)] } };
      },
      [`DELETE /api/shows/${enc(existing.name)}/onces/21`]: () => {
        ids = [];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /14時台/));
    const dialog = await screen.findByRole("dialog");
    const join = await within(dialog).findByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" });
    expect(join).toBeChecked();
    await press(user, dialog, "この回を録音");
    expect(await screen.findByRole("status", { name: "知らせ" })).toHaveTextContent(
      "この回を録音します。番組「ナイツ ザ・ラジオショー」に足しました（パートを 1 つのファイルにつなぐ設定にしました）",
    );
    const keys = calls.map((c) => c.key);
    expect(keys.indexOf(showKey("PUT", existing.name))).toBeLessThan(keys.indexOf(onces(existing.name)));
    expect(calls.find((c) => c.key === showKey("PUT", existing.name))?.body).toEqual({ ...shape(existing), split: "join" });

    await user.click(screen.getByRole("button", { name: "取り消す" }));
    await waitFor(() => expect(screen.getByRole("status", { name: "知らせ" })).toHaveTextContent("分割番組の設定を戻しました"));
    const puts = calls.filter((c) => c.key === showKey("PUT", existing.name));
    expect(puts.at(-1)?.body).toEqual(shape(existing));
    expect(calls.map((c) => c.key)).toContain(`DELETE /api/shows/${enc(existing.name)}/onces/21`);
  });

  it("この回を録音: チェックを外せば split を変えない。1 つしか当てないならチェックを出さない", async () => {
    const r13 = { ...n13, state: "planned" as const, showName: existing.name };
    const calls = mockApi({
      ...routes([r13, n14, other], [existing, { ...existing, name: "別の番組" }]),
      [onces(existing.name)]: () => ({ status: 201, body: { onces: [created(21, n14)] } }),
    });
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /別の番組/));
    let dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("button", { name: "この回を録音" });
    expect(within(dialog).queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeNull();

    await user.click(cell(g, /14時台/));
    dialog = screen.getByRole("dialog");
    await user.click(await within(dialog).findByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" }));
    await press(user, dialog, "この回を録音");
    expect(await screen.findByRole("status", { name: "知らせ" })).toHaveTextContent("番組「ナイツ ザ・ラジオショー」に足しました");
    expect(calls.some((c) => c.key.startsWith("PUT "))).toBe(false);
  });

  it("毎週録音: オンなら毎週の予約を足すのと同じ PUT で split を join にし、取り消しで足す前の形（split も）に戻す", async () => {
    const r13 = { ...n13, state: "planned" as const, showName: existing.name };
    const withWeekly: Show = { ...existing, weekly: [{ station: "LFR", weekday: new Date(`${today}T00:00:00Z`).getUTCDay(), start: "13:00" }] };
    let current: Show = withWeekly;
    const calls = mockApi({
      ...routes([r13, n14], [withWeekly]),
      [showKey("GET", existing.name)]: () => ({ status: 200, body: { ...current, onces: [] } }),
      [showKey("PUT", existing.name)]: (init) => {
        current = JSON.parse(String(init?.body));
        return { status: 200, body: current };
      },
    });
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /14時台/));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeChecked();
    await press(user, dialog, "毎週録音");
    expect(await screen.findByRole("status", { name: "知らせ" })).toHaveTextContent("（パートを 1 つのファイルにつなぐ設定にしました）");
    const puts = () => calls.filter((c) => c.key === showKey("PUT", existing.name)).map((c) => c.body as Show);
    expect(puts()).toHaveLength(1);
    expect(puts()[0].split).toBe("join");
    expect(puts()[0].weekly).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    await waitFor(() => expect(screen.getByRole("status", { name: "知らせ" })).toHaveTextContent("分割番組の設定を戻しました"));
    expect(puts().at(-1)).toEqual(shape(withWeekly));
  });

  it("分割番組でつなぐ・つながない既存の番組には、チェックを出さない", async () => {
    const r13 = { ...n13, state: "planned" as const, showName: existing.name };
    mockApi(routes([r13, n14], [{ ...existing, split: "separate" }]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /14時台/));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("button", { name: "この回を録音" });
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "この回を録音" })).not.toHaveAttribute("data-loading"));
    expect(within(dialog).queryByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeNull();
  });
});

describe("番組表の、つなぐ設定に変えるときの失敗", () => {
  const existing: Show = { name: "ナイツ ザ・ラジオショー", enabled: true, split: "none", check_title: true, weekly: [] };
  const shape = (r: Show) => ({ name: r.name, enabled: r.enabled, split: r.split, check_title: r.check_title, weekly: r.weekly });
  // 13時台はもうこの番組が当てている。14時台を足すと同じ放送日に 2 つになるので、つなぐ（既定でオン）が出る
  const r13 = { ...n13, state: "planned" as const, showName: existing.name };
  const putKey = showKey("PUT", existing.name);

  /** 番組の GET・PUT を持つ偽の API。put は PUT の n 回目（1 始まり）の応答を決める（undefined なら受け付ける）。 */
  function api(extra: Record<string, Handler>, put: (n: number, body: Show) => Reply | undefined = () => undefined) {
    let current: Show = existing;
    let n = 0;
    const calls = mockApi({
      ...routes([r13, n14], [existing]),
      [showKey("GET", existing.name)]: () => ({ status: 200, body: { ...current, onces: [] } }),
      [putKey]: (init) => {
        const body = JSON.parse(String(init?.body)) as Show;
        const reply = put(++n, body);
        if (reply) return reply;
        current = body;
        return { status: 200, body };
      },
      ...extra,
    });
    return { calls, change: (r: Show) => (current = r) };
  }

  async function openN14() {
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    await user.click(cell(g, /14時台/));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByRole("checkbox", { name: "パートを 1 つのファイルにつなぐ" })).toBeChecked();
    return { user, dialog };
  }

  const puts = (calls: { key: string; body?: unknown }[]) => calls.filter((c) => c.key === putKey).map((c) => c.body as Show);

  it("この回を録音: split を変える PUT が断られたら、理由を出し、1 回の予約は足さない", async () => {
    const { calls } = api({}, () => ({ status: 400, body: { error: "断った" } }));
    const { user, dialog } = await openN14();
    await press(user, dialog, "この回を録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("番組を保存できない");
    expect(calls.some((c) => c.key === onces(existing.name))).toBe(false);
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
  });

  it("この回を録音: 足すのが断られたら（409）、変えた split を戻して理由を出す", async () => {
    const { calls } = api({
      [onces(existing.name)]: () => ({
        status: 409,
        body: { error: "断った", reason: "other_show", showName: "別の番組", airing: { station: "LFR", start: n14.start } },
      }),
    });
    const { user, dialog } = await openN14();
    await press(user, dialog, "この回を録音");
    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent("ほかの番組「別の番組」がすでにこの放送回を当てている");
    expect(alert).not.toHaveTextContent("戻せなかった");
    expect(puts(calls).map((r) => r.split)).toEqual(["join", "none"]);
    expect(puts(calls).at(-1)).toEqual(shape(existing));
  });

  it("この回を録音: 足すのが断られ、split も戻せなかったら、つなぐに変えたまま戻せなかったと出す", async () => {
    api({ [onces(existing.name)]: () => ({ status: 400, body: { error: "断った" } }) }, (n) =>
      n === 2 ? { status: 500, body: { error: "内部エラー" } } : undefined,
    );
    const { user, dialog } = await openN14();
    await press(user, dialog, "この回を録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "番組「ナイツ ザ・ラジオショー」の分割番組の設定を「つなぐ」に変えたまま戻せなかった。番組の画面で確かめてほしい",
    );
  });

  it("この回を録音: 足せたか分からない失敗（5xx）なら split は戻さず、つなぐに変えたと出す", async () => {
    const { calls } = api({ [onces(existing.name)]: () => ({ status: 500, body: { error: "内部エラー" } }) });
    const { user, dialog } = await openN14();
    await press(user, dialog, "この回を録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("番組「ナイツ ザ・ラジオショー」の分割番組の設定は「つなぐ」に変えた");
    expect(puts(calls).map((r) => r.split)).toEqual(["join"]);
  });

  it("この回を録音の取り消し: その後に番組が変わっていたら、足した分は外し、split は戻さずに知らせる", async () => {
    const { calls, change } = api({
      [onces(existing.name)]: () => ({ status: 201, body: { onces: [created(21, n14)] } }),
    });
    const { user, dialog } = await openN14();
    await press(user, dialog, "この回を録音");
    await screen.findByRole("status", { name: "知らせ" });
    // 別の操作で番組の設定が変わった
    change({ ...existing, split: "join", check_title: false });
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(
      await screen.findByText("番組「ナイツ ザ・ラジオショー」がその後に変わったので、分割番組の設定は戻さなかった。番組の画面で確かめてほしい"),
    ).toBeInTheDocument();
    expect(puts(calls).map((r) => r.split)).toEqual(["join"]);
  });

  it("毎週録音: split を変える PUT（毎週の予約を足すのと同じ）が断られたら、理由を出す", async () => {
    const { calls } = api({}, () => ({ status: 400, body: { error: "断った" } }));
    const { user, dialog } = await openN14();
    await press(user, dialog, "毎週録音");
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("番組を保存できない");
    expect(puts(calls)).toHaveLength(1);
    expect(screen.queryByRole("status", { name: "知らせ" })).toBeNull();
  });

  it("毎週録音の取り消し: その後に番組が変わっていたら、戻さずに知らせる", async () => {
    const { calls, change } = api({});
    const { user, dialog } = await openN14();
    await press(user, dialog, "毎週録音");
    await screen.findByRole("status", { name: "知らせ" });
    change({ ...existing, split: "join", weekly: [], check_title: false });
    await user.click(screen.getByRole("button", { name: "取り消す" }));
    expect(
      await screen.findByText("番組「ナイツ ザ・ラジオショー」がその後に変わったので、取り消さなかった。番組の画面で確かめてほしい"),
    ).toBeInTheDocument();
    expect(puts(calls)).toHaveLength(1);
  });
});

describe("1 局の表示で局を切り替えたとき", () => {
  it("選択と違う局に切り替えたら選択を捨てる（帯も消える）。同じ局なら残す", async () => {
    mockApi(routes([n13, n14, n15]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    fireEvent.click(cell(g, /13時台/), { ctrlKey: true });
    fireEvent.click(cell(g, /14時台/), { ctrlKey: true });
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toHaveTextContent("2 件を選択中");
    // 局名を押して、選択と同じ局（ニッポン放送）の 1 局の表示にする: 選択は残る
    await user.click(screen.getByRole("button", { name: "ニッポン放送だけを表示" }));
    const table = await screen.findByTestId("guide-station-table");
    expect(table.querySelectorAll("[data-selected]")).toHaveLength(2);
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toHaveTextContent("2 件を選択中");
    // 違う局に切り替える: 選択を捨てる
    const select = screen.getByRole("combobox", { name: "局" });
    await user.click(select);
    await user.click(await screen.findByRole("option", { name: "TBSラジオ", hidden: true }));
    expect(screen.queryByRole("region", { name: "選択中の放送回" })).toBeNull();
    expect(screen.queryByRole("button", { name: /この \d+ 件を予約/ })).toBeNull();
  });

  it("選択の局と違う局を覚えたまま「1 局」に切り替えたら、選択を捨てる", async () => {
    window.localStorage.setItem("rajikura.guide.station", "TBS");
    mockApi(routes([n13, n14, n15]));
    const user = userEvent.setup();
    renderPage();
    const g = await grid();
    fireEvent.click(cell(g, /13時台/), { ctrlKey: true });
    fireEvent.click(cell(g, /14時台/), { ctrlKey: true });
    expect(screen.getByRole("region", { name: "選択中の放送回" })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "1 局" }));
    await screen.findByTestId("guide-station-table");
    expect(screen.queryByRole("region", { name: "選択中の放送回" })).toBeNull();
  });
});
