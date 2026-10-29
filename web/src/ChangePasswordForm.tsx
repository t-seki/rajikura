import { Alert, Button, PasswordInput, Stack } from "@mantine/core";
import { useState } from "react";
import { ApiError, changePassword } from "./api";

/** Web UI のパスワードを変えるフォーム（ヘッダーの「パスワードを変える」のダイアログの中身）。今のパスワードを入れさせる（PUT /api/password）。 */
export function ChangePasswordForm() {
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setDone(false);
    if (password !== confirm) {
      setError("2 つの新しいパスワードが違う");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await changePassword(current, password);
      setDone(true);
      setCurrent("");
      setPassword("");
      setConfirm("");
    } catch (err) {
      // API が断った理由（ApiError）はそのまま出し、通信の失敗などはまとめた文にする
      setError(err instanceof ApiError ? err.message : "パスワードを変えられなかった。通信を確かめてほしい");
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
        {done && (
          <Alert color="green" role="status">
            パスワードを変えた
          </Alert>
        )}
        <PasswordInput
          label="今のパスワード"
          value={current}
          onChange={(e) => setCurrent(e.currentTarget.value)}
          autoComplete="current-password"
          required
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
          パスワードを変える
        </Button>
      </Stack>
    </form>
  );
}
