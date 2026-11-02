// 番組表の API（internal/server/guide.go）と 1 回の予約の API（internal/server/onces.go）を呼ぶ。

import { ApiError } from "../api";
import type { Once } from "../shows/api";

/**
 * 放送回の状態。none = 予約なし、planned = 録音予定、done = 録音済み、missed = 録音できなかった、
 * skip_planned = 録らない予定（rajikura か人が録らないと決めていて、まだ確定していない）、
 * skipped = 録らなかった（録らないと決め、放送の後に確定した）（画面での呼び方。ADR 0019）。
 */
export type AiringState = "none" | "planned" | "done" | "missed" | "skip_planned" | "skipped";

/** 説明の部品。href があればリンク（http・https・mailto だけ）。 */
export type DescriptionPart = { text: string; href?: string };

export type GuideAiring = {
  stationId: string;
  stationName: string;
  start: string;
  end: string;
  title: string;
  performer: string;
  description: string;
  /** 説明を、リンクの部品とそうでない部品に分けた並び（text をつなぐと description になる）。 */
  descriptionParts: DescriptionPart[];
  imageUrl?: string;
  state: AiringState;
  showName?: string;
  /** 放送回のジョブの ID（ジョブがあるときだけ）。番組表の詳細の「録らない」「録る」が使う。 */
  jobId?: number;
  /** state が skip_planned・skipped のとき、誰が録らないと決めたか（title = rajikura が番組名の食い違いで、person = 人が）。 */
  skipBy?: "title" | "person";
  /** 「録らない」を受け付けるか（ジョブが、試行がまだ無い録音予定）。API は必ず付ける（検索の結果も同じ形）。 */
  skippable?: boolean;
  /** タイムフリーの対象外（ts_in_ng。自分のエリアの外の局は ts_out_ng。サーバーが決める）。ライブで録る（画面は「ライブで録る」と出す）。 */
  notAvailable: boolean;
  /** タイムフリー30 の対象外（tsplus_in_ng。自分のエリアの外の局は tsplus_out_ng。サーバーが決める）で、録音期限が 7 日になる放送回か。会員種別がタイムフリー30・ダブルプランのときだけ立つ。 */
  timeFree30Excluded: boolean;
  /** 全国の検索の結果で、局が自分のエリアの局一覧に無いときの、局のエリアの名前（都道府県名）。自分のエリアの局と、全国の検索でない結果には付かない。 */
  stationAreaName?: string;
  /** stationAreaName と同じ条件で付く、局のエリアの ID。全国の検索の結果から開いた詳細が、ほかのパートを探す番組表のエリアに使う。 */
  stationAreaId?: string;
};

export type GuideStation = { id: string; name: string; airings: GuideAiring[] };

/** GET /api/guide の応答。 */
export type GuideResult = { day: string; stations: GuideStation[] };

/**
 * GET /api/guide/search の応答。scope と failedStations は全国の検索（scope=all）のときだけ付く。
 * failedStations は、番組表を一部（ある放送日を 1 つでも）取れなかった局の数。
 */
export type SearchResult = { q: string; airings: GuideAiring[]; truncated: boolean; scope?: "all"; failedStations?: number };

type ErrorBody = {
  error?: string;
  reason?: ConflictReason;
  showName?: string;
  airing?: { station: string; start: string };
};

async function errorBody(res: Response): Promise<ErrorBody> {
  try {
    return (await res.json()) as ErrorBody;
  } catch {
    return {};
  }
}

const loggedOut = "ログインが切れた。再読み込みしてログインし直してほしい";

async function getJSON<T>(path: string, what: string): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin" });
  if (!res.ok) {
    const body = await errorBody(res);
    const msg = res.status === 401 ? loggedOut : (body.error ?? "");
    throw new ApiError(msg ? `${what}: ${msg}` : `${what}（${res.status}）`);
  }
  return (await res.json()) as T;
}

/** 番組表で選べるエリア。 */
export type GuideArea = { id: string; name: string };

/** GET /api/guide/areas の応答。pastDays は番組表で選べる過去の日数（会員種別の録音期限。7 または 30）。switchable が true（エリアフリー・ダブルプラン）のときだけ、画面はエリアの切り替えを出す。 */
export type GuideAreas = { own: string; switchable: boolean; pastDays: number; areas: GuideArea[] };

/** area（エリア ID。空なら自分のエリア）を付けた問い合わせ。 */
const withArea = (params: Record<string, string>, area: string) =>
  new URLSearchParams(area ? { ...params, area } : params);

/** その放送日（YYYY-MM-DD）の、エリア（area。空なら自分のエリア）の全局の放送回。 */
export const getGuide = (day: string, area = "") =>
  getJSON<GuideResult>(`/api/guide?${withArea({ day }, area)}`, "番組表を読めない");

/** 番組表の検索。all なら全国の検索（scope=all。area は付けない）、そうでなければエリア（area。空なら自分のエリア）の検索。 */
export const searchGuide = (q: string, area = "", all = false) =>
  getJSON<SearchResult>(
    `/api/guide/search?${all ? new URLSearchParams({ q, scope: "all" }) : withArea({ q }, area)}`,
    "番組表を検索できない",
  );

/** 番組表で選べるエリアの一覧。 */
export const getGuideAreas = () => getJSON<GuideAreas>("/api/guide/areas", "エリアの一覧を読めない");

export type ConflictReason = "other_show" | "already";

/** 1 回の予約を足す API が 4xx の応答で断った（足していないと分かる）。通信の失敗や 5xx は含まない。 */
export class OnceRefused extends ApiError {}

/** 1 回の予約を足す API が 409 で断った。 */
export class OnceConflict extends OnceRefused {
  constructor(
    message: string,
    readonly reason: ConflictReason | undefined,
    readonly showName: string | undefined,
    /** 断った放送回。競合で保存先に断られたときは付かない。 */
    readonly airing: { station: string; start: string } | undefined,
  ) {
    super(message);
  }
}

/** 番組に 1 回の予約を足す。全部足すか 1 つも足さないか。足した 1 回の予約（ID 付き）を返す。 */
export async function addOnces(
  showName: string,
  airings: { station: string; start: string }[],
): Promise<Once[]> {
  const res = await fetch(`/api/shows/${encodeURIComponent(showName)}/onces`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ airings }),
  });
  if (res.ok) {
    try {
      return ((await res.json()) as { onces?: Once[] }).onces ?? [];
    } catch {
      return [];
    }
  }
  const body = await errorBody(res);
  if (res.status === 409) throw new OnceConflict(body.error ?? "録音の予約ができない", body.reason, body.showName, body.airing);
  const msg = res.status === 401 ? loggedOut : body.error ? `1 回の予約を足せない: ${body.error}` : `1 回の予約を足せない（${res.status}）`;
  if (res.status >= 400 && res.status < 500) throw new OnceRefused(msg);
  throw new ApiError(msg);
}
