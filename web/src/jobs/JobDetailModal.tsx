import { Alert, Button, Divider, Group, Loader, Modal, Progress, Stack, Text } from "@mantine/core";
import { useCallback, useEffect, useRef, useState } from "react";
import { broadcastRange } from "../guide/format";
import { notifySuccess } from "../ui/toast";
import { formatDateTime, formatDuration, progressPercent, progressText, stageLabel } from "./format";
import {
  chaptersLabels,
  getJob,
  jobAudioError,
  jobAudioUrl,
  recordJob,
  rerecordJob,
  retryJob,
  skipJob,
  viewState,
  type JobDetail,
} from "./jobsApi";
import { StateBadge } from "./StateBadge";

/** skipped のジョブを、誰がなぜ録らないと決めたか（ADR 0019）。 */
export function skipReason(job: JobDetail): string {
  const what = viewState(job) === "skip_planned" ? "録らない予定にした" : "録らなかった";
  if (job.byPerson) return `人が録らないと決めたので、${what}。`;
  const titles = [...new Set(job.airings.map((a) => a.title))].map((t) => `「${t}」`).join("");
  return `番組表の番組名${titles}が番組「${job.showName}」の名前で始まらないので、${what}。`;
}

/** 「録り直す」を押したときの確かめの文。 */
export const rerecordConfirm = "radiko から録り直します。前に保存したファイルと同じ名前なら上書きします。よろしいですか。";

// ジョブの一覧と開いている詳細を読み直す間隔。録音の状態が変わっても知らせは出さず、表示を最新にするだけ
// （完了と失敗は通知の画面と通知先に出る）。録音中かどうかでは変えない。
export const jobsPollInterval = 10_000;

/**
 * 画面が見えている間（`document.visibilityState` が `visible`）だけ、`jobsPollInterval` ごとに `tick` を呼ぶ。
 * 裏にある間は止め、表に戻ったらすぐ 1 回呼んで間隔を数え直す。`active` が false の間は何もしない。
 */
export function useVisiblePolling(tick: () => void, active = true) {
  const latest = useRef(tick);
  latest.current = tick;
  useEffect(() => {
    if (!active) return;
    let timer: ReturnType<typeof setInterval> | undefined;
    const stop = () => {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
    };
    const start = () => {
      stop();
      timer = setInterval(() => latest.current(), jobsPollInterval);
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        latest.current();
        start();
      } else {
        stop();
      }
    };
    if (document.visibilityState === "visible") start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [active]);
}

/**
 * 録音を聴く `<audio>`。再生・シーク・音量はブラウザの機能だけ。再生できなかった（`error` イベント）ときだけ、
 * 同じ URL を読み直して、API が断った理由を下に出す。詳細の読み直しで作り直さないよう、保存先ごとに `key` を付けて使う。
 */
function JobAudio({ id }: { id: number }) {
  const [reason, setReason] = useState<string | null>(null);
  return (
    <Stack gap={4}>
      <audio
        controls
        preload="none"
        src={jobAudioUrl(id)}
        style={{ width: "100%" }}
        data-testid="job-audio"
        onError={() => {
          void jobAudioError(id).then(setReason);
        }}
        onPlay={() => setReason(null)}
      />
      {reason && (
        <Text size="sm" c="red" role="alert" style={{ overflowWrap: "anywhere" }}>
          {reason}
        </Text>
      )}
    </Stack>
  );
}

interface Props {
  /** 開くジョブの ID。null なら閉じる。 */
  id: number | null;
  onClose: () => void;
  /** 再試行・録らない・録る・録り直すをしたとき、一覧を読み直すために呼ぶ。 */
  onChanged: () => void;
}

export function JobDetailModal({ id, onClose, onChanged }: Props) {
  const [job, setJob] = useState<JobDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (jobId: number) => {
    try {
      setJob(await getJob(jobId));
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "ジョブを読めなかった");
    }
  }, []);

  useEffect(() => {
    setJob(null);
    setError(null);
    if (id !== null) void load(id);
  }, [id, load]);

  // 開いている間は詳細も読み直す。操作の最中は読み直さず、読み直しの失敗は表示もエラーも変えない
  // （操作の失敗の Alert を上書きしないため）。
  const jobRef = useRef(job);
  jobRef.current = job;
  const currentId = useRef(id);
  currentId.current = id;
  useVisiblePolling(() => {
    if (id === null || busy) return;
    getJob(id)
      .then((j) => {
        if (currentId.current !== id) return;
        // 最初の読み込みが失敗して詳細が無いままのときだけ、読み直しの成功でそのエラーを消す
        // （操作の失敗の Alert は、詳細が出ているので残す）。
        if (!jobRef.current) setError(null);
        setJob(j);
      })
      .catch(() => {});
  }, id !== null);

  async function act(op: typeof retryJob, done: string) {
    if (id === null) return;
    setBusy(true);
    try {
      await op(id);
      notifySuccess(done);
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : "操作できなかった");
    } finally {
      setBusy(false);
    }
    // 失敗しても、状態が変わっているかもしれないので読み直す（エラーの表示は残す）
    try {
      setJob(await getJob(id));
    } catch {
      // 読み直せなくても、上のエラーか前の表示を残す
    }
  }

  return (
    <Modal opened={id !== null} onClose={onClose} title={job ? `${job.showName} ${job.day}` : "ジョブ"} size="lg">
      <Stack>
        {error && (
          <Alert color="red" role="alert">
            {error}
          </Alert>
        )}
        {!job && !error && <Loader size="sm" />}
        {job && (
          <>
            <Group justify="space-between">
              <StateBadge job={job} />
              <Text size="sm" c="dimmed">
                録音期限 {formatDateTime(job.deadline)}
              </Text>
            </Group>
            {job.state === "running" && job.progress && (
              <Stack gap={4} data-testid="job-progress">
                <Text size="sm">{progressText(job.progress)}</Text>
                {progressPercent(job.progress) !== null && (
                  <Progress value={progressPercent(job.progress) ?? 0} aria-label="入手の進捗" />
                )}
              </Stack>
            )}
            {job.live && (
              <Text size="sm">
                ライブ録音のジョブ（タイムフリーの対象外の放送回を含むので、放送中に録る。放送の時刻に rajikura が動いている必要がある）。
              </Text>
            )}
            {job.state === "skipped" && <Text size="sm">{skipReason(job)}</Text>}
            {job.saveRetryUntil && (
              <Text size="sm">録れた分を保存できなかったので、保存を自動でやり直している（{formatDateTime(job.saveRetryUntil)} まで）。</Text>
            )}
            {job.keepUntil && (
              <Text size="sm">
                録れた分を {formatDateTime(job.keepUntil)} まで残している。保存先などの原因を直してから「手動で再試行」を押すと、保存からやり直す。
              </Text>
            )}
            {(job.retryable || job.skippable || job.recordable || job.rerecordable) && (
              <Group>
                {job.retryable && (
                  <Button disabled={busy} onClick={() => act(retryJob, "再試行を依頼した")}>
                    手動で再試行
                  </Button>
                )}
                {job.recordable && !job.retryable && (
                  <Button disabled={busy} onClick={() => act(recordJob, viewState(job) === "skip_planned" ? "録音予定に戻した" : "再試行を依頼した")}>
                    録る
                  </Button>
                )}
                {job.skippable && (
                  <Button variant="default" disabled={busy} onClick={() => act(skipJob, "録らないにした")}>
                    録らない
                  </Button>
                )}
                {job.rerecordable && (
                  <Button
                    variant="default"
                    disabled={busy}
                    onClick={() => {
                      // 前のファイルを上書きしうる操作なので、確かめてから呼ぶ（ADR 0025）。
                      if (!window.confirm(rerecordConfirm)) return;
                      void act(rerecordJob, "録り直しを依頼した");
                    }}
                  >
                    録り直す
                  </Button>
                )}
              </Group>
            )}

            <Divider label="結果" />
            <Text size="sm" style={{ overflowWrap: "anywhere" }}>
              保存先: {job.outputPath || "-"}
            </Text>
            {job.outputPath && <JobAudio key={`${job.id}:${job.outputPath}`} id={job.id} />}
            <Text size="sm">長さ: {job.durationSeconds > 0 ? formatDuration(job.durationSeconds) : "記録なし"}</Text>
            {job.chapters && <Text size="sm">チャプター: {chaptersLabels[job.chapters]}</Text>}

            <Divider label="放送回" />
            {job.airings.map((a) => (
              <Stack key={`${a.stationId}@${a.start}`} gap={0} data-testid="airing">
                <Text size="sm" fw={600} style={{ overflowWrap: "anywhere" }}>
                  {a.title}
                  {a.missing && "（欠落）"}
                </Text>
                <Text size="xs" c="dimmed">
                  {a.stationId} ・ {broadcastRange(a)}
                </Text>
              </Stack>
            ))}

            <Divider label="試行の履歴" />
            {job.attemptLog.length === 0 && (
              <Text size="sm" c="dimmed">
                試行はまだ無い。
              </Text>
            )}
            {job.attemptLog.map((a) => (
              <Stack key={a.startedAt} gap={0} data-testid="attempt">
                <Text size="sm">
                  {formatDateTime(a.startedAt)} ・ {a.failedStage ? `失敗（${stageLabel(a.failedStage)}）` : "成功"}
                </Text>
                {a.error && (
                  <Text size="xs" c="red" style={{ overflowWrap: "anywhere" }}>
                    {a.error}
                  </Text>
                )}
              </Stack>
            ))}
          </>
        )}
      </Stack>
    </Modal>
  );
}
