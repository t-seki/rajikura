import { Alert, Badge, Button, Group, Loader, Modal, Stack, Switch, Table, Text, Title } from "@mantine/core";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { listJobs } from "../jobs/jobsApi";
import { SortableTh } from "../ui/SortableTh";
import type { SortState } from "../ui/sort";
import { notifySuccess } from "../ui/toast";
import {
  createShow,
  deleteShow,
  getShow,
  listShows,
  listStations,
  replaceShow,
  setShowEnabled,
  type Show,
  type Station,
} from "./api";
import { nextAiringLabel, unmatchedDayLines, weeklySummaries } from "./format";
import { ShowForm } from "./ShowForm";

const message = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

type SortKey = "name" | "next";

// 次の放送予定の時刻（ミリ秒）。無効な番組と、当たる放送回が無い番組は null。
const nextTime = (r: Show) => (r.enabled && r.next_airing ? new Date(r.next_airing).getTime() : null);
const byName = (a: Show, b: Show) => a.name.localeCompare(b.name, "ja");

// 既定は次の放送予定の近い順。次の放送予定が無い番組は、向きによらず下に置き、その中は名前順。
function sortShows(list: Show[], sort: SortState<SortKey>): Show[] {
  const out = [...list];
  if (sort?.key === "name") {
    out.sort((a, b) => (sort.dir === "asc" ? byName(a, b) : byName(b, a)));
    return out;
  }
  const dir = sort?.dir === "desc" ? -1 : 1;
  out.sort((a, b) => {
    const ta = nextTime(a);
    const tb = nextTime(b);
    if (ta === null && tb === null) return byName(a, b);
    if (ta === null) return 1;
    if (tb === null) return -1;
    return ta === tb ? byName(a, b) : dir * (ta - tb);
  });
  return out;
}

/**
 * 番組の画面。一覧・作成・編集・削除・有効／無効。
 * onOpenEdit があれば、編集を開く番組は URL（`/shows/{名前}`）で決まる: 一覧を読んだ後に openName の番組の編集を開き、
 * 編集を開く・閉じる操作は onOpenEdit・onCloseEdit に頼む（ADR 0024）。openName の番組が一覧に無ければ、編集を開かずに
 * 「見つからない」を出して onMissingEdit を呼ぶ（一覧の URL に置き換える。閉じるボタンの onCloseEdit と違い、戻らない）。
 * 一覧を読めなかったときは、エラーを出したまま開かない（URL もそのまま）。作るダイアログは URL にしない。
 * onOpenEdit が無ければ、編集はこの画面の中だけで開く。
 */
export function ShowsPage({
  openName = null,
  onOpenEdit,
  onCloseEdit,
  onMissingEdit,
}: { openName?: string | null; onOpenEdit?: (name: string) => void; onCloseEdit?: () => void; onMissingEdit?: () => void } = {}) {
  const [shows, setShows] = useState<Show[] | null>(null);
  const [stations, setStations] = useState<Station[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // editing は、編集中の番組（undefined なら作成、null ならフォームを閉じている）。
  const [editing, setEditing] = useState<Show | undefined | null>(null);
  const [deleting, setDeleting] = useState<Show | null>(null);
  const [busy, setBusy] = useState(false);
  // 無効にする件数を取っている間は、スイッチ・編集・削除を押せなくする（別の番組の確かめや Modal の重なりを防ぐ）
  const [checking, setChecking] = useState(false);
  const [sort, setSort] = useState<SortState<SortKey>>(null);
  // 無効にする確かめ。count は消える録音予定のジョブの数（取れなければ null）
  const [disabling, setDisabling] = useState<{ show: Show; count: number | null } | null>(null);
  const sorted = useMemo(() => (shows ? sortShows(shows, sort) : null), [shows, sort]);
  // 一覧を最初に読み終えた（読めなかったときも）。openName を片付ける合図
  const [loaded, setLoaded] = useState(false);
  // 毎週の予約が無い番組ごとの、1 回の予約の数（番組を 1 つ読んで数える）。読めなかった番組は入らない。
  const [onceCounts, setOnceCounts] = useState<Record<string, number>>({});
  const [showIdle, setShowIdle] = useState(false);
  // 予約の無い番組（毎週も 1 回も持たない）の名前。1 回の予約の数が分かった番組だけ。
  const idle = useMemo(
    () => new Set((shows ?? []).filter((r) => r.weekly.length === 0 && onceCounts[r.name] === 0).map((r) => r.name)),
    [shows, onceCounts],
  );

  const reload = useCallback(async () => {
    try {
      setShows(await listShows());
    } catch (e) {
      setError(message(e, "番組の一覧を読めない"));
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    reload();
    // 局の一覧は、取れなければ局 ID を手で打つ（フォームが TextInput にする）。
    listStations().then(setStations, () => setStations([]));
  }, [reload]);

  // 一覧は 1 回の予約の数を持たないので、毎週が無い番組だけ 1 つずつ読んで数える（読めなければ分けずに一覧に残す）。
  useEffect(() => {
    // 編集ダイアログが開いている間は読まない。閉じたときに読み直す（ダイアログで 1 回の予約を足す・消すだけでも数が変わる）。
    if (!shows || editing !== null) return;
    let live = true;
    for (const r of shows) {
      if (r.weekly.length > 0) continue;
      getShow(r.name).then(
        (d) => live && setOnceCounts((c) => ({ ...c, [r.name]: (d.onces ?? []).length })),
        () => {},
      );
    }
    return () => {
      live = false;
    };
  }, [shows, editing]);

  const routed = onOpenEdit !== undefined;
  // URL の番組の編集を開く・閉じる。一覧を読み直しても（保存の後など）開き直さないよう、URL が変わったときと最初に読み終えたときだけ見る。
  const missingRef = useRef(onMissingEdit);
  missingRef.current = onMissingEdit;
  useEffect(() => {
    if (!routed) return;
    if (openName === null) {
      // 作るダイアログ（undefined）は URL によらない
      setEditing((cur) => (cur === undefined ? cur : null));
      return;
    }
    // 一覧を読めなかったときは、エラーはもう出ているので開かない
    if (!loaded || shows === null) return;
    const target = shows.find((s) => s.name === openName);
    if (target) setEditing(target);
    else {
      setError(`番組「${openName}」が見つからない`);
      missingRef.current?.();
    }
  }, [routed, openName, loaded]);

  /** 編集を開く。 */
  const startEdit = (r: Show) => (routed ? onOpenEdit(r.name) : setEditing(r));
  /** 作る・編集のダイアログを閉じる。編集なら URL も一覧に戻す。 */
  const closeForm = () => {
    const wasEditing = editing !== null && editing !== undefined;
    setEditing(null);
    if (routed && wasEditing) onCloseEdit?.();
  };

  async function save(show: Show) {
    if (editing === undefined) await createShow(show);
    else if (editing) await replaceShow(editing.name, show);
    notifySuccess(editing === undefined ? `番組「${show.name}」を作った` : `番組「${show.name}」を保存した`);
    closeForm();
    setError(null);
    await reload();
  }

  async function setEnabled(show: Show, enabled: boolean, count: number | null) {
    setError(null);
    setBusy(true);
    try {
      await setShowEnabled(show.name, enabled);
      setDisabling(null);
      notifySuccess(
        enabled
          ? "有効にした。番組表を展開して、今日から先の録音予定のジョブを作る"
          : count === null
            ? "無効にした。録音予定のジョブを消す"
            : `無効にした。録音予定のジョブ ${count} 件を消す`,
      );
      await reload();
    } catch (e) {
      setDisabling(null);
      setError(message(e, "有効／無効を変えられない"));
    } finally {
      setBusy(false);
    }
  }

  // 無効にするときだけ確かめる。消える件数は waiting のジョブをその番組の名前で数える（取れなくても止めない）。
  async function toggle(show: Show, enabled: boolean) {
    if (enabled) {
      await setEnabled(show, true, null);
      return;
    }
    if (checking) return;
    setChecking(true);
    let count: number | null = null;
    try {
      count = (await listJobs(["waiting"])).filter((j) => j.showName === show.name).length;
    } catch {
      count = null;
    } finally {
      setChecking(false);
    }
    setDisabling({ show, count });
  }

  async function remove() {
    if (!deleting) return;
    setBusy(true);
    setError(null);
    try {
      await deleteShow(deleting.name);
      setDeleting(null);
      notifySuccess(`番組「${deleting.name}」を消した`);
      await reload();
    } catch (e) {
      setDeleting(null);
      setError(message(e, "番組を消せない"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>番組の一覧</Title>
        <Button onClick={() => setEditing(undefined)}>番組を作る</Button>
      </Group>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {shows === null ? (
        !error && <Loader />
      ) : shows.length === 0 ? (
        <Text c="dimmed">番組がまだ無い。</Text>
      ) : (
        ([false, true] as const).map((idleGroup) => {
          const rows = (sorted ?? []).filter((r) => idle.has(r.name) === idleGroup);
          // 予約の無い番組は、分けて畳んで出す
          // 行の無い表（見出しだけ）は出さない
          if (rows.length === 0) return null;
          return (
        <Stack key={String(idleGroup)} gap="xs">
        {idleGroup && (
          <Button
            variant="subtle"
            size="xs"
            style={{ alignSelf: "flex-start" }}
            onClick={() => setShowIdle((v) => !v)}
            aria-expanded={showIdle}
          >
            {showIdle ? "▾" : "▸"} 予約の無い番組（{rows.length} 件）
          </Button>
        )}
        {(!idleGroup || showIdle) && (
        <Table.ScrollContainer minWidth={640}>
          <Table striped>
            <Table.Thead>
              <Table.Tr>
                <SortableTh label="名前" sortKey="name" sort={sort} onSort={setSort} />
                <Table.Th>有効</Table.Th>
                <Table.Th>予約</Table.Th>
                <SortableTh label="次の放送予定" sortKey="next" sort={sort} onSort={setSort} />
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((s) => (
                <Table.Tr key={s.name}>
                  <Table.Td>
                    {s.name} {!s.enabled && <Badge color="gray">無効</Badge>}
                  </Table.Td>
                  <Table.Td>
                    <Switch
                      aria-label={`${s.name} を有効にする`}
                      checked={s.enabled}
                      disabled={checking}
                      onChange={(e) => toggle(s, e.currentTarget.checked)}
                    />
                  </Table.Td>
                  <Table.Td>
                    {s.weekly.length === 0 ? (
                      <Text c="dimmed" size="sm">
                        {onceCounts[s.name] ? `1 回の予約 ${onceCounts[s.name]} 件` : "予約が無い"}
                      </Text>
                    ) : (
                      weeklySummaries(s.weekly, stations ?? []).map((text, i) => (
                        <Text key={i} size="sm">
                          {text}
                        </Text>
                      ))
                    )}
                  </Table.Td>
                  <Table.Td>
                    {nextAiringLabel(s.next_airing, s.enabled)}
                    {/* 当たらない日は、次の放送予定の有無によらず、その下に放送日の順に全部出す（無効な番組には付かない）。 */}
                    {s.enabled &&
                      (s.unmatched_days ?? []).map((u) => (
                        <Text key={`${u.station}@${u.start}`} size="xs" c="orange">
                          {unmatchedDayLines(u).map((line, i) => (
                            <span key={i} style={{ display: "block" }}>
                              {line}
                            </span>
                          ))}
                        </Text>
                      ))}
                  </Table.Td>
                  <Table.Td>
                    <Group gap="xs" wrap="nowrap">
                      <Button size="xs" variant="default" disabled={checking} onClick={() => startEdit(s)} aria-label={`${s.name} を編集`}>
                        編集
                      </Button>
                      <Button
                        size="xs"
                        variant="subtle"
                        color="red"
                        disabled={checking}
                        onClick={() => setDeleting(s)}
                        aria-label={`${s.name} を消す`}
                      >
                        削除
                      </Button>
                    </Group>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        )}
        </Stack>
          );
        })
      )}

      <Modal
        opened={editing !== null}
        onClose={closeForm}
        title={editing === undefined ? "番組を作る" : "番組を編集する"}
        size="lg"
      >
        {editing !== null && (
          <ShowForm
            key={editing?.name ?? ""}
            initial={editing}
            stations={stations}
            onSubmit={save}
            onCancel={closeForm}
          />
        )}
      </Modal>

      <Modal opened={disabling !== null} onClose={() => setDisabling(null)} title="番組を無効にする">
        <Stack>
          <Text>
            番組「{disabling?.show.name}」を無効にする。
            {disabling?.count === null
              ? "録音予定のジョブが消える。"
              : disabling?.count === 0
                ? "消える録音予定のジョブは 0 件。"
                : `録音予定のジョブ ${disabling?.count} 件が消える。`}
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDisabling(null)}>
              やめる
            </Button>
            <Button
              color="red"
              loading={busy}
              onClick={() => disabling && setEnabled(disabling.show, false, disabling.count)}
            >
              無効にする
            </Button>
          </Group>
        </Stack>
      </Modal>

      <Modal opened={deleting !== null} onClose={() => setDeleting(null)} title="番組を消す">
        <Stack>
          <Text>
            番組「{deleting?.name}」とその予約・1 回の予約を消す。ジョブと保存済みのファイルは消さない。まだ一度も試していない録音予定
            のジョブは、消した直後の展開で消える。
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleting(null)}>
              やめる
            </Button>
            <Button color="red" loading={busy} onClick={remove}>
              消す
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
