// キーワードの API（internal/server/keywords.go）を呼ぶ。

import { ApiError } from "../api";
import type { GuideAiring } from "../guide/api";

export type Keyword = {
  id: number;
  words: string;
  exclude: string;
  stations: string[];
  enabled: boolean;
  /** 足した放送回のうち、まだ録音を終えていない数（上限 keyword_max_pending に数える）。 */
  pending: number;
};

/** 作る・変えるときの body。 */
export type KeywordInput = { words: string; exclude: string; stations: string[]; enabled: boolean };

/**
 * 足した放送回の状態。skip_planned = 録らない予定（その放送回のジョブが確定の前の skipped）。skipped = 録らなかった（その放送回のジョブが
 * 確定した skipped）。removed = 録音を終える前に、1 回の予約が外された。
 */
export type KeywordAiringState = "planned" | "done" | "missed" | "skip_planned" | "skipped" | "removed";

export type KeywordAiring = {
  stationId: string;
  stationName: string;
  start: string;
  end: string;
  title: string;
  showName: string;
  state: KeywordAiringState;
};

export type KeywordPreview = { airings: GuideAiring[]; truncated: boolean };

async function call<T>(method: string, path: string, what: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = "";
    try {
      msg = ((await res.json()) as { error?: string }).error ?? "";
    } catch {
      // 本文が JSON でなければ、状態だけを出す
    }
    if (res.status === 401) msg = "ログインが切れた。再読み込みしてログインし直してほしい";
    throw new ApiError(msg ? `${what}: ${msg}` : `${what}（${res.status}）`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export async function listKeywords(): Promise<Keyword[]> {
  return (await call<{ keywords?: Keyword[] }>("GET", "/api/keywords", "キーワードの一覧を読めない")).keywords ?? [];
}

export const createKeyword = (k: KeywordInput) => call<Keyword>("POST", "/api/keywords", "キーワードを作れない", k);

export const updateKeyword = (id: number, k: KeywordInput) => call<Keyword>("PUT", `/api/keywords/${id}`, "キーワードを保存できない", k);

export const deleteKeyword = (id: number) => call<void>("DELETE", `/api/keywords/${id}`, "キーワードを消せない");

export async function listKeywordAirings(id: number): Promise<KeywordAiring[]> {
  return (await call<{ airings?: KeywordAiring[] }>("GET", `/api/keywords/${id}/airings`, "足した放送回を読めない")).airings ?? [];
}

/** 今このキーワードを作ったら当たる、まだ始まっていない放送回。 */
export async function previewKeyword(words: string, exclude: string, stations: string[]): Promise<KeywordPreview> {
  const p = await call<Partial<KeywordPreview>>(
    "GET",
    `/api/keywords/preview?${new URLSearchParams({ words, exclude, stations: stations.join(",") })}`,
    "当たる放送回を調べられない",
  );
  return { airings: p.airings ?? [], truncated: p.truncated ?? false };
}

export const stateLabels: Record<KeywordAiringState, string> = {
  planned: "録音予定",
  done: "録音済み",
  missed: "録音できなかった",
  skip_planned: "録らない予定",
  skipped: "録らなかった",
  removed: "予約が外された",
};

export const stateColors: Record<KeywordAiringState, string> = {
  planned: "blue",
  done: "green",
  missed: "red",
  skip_planned: "yellow",
  skipped: "yellow",
  removed: "gray",
};
