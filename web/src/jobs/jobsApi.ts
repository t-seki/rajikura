// ジョブの API（internal/server/jobs.go）を呼ぶ。

export type JobState = "waiting" | "running" | "retrying" | "done" | "expired" | "skipped";

export const jobStates: { value: JobState; label: string }[] = [
  { value: "waiting", label: "録音予定" },
  { value: "running", label: "録音中" },
  { value: "retrying", label: "録音予定（失敗・再試行待ち）" },
  { value: "done", label: "録音済み" },
  { value: "expired", label: "録音できなかった（期限切れ）" },
  { value: "skipped", label: "録らない予定・録らなかった" },
];

export const stateLabel = (s: string) => jobStates.find((x) => x.value === s)?.label ?? s;

/**
 * 画面に出す状態。API の状態（JobState）のうち skipped を、確定の前（settled が false）の録らない予定（skip_planned）と、
 * 確定した録らなかった（skipped）に分ける（ADR 0019）。
 */
export type JobViewState = Exclude<JobState, "skipped"> | "skip_planned" | "skipped";

/** 画面に出す状態の並び（状態の列で並べるときの順）と名前。 */
export const jobViewStates: { value: JobViewState; label: string }[] = [
  { value: "waiting", label: "録音予定" },
  { value: "skip_planned", label: "録らない予定" },
  { value: "running", label: "録音中" },
  { value: "retrying", label: "録音予定（失敗・再試行待ち）" },
  { value: "done", label: "録音済み" },
  { value: "expired", label: "録音できなかった（期限切れ）" },
  { value: "skipped", label: "録らなかった" },
];

export const viewState = (j: { state: JobState; settled: boolean }): JobViewState =>
  j.state === "skipped" && !j.settled ? "skip_planned" : j.state;

export const viewStateLabel = (s: JobViewState) => jobViewStates.find((x) => x.value === s)?.label ?? s;

/** 局と時刻の組。同じ局の放送回は、最初の開始から最後の終わりまでの 1 組。 */
export interface JobStation {
  stationId: string;
  start: string;
  end: string;
}

/**
 * 録音中のジョブの進捗（用語集の「進捗」）。今の試行の段階と、入手の間だけ、録れた音の秒数とジョブの放送回の長さの合計（秒）。
 * 検証・保存の段階と、ライブ録音の保存だけのやり直しでは、秒数は無い。
 */
export interface JobProgress {
  stage: "fetch" | "verify" | "save";
  fetchedSeconds?: number;
  totalSeconds?: number;
}

export interface JobSummary {
  id: number;
  showName: string;
  day: string;
  part: number;
  state: JobState;
  attempts: number;
  nextAttemptAt: string;
  deadline: string;
  /** ジョブが済んだ時刻（RFC 3339）。「これから」のジョブと、試行が無い録音済みでは空文字（無い場合もある）。 */
  finishedAt?: string;
  retryable: boolean;
  /** ライブ録音の録れた分の保存を自動でやり直している間だけ、その打ち切り（放送の終了から 24 時間）。 */
  saveRetryUntil?: string;
  /** 保存できなかったライブ録音の録れた分を残している間だけ、残す期限（最後の試行の終わりから 30 日）。 */
  keepUntil?: string;
  /** skipped のジョブが確定したか（false なら録らない予定、true なら録らなかった）。skipped でなければ false。 */
  settled: boolean;
  /** 人が録る・録らないを決めたジョブか（番組名では状態が変わらない）。 */
  byPerson: boolean;
  /** ライブで録るジョブか（タイムフリーの対象外の放送回を含む）。録る前のジョブでは、その時点の番組表での見込みで、番組表の同期で変わりうる。 */
  live: boolean;
  /** いま「録らない」を受け付けるか（試行がまだ無い録音予定）。 */
  skippable: boolean;
  /** いま「録る」を受け付けるか（録らない予定、または録音期限を過ぎていない録らなかった）。 */
  recordable: boolean;
  /** いま「録り直す」を受け付けるか（録音済みのタイムフリーのジョブで、録音期限を過ぎていない。ADR 0036）。 */
  rerecordable: boolean;
  /**
   * いま「チャプターを入れ直す」を受け付けるか（タイムフリーから録った録音済みのジョブ。チャプターが入ったジョブも。ADR 0038）。
   * この版より前の serve では無い。
   */
  chaptersRedoable?: boolean;
  /** チャプターの入れ直しを頼んで、rajikura serve の録音のループがまだ書き込んでいない間だけ true。 */
  chaptersRedoRequested?: boolean;
  /** いま「保存先を移す」を受け付けるか（保存先があり、録音中でないジョブ。ADR 0039）。この版より前の serve では無い。 */
  movable?: boolean;
  /** 保存先の移しを頼んで（番組の名前の変更か、人の頼み）、rajikura serve の録音のループがまだ移していない間だけ true。 */
  moveRequested?: boolean;
  /** チャプターが入ったか。取りに行っていないジョブ（ライブ録音、設定がオフ、まだ保存していないなど）では無い。一覧にも付く。 */
  chapters?: JobChapters;
  outputPath: string;
  missingCount: number;
  lastFailedStage: string;
  lastError: string;
  stations: JobStation[];
  /** 録音中（running）で、rajikura serve のメモリに進捗があるときだけ。serve を再起動した後は、次に試行が進むまで無い。 */
  progress?: JobProgress;
}

export interface JobAiring {
  stationId: string;
  start: string;
  end: string;
  title: string;
  performer: string;
  description: string;
  missing: boolean;
}

export interface JobAttempt {
  startedAt: string;
  endedAt: string;
  failedStage: string;
  error: string;
  durationSeconds: number;
}

/** 保存したファイルにチャプターが入ったか（入った・待っている・入らなかった。ADR 0027）。 */
export type JobChapters = "added" | "waiting" | "not_added";

export const chaptersLabels: Record<JobChapters, string> = {
  added: "入った",
  waiting: "待っている",
  not_added: "入らなかった",
};

/** チャプターの書き込みを誰が起こしたか（refetch: チャプターの取り直し、redo: チャプターの入れ直し。ADR 0038）。 */
export type ChapterWriteBy = "refetch" | "redo";

/** チャプターの書き込みの結果（written: 書き込んだ、not_fetched: 取れなかったので書かなかった、failed: 書き込めなかった）。 */
export type ChapterWriteResult = "written" | "not_fetched" | "failed";

/** チャプターの書き込みの履歴の 1 行（保存したファイルへの書き込みのたびに 1 行。試行の履歴とは別。ADR 0038）。 */
export interface JobChapterWrite {
  at: string;
  by: ChapterWriteBy;
  result: ChapterWriteResult;
  /** 書き込んだチャプターの件数（written のときだけ）。 */
  count: number;
  /** 書き込めなかった理由（failed のときだけ）。 */
  error: string;
}

export const chapterWriteByLabels: Record<ChapterWriteBy, string> = {
  refetch: "チャプターの取り直し",
  redo: "チャプターの入れ直し",
};

/** チャプターの書き込みの履歴の 1 行の結果の文（「12 件を書き込んだ」など）。 */
export function chapterWriteResultText(w: JobChapterWrite): string {
  switch (w.result) {
    case "written":
      return `${w.count} 件を書き込んだ`;
    case "not_fetched":
      return "チャプターを取れなかったので書かなかった";
    case "failed":
      return "書き込めなかった（ファイルは前のまま）";
  }
}

/**
 * 保存先の移しの結果（moved: 移した、album_rewritten: 保存先は同じなので、アルバムの名前だけ書き換えた（from と to は同じ）、
 * target_exists: 移し先に同じ名前のファイルがあるので移さなかった、no_file: 保存先にファイルが無いので移さなかった、
 * not_show_path: 保存先が番組の名前の形でないので移さなかった、failed: 移せなかった。ADR 0039）。
 */
export type OutputMoveResult = "moved" | "album_rewritten" | "target_exists" | "no_file" | "not_show_path" | "failed";

/** 保存先の移しの履歴の 1 行（移しのたびに 1 行。試行の履歴・チャプターの書き込みの履歴とは別。ADR 0039）。 */
export interface JobOutputMove {
  at: string;
  /** 移す前の保存先。 */
  from: string;
  /** 移し先（保存先が番組の名前の形でなく、移し先を組めなかったときは空。album_rewritten のときは from と同じ）。 */
  to: string;
  result: OutputMoveResult;
  /** 移せなかった理由（failed のときだけ）。 */
  error: string;
}

/** 保存先の移しの結果の文（rajikura jobs <ID> の outputMoveResultText と同じ文）。 */
export const outputMoveResultLabels: Record<OutputMoveResult, string> = {
  moved: "移した",
  album_rewritten: "保存先は同じなので、アルバムの名前だけ書き換えた",
  target_exists: "移し先に同じ名前のファイルがあるので移さなかった",
  no_file: "保存先にファイルが無いので移さなかった",
  not_show_path: "保存先が番組の名前の形でないので移さなかった",
  failed: "移せなかった（ファイルは前のまま）",
};

export interface JobDetail extends JobSummary {
  airings: JobAiring[];
  attemptLog: JobAttempt[];
  /** チャプターの書き込みの履歴（書き込んだ順）。API は常に配列で返す（この版より前の serve では無い）。 */
  chapterWrites?: JobChapterWrite[];
  /** 保存先の移しの履歴（移した順）。API は常に配列で返す（この版より前の serve では無い）。 */
  outputMoves?: JobOutputMove[];
  durationSeconds: number;
}

export class JobsApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function call<T>(method: string, url: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin" };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  const res = await fetch(url, init);
  if (!res.ok) {
    let msg = `${url} が ${res.status} を返した`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      // 本文が JSON でなければ、上の既定の文のまま
    }
    throw new JobsApiError(msg, res.status);
  }
  return (await res.json()) as T;
}

/** GET /api/jobs の応答。 */
export type JobList = { jobs: JobSummary[] };

export async function listJobs(states: JobState[]): Promise<JobSummary[]> {
  const q = states.length > 0 ? `?state=${states.join(",")}` : "";
  return (await call<JobList>("GET", `/api/jobs${q}`)).jobs;
}

export const getJob = (id: number) => call<JobDetail>("GET", `/api/jobs/${id}`);
export const retryJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/retry`);
/** 「録らない」。試行がまだ無い録音予定のジョブを、録らない予定（放送の後なら録らなかった）にする。 */
export const skipJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/skip`);
/** 「録る」。録らない予定のジョブは録音予定に戻し、録らなかったのジョブは手動再試行と同じく再試行待ちにする。 */
export const recordJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/record`);
/** 「録り直す」。録音済みのジョブを radiko から録り直させる（再試行待ちにする）。前に保存したファイルと同じ名前なら上書きする（ADR 0036）。 */
export const rerecordJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/rerecord`);
/**
 * 「チャプターを入れ直す」。録音済みのジョブの保存したファイルに、radiko から音声を取り直さずにチャプターを書き込ませる（ADR 0038）。
 * 受け付けたところで返り、書き込みは rajikura serve の録音のループが後で行う（結果はチャプターの値と書き込みの履歴に出る）。
 */
export const redoJobChapters = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/chapters`);
/**
 * 「保存先を移す」。保存したファイルを、今の番組（show を渡せば、その番組に付け替えて）の名前のフォルダ・ファイル名へ移させる
 * （ADR 0039）。受け付けたところで返り、移すのは rajikura serve の録音のループが後で行う（結果は保存先と移しの履歴に出る）。
 */
export const moveJob = (id: number, show?: string) =>
  call<JobSummary>("POST", `/api/jobs/${id}/move`, show ? { show } : undefined);

/** 録音のファイルを返す URL（`<audio src>` に渡す。同じオリジンなので cookie が付く）。 */
export const jobAudioUrl = (id: number) => `/api/jobs/${id}/audio`;

/**
 * `<audio>` が再生できなかったときに、同じ URL を `Range: bytes=0-0` で読み直し、API が断った理由（`error`）を返す。
 * 応答が成功した・JSON でない・通信に失敗したなど、理由が取れなければ「再生できない」。
 */
export async function jobAudioError(id: number): Promise<string> {
  const fallback = "再生できない";
  try {
    const res = await fetch(jobAudioUrl(id), { credentials: "same-origin", headers: { Range: "bytes=0-0" } });
    if (res.ok) return fallback;
    const body = (await res.json()) as { error?: string };
    return body.error || fallback;
  } catch {
    return fallback;
  }
}
