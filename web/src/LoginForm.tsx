import { Alert, Button, Card, Center, Group, Image, PasswordInput, Stack, Text, TextInput, Title } from "@mantine/core";
import { useState } from "react";
import { LoginLimitedError, login, setupPassword, type AuthState } from "./api";

/** ログインとパスワードを決める画面の枠（アイコンと rajikura の文字）。 */
function AuthCard({ children, onSubmit }: { children: React.ReactNode; onSubmit: (e: React.FormEvent) => void }) {
  return (
    <Center mih="100dvh" p="md">
      <Card withBorder padding="lg" w="100%" maw={400}>
        <form onSubmit={onSubmit}>
          <Stack>
            <Group gap="sm" wrap="nowrap">
              <Image src="/favicon.svg" alt="" w={48} h={48} />
              <Title order={2}>rajikura</Title>
            </Group>
            {children}
          </Stack>
        </form>
      </Card>
    </Center>
  );
}

export function LoginForm({ onState }: { onState: (s: AuthState) => void }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const s = await login(password);
      if (s === "unauthenticated") setError("パスワードが違う");
      else onState(s);
    } catch (err) {
      // 続けて間違えて受け付けを止めているときは、あと何秒待てばよいかを出す
      setError(err instanceof LoginLimitedError ? err.message : "ログインできなかった。通信を確かめてほしい");
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthCard onSubmit={submit}>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      <PasswordInput
        label="パスワード"
        value={password}
        onChange={(e) => setPassword(e.currentTarget.value)}
        autoComplete="current-password"
        required
        autoFocus
      />
      <Button type="submit" loading={busy}>
        ログイン
      </Button>
    </AuthCard>
  );
}

/**
 * パスワードがまだ無いときの、セットアップコードを入れてパスワードを決める画面。
 * locked なら、セットアップコードを 5 回続けて間違えたので、serve を起動し直すまで受け付けないと出す。
 */
export function SetupForm({ locked, onState }: { locked: boolean; onState: (s: AuthState) => void }) {
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError("2 つのパスワードが違う");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await setupPassword(code, password);
      switch (r.kind) {
        case "ok":
          onState("authenticated");
          break;
        case "wrong":
          setError(`セットアップコードが違う（あと ${r.remaining} 回）`);
          break;
        case "locked":
          onState("setupLocked");
          break;
        case "decided":
          // ほかの所でもう決めた。ログインの画面に移る
          onState("unauthenticated");
          break;
        case "invalid":
          setError(r.message);
          break;
      }
    } catch {
      setError("パスワードを決められなかった。通信を確かめてほしい");
    } finally {
      setBusy(false);
    }
  }

  if (locked) {
    return (
      <AuthCard onSubmit={(e) => e.preventDefault()}>
        <Alert color="red" role="alert">
          セットアップコードを 5 回続けて間違えたので、受け付けを止めた。rajikura（serve）を起動し直すと、新しいセットアップコードで決め直せる
        </Alert>
      </AuthCard>
    );
  }

  return (
    <AuthCard onSubmit={submit}>
      <Text size="sm">
        パスワードがまだ決まっていない。rajikura（serve）のデータのフォルダの setup-code.txt か、起動したときのログにあるセットアップコードを入れて、パスワードを決める。
      </Text>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      <TextInput
        label="セットアップコード"
        value={code}
        onChange={(e) => setCode(e.currentTarget.value)}
        autoComplete="off"
        required
        autoFocus
      />
      <PasswordInput
        label="新しいパスワード"
        value={password}
        onChange={(e) => setPassword(e.currentTarget.value)}
        autoComplete="new-password"
        required
      />
      <PasswordInput
        label="新しいパスワード（もう一度）"
        value={confirm}
        onChange={(e) => setConfirm(e.currentTarget.value)}
        autoComplete="new-password"
        required
      />
      <Button type="submit" loading={busy}>
        パスワードを決める
      </Button>
    </AuthCard>
  );
}
