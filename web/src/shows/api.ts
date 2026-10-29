// 番組の管理の API（internal/server/shows.go）を呼ぶ。

import { ApiError } from "../api";

/**
 * 毎週の予約。weekday は放送日の曜日（0 = 日曜）、start は HH:MM、from は開始日、until は終了日（どちらも放送日の YYYY-MM-DD で、
 * その放送日を含む。省くか空なら制限なし）。
 */
export type Weekly = { station: string; weekday: number; start: string; from?: string; until?: string };

/**
 * 番組の分割番組の設定（ADR 0003）。"none" は分割番組でない（同じ日に 2 回当たっても別々のファイル）、"join" は分割番組で
 * パートを 1 つのファイルにつなぐ、"separate" は分割番組でパートごとに -1・-2 のファイルにする。
 */
export type SplitMode = "none" | "join" | "separate";

export type Show = {
  name: string;
  enabled: boolean;
  split: SplitMode;
  check_title: boolean;
  weekly: Weekly[];
  /** 次の放送予定（その番組の waiting のジョブの放送回のうち、開始時刻が今より後のいちばん早いものの開始時刻）。一覧でだけ付き、無効な番組には付かない。 */
  next_airing?: string;
  /** 当たらない日（枠の開始時刻が今より後のもの。枠の開始時刻の順）。一覧でだけ付き、無い番組と無効な番組には付かない。 */
  unmatched_days?: UnmatchedDay[];
};

/** 当たらない日に添える放送回（番組名・開始・終了）。 */
export type UnmatchedAiring = { title: string; start: string; end: string };

/**
 * 当たらない日（毎週の予約の枠に、その開始時刻で始まる放送回が番組表に無い放送日）。day は放送日（YYYY-MM-DD）、start は枠の開始時刻。
 * covering はその時刻を含む放送回（無ければ null）、titled は同じ局・同じ放送日で番組名の合う放送回。
 */
export type UnmatchedDay = { day: string; station: string; start: string; covering: UnmatchedAiring | null; titled: UnmatchedAiring[] };

export type Station = { id: string; name: string };

export type ShowName = { found: boolean; day: string; name?: string; title?: string };

/** サーバーが返したエラーの文（{"error": "..."}）を添えて投げる。 */
async function fail(res: Response, what: string): Promise<never> {
  let msg = "";
  try {
    const body = (await res.json()) as { error?: string };
    msg = body.error ?? "";
  } catch {
    // 本文が JSON でなければ、状態だけを出す
  }
  if (res.status === 401) msg = "ログインが切れた。再読み込みしてログインし直してほしい";
  throw new ApiError(msg ? `${what}: ${msg}` : `${what}（${res.status}）`);
}

async function call<T>(method: string, path: string, what: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) return fail(res, what);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const showPath = (name: string) => `/api/shows/${encodeURIComponent(name)}`;

export const listShows = () => call<Show[]>("GET", "/api/shows", "番組の一覧を読めない");

export const createShow = (show: Show) => call<Show>("POST", "/api/shows", "番組を作れない", show);

/** 名前が name の番組を show で丸ごと置き換える。show.name が name と違えば名前の変更。 */
export const replaceShow = (name: string, show: Show) => call<Show>("PUT", showPath(name), "番組を保存できない", show);

export const deleteShow = (name: string) => call<void>("DELETE", showPath(name), "番組を消せない");

export const setShowEnabled = (name: string, enabled: boolean) =>
  call<{ enabled: boolean }>("PUT", `${showPath(name)}/enabled`, "有効／無効を変えられない", { enabled });

export const listStations = () => call<Station[]>("GET", "/api/stations", "局の一覧を取れない");

/** 局・曜日（放送日の曜日。0 = 日曜）・開始時刻（HH:MM）から、番組表の番組名を探す。 */
export const suggestShowName = (station: string, weekday: number, start: string) =>
  call<ShowName>(
    "GET",
    `/api/show-name?${new URLSearchParams({ station, weekday: String(weekday), start })}`,
    "番組表を探せない",
  );

/** 番組に属する 1 回の予約（GET /api/shows/{name} の onces）。title などは番組表から引けたときだけ付く。 */
export type Once = {
  id: number;
  stationId: string;
  stationName?: string;
  start: string;
  end?: string;
  title?: string;
  /** 番組表の範囲の放送日なのに、その放送回が番組表に無い（開始時刻が変わった・消えた）。 */
  missing: boolean;
  /** この 1 回の予約を足したキーワードの ID と語。人が足したもの、キーワードを消した後は無い。 */
  keywordId?: number;
  keywordWords?: string;
};

/** 番組を 1 つ読む。1 回の予約の一覧（onces）が付く。 */
export const getShow = (name: string) => call<Show & { onces?: Once[] }>("GET", showPath(name), "番組を読めない");

export const deleteOnce = (name: string, id: number) =>
  call<void>("DELETE", `${showPath(name)}/onces/${id}`, "1 回の予約を消せない");
