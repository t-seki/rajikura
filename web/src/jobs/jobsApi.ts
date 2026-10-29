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
  /** いま「録り直す」を受け付けるか（録音済みのタイムフリーのジョブで、録音期限を過ぎていない。ADR 0025）。 */
  rerecordable: boolean;
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

export interface JobDetail extends JobSummary {
  airings: JobAiring[];
  attemptLog: JobAttempt[];
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

async function call<T>(method: string, url: string): Promise<T> {
  const res = await fetch(url, { method, credentials: "same-origin" });
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

export async function listJobs(states: JobState[]): Promise<JobSummary[]> {
  const q = states.length > 0 ? `?state=${states.join(",")}` : "";
  return (await call<{ jobs: JobSummary[] }>("GET", `/api/jobs${q}`)).jobs;
}

export const getJob = (id: number) => call<JobDetail>("GET", `/api/jobs/${id}`);
export const retryJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/retry`);
/** 「録らない」。試行がまだ無い録音予定のジョブを、録らない予定（放送の後なら録らなかった）にする。 */
export const skipJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/skip`);
/** 「録る」。録らない予定のジョブは録音予定に戻し、録らなかったのジョブは手動再試行と同じく再試行待ちにする。 */
export const recordJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/record`);
/** 「録り直す」。録音済みのジョブを radiko から録り直させる（再試行待ちにする）。前に保存したファイルと同じ名前なら上書きする（ADR 0025）。 */
export const rerecordJob = (id: number) => call<JobSummary>("POST", `/api/jobs/${id}/rerecord`);

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
