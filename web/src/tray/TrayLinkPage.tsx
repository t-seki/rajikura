import { Alert, Button, Stack, Text, Title } from "@mantine/core";
import { useState } from "react";
import { createToken } from "../tokens/tokensApi";

/** URL の検索部分（`?port=…&state=…&name=…`）から、連携に要る値を読む。port が 1024〜65535 の数でない、state が空なら null。 */
export function parseTrayLink(search: string): { port: number; state: string; name: string } | null {
  const q = new URLSearchParams(search);
  const rawPort = q.get("port") ?? "";
  const state = q.get("state") ?? "";
  if (!/^\d{1,5}$/.test(rawPort) || state === "") return null;
  const port = Number(rawPort);
  if (port < 1024 || port > 65535) return null;
  return { port, state, name: q.get("name") ?? "" };
}

/** トークンと state を、トレイの受け口（http://127.0.0.1:<port>/callback）へフォームの POST で送る。URL にトークンを載せない。 */
function postToTray(port: number, token: string, state: string) {
  const form = document.createElement("form");
  form.method = "POST";
  form.action = `http://127.0.0.1:${port}/callback`;
  form.style.display = "none";
  for (const [k, v] of [
    ["token", token],
    ["state", state],
  ]) {
    const input = document.createElement("input");
    input.type = "hidden";
    input.name = k;
    input.value = v;
    form.appendChild(input);
  }
  document.body.appendChild(form);
  form.submit();
}

// トレイとの連携の画面（用語は GLOSSARY.md の「連携」）。確かめのボタンを押すまでトークンを発行しない。
export function TrayLinkPage({ search }: { search: string }) {
  const link = parseTrayLink(search);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!link) {
    return (
      <Alert color="red" role="alert">
        連携の URL が正しくない。トレイのメニューから連携し直してほしい
      </Alert>
    );
  }

  async function approve() {
    if (!link) return;
    setBusy(true);
    setError(null);
    try {
      const created = await createToken(`トレイ（${link.name}）`);
      postToTray(link.port, created.token, link.state);
    } catch (e) {
      setError(e instanceof Error ? e.message : "トークンを発行できなかった");
      setBusy(false);
    }
  }

  return (
    <Stack maw={480}>
      <Title order={3}>トレイとの連携</Title>
      <Text>
        {link.name ? `「${link.name}」` : "この PC"}のトレイに、rajikura を読めるようにする API トークンを渡す。心当たりがなければ押さない。
      </Text>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      <Button onClick={approve} loading={busy}>
        このトレイと連携する
      </Button>
    </Stack>
  );
}
