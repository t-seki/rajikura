import { Alert, Badge, Button, Card, Group, Loader, Modal, Stack, Switch, Text, Title } from "@mantine/core";
import { useCallback, useEffect, useState } from "react";
import { broadcastDayOf, broadcastRange, dayLabel } from "../guide/format";
import { listStations, type Station } from "../shows/api";
import { notifySuccess } from "../ui/toast";
import { KeywordForm } from "./KeywordForm";
import {
  createKeyword,
  deleteKeyword,
  listKeywordAirings,
  listKeywords,
  stateColors,
  stateLabels,
  updateKeyword,
  type Keyword,
  type KeywordAiring,
  type KeywordInput,
} from "./keywordsApi";

const message = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/** キーワードが足した放送回（新しい順）。押すと番組を開く。 */
function AddedAirings({ keyword, onOpenShow }: { keyword: Keyword; onOpenShow: (name: string) => void }) {
  const [items, setItems] = useState<KeywordAiring[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    listKeywordAirings(keyword.id).then(
      (a) => live && setItems(a),
      (e) => live && setError(message(e, "足した放送回を読めない")),
    );
    return () => {
      live = false;
    };
  }, [keyword.id]);

  return (
    <Stack gap={4} aria-label="足した放送回">
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {items === null ? (
        !error && <Loader size="sm" />
      ) : items.length === 0 ? (
        <Text size="sm" c="dimmed">
          まだ足した放送回は無い。
        </Text>
      ) : (
        items.map((a) => (
          <Group key={`${a.stationId}${a.start}`} gap="xs" data-testid="added-airing">
            <Text size="sm">
              {a.title}（{dayLabel(broadcastDayOf(a.start))} {broadcastRange(a)} {a.stationName}）
            </Text>
            <Button size="compact-xs" variant="subtle" onClick={() => onOpenShow(a.showName)} aria-label={`番組「${a.showName}」を開く`}>
              {a.showName}
            </Button>
            <Badge color={stateColors[a.state]}>{stateLabels[a.state] ?? a.state}</Badge>
          </Group>
        ))
      )}
    </Stack>
  );
}

/**
 * キーワードの画面。一覧・作る・変える・消す・有効／無効、足した放送回。
 * draftWords があれば、一覧を読んだ後にその語を入れた作るダイアログを開き、onDraftHandled を呼ぶ（番組表の検索の「このキーワードで録音」から来たとき）。
 */
export function KeywordsPage({
  draftWords,
  onDraftHandled,
  onOpenShow = () => {},
}: {
  draftWords?: string | null;
  onDraftHandled?: () => void;
  onOpenShow?: (name: string) => void;
} = {}) {
  const [keywords, setKeywords] = useState<Keyword[] | null>(null);
  const [stations, setStations] = useState<Station[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // editing は、変える途中のキーワード（undefined なら作る、null ならダイアログを閉じている）。
  const [editing, setEditing] = useState<Keyword | undefined | null>(null);
  // 作るダイアログの語の初期値
  const [initialWords, setInitialWords] = useState("");
  const [deleting, setDeleting] = useState<Keyword | null>(null);
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState<number | null>(null);

  const reload = useCallback(async () => {
    try {
      setKeywords(await listKeywords());
    } catch (e) {
      setError(message(e, "キーワードの一覧を読めない"));
    }
  }, []);

  useEffect(() => {
    void reload();
    listStations().then(setStations, () => setStations([]));
  }, [reload]);

  useEffect(() => {
    if (draftWords === undefined || draftWords === null) return;
    setInitialWords(draftWords);
    setEditing(undefined);
    onDraftHandled?.();
  }, [draftWords, onDraftHandled]);

  const stationName = (id: string) => stations?.find((s) => s.id === id)?.name ?? id;

  async function save(input: KeywordInput) {
    const creating = editing === undefined;
    if (creating) await createKeyword(input);
    else if (editing) await updateKeyword(editing.id, input);
    notifySuccess(creating ? `キーワード「${input.words}」を作った` : `キーワード「${input.words}」を保存した`);
    setEditing(null);
    setError(null);
    await reload();
  }

  async function toggle(k: Keyword, enabled: boolean) {
    setError(null);
    try {
      await updateKeyword(k.id, { words: k.words, exclude: k.exclude, stations: k.stations, enabled });
      notifySuccess(enabled ? `キーワード「${k.words}」を有効にした` : `キーワード「${k.words}」を無効にした`);
      await reload();
    } catch (e) {
      setError(message(e, "有効／無効を変えられない"));
    }
  }

  async function remove() {
    if (!deleting) return;
    setBusy(true);
    setError(null);
    try {
      await deleteKeyword(deleting.id);
      setDeleting(null);
      if (shown === deleting.id) setShown(null);
      notifySuccess(`キーワード「${deleting.words}」を消した`);
      await reload();
    } catch (e) {
      setDeleting(null);
      setError(message(e, "キーワードを消せない"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>キーワード</Title>
        <Button
          onClick={() => {
            setInitialWords("");
            setEditing(undefined);
          }}
        >
          キーワードを作る
        </Button>
      </Group>
      <Text size="sm" c="dimmed">
        番組表を探して、当たったまだ始まっていない放送回を、番組名から決まる番組の 1 回の予約として足す（キーワードで録音）。
      </Text>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {keywords === null ? (
        !error && <Loader />
      ) : keywords.length === 0 ? (
        <Text c="dimmed">キーワードがまだ無い。</Text>
      ) : (
        keywords.map((k) => (
          <Card key={k.id} withBorder padding="sm" data-testid="keyword">
            <Stack gap="xs">
              <Group justify="space-between" align="flex-start" wrap="nowrap">
                <Stack gap={2} style={{ minWidth: 0 }}>
                  <Group gap="xs">
                    <Text fw={600} style={{ overflowWrap: "anywhere" }}>
                      {k.words}
                    </Text>
                    {!k.enabled && <Badge color="gray">無効</Badge>}
                  </Group>
                  {k.exclude && (
                    <Text size="sm" c="dimmed">
                      除外: {k.exclude}
                    </Text>
                  )}
                  <Text size="sm" c="dimmed">
                    局: {k.stations.length === 0 ? "エリアの全局" : k.stations.map(stationName).join("・")}
                  </Text>
                  <Text size="sm" c="dimmed">
                    録音を終えていない足した放送回: {k.pending} 件
                  </Text>
                </Stack>
                <Group gap="xs" wrap="nowrap">
                  <Switch aria-label={`${k.words} を有効にする`} checked={k.enabled} onChange={(e) => toggle(k, e.currentTarget.checked)} />
                  <Button size="xs" variant="default" onClick={() => setShown(shown === k.id ? null : k.id)} aria-label={`${k.words} の足した放送回`}>
                    足した放送回
                  </Button>
                  <Button size="xs" variant="default" onClick={() => setEditing(k)} aria-label={`${k.words} を編集`}>
                    編集
                  </Button>
                  <Button size="xs" variant="subtle" color="red" onClick={() => setDeleting(k)} aria-label={`${k.words} を消す`}>
                    削除
                  </Button>
                </Group>
              </Group>
              {shown === k.id && <AddedAirings keyword={k} onOpenShow={onOpenShow} />}
            </Stack>
          </Card>
        ))
      )}

      <Modal opened={editing !== null} onClose={() => setEditing(null)} title={editing === undefined ? "キーワードを作る" : "キーワードを変える"} size="lg">
        {editing !== null && (
          <KeywordForm
            key={editing?.id ?? "new"}
            initial={editing}
            initialWords={initialWords}
            stations={stations}
            onSubmit={save}
            onCancel={() => setEditing(null)}
          />
        )}
      </Modal>

      <Modal opened={deleting !== null} onClose={() => setDeleting(null)} title="キーワードを消す">
        <Stack>
          <Text>キーワード「{deleting?.words}」を消す。足した 1 回の予約と、作った番組は残す。</Text>
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
