import { Alert, Button, Group, Loader, MultiSelect, ScrollArea, Stack, Switch, Text, TextInput } from "@mantine/core";
import { useEffect, useRef, useState } from "react";
import { broadcastDayOf, broadcastRange, dayLabel } from "../guide/format";
import type { Station } from "../shows/api";
import { previewKeyword, type Keyword, type KeywordInput, type KeywordPreview } from "./keywordsApi";

// 入力が止まってから当たる放送回を調べるまでの間。
const previewDelay = 400;

/** 当たる放送回の件数つきの一覧。 */
function Preview({ preview, error, loading, empty }: { preview: KeywordPreview | null; error: string | null; loading: boolean; empty: boolean }) {
  if (empty) return <Text size="sm" c="dimmed">語を入れると、当たるまだ始まっていない放送回がここに出る。</Text>;
  return (
    <Stack gap="xs" aria-label="当たる放送回">
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {loading && <Loader size="xs" />}
      {preview && (
        <>
          <Text size="sm" fw={500}>
            当たるまだ始まっていない放送回: {preview.airings.length} 件
          </Text>
          {preview.truncated && (
            <Alert color="yellow" role="status">
              500 件を超えたので、語を絞ってください。
            </Alert>
          )}
          {preview.airings.length > 0 && (
            <ScrollArea.Autosize mah={240}>
              <Stack gap={4}>
                {preview.airings.map((a) => (
                  <Text key={`${a.stationId}${a.start}`} size="sm" data-testid="preview-airing">
                    {dayLabel(broadcastDayOf(a.start))} {broadcastRange(a)} {a.stationName} {a.title}
                  </Text>
                ))}
              </Stack>
            </ScrollArea.Autosize>
          )}
        </>
      )}
    </Stack>
  );
}

/** キーワードを作る・変えるフォーム。initial があれば変える。initialWords は作るときの語の初期値（番組表の検索から来たとき）。 */
export function KeywordForm({
  initial,
  initialWords = "",
  stations,
  onSubmit,
  onCancel,
}: {
  initial?: Keyword;
  initialWords?: string;
  stations: Station[] | null;
  onSubmit: (input: KeywordInput) => Promise<void>;
  onCancel: () => void;
}) {
  const [words, setWords] = useState(initial?.words ?? initialWords);
  const [exclude, setExclude] = useState(initial?.exclude ?? "");
  const [picked, setPicked] = useState<string[]>(initial?.stations ?? []);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [preview, setPreview] = useState<KeywordPreview | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const seq = useRef(0);

  const stationKey = picked.join(",");
  useEffect(() => {
    const my = ++seq.current;
    if (words.trim() === "") {
      setPreview(null);
      setPreviewError(null);
      setPreviewing(false);
      return;
    }
    setPreviewing(true);
    const id = setTimeout(() => {
      previewKeyword(words.trim(), exclude.trim(), picked).then(
        (p) => {
          if (my !== seq.current) return;
          setPreview(p);
          setPreviewError(null);
          setPreviewing(false);
        },
        (e) => {
          if (my !== seq.current) return;
          setPreview(null);
          setPreviewError(e instanceof Error ? e.message : "当たる放送回を調べられない");
          setPreviewing(false);
        },
      );
    }, previewDelay);
    return () => clearTimeout(id);
    // picked は stationKey の変化で見る
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [words, exclude, stationKey]);

  const stationData = (stations ?? []).map((s) => ({ value: s.id, label: s.name }));
  for (const id of picked) if (!stationData.some((d) => d.value === id)) stationData.push({ value: id, label: id });

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await onSubmit({ words: words.trim(), exclude: exclude.trim(), stations: picked, enabled });
    } catch (err) {
      setError(err instanceof Error ? err.message : "キーワードを保存できない");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit}>
      <Stack>
        {error && (
          <Alert color="red" role="alert">
            {error}
          </Alert>
        )}
        <TextInput
          label="語"
          description="空白で区切った語が全部、番組名・出演者・説明のどれかに入っている放送回に当たる"
          value={words}
          onChange={(e) => setWords(e.currentTarget.value)}
          required
        />
        <TextInput
          label="除外する語"
          description="どれか 1 つでも入っていれば外す（空白で区切る）"
          value={exclude}
          onChange={(e) => setExclude(e.currentTarget.value)}
        />
        <MultiSelect
          label="局"
          description="空ならエリアの全局"
          data={stationData}
          value={picked}
          onChange={setPicked}
          searchable
          clearable
        />
        <Switch label="有効" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />
        <Preview preview={preview} error={previewError} loading={previewing} empty={words.trim() === ""} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onCancel}>
            やめる
          </Button>
          <Button type="submit" loading={busy} disabled={words.trim() === ""}>
            {initial ? "保存" : "作る"}
          </Button>
        </Group>
      </Stack>
    </form>
  );
}
