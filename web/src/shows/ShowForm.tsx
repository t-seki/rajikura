import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Group,
  Loader,
  Select,
  Stack,
  Switch,
  Text,
  TextInput,
} from "@mantine/core";
import { useEffect, useState } from "react";
import { deleteOnce, getShow, suggestShowName, type Once, type Show, type Station } from "./api";
import {
  airingStartLabel,
  broadcastClockErrors,
  fromBroadcastClock,
  groupWeekly,
  toBroadcastClock,
  ungroupWeekly,
  weekdayNames,
  type WeeklyGroup,
} from "./format";

/** フォームに並べる曜日のボタン（月曜始まり）。 */
const weekdayButtons = [1, 2, 3, 4, 5, 6, 0].map((i) => ({ value: i, label: weekdayNames[i] }));

export const emptyShow = (): Show => ({
  name: "",
  enabled: true,
  split: "none",
  check_title: true,
  weekly: [],
});

function newCard(station: string): WeeklyGroup {
  return { station, weekdays: [], start: "" };
}

function StationInput({
  value,
  onChange,
  stations,
}: {
  value: string;
  onChange: (v: string) => void;
  stations: Station[] | null;
}) {
  // 局の一覧を取れなければ、局 ID を手で打つ。
  if (!stations || stations.length === 0) {
    return (
      <TextInput
        label="局 ID"
        value={value}
        onChange={(e) => onChange(e.currentTarget.value)}
        placeholder="TBS"
        required
      />
    );
  }
  const data = stations.map((s) => ({ value: s.id, label: `${s.name}（${s.id}）` }));
  if (value && !stations.some((s) => s.id === value)) data.push({ value, label: value });
  return (
    <Select
      label="局"
      data={data}
      value={value || null}
      onChange={(v) => onChange(v ?? "")}
      searchable
      required
    />
  );
}

/** 番組の 1 回の予約の一覧と、消すボタン。無効なら、当たらないことを知らせる。 */
function OnceList({ showName, enabled, stations }: { showName: string; enabled: boolean; stations: Station[] | null }) {
  const [items, setItems] = useState<Once[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    getShow(showName).then(
      (s) => {
        if (live) setItems(s.onces ?? []);
      },
      (e) => {
        if (live) setError(e instanceof Error ? e.message : "1 回の予約を読めない");
      },
    );
    return () => {
      live = false;
    };
  }, [showName]);

  async function remove(o: Once) {
    setError(null);
    try {
      await deleteOnce(showName, o.id);
      setItems((xs) => (xs ?? []).filter((x) => x.id !== o.id));
    } catch (e) {
      setError(e instanceof Error ? e.message : "1 回の予約を消せない");
    }
  }

  const label = (o: Once) =>
    `${airingStartLabel(o.start)} ${o.stationName ?? stations?.find((s) => s.id === o.stationId)?.name ?? o.stationId}`;

  return (
    <Stack gap="xs" aria-label="1 回の予約">
      <Text fw={500}>1 回の予約</Text>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {!enabled && items && items.length > 0 && (
        <Alert color="yellow" role="status">
          この番組は無効なので、1 回の予約も放送回に当たらない（有効にすると当たる）。
        </Alert>
      )}
      {items === null ? (
        !error && <Loader size="sm" />
      ) : items.length === 0 ? (
        <Text size="sm" c="dimmed">
          1 回の予約は無い。
        </Text>
      ) : (
        items.map((o) => (
          <Group key={o.id} justify="space-between" wrap="nowrap">
            <Text size="sm">
              {label(o)} {o.title} {o.missing && <Badge color="yellow">番組表に無い</Badge>}{" "}
              {o.keywordId !== undefined && (
                <Badge color="indigo" variant="light">
                  キーワード『{o.keywordWords}』が足した
                </Badge>
              )}
            </Text>
            <Button size="xs" variant="subtle" color="red" onClick={() => remove(o)} aria-label={`1 回の予約 ${label(o)} を消す`}>
              消す
            </Button>
          </Group>
        ))
      )}
      <Text size="xs" c="dimmed">
        番組表の「この回を録音」で足す。録音済みになると消える。
      </Text>
    </Stack>
  );
}

/** 番組を作る・編集するフォーム。initial があれば編集（名前の変更を知らせる）、無ければ作成（番組表から名前を探せる）。 */
export function ShowForm({
  initial,
  stations,
  onSubmit,
  onCancel,
}: {
  initial?: Show;
  stations: Station[] | null;
  onSubmit: (show: Show) => Promise<void>;
  onCancel: () => void;
}) {
  const creating = initial === undefined;
  const [show, setShow] = useState<Show>(() => {
    const s = initial ?? emptyShow();
    // 毎週の予約は下の cards が持つ。保存のときに ungroupWeekly(cards) で weekly に入れる。
    return { ...s, weekly: [], next_airing: undefined, unmatched_days: undefined };
  });
  // 毎週の予約は、局・開始時刻・開始日・終了日が同じものを 1 枚のカードにまとめて持つ。並べるのは開いたときの 1 回だけ。
  // カードの開始時刻は放送日の時刻（05:00〜28:59）で持ち、保存のときに API の値（00:00〜23:59）に戻す。
  const [cards, setCards] = useState<WeeklyGroup[]>(() =>
    groupWeekly(initial?.weekly ?? []).map((g) => ({ ...g, start: toBroadcastClock(g.start) })),
  );
  const [cardErrors, setCardErrors] = useState<Record<number, string>>({});
  const [startErrors, setStartErrors] = useState<Record<number, string>>({});
  const [untilErrors, setUntilErrors] = useState<Record<number, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [suggest, setSuggest] = useState<{ index: number; text: string; color: string } | null>(null);

  const renamed = !creating && show.name.trim() !== initial.name;

  function setCard(i: number, r: WeeklyGroup) {
    setCards((cs) => cs.map((x, k) => (k === i ? r : x)));
    setCardErrors((es) => {
      const { [i]: _, ...rest } = es;
      return rest;
    });
    setStartErrors((es) => {
      const { [i]: _, ...rest } = es;
      return rest;
    });
    setUntilErrors((es) => {
      const { [i]: _, ...rest } = es;
      return rest;
    });
  }

  function toggleWeekday(i: number, r: WeeklyGroup, weekday: number) {
    const on = r.weekdays.includes(weekday);
    const picked = on ? r.weekdays.filter((w) => w !== weekday) : [...r.weekdays, weekday];
    setCard(i, { ...r, weekdays: weekdayButtons.map((b) => b.value).filter((w) => picked.includes(w)) });
  }

  /**
   * 保存してよいか。開始が放送日の時刻（05:00〜28:59）でない欄、終了日が開始日より前の欄、曜日が無いカード、
   * 同じ局・開始時刻・開始日・終了日で曜日が重なるカードにエラーを付ける。
   */
  function validateCards(): boolean {
    const errors: Record<number, string> = {};
    const starts: Record<number, string> = {};
    const untils: Record<number, string> = {};
    cards.forEach((c, i) => {
      const got = fromBroadcastClock(c.start);
      if (!got.ok) starts[i] = broadcastClockErrors[got.reason];
      if (c.from && c.until && c.until < c.from) untils[i] = "終了日は開始日と同じ日か、それより後にする";
    });
    cards.forEach((c, i) => {
      if (c.weekdays.length === 0) {
        errors[i] = "曜日を 1 つ以上選んでほしい";
        return;
      }
      const clash = cards.slice(0, i).some(
        (o) =>
          o.station === c.station &&
          o.start === c.start &&
          (o.from || "") === (c.from || "") &&
          (o.until || "") === (c.until || "") &&
          o.weekdays.some((w) => c.weekdays.includes(w)),
      );
      if (clash) errors[i] = "同じ局・曜日・開始の予約が重なっている";
    });
    setCardErrors(errors);
    setStartErrors(starts);
    setUntilErrors(untils);
    return Object.keys(errors).length === 0 && Object.keys(starts).length === 0 && Object.keys(untils).length === 0;
  }

  async function findName(i: number, r: WeeklyGroup) {
    setSuggest(null);
    if (r.weekdays.length === 0) {
      setSuggest({ index: i, text: "曜日を選んでほしい", color: "yellow" });
      return;
    }
    if (!r.station || !/^\d\d:\d\d$/.test(r.start)) {
      setSuggest({ index: i, text: "局と開始時刻（HH:MM）を入れてほしい", color: "yellow" });
      return;
    }
    const start = fromBroadcastClock(r.start);
    if (!start.ok) {
      setSuggest({ index: i, text: broadcastClockErrors[start.reason], color: "yellow" });
      return;
    }
    try {
      const got = await suggestShowName(r.station, r.weekdays[0], start.start);
      if (got.found && got.name) {
        setShow((s) => ({ ...s, name: got.name! }));
        setSuggest({ index: i, text: `${got.day} の番組表の「${got.title}」から名前を入れた。直してよい`, color: "blue" });
      } else {
        setSuggest({
          index: i,
          text: `${got.day} の番組表に、開始時刻が ${r.start} の放送回が無い。名前は手で打ってほしい`,
          color: "yellow",
        });
      }
    } catch (e) {
      setSuggest({ index: i, text: e instanceof Error ? e.message : "番組表を探せない", color: "red" });
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!validateCards()) return;
    setBusy(true);
    setError(null);
    try {
      // validateCards で確かめたので、開始時刻はどれも戻せる
      const toApi = cards.map((c) => {
        const got = fromBroadcastClock(c.start);
        return got.ok ? { ...c, start: got.start } : c;
      });
      await onSubmit({ ...show, name: show.name.trim(), weekly: ungroupWeekly(toApi) });
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存できなかった");
    } finally {
      setBusy(false);
    }
  }

  const firstStation = cards[0]?.station ?? "";

  return (
    <form onSubmit={submit}>
      <Stack>
        {error && (
          <Alert color="red" role="alert">
            {error}
          </Alert>
        )}
        <TextInput
          label="番組の名前（保存先のフォルダ名になる）"
          description="radiko の番組表の番組名を写し、番組名の末尾の印と放送回ごとに変わる副題だけを落とす"
          value={show.name}
          onChange={(e) => {
            const name = e.currentTarget.value;
            setShow((s) => ({ ...s, name }));
          }}
          required
        />
        {renamed && (
          <Alert color="yellow" title="名前を変える">
            録音済みでないジョブ（録音予定・録音できなかったもの）は、新しい名前に付け替わって残る。保存済みのファイルは古い名前のフォルダに残る（動かさない）。
          </Alert>
        )}
        <Switch
          label="有効（無効にすると、番組表の同期で放送回に当てない）"
          checked={show.enabled}
          onChange={(e) => {
            const enabled = e.currentTarget.checked;
            setShow((s) => ({ ...s, enabled }));
          }}
        />
        <Group>
          <Stack gap={4}>
            <Checkbox
              label="分割番組（1 回の放送が番組表で複数のパートに分かれて載る）"
              description="同じ日のパートを 1 回の放送として扱う。オフなら、同じ日に 2 回当たっても別々のファイルにする"
              checked={show.split !== "none"}
              onChange={(e) => {
                const split = e.currentTarget.checked ? "join" : "none";
                setShow((s) => ({ ...s, split }));
              }}
            />
            {show.split !== "none" && (
              <Checkbox
                ml="xl"
                label="パートを 1 つのファイルにつなぐ"
                description="オフなら、パートごとに -1・-2 のファイルにする"
                checked={show.split === "join"}
                onChange={(e) => {
                  const split = e.currentTarget.checked ? "join" : "separate";
                  setShow((s) => ({ ...s, split }));
                }}
              />
            )}
          </Stack>
          <Checkbox
            label="番組名が変わった回は録らない"
            description="録音の通知にも番組名の食い違いを添える。毎週の予約だけに当たる回が対象"
            checked={show.check_title}
            onChange={(e) => {
              const check_title = e.currentTarget.checked;
              setShow((s) => ({ ...s, check_title }));
            }}
          />
        </Group>

        <Text fw={500}>毎週の予約</Text>
        <Text size="sm" c="dimmed">
          曜日は 5 時で区切った放送日の曜日で、開始は 05:00〜28:59（深夜 1 時の放送は前の曜日の 25:00）。
        </Text>
        {cards.map((r, i) => (
          <Card key={i} withBorder padding="sm" aria-label={`予約 ${i + 1}`}>
            <Stack gap="xs">
              <StationInput value={r.station} onChange={(station) => setCard(i, { ...r, station })} stations={stations} />
              <Stack gap={4}>
                <Text size="sm" fw={500}>
                  曜日（放送日）
                </Text>
                <Group gap="xs">
                  {weekdayButtons.map((b) => (
                    <Button
                      key={b.value}
                      size="xs"
                      variant={r.weekdays.includes(b.value) ? "filled" : "default"}
                      aria-pressed={r.weekdays.includes(b.value)}
                      onClick={() => toggleWeekday(i, r, b.value)}
                    >
                      {b.label}
                    </Button>
                  ))}
                </Group>
              </Stack>
              <Group grow align="flex-end">
                <TextInput
                  label="開始（HH:MM）"
                  value={r.start}
                  placeholder="08:30"
                  onChange={(e) => setCard(i, { ...r, start: e.currentTarget.value })}
                  error={startErrors[i]}
                  required
                />
                <TextInput
                  label="開始日（放送日）"
                  description="空なら制限なし。入れると、それより前の放送日の放送回には当たらない"
                  type="date"
                  value={r.from ?? ""}
                  onChange={(e) => setCard(i, { ...r, from: e.currentTarget.value })}
                />
                <TextInput
                  label="終了日（放送日）"
                  description="空なら制限なし。入れると、それより後の放送日の放送回には当たらない（終了日の放送日は当たる）"
                  type="date"
                  value={r.until ?? ""}
                  onChange={(e) => setCard(i, { ...r, until: e.currentTarget.value })}
                  error={untilErrors[i]}
                />
              </Group>
              {cardErrors[i] && (
                <Alert color="red" role="alert">
                  {cardErrors[i]}
                </Alert>
              )}
              {suggest?.index === i && (
                <Alert color={suggest.color} role="status">
                  {suggest.text}
                </Alert>
              )}
              <Group justify="flex-end">
                {creating && (
                  <Button variant="light" size="xs" onClick={() => findName(i, r)}>
                    番組表から名前を入れる
                  </Button>
                )}
                <Button
                  variant="subtle"
                  color="red"
                  size="xs"
                  onClick={() => {
                    setSuggest(null);
                    setCardErrors({});
                    setStartErrors({});
                    setUntilErrors({});
                    setCards((cs) => cs.filter((_, k) => k !== i));
                  }}
                >
                  予約を消す
                </Button>
              </Group>
            </Stack>
          </Card>
        ))}
        <Group>
          <Button variant="default" size="xs" onClick={() => setCards((cs) => [...cs, newCard(firstStation)])}>
            毎週の予約を足す
          </Button>
        </Group>

        {!creating && <OnceList showName={initial.name} enabled={show.enabled} stations={stations} />}

        <Group justify="flex-end">
          <Button variant="default" onClick={onCancel}>
            やめる
          </Button>
          <Button type="submit" loading={busy}>
            保存
          </Button>
        </Group>
      </Stack>
    </form>
  );
}
