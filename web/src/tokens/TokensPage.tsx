import { Alert, Button, Card, Code, Group, Loader, Stack, Text, TextInput } from "@mantine/core";
import { useCallback, useEffect, useState } from "react";
import { formatDateTime } from "../jobs/format";
import { notifySuccess } from "../ui/toast";
import { createToken, deleteToken, listTokens, type ApiToken, type CreatedApiToken } from "./tokensApi";

// API トークンの画面。CLI や AI が Authorization: Bearer で API を叩くための鍵を、発行・失効する。
export function TokensPage() {
  const [tokens, setTokens] = useState<ApiToken[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  // 発行した直後のトークン。そのものを見せるのはこの 1 回だけ。
  const [created, setCreated] = useState<CreatedApiToken | null>(null);

  const reload = useCallback(async (prefix = "") => {
    try {
      setTokens(await listTokens());
      setError(null);
    } catch (e) {
      setError(prefix + (e instanceof Error ? e.message : "API トークンを読めなかった"));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  async function issue() {
    setBusy(true);
    setError(null);
    try {
      setCreated(await createToken(name.trim()));
      setName("");
      await reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : "API トークンを発行できなかった");
    } finally {
      setBusy(false);
    }
  }

  async function revoke(t: ApiToken) {
    if (!window.confirm(`API トークン「${t.name}」を失効しますか。このトークンを使う CLI や AI は、以後 API を呼べなくなります。`)) return;
    setError(null);
    try {
      await deleteToken(t.id);
      if (created?.id === t.id) setCreated(null);
      notifySuccess(`API トークン「${t.name}」を失効した`);
    } catch (e) {
      setError(e instanceof Error ? e.message : "API トークンを失効できなかった");
      return;
    }
    // 失効は済んでいる。読み直しの失敗は、失効の失敗と区別して出す。
    await reload("失効したが、一覧を読み直せなかった: ");
  }

  return (
    <Stack>
      <Text size="sm" c="dimmed">
        CLI や AI が Web UI を通さずに API を使うための鍵。どのトークンも Web UI でできることは全部できる。リクエストの <Code>Authorization: Bearer</Code> に入れて使う。
      </Text>
      <Group align="flex-end" wrap="nowrap">
        <TextInput
          label="名前"
          placeholder="例: 手元の CLI"
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          style={{ flex: 1 }}
        />
        <Button onClick={issue} loading={busy} disabled={name.trim() === ""}>
          発行
        </Button>
      </Group>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {created && (
        <Alert color="yellow" title={`「${created.name}」を発行した`}>
          <Stack gap="xs">
            <Text size="sm">このトークンは今だけ表示する。あとでは見られないので、いま控えてほしい。</Text>
            <Code block data-testid="new-token">
              {created.token}
            </Code>
            <Group>
              <Button size="compact-sm" variant="default" onClick={() => setCreated(null)}>
                閉じる
              </Button>
            </Group>
          </Stack>
        </Alert>
      )}
      {tokens === null && !error && <Loader size="sm" />}
      {tokens?.length === 0 && <Text c="dimmed">発行した API トークンは無い。</Text>}
      {tokens?.map((t) => (
        <Card key={t.id} withBorder padding="sm" data-testid="token">
          <Group justify="space-between" wrap="nowrap" align="flex-start">
            <Stack gap={2} style={{ minWidth: 0 }}>
              <Text fw={600} style={{ overflowWrap: "anywhere" }}>
                {t.name}
              </Text>
              <Text size="sm">発行 {formatDateTime(t.createdAt)}</Text>
              <Text size="sm" c="dimmed">
                最後に使った日時 {t.lastUsedAt ? formatDateTime(t.lastUsedAt) : "まだ使っていない"}
              </Text>
            </Stack>
            <Button size="compact-sm" color="red" variant="default" onClick={() => revoke(t)} aria-label={`${t.name}を失効`}>
              失効
            </Button>
          </Group>
        </Card>
      ))}
    </Stack>
  );
}
