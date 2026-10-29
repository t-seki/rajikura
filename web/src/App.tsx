import { Alert, Center, Loader } from "@mantine/core";
import { useCallback, useEffect, useState } from "react";
import { fetchMe, logout, type AuthState } from "./api";
import { LoginForm, SetupForm } from "./LoginForm";
import { Shell } from "./Shell";

function Message({ color, children }: { color: string; children: React.ReactNode }) {
  return (
    <Center mih="100dvh" p="md">
      <Alert color={color}>{children}</Alert>
    </Center>
  );
}

export function App() {
  const [state, setState] = useState<AuthState | "loading" | "failed">("loading");

  const [version, setVersion] = useState("");

  useEffect(() => {
    fetchMe().then(
      (me) => {
        setVersion(me.version);
        setState(me.state);
      },
      () => setState("failed"),
    );
  }, []);

  const onLogout = useCallback(async () => {
    // 失敗したら例外のまま Shell に返し、状態は変えない（Shell がエラーを出す）
    await logout();
    setState("unauthenticated");
  }, []);

  const onAuthState = (s: AuthState) => {
    setState(s);
    // ログインとパスワードを決めた応答に版は無いので、ログインできたら /api/me から版を読む（読めなくても版が出ないだけ）
    if (s === "authenticated") fetchMe().then((me) => setVersion(me.version), () => {});
  };

  switch (state) {
    case "loading":
      return (
        <Center mih="100dvh">
          <Loader />
        </Center>
      );
    case "failed":
      return <Message color="red">サーバーにつながらない。再読み込みしてほしい</Message>;
    case "setup":
    case "setupLocked":
      return <SetupForm locked={state === "setupLocked"} onState={onAuthState} />;
    case "unauthenticated":
      return <LoginForm onState={onAuthState} />;
    case "authenticated":
      return <Shell onLogout={onLogout} version={version} />;
  }
}
