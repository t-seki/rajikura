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
  /** タイムフリーの対象外（ts_in_ng）。ライブで録る（画面は「ライブで録る」と出す）。 */
  notAvailable: boolean;
};

export type GuideStation = { id: string; name: string; airings: GuideAiring[] };

export type SearchResult = { q: string; airings: GuideAiring[]; truncated: boolean };

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

/** その放送日（YYYY-MM-DD）の、エリアの全局の放送回。 */
export const getGuide = (day: string) =>
  getJSON<{ day: string; stations: GuideStation[] }>(`/api/guide?${new URLSearchParams({ day })}`, "番組表を読めない");

export const searchGuide = (q: string) =>
  getJSON<SearchResult>(`/api/guide/search?${new URLSearchParams({ q })}`, "番組表を検索できない");

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
