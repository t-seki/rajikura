import {
  Alert,
  Anchor,
  Badge,
  Button,
  Checkbox,
  Drawer,
  Group,
  Loader,
  Modal,
  Select,
  Stack,
  Text,
  TextInput,
} from "@mantine/core";
import { useEffect, useState } from "react";
import { listShows, getShow, type Once, type Show } from "../shows/api";
import { weeklySummaries } from "../shows/format";
import type { GuideAiring, GuideStation } from "./api";
import {
  airingDayRange,
  broadcastDayOf,
  broadcastRange,
  liveExpired,
  notAvailableLabel,
  notAvailableMark,
  stateColor,
  stateLabel,
  stateMark,
} from "./format";
import {
  defaultDest,
  disabledDest,
  findOnce,
  groupName,
  offersJoin,
  recordOnce,
  recordSkipped,
  recordWeekly,
  skipPlanned,
  stopOnce,
  type Dest,
  type Outcome,
} from "./reserve";

const message = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/** 「どの番組に足すか」で「新しい番組を作る」を表す値（番組の名前と重ならないもの）。 */
const NEW_SHOW = "\u0000new";

/** PC 幅の右の欄の幅（px）。番組表をこの分だけ空けて、隠さない。 */
export const PANEL_WIDTH = 400;

export function StateBadge({ airing }: { airing: GuideAiring }) {
  return (
    <Group gap={4} wrap="nowrap">
      <Badge
        color={stateColor[airing.state]}
        variant={airing.state === "none" ? "outline" : "filled"}
        leftSection={stateMark[airing.state] || undefined}
      >
        {stateLabel[airing.state]}
      </Badge>
      {airing.notAvailable && (
        <Badge color="orange" variant="light" leftSection={notAvailableMark}>
          {notAvailableLabel}
        </Badge>
      )}
    </Group>
  );
}

/**
 * 番組表の選択（GuidePage が持つ）。rows は詳細に並べる放送回（開始の順。チェックを外した回も、詳細を開いている間は残す）、
 * picked は選んだ放送回の開始、auto は普通のクリックの自動の組（2 件以上）で選んだままか、pending は自動の組を調べている間か、
 * error は自動の組を調べられなかった理由。
 */
export type PanelSelection = { rows: GuideAiring[]; picked: string[]; auto: boolean; pending: boolean; error?: string };

/** 録音予定の放送回の、当たっている番組の 1 回の予約。null なら 1 回の予約ではない（毎週の予約で当たっている）。 */
type Match = { status: "loading" } | { status: "done"; once: Once | null } | { status: "error"; error: string };

/**
 * 放送回の詳細。PC 幅は右の欄（番組表を隠さない）、スマホ幅は画面いっぱいに出す。
 * 予約なしの放送回では、番組表の選択（selection。普通のクリックなら自動の組）を足す放送回にする。選択が 2 件以上なら、選んだ
 * 放送回をチェック付きで並べ（外すと選択から外れる。onToggle）、自動の組なら「この回だけにする」（onOnly）を出す。
 * onMoreSelect があれば（タッチの操作）、1 件の詳細に「ほかの回も選ぶ」を出す。
 * 「この回を録音」「毎週録音」は既定の足す先の番組（defaultDest）にすぐ予約し、「設定を変えて予約」で足す先の番組などを選べる。
 * 足す先の番組が、足した後にその放送日に 2 つ以上の放送回を当てる、新しく作る番組か分割番組でない番組なら、「パートを 1 つのファイルに
 * つなぐ」（既定はオン）を折りたたみの外に出す（offersJoin）。
 * 録音予定の放送回では、1 回の予約なら「この回の録音をやめる」、毎週の予約なら「番組の設定を開く」を出す。
 * 録音予定の放送回でジョブ（試行がまだ無いもの）があれば「録らない」も出す。
 * 録らない予定・録らなかったのジョブがある放送回では、誰がなぜ止めたかと「録る」（録らない予定は録音予定に戻し、録らなかったは
 * 手動再試行）を出す（ADR 0019）。録らない予定・録らなかったの放送回でジョブが無いもの（展開の前、連結する番組で一部だけ
 * 番組名が違ったとき）は、予約なしの放送回と同じく「この回を録音」などを出す。
 * loadDay は、足す先の番組がその放送日にもう当てている放送回を数えるために、その放送日の番組表を返す。
 */
export function AiringPanel({
  airing,
  wide,
  stations,
  loadDay,
  selection,
  onToggle,
  onOnly,
  onMoreSelect,
  onClose,
  onDone,
  onOpenShow,
}: {
  airing: GuideAiring | null;
  wide: boolean;
  stations: { id: string; name: string }[];
  loadDay: (day: string) => Promise<GuideStation[]>;
  /** 番組表の選択。予約済みの放送回を開いたときは null（選択に入れない）。 */
  selection: PanelSelection | null;
  onToggle: (a: GuideAiring) => void;
  onOnly: () => void;
  onMoreSelect?: () => void;
  onClose: () => void;
  onDone: (o: Outcome) => void;
  onOpenShow: (name: string) => void;
}) {
  const title = airing?.title ?? "";
  const body = airing && (
    <AiringDetail
      // 放送回か状態が変わったら、中身を作り直す（予約した後の読み直しで状態が変わる）
      key={`${airing.stationId}|${airing.start}|${airing.state}|${airing.showName ?? ""}`}
      airing={airing}
      stations={stations}
      loadDay={loadDay}
      selection={selection}
      onToggle={onToggle}
      onOnly={onOnly}
      onMoreSelect={onMoreSelect}
      onDone={onDone}
      onOpenShow={onOpenShow}
    />
  );
  if (wide) {
    return (
      <Drawer
        opened={airing !== null}
        onClose={onClose}
        title={title}
        position="right"
        size={PANEL_WIDTH}
        withOverlay={false}
        lockScroll={false}
        trapFocus={false}
        closeOnClickOutside={false}
        data-testid="airing-panel"
      >
        {body}
      </Drawer>
    );
  }
  return (
    <Modal opened={airing !== null} onClose={onClose} title={title} fullScreen data-testid="airing-panel">
      {body}
    </Modal>
  );
}

function AiringDetail({
  airing,
  stations,
  loadDay,
  selection,
  onToggle,
  onOnly,
  onMoreSelect,
  onDone,
  onOpenShow,
}: {
  airing: GuideAiring;
  stations: { id: string; name: string }[];
  loadDay: (day: string) => Promise<GuideStation[]>;
  selection: PanelSelection | null;
  onToggle: (a: GuideAiring) => void;
  onOnly: () => void;
  onMoreSelect?: () => void;
  onDone: (o: Outcome) => void;
  onOpenShow: (name: string) => void;
}) {
  // 録音予定の回には予約のボタンを出さない。番組表の API は録音予定の回に必ず showName を付ける（guide.go の view）が、
  // 無いときも予約のボタンは出さず、当たっている番組が分からないと出す
  const planned = airing.state === "planned";
  // 録らない予定・録らなかったのジョブ（番組表の API は、ジョブのある放送回に jobId を付ける）。ジョブが無ければ null
  const isSkipPlanned = airing.state === "skip_planned";
  const skippedJob = (isSkipPlanned || airing.state === "skipped") && airing.jobId !== undefined ? airing.jobId : null;
  // 「録らない」にできる録音予定のジョブ。無ければ null
  const skippableJob = planned && airing.skippable && airing.jobId !== undefined ? airing.jobId : null;
  // 足す放送回。番組表の選択があれば、選んだ放送回（開始の順）。無ければ（予約済みの放送回でも足せるもの）この放送回だけ
  const targets = selection ? selection.rows.filter((a) => selection.picked.includes(a.start)) : [airing];
  // 録音期限（放送の終了の 5 分後）を過ぎたタイムフリーの対象外の放送回。「この回を録音」は API が断るので出さない
  const expiredLive = liveExpired(airing);
  const expiredTarget = expiredLive || targets.some((a) => liveExpired(a));
  const [shows, setShows] = useState<Show[] | null>(null);
  const [showsError, setShowsError] = useState<string | null>(null);
  // その放送日の番組表の全局の放送回（足す先の番組がもう当てている放送回を数える）。読めなければ空
  const [dayAirings, setDayAirings] = useState<GuideAiring[] | null>(null);
  const [match, setMatch] = useState<Match>({ status: "loading" });

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [destValue, setDestValue] = useState<string | null>(null);
  // 名前の欄を直したら、その名前。直していなければ選んだ放送回から決まる名前（groupName）
  const [nameEdit, setNameEdit] = useState<string | null>(null);
  const [join, setJoin] = useState(true);
  const [busy, setBusy] = useState<"once" | "weekly" | "stop" | "retry" | "skip" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    if (skippedJob !== null) return;
    if (planned) {
      if (!airing.showName) {
        setMatch({ status: "error", error: "当たっている番組が分からない" });
        return;
      }
      getShow(airing.showName).then(
        (s) => live && setMatch({ status: "done", once: findOnce(s.onces, airing) ?? null }),
        (e) => live && setMatch({ status: "error", error: message(e, "番組を読めない") }),
      );
      return () => {
        live = false;
      };
    }
    listShows().then(
      (s) => live && setShows(s),
      (e) => live && setShowsError(message(e, "番組の一覧を読めない")),
    );
    loadDay(broadcastDayOf(airing.start)).then(
      (st) => live && setDayAirings(st.flatMap((s) => s.airings)),
      // 番組表を読めなくても録音できる（もう当てている放送回は数えない。読めなかったことは番組表の選択が知らせる）
      () => live && setDayAirings([]),
    );
    return () => {
      live = false;
    };
    // 放送回か状態が変わると、親が key を変えて作り直すので、最初に 1 回だけ読む。
    // 番組表の読み直しで airing のオブジェクトが替わっても読み直さない
  }, []);

  // 足す先の番組。初めは既定（選んだ放送回の番組名から決まる名前の番組。defaultDest）。
  const named = targets.length > 0 ? targets : [airing];
  const newName = nameEdit ?? groupName(named);
  const initial = shows ? defaultDest(shows, named) : null;
  const chosenValue = destValue ?? (initial ? (initial.kind === "existing" ? initial.show.name : NEW_SHOW) : null);
  const chosenShow = shows?.find((s) => s.name === chosenValue);
  const picked: Dest | null = !shows
    ? null
    : !settingsOpen
      ? // 折りたたみを開いていなければ既定のまま
        initial
      : chosenShow
        ? { kind: "existing", show: chosenShow }
        : { kind: "new", name: newName, join };
  // 新しく作る番組のつなぐ設定は、折りたたみの外の「パートを 1 つのファイルにつなぐ」に従う
  const dest: Dest | null = picked?.kind === "new" ? { ...picked, join } : picked;
  // 足す先の番組を変えたら、出す・出さないを決め直す
  const joinOffered = dest !== null && dayAirings !== null && offersJoin(dest, targets, dayAirings);
  const waiting = !dest || dayAirings === null || (selection?.pending ?? false);

  async function run(kind: "once" | "weekly") {
    if (!dest) return;
    setError(null);
    if (targets.length === 0) {
      setError("選んだ回が無い");
      return;
    }
    if (disabledDest(dest) && !settingsOpen) {
      // 足す先の番組が無効な番組なら、すぐには足さない。折りたたみを開いて知らせる
      setSettingsOpen(true);
      setNotice(
        `番組「${dest.kind === "existing" ? dest.show.name : ""}」は無効なので、すぐには録音しなかった。足す先を選び直すか、このまま押すと無効な番組に足す（番組を有効にするまで録音されない）。`,
      );
      return;
    }
    setBusy(kind);
    try {
      // 選んだ放送回を全部足す（「この回を録音」と「毎週録音」で同じ）
      const joinIt = joinOffered && join;
      onDone(kind === "once" ? await recordOnce(dest, targets, joinIt) : await recordWeekly(dest, targets, joinIt));
    } catch (e) {
      setError(message(e, "録音の予約ができなかった"));
    } finally {
      setBusy(null);
    }
  }

  async function stop(once: Once) {
    setError(null);
    setBusy("stop");
    try {
      onDone(await stopOnce(airing.showName!, once, airing));
    } catch (e) {
      setError(message(e, "この回の録音をやめられなかった"));
    } finally {
      setBusy(null);
    }
  }

  async function retry(jobId: number) {
    setError(null);
    setBusy("retry");
    try {
      onDone(await recordSkipped(jobId, airing.showName ?? "", isSkipPlanned));
    } catch (e) {
      setError(message(e, "この回を録音にできなかった"));
    } finally {
      setBusy(null);
    }
  }

  async function skip(jobId: number) {
    setError(null);
    setBusy("skip");
    try {
      onDone(await skipPlanned(jobId, airing.showName ?? ""));
    } catch (e) {
      setError(message(e, "この回を録らないにできなかった"));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Stack>
      <Text size="sm">
        {airing.stationName} {airingDayRange(airing)}
      </Text>
      <StateBadge airing={airing} />
      {airing.showName && <Text size="sm">当たっている番組: {airing.showName}</Text>}
      {airing.performer && <Text size="sm">出演: {airing.performer}</Text>}
      {airing.descriptionParts.length > 0 && (
        <Text size="sm" style={{ whiteSpace: "pre-wrap" }} c="dimmed">
          {airing.descriptionParts.map((p, i) =>
            p.href ? (
              <Anchor key={i} href={p.href} target="_blank" rel="noopener noreferrer">
                {p.text}
              </Anchor>
            ) : (
              <span key={i}>{p.text}</span>
            ),
          )}
        </Text>
      )}
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}

      {skippedJob !== null ? (
        <Stack gap="xs">
          <Text size="sm" c="dimmed">
            {airing.skipBy === "person"
              ? `人が録らないと決めたので、${isSkipPlanned ? "録らない予定にした" : "録らなかった"}。`
              : `番組表の番組名「${airing.title}」が番組「${airing.showName}」の名前で始まらないので、${isSkipPlanned ? "録らない予定にした（放送の後、録りにいく時刻にもう 1 度番組表で確かめる）" : "録らなかった"}。`}
            録るなら「録る」を押す（ジョブの画面の「録る」と同じ）。
          </Text>
          <Button onClick={() => retry(skippedJob)} loading={busy === "retry"} style={{ alignSelf: "flex-start" }}>
            録る
          </Button>
        </Stack>
      ) : planned ? (
        <>
          <PlannedActions
            airing={airing}
            match={match}
            busy={busy === "stop"}
            onStop={stop}
            onOpenShow={airing.showName ? () => onOpenShow(airing.showName!) : undefined}
          />
          {skippableJob !== null && (
            <Button
              variant="default"
              onClick={() => skip(skippableJob)}
              loading={busy === "skip"}
              disabled={busy !== null && busy !== "skip"}
              style={{ alignSelf: "flex-start" }}
            >
              録らない
            </Button>
          )}
        </>
      ) : showsError ? (
        <Alert color="red" role="alert">
          {showsError}
        </Alert>
      ) : (
        <>
          {airing.notAvailable && !expiredLive && (
            <Text size="sm" c="dimmed">
              タイムフリーの対象外なので、放送中にライブで録る（放送の時刻に rajikura が動いている必要がある）。
            </Text>
          )}
          {expiredLive && (
            <Text size="sm" c="dimmed">
              タイムフリーの対象外で、録音期限（放送の終了の 5 分後）を過ぎたので、この回はもう録れない。
            </Text>
          )}
          {selection?.error && (
            <Text size="sm" c="yellow.8">
              {selection.error}
            </Text>
          )}
          {selection && selection.rows.length >= 2 && (
            <Stack gap={4} role="group" aria-label="選んだ放送回">
              <Text size="sm" fw={500}>
                パート {targets.length} 件{selection.auto ? "（自動で選びました）" : ""}
              </Text>
              {selection.rows.map((a) => (
                <Checkbox
                  key={`${a.stationId}|${a.start}`}
                  label={`${broadcastRange(a)} ${a.title}`}
                  checked={selection.picked.includes(a.start)}
                  onChange={() => onToggle(a)}
                />
              ))}
              {selection.auto && (
                <Button variant="subtle" size="xs" onClick={onOnly} style={{ alignSelf: "flex-start" }}>
                  この回だけにする
                </Button>
              )}
            </Stack>
          )}
          {onMoreSelect && selection && selection.rows.length === 1 && (
            <Button variant="default" size="xs" onClick={onMoreSelect} style={{ alignSelf: "flex-start" }}>
              ほかの回も選ぶ
            </Button>
          )}
          <Group>
            {!expiredTarget && (
              <Button onClick={() => run("once")} loading={busy === "once" || waiting} disabled={busy !== null}>
                この回を録音
              </Button>
            )}
            <Button variant="light" onClick={() => run("weekly")} loading={busy === "weekly" || waiting} disabled={busy !== null}>
              毎週録音
            </Button>
          </Group>
          {joinOffered && dest && (
            <Checkbox
              label="パートを 1 つのファイルにつなぐ"
              description={
                dest.kind === "existing"
                  ? `オンなら、番組「${dest.show.name}」の分割番組の設定を「つなぐ」に変える。オフなら、パートごとに -1・-2 のファイルにする`
                  : "オフなら、パートごとに -1・-2 のファイルにする"
              }
              checked={join}
              onChange={(e) => setJoin(e.currentTarget.checked)}
            />
          )}
          <Button
            variant="subtle"
            size="xs"
            onClick={() => setSettingsOpen((o) => !o)}
            aria-expanded={settingsOpen}
            style={{ alignSelf: "flex-start" }}
          >
            <span aria-hidden>{settingsOpen ? "▾" : "▸"}&nbsp;</span>
            設定を変えて予約
          </Button>
          {settingsOpen &&
            (shows === null ? (
              <Loader />
            ) : (
              <Stack gap="xs" aria-label="設定を変えて予約">
                {notice && (
                  <Alert color="yellow" role="status">
                    {notice}
                  </Alert>
                )}
                <Select
                  label="どの番組に足すか"
                  data={[
                    ...shows.map((s) => ({ value: s.name, label: s.name })),
                    { value: NEW_SHOW, label: "新しい番組を作る" },
                  ]}
                  value={chosenValue}
                  onChange={(v) => {
                    if (!v) return;
                    setDestValue(v);
                    setNotice(null);
                  }}
                  allowDeselect={false}
                  searchable
                />
                {chosenShow ? (
                  <ExistingShowInfo show={chosenShow} stations={stations} />
                ) : (
                  <>
                    <TextInput
                      label="番組の名前（保存先のフォルダ名になる）"
                      description={
                        targets.length >= 2
                          ? "選んだ放送回の番組名の共通の頭。直してよい"
                          : "番組表の番組名から末尾の印を落としたもの。直してよい"
                      }
                      value={newName}
                      onChange={(e) => setNameEdit(e.currentTarget.value)}
                    />
                  </>
                )}
              </Stack>
            ))}
        </>
      )}
    </Stack>
  );
}

/** 番組の分割番組の設定の表示。 */
const splitLabel = { none: "分割番組でない", join: "分割番組・つなぐ", separate: "分割番組・つながない" } as const;

/** 既存の番組を選んだときに、その番組の分割番組の設定と予約を表示する（変えない）。 */
function ExistingShowInfo({ show, stations }: { show: Show; stations: { id: string; name: string }[] }) {
  return (
    <Stack gap={4}>
      <Text size="sm" c="dimmed">
        分割番組の設定: {splitLabel[show.split] ?? show.split}
      </Text>
      <Text size="sm" c="dimmed">
        予約: {show.weekly.length === 0 ? "無い" : weeklySummaries(show.weekly, stations).join("、")}
      </Text>
      {!show.enabled && (
        <Alert color="yellow" role="status">
          この番組は無効なので、足しても録音されない（番組を有効にすると録音される）。
        </Alert>
      )}
    </Stack>
  );
}

/** 録音予定の放送回の操作。1 回の予約で当たっていれば「この回の録音をやめる」、毎週の予約で当たっていれば「番組の設定を開く」。 */
function PlannedActions({
  airing,
  match,
  busy,
  onStop,
  onOpenShow,
}: {
  airing: GuideAiring;
  match: Match;
  busy: boolean;
  onStop: (o: Once) => void;
  /** 当たっている番組が分からないときは undefined（「番組の設定を開く」を出さない）。 */
  onOpenShow?: () => void;
}) {
  if (match.status === "loading") return <Loader size="sm" />;
  if (match.status === "error") {
    return (
      <Stack gap="xs">
        <Alert color="red" role="alert">
          {match.error}
        </Alert>
        {onOpenShow && (
          <Button variant="default" onClick={onOpenShow} style={{ alignSelf: "flex-start" }}>
            番組の設定を開く
          </Button>
        )}
      </Stack>
    );
  }
  if (match.once) {
    const o = match.once;
    return (
      <Group>
        <Button color="red" variant="light" onClick={() => onStop(o)} loading={busy}>
          この回の録音をやめる
        </Button>
      </Group>
    );
  }
  return (
    <Stack gap="xs">
      <Button variant="default" onClick={onOpenShow} style={{ alignSelf: "flex-start" }}>
        番組の設定を開く
      </Button>
      <Text size="sm" c="dimmed">
        番組「{airing.showName}」の予約で録音する回。
      </Text>
    </Stack>
  );
}
