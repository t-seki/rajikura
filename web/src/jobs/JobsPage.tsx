import { Alert, Button, Card, Group, Loader, MultiSelect, Stack, Table, Text } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { listStations } from "../shows/api";
import { SortableTh } from "../ui/SortableTh";
import type { SortState } from "../ui/sort";
import { formatDateTime, formatTime, progressText, stageLabel, truncate } from "./format";
import { JobDetailModal, useVisiblePolling } from "./JobDetailModal";
import { getJob, jobStates, JobsApiError, listJobs, type JobState, type JobSummary } from "./jobsApi";
import { nextOrDeadline, sortJobs, type JobSortKey } from "./jobsSort";
import { ChaptersBadge } from "./ChaptersBadge";
import { LiveBadge } from "./LiveBadge";
import { StateBadge } from "./StateBadge";

const jobTitle = (j: JobSummary) => `${j.showName}${j.part > 0 ? `（パート ${j.part}）` : ""}`;

const failureReason = (j: JobSummary) =>
  j.lastFailedStage || j.lastError ? `${j.lastFailedStage ? `${stageLabel(j.lastFailedStage)}で失敗` : "失敗"}: ${truncate(j.lastError)}` : "";

/** 録音中のジョブの進捗を、状態の横に文字だけで出す（一覧にはプログレスバーを置かない）。進捗が無ければ何も出さない。 */
function ProgressText({ job }: { job: JobSummary }) {
  if (job.state !== "running" || !job.progress) return null;
  return (
    <Text size="sm" data-testid="job-progress">
      {progressText(job.progress)}
    </Text>
  );
}

/** 局と時刻の組を縦に並べる。時刻はジョブの放送日を基準にした放送日の時刻。局名が引けなければ局 ID を出す。 */
function StationTimes({ job, names }: { job: JobSummary; names: Record<string, string> }) {
  return (
    <Stack gap={0}>
      {job.stations.map((s) => (
        <Text key={s.stationId} size="sm">
          {names[s.stationId] ?? s.stationId} {formatTime(s.start, job.day)}〜{formatTime(s.end, job.day)}
        </Text>
      ))}
    </Stack>
  );
}

/**
 * ジョブの画面。
 * onOpenJob があれば、詳細を開くジョブは URL（`/jobs/{id}`）で決まる: openId のジョブの詳細を開き、詳細を開く・閉じる操作は
 * onOpenJob・onCloseJob に頼む（ADR 0024）。openId が数でなければ、詳細を開かずに onMissingJob を呼ぶ。数なら詳細を開いて読み、
 * ジョブが無い（404）と分かったら onMissingJob を呼ぶ（詳細は一度開いてから閉じる）。404 でなく読めないだけのときは、詳細に
 * エラーを出したままにする。onMissingJob は一覧の URL に置き換える（閉じるボタンの onCloseJob と違い、戻らない）。
 * onOpenJob が無ければ、詳細はこの画面の中だけで開く。
 */
export function JobsPage({
  openId = null,
  onOpenJob,
  onCloseJob,
  onMissingJob,
}: { openId?: string | null; onOpenJob?: (id: number) => void; onCloseJob?: () => void; onMissingJob?: () => void } = {}) {
  const [filter, setFilter] = useState<JobState[]>([]);
  const [jobs, setJobs] = useState<JobSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ownSelected, setOwnSelected] = useState<number | null>(null);
  const routed = onOpenJob !== undefined;
  const routedId = openId !== null && /^[1-9][0-9]*$/.test(openId) ? Number(openId) : null;
  const selected = routed ? routedId : ownSelected;
  const setSelected = (id: number | null) => {
    if (!routed) setOwnSelected(id);
    else if (id === null) onCloseJob?.();
    else onOpenJob(id);
  };
  const [sort, setSort] = useState<SortState<JobSortKey>>(null);
  const [stationNames, setStationNames] = useState<Record<string, string>>({});
  const wide = useMediaQuery("(min-width: 48em)", false, { getInitialValueInEffect: false });

  // 読み直しが飛んでいる間に絞り込みを変えても、古い要求の応答で新しい一覧を上書きしないよう、最新の要求の応答だけを使う。
  const requestSeq = useRef(0);
  const reload = useCallback(async () => {
    const seq = ++requestSeq.current;
    try {
      const list = await listJobs(filter);
      if (seq !== requestSeq.current) return;
      setJobs(list);
      setError(null);
    } catch (e) {
      if (seq !== requestSeq.current) return;
      setError(e instanceof Error ? e.message : "ジョブを読めなかった");
    }
  }, [filter]);

  useEffect(() => {
    setJobs(null);
    void reload();
  }, [reload]);

  // URL の id が数でなければ、詳細を開かずに一覧の URL に置き換える。数なら（詳細を開いたまま）読み、404 なら一覧の URL に置き換える
  // （詳細は閉じる）。404 でなく読めないだけのときは、詳細にエラーを出したままにする。
  const missingRef = useRef(onMissingJob);
  missingRef.current = onMissingJob;
  useEffect(() => {
    if (!routed || openId === null) return;
    if (routedId === null) {
      missingRef.current?.();
      return;
    }
    let live = true;
    getJob(routedId).catch((e) => {
      if (live && e instanceof JobsApiError && e.status === 404) missingRef.current?.();
    });
    return () => {
      live = false;
    };
  }, [routed, openId, routedId]);

  // 一覧は見えている間だけ読み直す。ローディングは出さず、失敗は reload と同じく Alert を出して前の行を残す。
  useVisiblePolling(() => void reload());

  // 局名は引けなくても（番組表の取得元が無い、失敗した）局 ID を出せばよいので、失敗は黙って捨てる。
  useEffect(() => {
    let live = true;
    listStations()
      .then((list) => live && setStationNames(Object.fromEntries(list.map((s) => [s.id, s.name]))))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  // 表は見出しで選んだ列、カードは既定の並び（見出しが無いので並べ替えは無い）。
  const rows = useMemo(() => (jobs ? sortJobs(jobs, wide ? sort : null) : null), [jobs, sort, wide]);

  return (
    <Stack>
      <MultiSelect
        label="状態で絞り込み"
        placeholder={filter.length === 0 ? "すべて" : undefined}
        data={jobStates}
        value={filter}
        onChange={(v) => setFilter(v as JobState[])}
        clearable
      />
      <Text size="sm" c="dimmed">
        放送日が 120 日前以降のジョブだけを表示している。
      </Text>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {jobs === null && !error && <Loader size="sm" />}
      {jobs?.length === 0 && <Text c="dimmed">該当するジョブは無い。</Text>}
      {rows && rows.length > 0 && wide && (
        <Table.ScrollContainer minWidth={900}>
          <Table highlightOnHover verticalSpacing="xs">
            <Table.Thead>
              <Table.Tr>
                <SortableTh label="状態" sortKey="state" sort={sort} onSort={setSort} />
                <SortableTh label="放送日" sortKey="day" sort={sort} onSort={setSort} />
                <SortableTh label="番組の名前" sortKey="name" sort={sort} onSort={setSort} />
                <Table.Th>局と時刻</Table.Th>
                <SortableTh label="試行" sortKey="attempts" sort={sort} onSort={setSort} />
                <SortableTh label="次の試行・録音期限" sortKey="next" sort={sort} onSort={setSort} />
                <Table.Th>直近の失敗の理由</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((j) => (
                <Table.Tr key={j.id} data-testid="job" style={{ cursor: "pointer" }} onClick={() => setSelected(j.id)}>
                  <Table.Td>
                    <Group gap={4}>
                      <StateBadge job={j} />
                      {j.live && <LiveBadge />}
                      <ChaptersBadge chapters={j.chapters} />
                      <ProgressText job={j} />
                    </Group>
                  </Table.Td>
                  <Table.Td>{j.day}</Table.Td>
                  <Table.Td style={{ overflowWrap: "anywhere" }}>{jobTitle(j)}</Table.Td>
                  <Table.Td>
                    <StationTimes job={j} names={stationNames} />
                  </Table.Td>
                  <Table.Td>{j.attempts} 回</Table.Td>
                  <Table.Td>{formatDateTime(nextOrDeadline(j))}</Table.Td>
                  <Table.Td>
                    <Text size="sm" c="red" style={{ overflowWrap: "anywhere" }}>
                      {failureReason(j)}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Button
                      size="compact-sm"
                      variant="default"
                      onClick={(e) => {
                        e.stopPropagation();
                        setSelected(j.id);
                      }}
                      aria-label={`${j.showName}の詳細`}
                    >
                      詳細
                    </Button>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      {!wide &&
        rows?.map((j) => (
          <Card key={j.id} withBorder padding="sm" data-testid="job">
            <Group justify="space-between" wrap="nowrap" align="flex-start">
              <Stack gap={2} style={{ minWidth: 0 }}>
                <Text fw={600} style={{ overflowWrap: "anywhere" }}>
                  {jobTitle(j)}
                </Text>
                <Text size="sm">放送日 {j.day}</Text>
                <StationTimes job={j} names={stationNames} />
                <Text size="sm" c="dimmed">
                  試行 {j.attempts} 回
                  {(j.state === "waiting" || j.state === "retrying") && ` ・ 次の試行 ${formatDateTime(j.nextAttemptAt)}`}
                </Text>
                <Text size="sm" c="dimmed">
                  録音期限 {formatDateTime(j.deadline)}
                </Text>
                {failureReason(j) && (
                  <Text size="sm" c="red" style={{ overflowWrap: "anywhere" }}>
                    {failureReason(j)}
                  </Text>
                )}
              </Stack>
              <Stack gap="xs" align="flex-end">
                <StateBadge job={j} />
                {j.live && <LiveBadge />}
                <ChaptersBadge chapters={j.chapters} />
                <ProgressText job={j} />
                <Button size="compact-sm" variant="default" onClick={() => setSelected(j.id)} aria-label={`${j.showName}の詳細`}>
                  詳細
                </Button>
              </Stack>
            </Group>
          </Card>
        ))}
      <JobDetailModal id={selected} onClose={() => setSelected(null)} onChanged={reload} />
    </Stack>
  );
}
