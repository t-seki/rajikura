import {
  Alert,
  Badge,
  Box,
  Button,
  Checkbox,
  Group,
  Loader,
  Paper,
  Popover,
  SegmentedControl,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
  UnstyledButton,
} from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { AiringPanel, PANEL_WIDTH, StateBadge, type PanelSelection } from "./AiringPanel";
import { getGuide, searchGuide, type GuideAiring, type GuideStation, type SearchResult } from "./api";
import {
  addDays,
  broadcastClock,
  broadcastDayOf,
  broadcastRange,
  dayLabel,
  dayStart,
  notAvailableLabel,
  notAvailableMark,
  stateColor,
  stateMark,
  stateLabel,
} from "./format";
import { matchRanges } from "./highlight";
import { autoGroup, type Outcome } from "./reserve";

const message = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/** 表示する放送日の範囲（今日の 7 日前から 7 日先まで）。 */
const PAST_DAYS = 7;
const FUTURE_DAYS = 7;

const PX_PER_MIN = 1.6;
const HOURS = 24;
const GRID_HEIGHT = HOURS * 60 * PX_PER_MIN;
/** この距離（px）以上動いてからをドラッグとする。 */
const DRAG_THRESHOLD = 5;
const ROW_HEIGHT = 40;
const STATION_KEY = "rajikura.guide.station";

function rememberedStation(): string | null {
  try {
    return window.localStorage.getItem(STATION_KEY);
  } catch {
    return null;
  }
}

function remember(id: string) {
  try {
    window.localStorage.setItem(STATION_KEY, id);
  } catch {
    // 覚えられなくても動く
  }
}

const VIEW_KEY = "rajikura.guide.view";

/** PC の番組表の表示。"all" は全局のグリッド、"station" は 1 局の表。読めない・壊れているときは全局。 */
type View = "all" | "station";

function rememberedView(): View {
  try {
    return window.localStorage.getItem(VIEW_KEY) === "station" ? "station" : "all";
  } catch {
    return "all";
  }
}

function rememberView(v: View) {
  try {
    window.localStorage.setItem(VIEW_KEY, v);
  } catch {
    // 覚えられなくても動く
  }
}

const HIDDEN_KEY = "rajikura.guide.hiddenStations";

/** 隠した局の ID。読めない・壊れているときは空（隠さない）。 */
function rememberedHidden(): string[] {
  try {
    const v: unknown = JSON.parse(window.localStorage.getItem(HIDDEN_KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

function rememberHidden(ids: string[]) {
  try {
    window.localStorage.setItem(HIDDEN_KEY, JSON.stringify(ids));
  } catch {
    // 覚えられなくても動く
  }
}

/** 放送回の読み上げの名前。時刻は放送日の時刻。onAir なら放送中と添える（色だけに頼らない）。 */
const airingLabel = (a: GuideAiring, onAir = false) =>
  `${a.title} ${broadcastRange(a)}${onAir ? ` ${ON_AIR_LABEL}` : ""} ${stateLabel[a.state]}${a.notAvailable ? ` ${notAvailableLabel}` : ""}`;

const ON_AIR_LABEL = "放送中";
/** 今の時刻の線・札と放送中の印の色。選択の青、録音予定の塗りとまぎれない赤。 */
const NOW_COLOR = "var(--mantine-color-red-6)";
/** 今の時刻を読み直す間隔。 */
const NOW_REFRESH_MS = 60 * 1000;

/** 開始 ≤ 今 < 終了 の回を放送中とする。 */
const isOnAir = (a: Pick<GuideAiring, "start" | "end">, now: number) =>
  new Date(a.start).getTime() <= now && now < new Date(a.end).getTime();

/** 今の時刻（ミリ秒）。1 分ほどごとと、タブが表に戻ったときに読み直す。 */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const tick = () => setNow(Date.now());
    const id = setInterval(tick, NOW_REFRESH_MS);
    const onVisible = () => {
      if (document.visibilityState === "visible") tick();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);
  return now;
}

/** 番組名を 1 行で省略するスタイル。 */
const ONE_LINE: CSSProperties = { whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" };

/** 枠の塗り分け。予約なしは淡く、録音予定・録音済みは色で塗り、録音できなかった・録らない予定・録らなかったは破線の枠。 */
function cellStyle(state: GuideAiring["state"], color: string): CSSProperties {
  switch (state) {
    case "none":
      return { border: "1px solid var(--mantine-color-default-border)", background: "transparent", opacity: 0.75 };
    case "missed":
    case "skip_planned":
    case "skipped":
      return { border: `2px dashed var(--mantine-color-${color}-6)`, background: "transparent" };
    default:
      return {
        border: `1px solid var(--mantine-color-${color}-6)`,
        background: `var(--mantine-color-${color}-light)`,
      };
  }
}

/** 押したときの修飾キー。toggle は Ctrl（Mac は ⌘）、range は Shift。 */
type Mods = { toggle: boolean; range: boolean };

const modsOf = (e: { ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }): Mods => ({
  toggle: e.ctrlKey || e.metaKey,
  range: e.shiftKey,
});

/** 放送回を見分ける鍵（局と開始）。 */
const keyOf = (a: Pick<GuideAiring, "stationId" | "start">) => `${a.stationId}|${a.start}`;

/** 選んだ放送回の枠（番組表の枠・行に付ける）。 */
const SELECTED_OUTLINE: CSSProperties = { outline: "3px solid var(--mantine-color-blue-filled)", outlineOffset: -2 };

function Grid({
  day,
  now,
  stations,
  selected,
  onOpen,
  onHide,
  onPick,
}: {
  day: string;
  /** 今の時刻（ミリ秒）。開いている放送日が今日のときだけ、今の時刻の線と札を出す。 */
  now: number;
  stations: GuideStation[];
  /** 選んだ放送回の鍵（keyOf）。 */
  selected: Set<string>;
  onOpen: (a: GuideAiring, mods: Mods) => void;
  onHide: (id: string) => void;
  onPick: (id: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const [grabbing, setGrabbing] = useState(false);
  // ドラッグで送った直後の click（離した場所の枠に飛ぶ）を 1 回だけ捨てる
  const dragged = useRef(false);
  // ドラッグ中なら、それを終える関数
  const stopDrag = useRef<(() => void) | null>(null);
  const start = dayStart(day).getTime();
  const end = start + HOURS * 3600 * 1000;

  // 今日を開いたら、今の時刻まで送る。放送日を開いたとき（日を変えると番組表を読み直してこの部品を作り直す）だけ送り、
  // 局を隠す・戻す、詳細を開く、検索欄に打つなどの描画し直しでは送り直さない（stations を依存に入れない）
  useEffect(() => {
    if (!scroller.current || broadcastDayOf(new Date()) !== day) return;
    const mins = (Date.now() - start) / 60000;
    scroller.current.scrollTop = Math.max(0, mins * PX_PER_MIN - 40);
  }, [day, start]);

  // マウスのドラッグで上下左右に送る。DRAG_THRESHOLD px 以上動くまではドラッグにせず、押しただけなら枠の click が効く
  function onMouseDown(e: ReactMouseEvent) {
    const el = scroller.current;
    if (!el || e.button !== 0) return;
    // スクロールバーの上の押下はブラウザに任せる（つまみの操作と scrollLeft/scrollTop の書き換えがぶつかるため）。
    // レイアウトの無い環境（clientWidth が 0）では見分けられないので、判定しない
    if (el.clientWidth > 0) {
      const box = el.getBoundingClientRect();
      if (e.clientX - box.left >= el.clientWidth || e.clientY - box.top >= el.clientHeight) return;
    }
    stopDrag.current?.();
    const x0 = e.clientX;
    const y0 = e.clientY;
    const left0 = el.scrollLeft;
    const top0 = el.scrollTop;
    let moving = false;
    const move = (m: MouseEvent) => {
      // mouseup を取りこぼした（ウィンドウの外で離した、コンテキストメニューが出たなど）ら、ここで終える
      if (m.buttons === 0) {
        up();
        return;
      }
      const dx = m.clientX - x0;
      const dy = m.clientY - y0;
      if (!moving) {
        if (Math.hypot(dx, dy) < DRAG_THRESHOLD) return;
        moving = true;
        setGrabbing(true);
      }
      el.scrollLeft = left0 - dx;
      el.scrollTop = top0 - dy;
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      stopDrag.current = null;
      if (!moving) return;
      setGrabbing(false);
      dragged.current = true;
      // 枠の外で離すと click が来ないので、次の描画のあとには捨てる
      setTimeout(() => (dragged.current = false), 0);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
    stopDrag.current = up;
  }

  // 部品が外れるときは、ドラッグ中のリスナーを残さない
  useEffect(() => () => stopDrag.current?.(), []);

  return (
    <Box
      ref={scroller}
      data-testid="guide-grid"
      onMouseDown={onMouseDown}
      onClickCapture={(e: ReactMouseEvent) => {
        if (!dragged.current) return;
        dragged.current = false;
        e.preventDefault();
        e.stopPropagation();
      }}
      style={{
        overflow: "auto",
        maxHeight: "72dvh",
        cursor: grabbing ? "grabbing" : "grab",
        userSelect: "none",
      }}
    >
      <div style={{ display: "flex", minWidth: "max-content", position: "relative" }}>
        {broadcastDayOf(now) === day && (
          // 放送回の枠の上に重ねる。枠のクリックとドラッグを素通りさせ、局の見出しと時刻の列の上端（zIndex 2）の下に隠す
          <div
            data-testid="guide-now-line"
            aria-hidden
            style={{
              position: "absolute",
              left: 0,
              right: 0,
              top: 32 + ((now - start) / 60000) * PX_PER_MIN - 1,
              height: 2,
              background: NOW_COLOR,
              pointerEvents: "none",
              zIndex: 1,
            }}
          >
            <span
              data-testid="guide-now-label"
              style={{
                position: "absolute",
                left: 2,
                top: -8,
                width: 44,
                textAlign: "center",
                fontSize: 11,
                lineHeight: "18px",
                color: "white",
                background: NOW_COLOR,
                borderRadius: 3,
              }}
            >
              {broadcastClock(new Date(now), day)}
            </span>
          </div>
        )}
        <div style={{ width: 48, flex: "none" }}>
          <div style={{ height: 32, position: "sticky", top: 0, background: "var(--mantine-color-body)", zIndex: 2 }} />
          <div style={{ position: "relative", height: GRID_HEIGHT }}>
            {Array.from({ length: HOURS }, (_, i) => (
              <Text key={i} size="xs" c="dimmed" style={{ position: "absolute", top: i * 60 * PX_PER_MIN, right: 4 }}>
                {5 + i}時
              </Text>
            ))}
          </div>
        </div>
        {stations.map((st) => (
          <div key={st.id} style={{ width: 150, flex: "none", borderLeft: "1px solid var(--mantine-color-default-border)" }}>
            <Group
              gap={2}
              justify="center"
              wrap="nowrap"
              style={{ height: 32, position: "sticky", top: 0, background: "var(--mantine-color-body)", zIndex: 2 }}
            >
              <UnstyledButton
                onClick={() => onPick(st.id)}
                aria-label={`${st.name}だけを表示`}
                title={`${st.name}だけを表示`}
                fw={500}
                fz="sm"
              >
                {st.name}
              </UnstyledButton>
              <UnstyledButton
                onClick={() => onHide(st.id)}
                aria-label={`${st.name}を隠す`}
                title={`${st.name}を隠す`}
                c="dimmed"
                px={4}
              >
                ×
              </UnstyledButton>
            </Group>
            <div style={{ position: "relative", height: GRID_HEIGHT }}>
              {st.airings.map((a) => {
                const from = Math.max(new Date(a.start).getTime(), start);
                const to = Math.min(new Date(a.end).getTime(), end);
                if (to <= from) return null;
                const color = stateColor[a.state];
                const isSelected = selected.has(keyOf(a));
                return (
                  <UnstyledButton
                    key={a.start}
                    onClick={(e: ReactMouseEvent) => onOpen(a, modsOf(e))}
                    aria-label={`${st.name} ${airingLabel(a)}`}
                    aria-pressed={isSelected || undefined}
                    data-selected={isSelected || undefined}
                    title={airingLabel(a)}
                    style={{
                      position: "absolute",
                      left: 2,
                      right: 2,
                      top: ((from - start) / 60000) * PX_PER_MIN,
                      height: ((to - from) / 60000) * PX_PER_MIN - 2,
                      overflow: "hidden",
                      padding: "0 4px",
                      borderRadius: 4,
                      ...cellStyle(a.state, color),
                      ...(isSelected ? SELECTED_OUTLINE : {}),
                    }}
                  >
                    <Text size="xs" style={ONE_LINE} fw={a.state === "none" || a.state === "missed" || a.state === "skip_planned" || a.state === "skipped" ? 400 : 600} lh="14px">
                      {(stateMark[a.state] || a.notAvailable) && (
                        <span aria-hidden>
                          {stateMark[a.state]}
                          {a.notAvailable && notAvailableMark}{" "}
                        </span>
                      )}
                      {broadcastClock(a.start)} {a.title}
                    </Text>
                  </UnstyledButton>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </Box>
  );
}

/** 1 局の表示。放送回を時刻順に同じ高さの行で並べた表で、行を押すと詳細を開く。 */
function StationTable({
  day,
  now,
  station,
  selected,
  onOpen,
}: {
  day: string;
  now: number;
  station: GuideStation;
  selected: Set<string>;
  onOpen: (a: GuideAiring, mods: Mods) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);

  // 今日を開いたら、今放送中（無ければ次）の放送回の行を上の方に出す。放送日を開いたときだけ送り、
  // 局を選ぶ・詳細を開くなどの描画し直しでは送り直さない（station を依存に入れない）
  useEffect(() => {
    const box = scroller.current;
    if (!box || broadcastDayOf(new Date()) !== day) return;
    const now = Date.now();
    const i = station.airings.findIndex((a) => new Date(a.end).getTime() > now);
    const row = i < 0 ? undefined : box.querySelectorAll("tbody tr")[i];
    if (!row) return;
    const top = row.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop;
    box.scrollTop = Math.max(0, top - ROW_HEIGHT);
  }, [day]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Box ref={scroller} data-testid="guide-station-table" style={{ overflow: "auto", maxHeight: "72dvh" }}>
      <Table stickyHeader layout="fixed" style={{ minWidth: 640 }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th w={130}>時刻</Table.Th>
            <Table.Th>番組名</Table.Th>
            <Table.Th w={220}>出演者</Table.Th>
            <Table.Th w={230}>状態</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {station.airings.map((a) => {
            const isSelected = selected.has(keyOf(a));
            const onAir = broadcastDayOf(now) === day && isOnAir(a, now);
            return (
            <Table.Tr
              key={a.start}
              onClick={(e: ReactMouseEvent) => onOpen(a, modsOf(e))}
              // Shift+Click で文字を選ばない
              onMouseDown={(e: ReactMouseEvent) => e.shiftKey && e.preventDefault()}
              data-selected={isSelected || undefined}
              style={{
                cursor: "pointer",
                height: ROW_HEIGHT,
                ...(isSelected ? { ...SELECTED_OUTLINE, background: "var(--mantine-color-blue-light)" } : {}),
              }}
            >
              <Table.Td style={onAir ? { boxShadow: `inset 4px 0 0 ${NOW_COLOR}` } : undefined}>
                {onAir && (
                  <Text span c="red.6" fw={600} mr={6}>
                    {ON_AIR_LABEL}
                  </Text>
                )}
                {broadcastRange(a)}
              </Table.Td>
              <Table.Td style={ONE_LINE}>
                <UnstyledButton
                  aria-label={airingLabel(a, onAir)}
                  aria-pressed={isSelected || undefined}
                  title={a.title}
                  fz="sm"
                  style={{ maxWidth: "100%", ...ONE_LINE }}
                >
                  {a.title}
                </UnstyledButton>
              </Table.Td>
              <Table.Td style={ONE_LINE} title={a.performer || undefined}>
                {a.performer}
              </Table.Td>
              <Table.Td>
                <StateBadge airing={a} />
              </Table.Td>
            </Table.Tr>
            );
          })}
        </Table.Tbody>
      </Table>
    </Box>
  );
}

/** text のうち query に当たった部分を <mark> で囲む。 */
function Highlight({ text, query }: { text: string; query?: string }) {
  const ranges = query ? matchRanges(text, query) : [];
  if (ranges.length === 0) return <>{text}</>;
  const parts: ReactNode[] = [];
  let at = 0;
  ranges.forEach(([s, e], i) => {
    if (s > at) parts.push(text.slice(at, s));
    parts.push(<mark key={i}>{text.slice(s, e)}</mark>);
    at = e;
  });
  if (at < text.length) parts.push(text.slice(at));
  return <>{parts}</>;
}

/** withDate は検索の結果の行（時刻・局・出演者を出し、query に当たった部分を強調する。日付は見出しが出す）。 */
function AiringRow({
  a,
  withDate,
  query,
  selected = false,
  onAir = false,
  onOpen,
}: {
  a: GuideAiring;
  withDate?: boolean;
  query?: string;
  selected?: boolean;
  /** 放送中の回。左の赤い帯と「放送中」の字を付ける。 */
  onAir?: boolean;
  onOpen: (a: GuideAiring, mods: Mods) => void;
}) {
  return (
    <UnstyledButton
      onClick={(e: ReactMouseEvent) => onOpen(a, modsOf(e))}
      aria-label={withDate ? `${a.stationName} ${dayLabel(broadcastDayOf(a.start))} ${airingLabel(a)}` : airingLabel(a, onAir)}
      aria-pressed={selected || undefined}
      data-selected={selected || undefined}
      style={{
        borderBottom: "1px solid var(--mantine-color-default-border)",
        padding: onAir ? "8px 4px 8px 10px" : "8px 4px",
        textAlign: "left",
        ...(onAir ? { boxShadow: `inset 4px 0 0 ${NOW_COLOR}` } : {}),
        ...(selected ? { ...SELECTED_OUTLINE, background: "var(--mantine-color-blue-light)" } : {}),
      }}
    >
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <Stack gap={0} style={{ minWidth: 0 }}>
          <Text size="xs" c="dimmed">
            {onAir && (
              <Text span size="xs" c="red.6" fw={600} mr={6}>
                {ON_AIR_LABEL}
              </Text>
            )}
            {withDate ? `${broadcastRange(a)} ${a.stationName}` : broadcastRange(a)}
          </Text>
          <Text size="sm">{withDate ? <Highlight text={a.title} query={query} /> : a.title}</Text>
          {withDate && a.performer && (
            <Text size="xs" c="dimmed" style={ONE_LINE}>
              <Highlight text={a.performer} query={query} />
            </Text>
          )}
        </Stack>
        <StateBadge airing={a} />
      </Group>
    </UnstyledButton>
  );
}

/** 検索の結果を、放送回の放送日（5 時区切り）ごとの見出しで区切る（結果は開始時刻の順。行の時刻は放送日の時刻）。 */
function SearchResults({
  airings,
  query,
  onOpen,
}: {
  airings: GuideAiring[];
  query: string;
  onOpen: (a: GuideAiring, mods: Mods) => void;
}) {
  const groups: { date: string; items: GuideAiring[] }[] = [];
  for (const a of airings) {
    const date = broadcastDayOf(a.start);
    const last = groups[groups.length - 1];
    if (last && last.date === date) last.items.push(a);
    else groups.push({ date, items: [a] });
  }
  return (
    <Stack gap="xs">
      {groups.map((g) => (
        <Stack key={g.date} gap={0} role="group" aria-label={dayLabel(g.date)}>
          <Title order={4} py={4}>
            {dayLabel(g.date)}
          </Title>
          {g.items.map((a) => (
            <AiringRow key={`${a.stationId}${a.start}`} a={a} withDate query={query} onOpen={onOpen} />
          ))}
        </Stack>
      ))}
    </Stack>
  );
}

function List({
  day,
  now,
  station,
  selected,
  onOpen,
}: {
  day: string;
  now: number;
  station: GuideStation;
  selected: Set<string>;
  onOpen: (a: GuideAiring, mods: Mods) => void;
}) {
  return (
    <Stack gap={0} data-testid="guide-list">
      {station.airings.map((a) => (
        <AiringRow
          key={a.start}
          a={a}
          selected={selected.has(keyOf(a))}
          onAir={broadcastDayOf(now) === day && isOnAir(a, now)}
          onOpen={onOpen}
        />
      ))}
    </Stack>
  );
}

/**
 * 予約した後の帯。undo があれば「取り消す」を出し、showName があれば「番組を開く」を出す。
 * id は帯ごとに振る番号。取り消しの結果は、取り消しを始めた帯がまだ出ているときだけ反映する。
 */
type Band = { id: number; text: string; showName?: string; undo?: Outcome["undo"]; error?: string };

const sameAiring = (a: GuideAiring, b: GuideAiring) => a.stationId === b.stationId && a.start === b.start;

/** 選べる放送回（予約なし）。予約済みの放送回（どの番組が当てていても）は選べない。 */
const selectable = (a: GuideAiring) => a.state === "none";

/** 予約済みの放送回を選ぼうとしたときの知らせ。 */
const REFUSED_NOTICE = "予約済みの回は選べない";

/**
 * 番組表の選択。同じ局・同じ放送日の予約なしの放送回だけ。anchor は Shift+Click の起点、fromSearch は検索の結果から開いた
 * （詳細を閉じたら選択も捨てる）。rows・picked・auto・pending・error は詳細に渡す形（PanelSelection）。
 */
type Selection = PanelSelection & { stationId: string; day: string; anchor: string; fromSearch: boolean };

/**
 * URL のクエリ `airing` の値。放送回を局と開始時刻（日本時間の YYYYMMDDHHmmss）で指す（例: `TBS_20261023140000`。ADR 0024）。
 */
export function airingParam(a: Pick<GuideAiring, "stationId" | "start">): string {
  const jst = new Date(new Date(a.start).getTime() + 9 * 3600 * 1000);
  return `${a.stationId}_${jst.toISOString().slice(0, 19).replace(/[-T:]/g, "")}`;
}

/** `airing` の値の開始時刻。形が違えば null。 */
function airingStart(param: string): Date | null {
  const m = /^.+_(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/.exec(param);
  if (!m) return null;
  const t = new Date(`${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}+09:00`);
  return Number.isNaN(t.getTime()) ? null : t;
}

/** 番組表が URL のクエリに載せる状態（ブラウザに覚えていないもの）。day は表示する放送日の範囲の中だけ、それ以外は null。 */
type GuideQuery = { day: string | null; q: string; airing: string | null };

function parseGuideQuery(search: string, days: string[]): GuideQuery {
  const p = new URLSearchParams(search);
  const day = p.get("day");
  return { day: day && days.includes(day) ? day : null, q: (p.get("q") ?? "").trim(), airing: p.get("airing") || null };
}

/** 番組表の URL のクエリ（`?` から。何も無ければ空）。 */
function guideQuery({ day, q, airing }: GuideQuery): string {
  const p = new URLSearchParams();
  if (day) p.set("day", day);
  if (q) p.set("q", q);
  if (airing) p.set("airing", airing);
  const out = p.toString();
  return out ? `?${out}` : "";
}

/** 開始の順に並べ、同じ放送回を重ねない。 */
const sortedUnique = (as: GuideAiring[]) =>
  as.filter((a, i) => as.findIndex((b) => sameAiring(a, b)) === i).sort((a, b) => a.start.localeCompare(b.start));

/**
 * 番組表の画面。放送日ごとのグリッド（PC）・リスト（スマホ）、キーワード検索、放送回の詳細と番組。
 * 予約なしの放送回を普通に押すと、その放送回の自動の組（autoGroup。予約済みの放送回は除く）を選んで詳細を開く。
 * グリッド・1 局の表・リストでは、Ctrl+Click（Mac は ⌘+Click）で選択に足す・外し、Shift+Click で前に選んだ放送回からの範囲を
 * 足す（予約済みの放送回は飛ばす）。違う局・放送日の放送回ならやり直す。選択が 2 件以上で詳細を閉じているときは、画面の下に
 * 選択の帯を出す。タッチの操作では、1 件の詳細の「ほかの回も選ぶ」で、押すたびに足す・外す状態に入る。検索の結果は普通に
 * 押すだけ（修飾キーは見ない）。
 * 予約・やめた後は番組表の上の帯で知らせ、帯が出ている間だけ「取り消す」で戻せる。
 * onOpenShow は「番組を開く」「番組の設定を開く」で、番組の画面でその番組を開く。
 * onCreateKeyword は検索の結果の「このキーワードで録音」で、検索の語を入れたキーワードの作るダイアログを開く。
 * onNavigate があれば、放送日・検索の語・詳細を開いた放送回を URL のクエリ（search。`?day=`・`?q=`・`?airing=`）と揃える（ADR 0024）:
 * 開いたときと、「戻る」などで search が変わったときはクエリの状態にし、状態を変えたら onNavigate でクエリを書き換える
 * （詳細を開くのは push、閉じるのは close、放送日と検索の語は replace）。今日の放送日はクエリに載せない。範囲の外の放送日は
 * 今日にし、クエリを直す。指す放送回が無ければ、詳細を開かない。
 */
export function GuidePage({
  search: urlSearch = "",
  onNavigate,
  onOpenShow = () => {},
  onCreateKeyword = () => {},
}: {
  search?: string;
  onNavigate?: (search: string, how: "push" | "replace" | "close") => void;
  onOpenShow?: (name: string) => void;
  onCreateKeyword?: (words: string) => void;
} = {}) {
  const today = useMemo(() => broadcastDayOf(new Date()), []);
  const days = useMemo(() => Array.from({ length: PAST_DAYS + FUTURE_DAYS + 1 }, (_, i) => addDays(today, i - PAST_DAYS)), [today]);
  // 開いたときの URL のクエリ（onNavigate が無ければ使わない）
  const [initial] = useState(() => parseGuideQuery(onNavigate ? urlSearch : "", days));
  const wide = useMediaQuery("(min-width: 48em)", false, { getInitialValueInEffect: false });
  // タッチの操作（修飾キーが無い）。「ほかの回も選ぶ」を出す
  const touch = useMediaQuery("(pointer: coarse)", false, { getInitialValueInEffect: false });

  const now = useNow();
  const [day, setDay] = useState(initial.day ?? today);
  const [stations, setStations] = useState<GuideStation[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stationId, setStationId] = useState<string | null>(rememberedStation);
  const [view, setView] = useState<View>(rememberedView);
  const [hidden, setHidden] = useState<string[]>(rememberedHidden);
  // 「隠した局も表示」。覚えない
  const [showHidden, setShowHidden] = useState(false);
  const [query, setQuery] = useState(initial.q);
  const [search, setSearch] = useState<SearchResult | null>(null);
  const [searching, setSearching] = useState(initial.q !== "");
  // 流している（流した）検索の語。URL の `q` に載せる。「検索をやめる」で空にする
  const [activeQ, setActiveQ] = useState(initial.q);
  const [opened, setOpened] = useState<GuideAiring | null>(null);
  // URL の `airing` が指す、まだ開いていない放送回（番組表か検索の結果を読んでから開く）
  const [pendingAiring, setPendingAiring] = useState<string | null>(initial.airing);
  const [sel, setSel] = useState<Selection | null>(null);
  // タッチの「ほかの回も選ぶ」の後。押すたびに選択に足す・外す
  const [touchSelect, setTouchSelect] = useState(false);
  const [selectNotice, setSelectNotice] = useState<string | null>(null);
  // 「予約済みの回は選べない」は初めの 1 度だけ出す
  const noticed = useRef(false);
  // 自動の組を調べた結果を、その後に選択を変えていたら捨てる
  const groupToken = useRef(0);
  const [band, setBand] = useState<Band | null>(null);
  // 取り消しの応答を待っている帯の id
  const [undoing, setUndoing] = useState<number | null>(null);
  const bandSeq = useRef(0);
  const token = useRef(0);

  const loadDay = useCallback(async (d: string) => {
    const my = ++token.current;
    setError(null);
    try {
      const got = await getGuide(d);
      if (my === token.current) setStations(got.stations);
    } catch (e) {
      if (my === token.current) {
        setStations(null);
        setError(message(e, "番組表を読めない"));
      }
    }
  }, []);

  useEffect(() => {
    setStations(null);
    loadDay(day);
    // 放送日を変えたら、前の放送日の選択は捨てる
    groupToken.current++;
    setSel((cur) => (cur && !cur.fromSearch ? null : cur));
    setTouchSelect(false);
  }, [day, loadDay]);

  const runSearch = useCallback(async (q: string) => {
    setActiveQ(q);
    setSearching(true);
    setError(null);
    try {
      setSearch(await searchGuide(q));
    } catch (e) {
      setError(message(e, "番組表を検索できない"));
    } finally {
      setSearching(false);
    }
  }, []);

  // 詳細が分割番組のパートを探すための、その放送日の番組表。いま開いている日ならそのまま使う。
  const stationsRef = useRef<{ day: string; stations: GuideStation[] } | null>(null);
  useEffect(() => {
    stationsRef.current = stations ? { day, stations } : null;
  }, [day, stations]);
  const dayForParts = useCallback(async (d: string) => {
    const cur = stationsRef.current;
    if (cur && cur.day === d) return cur.stations;
    return (await getGuide(d)).stations;
  }, []);

  // 開いたときの URL に検索の語があれば、検索を流す
  useEffect(() => {
    if (initial.q) void runSearch(initial.q);
  }, [initial, runSearch]);

  // 予約した・取り消した後は、番組表（と検索の結果）の状態を取り直す
  const reload = () => Promise.all([loadDay(day), search ? runSearch(search.q) : Promise.resolve()]);

  /** 予約済みの放送回を選ぼうとした。初めの 1 度だけ知らせる。 */
  function refuse() {
    if (noticed.current) return;
    noticed.current = true;
    setSelectNotice(REFUSED_NOTICE);
  }

  function clearSelection() {
    groupToken.current++;
    setSel(null);
    setTouchSelect(false);
  }

  /** 普通に押した。予約なしなら自動の組を選んで詳細を開く。予約済みなら選択を捨てて、その放送回の詳細を開く。 */
  function openPlain(a: GuideAiring, fromSearch = false) {
    const my = ++groupToken.current;
    setPendingAiring(null);
    setOpened(a);
    if (!selectable(a)) {
      setSel(null);
      setTouchSelect(false);
      return;
    }
    const d = broadcastDayOf(a.start);
    setSel({ stationId: a.stationId, day: d, rows: [a], picked: [a.start], anchor: a.start, auto: false, pending: true, fromSearch });
    dayForParts(d).then(
      (st) => {
        if (my !== groupToken.current) return;
        const group = autoGroup(a, st.find((s) => s.id === a.stationId)?.airings ?? []).filter(
          (b) => sameAiring(a, b) || selectable(b),
        );
        setSel((cur) => cur && { ...cur, rows: group, picked: group.map((b) => b.start), auto: group.length >= 2, pending: false });
      },
      (e) => {
        if (my !== groupToken.current) return;
        // ほかのパートが分からなくても、この放送回だけは録音できる
        setSel((cur) => cur && { ...cur, pending: false, error: message(e, "ほかのパートを調べられなかった") });
      },
    );
  }

  /** 選択を next にする。詳細を開いていれば、詳細も同じ選択を指す（開いている放送回が並びに無ければ、押した放送回にする）。 */
  function applySelection(next: Selection, pressed: GuideAiring) {
    groupToken.current++;
    if (next.picked.length === 0 && !opened) {
      clearSelection();
      return;
    }
    setSel(next);
    if (opened && !next.rows.some((a) => sameAiring(a, opened))) setOpened(pressed);
  }

  /** Ctrl+Click（toggle）・Shift+Click（range）・タッチの選択の状態のタップ。 */
  function openWithMods(a: GuideAiring, mods: Mods) {
    if (!mods.toggle && !mods.range && !touchSelect) {
      openPlain(a);
      return;
    }
    if (!selectable(a)) {
      refuse();
      return;
    }
    const d = broadcastDayOf(a.start);
    const fresh: Selection = {
      stationId: a.stationId,
      day: d,
      rows: [a],
      picked: [a.start],
      anchor: a.start,
      auto: false,
      pending: false,
      fromSearch: false,
    };
    if (!sel || sel.stationId !== a.stationId || sel.day !== d || sel.fromSearch) {
      // 違う局・放送日なら、選択をその放送回からやり直す
      applySelection(fresh, a);
      return;
    }
    if (mods.range && !mods.toggle) {
      const list = stations?.find((s) => s.id === a.stationId)?.airings ?? [];
      const [from, to] = [sel.anchor, a.start].sort();
      const inRange = list.filter((b) => b.start >= from && b.start <= to);
      if (inRange.some((b) => !selectable(b))) refuse();
      const adding = inRange.filter(selectable);
      const picked = [...new Set([...sel.picked, ...adding.map((b) => b.start)])];
      applySelection({ ...sel, rows: sortedUnique([...sel.rows, ...adding]), picked, auto: false, pending: false }, a);
      return;
    }
    const has = sel.picked.includes(a.start);
    const picked = has ? sel.picked.filter((s) => s !== a.start) : [...sel.picked, a.start];
    // 詳細を閉じているときに外した回は、並びからも外す
    const rows = has && !opened ? sel.rows.filter((b) => !sameAiring(a, b)) : sortedUnique([...sel.rows, a]);
    applySelection({ ...sel, rows, picked, anchor: a.start, auto: false, pending: false }, a);
  }

  /** 詳細のチェックで、放送回 a を選択から外す・足し直す。 */
  function togglePicked(a: GuideAiring) {
    if (!sel) return;
    groupToken.current++;
    const picked = sel.picked.includes(a.start) ? sel.picked.filter((s) => s !== a.start) : [...sel.picked, a.start];
    setSel({ ...sel, picked, auto: false, pending: false });
  }

  /** 「この回だけにする」。開いている放送回だけを残す。 */
  function onlyOpened() {
    if (!sel || !opened) return;
    groupToken.current++;
    setSel({ ...sel, rows: [opened], picked: [opened.start], anchor: opened.start, auto: false, pending: false });
  }

  /** 詳細を閉じる。選択が 2 件以上（タッチの選択の状態なら 1 件以上）なら残し、外した回は並びから外す。 */
  function closeDetail() {
    setOpened(null);
    if (!sel) return;
    if (sel.fromSearch || sel.picked.length === 0 || (sel.picked.length < 2 && !touchSelect)) {
      clearSelection();
      return;
    }
    setSel({ ...sel, rows: sel.rows.filter((a) => sel.picked.includes(a.start)), auto: false });
  }

  async function done(o: Outcome) {
    // 予約した放送回は予約済みになるので、選択は捨てる
    clearSelection();
    // 次の操作をしたら、前の操作はもう取り消せない（帯を置き換える）
    setBand({ id: ++bandSeq.current, text: o.message, showName: o.showName, undo: o.undo });
    // スマホ幅は詳細が画面いっぱいなので閉じて帯を見せる。PC 幅は開いたまま次の放送回を選べる
    if (!wide) setOpened(null);
    await reload();
  }

  async function undo() {
    if (!band?.undo) return;
    const { id, undo: run } = band;
    // 応答を待つ間に帯を閉じた・次の操作をしたら、その後の帯を結果で上書きしない
    const onlySame = (next: (cur: Band) => Band | null) => setBand((cur) => (cur && cur.id === id ? next(cur) : cur));
    setUndoing(id);
    try {
      const r = await run();
      onlySame(() => ({ id: ++bandSeq.current, text: r.message, showName: r.showName }));
    } catch (e) {
      onlySame((cur) => ({ ...cur, error: message(e, "取り消せなかった") }));
    } finally {
      setUndoing((cur) => (cur === id ? null : cur));
    }
    await reload();
  }

  // 開いている放送回は、取り直した番組表（か検索の結果）のものに差し替えて、状態を新しくする
  const current =
    opened &&
    (stations?.find((s) => s.id === opened.stationId)?.airings.find((a) => sameAiring(a, opened)) ??
      search?.airings.find((a) => sameAiring(a, opened)) ??
      opened);

  // URL の `airing` の放送回を、番組表（検索の結果から開いたなら検索の結果）を読んでから開く。無ければ開かない。
  // その放送日の番組表を読めなかったときも開かない（開いている放送日の番組表なら、読めないエラーは画面に出たまま）。
  // どちらも `airing` を URL から外す。
  useEffect(() => {
    if (!pendingAiring) return;
    if (activeQ && searching) return;
    const fromSearch = search?.airings.find((a) => airingParam(a) === pendingAiring);
    if (fromSearch) {
      openPlain(fromSearch, true);
      return;
    }
    const start = airingStart(pendingAiring);
    if (!start) {
      setPendingAiring(null);
      return;
    }
    // 開いている放送日の番組表を読み終えてから探す（その日の放送回なら読み直さない）
    if (stations === null && !error) return;
    let live = true;
    dayForParts(broadcastDayOf(start)).then(
      (st) => {
        if (!live) return;
        const a = st.flatMap((s) => s.airings).find((b) => airingParam(b) === pendingAiring);
        if (a) openPlain(a);
        else setPendingAiring(null);
      },
      () => live && setPendingAiring(null),
    );
    return () => {
      live = false;
    };
    // openPlain は描くたびに作り直すので deps に入れない（開くときの描画のもので足りる）
  }, [pendingAiring, activeQ, searching, search, stations, error, dayForParts]);

  // 「戻る」「進む」などで URL のクエリが変わったら、その状態にする（状態から書いたクエリなら、もう同じなので何もしない）。
  // URL が変わったときだけ見る（状態の変化は、下の effect がクエリに書く）。
  useEffect(() => {
    if (!onNavigate) return;
    const u = parseGuideQuery(urlSearch, days);
    const d = u.day ?? today;
    if (d !== day) setDay(d);
    if (u.q !== activeQ) {
      if (u.q) {
        setQuery(u.q);
        void runSearch(u.q);
      } else {
        setSearch(null);
        setQuery("");
        setActiveQ("");
      }
    }
    const cur = opened ? airingParam(opened) : pendingAiring;
    if (u.airing !== cur) {
      if (opened) closeDetail();
      setPendingAiring(u.airing);
    }
  }, [urlSearch]);

  // 状態を変えたら、URL のクエリに書く。詳細を開くのは履歴に積み、閉じるのは戻る（か置き換える）。ほかは置き換える。
  // urlSearch の変化では書かない（上の effect が状態を URL に合わせる）。
  useEffect(() => {
    if (!onNavigate) return;
    const airing = opened ? airingParam(opened) : pendingAiring;
    const next = guideQuery({ day: day === today ? null : day, q: activeQ, airing });
    if (next === urlSearch) return;
    const was = new URLSearchParams(urlSearch).get("airing");
    onNavigate(next, !was && airing ? "push" : was && !airing ? "close" : "replace");
  }, [day, today, activeQ, opened, pendingAiring]);

  const selected = stations?.find((s) => s.id === stationId) ?? stations?.[0];
  // 選択の放送回は、取り直した番組表のものに差し替える（状態を新しくする）
  const freshOf = (a: GuideAiring) =>
    (sel && sel.day === day && stations?.find((s) => s.id === a.stationId)?.airings.find((b) => sameAiring(a, b))) || a;
  const panelSelection: PanelSelection | null = sel && { ...sel, rows: sel.rows.map(freshOf) };
  const selectedKeys = new Set(sel && sel.day === day ? sel.picked.map((start) => keyOf({ stationId: sel.stationId, start })) : []);
  const pickedAirings = sel ? sel.rows.filter((a) => sel.picked.includes(a.start)).map(freshOf) : [];
  // 選択の帯。詳細を閉じていて、2 件以上（タッチの選択の状態なら 1 件以上）を選んでいるとき
  const showSelectionBar = sel !== null && !opened && !sel.fromSearch && pickedAirings.length >= (touchSelect ? 1 : 2);
  const dayIndex = days.indexOf(day);

  const changeHidden = (next: string[]) => {
    const ids = [...new Set(next)]; // 「隠した局も表示」の間に隠し直しても重複を入れない
    setHidden(ids);
    rememberHidden(ids);
  };
  // 1 局だけを出す表示（1 局の表・スマホのリスト）で、選択の局と違う局を出すなら、選択を捨てる（見えていない局の放送回を
  // 帯から予約しないため。違う局の放送回を選んだときに選択をやり直すのと揃える）
  const dropSelectionOf = (shownId: string | undefined) => {
    if (sel&& !sel.fromSearch && shownId !== undefined && sel.stationId !== shownId) clearSelection();
  };
  const pickStation = (id: string) => {
    setStationId(id);
    remember(id);
    dropSelectionOf(id);
  };
  const changeView = (v: View) => {
    setView(v);
    rememberView(v);
    if (v === "station") dropSelectionOf(selected?.id);
  };
  const shown = stations && (showHidden ? stations : stations.filter((s) => !hidden.includes(s.id)));

  return (
    <Stack style={{ paddingRight: wide && opened ? PANEL_WIDTH : undefined }}>
      <Title order={2}>番組表</Title>
      {band && (
        <Alert
          color={band.error ? "red" : "green"}
          role="status"
          aria-label="知らせ"
          withCloseButton
          closeButtonLabel="知らせを閉じる"
          onClose={() => setBand(null)}
        >
          <Group justify="space-between" gap="xs">
            <Text size="sm">{band.text}</Text>
            <Group gap="xs">
              {band.showName && (
                <Button size="xs" variant="default" onClick={() => onOpenShow(band.showName!)}>
                  番組を開く
                </Button>
              )}
              {band.undo && (
                <Button size="xs" variant="light" onClick={undo} loading={undoing === band.id}>
                  取り消す
                </Button>
              )}
            </Group>
          </Group>
          {band.error && (
            <Text size="sm" c="red" mt={4}>
              {band.error}
            </Text>
          )}
        </Alert>
      )}
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {selectNotice && (
        <Alert
          color="yellow"
          role="status"
          aria-label="選択の知らせ"
          withCloseButton
          closeButtonLabel="選択の知らせを閉じる"
          onClose={() => setSelectNotice(null)}
        >
          {selectNotice}
        </Alert>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (query.trim()) runSearch(query.trim());
        }}
      >
        <Group align="flex-end">
          <TextInput
            label="番組名・出演者・説明で探す"
            value={query}
            onChange={(e) => setQuery(e.currentTarget.value)}
            style={{ flex: 1 }}
          />
          <Button type="submit" loading={searching}>
            検索
          </Button>
          {search && (
            <Button
              variant="default"
              onClick={() => {
                setSearch(null);
                setQuery("");
                setActiveQ("");
              }}
            >
              検索をやめる
            </Button>
          )}
        </Group>
      </form>

      {search ? (
        <Stack gap="xs" aria-label="検索結果">
          <Group justify="space-between">
            <Text size="sm">
              「{search.q}」: {search.airings.length} 件
            </Text>
            <Button size="xs" variant="light" onClick={() => onCreateKeyword(search.q)}>
              このキーワードで録音
            </Button>
          </Group>
          {search.truncated && (
            <Alert color="yellow" role="status">
              500 件を超えたので、キーワードを絞ってください。
            </Alert>
          )}
          {search.airings.length === 0 ? (
            <Text c="dimmed">見つからなかった。</Text>
          ) : (
            <SearchResults airings={search.airings} query={search.q} onOpen={(a) => openPlain(a, true)} />
          )}
        </Stack>
      ) : (
        <>
          <Group>
            <Button variant="default" size="xs" disabled={dayIndex <= 0} onClick={() => setDay(days[dayIndex - 1])}>
              前の日
            </Button>
            <Select
              aria-label="放送日"
              data={days.map((d) => ({ value: d, label: `${dayLabel(d)}${d === today ? " 今日" : ""}` }))}
              value={day}
              onChange={(v) => v && setDay(v)}
              allowDeselect={false}
              w={150}
            />
            <Button
              variant="default"
              size="xs"
              disabled={dayIndex >= days.length - 1}
              onClick={() => setDay(days[dayIndex + 1])}
            >
              次の日
            </Button>
            {!wide && stations && stations.length > 0 && (
              <Select
                aria-label="局"
                data={stations.map((s) => ({ value: s.id, label: s.name }))}
                value={selected?.id ?? null}
                onChange={(v) => v && pickStation(v)}
                allowDeselect={false}
                w={170}
              />
            )}
          </Group>
          <Group gap="xs" aria-label="凡例">
            {(Object.keys(stateLabel) as (keyof typeof stateLabel)[]).map((s) => (
              <Badge
                key={s}
                color={stateColor[s]}
                variant={s === "none" ? "outline" : "filled"}
                leftSection={stateMark[s] || undefined}
              >
                {stateLabel[s]}
              </Badge>
            ))}
            <Badge color="orange" variant="light" leftSection={notAvailableMark}>
              {notAvailableLabel}
            </Badge>
          </Group>
          {stations === null ? (
            !error && <Loader />
          ) : stations.length === 0 ? (
            <Text c="dimmed">この放送日の番組表は空。</Text>
          ) : wide ? (
            <>
              <Group>
                <SegmentedControl
                  size="xs"
                  aria-label="表示"
                  data={[
                    { value: "all", label: "全局" },
                    { value: "station", label: "1 局" },
                  ]}
                  value={view}
                  onChange={(v) => changeView(v as View)}
                />
                {view === "station" ? (
                  <Select
                    aria-label="局"
                    size="xs"
                    data={stations.map((s) => ({ value: s.id, label: s.name }))}
                    value={selected?.id ?? null}
                    onChange={(v) => v && pickStation(v)}
                    allowDeselect={false}
                    w={170}
                  />
                ) : (
                  <>
                    <Popover position="bottom-start" withArrow shadow="md">
                      <Popover.Target>
                        <Button variant="default" size="xs">
                          表示する局 {stations.filter((s) => !hidden.includes(s.id)).length} / {stations.length}
                        </Button>
                      </Popover.Target>
                      <Popover.Dropdown>
                        <Stack gap="xs">
                          <Group gap="xs">
                            <Button
                              size="compact-xs"
                              variant="default"
                              onClick={() => changeHidden(hidden.filter((id) => !stations.some((s) => s.id === id)))}
                            >
                              すべて選ぶ
                            </Button>
                            <Button
                              size="compact-xs"
                              variant="default"
                              onClick={() => changeHidden([...new Set([...hidden, ...stations.map((s) => s.id)])])}
                            >
                              すべて外す
                            </Button>
                          </Group>
                          {stations.map((s) => (
                            <Checkbox
                              key={s.id}
                              label={s.name}
                              checked={!hidden.includes(s.id)}
                              onChange={(e) =>
                                changeHidden(e.currentTarget.checked ? hidden.filter((id) => id !== s.id) : [...hidden, s.id])
                              }
                            />
                          ))}
                        </Stack>
                      </Popover.Dropdown>
                    </Popover>
                    <Switch
                      size="xs"
                      label="隠した局も表示"
                      checked={showHidden}
                      onChange={(e) => setShowHidden(e.currentTarget.checked)}
                    />
                  </>
                )}
              </Group>
              {view === "station" ? (
                selected && <StationTable day={day} now={now} station={selected} selected={selectedKeys} onOpen={openWithMods} />
              ) : shown && shown.length === 0 ? (
                <Text c="dimmed">表示する局が無い。「表示する局」から選ぶ。</Text>
              ) : (
                <Grid
                  day={day}
                  now={now}
                  stations={shown ?? []}
                  selected={selectedKeys}
                  onOpen={openWithMods}
                  onHide={(id) => changeHidden([...hidden, id])}
                  onPick={(id) => {
                    // 選択の局と違えば pickStation が選択を捨てる（changeView の決め直しは、まだ前の局を見るので通さない）
                    pickStation(id);
                    setView("station");
                    rememberView("station");
                  }}
                />
              )}
            </>
          ) : (
            selected && <List day={day} now={now} station={selected} selected={selectedKeys} onOpen={openWithMods} />
          )}
        </>
      )}

      {showSelectionBar && (
        <Paper
          withBorder
          shadow="md"
          p="xs"
          role="region"
          aria-label="選択中の放送回"
          style={{ position: "fixed", bottom: 16, left: "50%", transform: "translateX(-50%)", zIndex: 200 }}
        >
          <Group gap="xs" wrap="nowrap">
            <Text size="sm">
              {pickedAirings.length} 件を選択中（{broadcastClock(pickedAirings[0].start)}〜
              {broadcastClock(pickedAirings[pickedAirings.length - 1].end, broadcastDayOf(pickedAirings[0].start))}）
            </Text>
            <Button
              size="xs"
              onClick={() => {
                // 選んだ放送回の詳細を開く
                setSel({ ...sel, rows: pickedAirings, auto: false });
                setOpened(pickedAirings[0]);
              }}
            >
              この {pickedAirings.length} 件を予約
            </Button>
            <Button size="xs" variant="default" onClick={clearSelection}>
              選択を解除
            </Button>
          </Group>
        </Paper>
      )}

      <AiringPanel
        airing={current}
        wide={wide}
        stations={(stations ?? []).map(({ id, name }) => ({ id, name }))}
        loadDay={dayForParts}
        selection={panelSelection}
        onToggle={togglePicked}
        onOnly={onlyOpened}
        onMoreSelect={
          touch && !sel?.fromSearch
            ? () => {
                // 詳細を閉じて、以後のタップを選択に足す・外すにする
                setTouchSelect(true);
                setOpened(null);
              }
            : undefined
        }
        onClose={closeDetail}
        onDone={done}
        onOpenShow={onOpenShow}
      />
    </Stack>
  );
}
