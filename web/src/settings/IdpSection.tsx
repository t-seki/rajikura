import { Alert, Badge, Button, Code, CopyButton, Group, PasswordInput, Stack, Text, TextInput } from "@mantine/core";
import { useEffect, useState } from "react";
import { notifySuccess } from "../ui/toast";
import { fetchIdp, IdpApiError, idpErrorMessage, saveIdp, startIdpLink, takeQuery, unlinkIdp, type IdpView } from "./idpApi";

/** 区切りの枠（SettingsPage の Section）。循環 import を避けるため、渡してもらう。 */
type SectionComponent = React.ComponentType<{ title: string; hint?: string; error: string | null; children: React.ReactNode }>;

/**
 * 設定の画面の区切り「IdP のログイン」（ADR 0040）。IdP の設定（issuer・client id・client secret・Web UI の URL）を保存し、
 * 今のパスワードを入れて紐づけを始め、紐づけた相手を出して外せる。紐づけて戻ってきたときの idp_linked・idp_error は、
 * 読んだら URL から消す。
 */
export function IdpSection({ Section }: { Section: SectionComponent }) {
  const [view, setView] = useState<IdpView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [linked] = useState(() => takeQuery("idp_linked") === "1");
  const [returnedError] = useState(() => takeQuery("idp_error"));

  useEffect(() => {
    fetchIdp().then(setView, (e) => setError(e instanceof Error ? e.message : "IdP の設定を読めなかった"));
  }, []);

  return (
    <Section
      title="IdP のログイン"
      hint="自宅の IdP（OpenID Connect）でも Web UI にログインできるようにする。パスワードのログインはそのまま使える。"
      error={error ?? (returnedError ? idpErrorMessage(returnedError) : null)}
    >
      {linked && (
        <Alert color="green" role="status">
          IdP を紐づけた
        </Alert>
      )}
      {view && <IdpBody view={view} onChanged={setView} setError={setError} />}
    </Section>
  );
}

interface BodyProps {
  view: IdpView;
  onChanged: (v: IdpView) => void;
  setError: (m: string | null) => void;
}

function IdpBody({ view, onChanged, setError }: BodyProps) {
  const [issuer, setIssuer] = useState(view.issuer);
  const [clientId, setClientId] = useState(view.client_id);
  const [webUrl, setWebUrl] = useState(view.web_url);
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);

  async function save(clearSecret: boolean) {
    setBusy(true);
    setError(null);
    try {
      const values = { issuer, client_id: clientId, web_url: webUrl };
      const v = await saveIdp(clearSecret ? { ...values, client_secret: "" } : secret !== "" ? { ...values, client_secret: secret } : values);
      onChanged(v);
      setSecret("");
      notifySuccess("IdP の設定を保存した");
    } catch (err) {
      setError(err instanceof IdpApiError ? err.message : "IdP の設定を保存できなかった。通信を確かめてほしい");
    } finally {
      setBusy(false);
    }
  }

  const unreadable = view.client_secret_unreadable;
  return (
    <Stack>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save(false);
        }}
      >
        <Stack>
          <TextInput label="issuer" value={issuer} onChange={(e) => setIssuer(e.currentTarget.value)} placeholder="https://idp.example" autoComplete="off" />
          <TextInput label="client id" value={clientId} onChange={(e) => setClientId(e.currentTarget.value)} autoComplete="off" />
          <Group align="flex-end" wrap="nowrap">
            <PasswordInput
              label="client secret"
              value={secret}
              onChange={(e) => setSecret(e.currentTarget.value)}
              placeholder={unreadable ? "復号できない。入れ直して" : view.client_secret_set ? "設定済み（変えるときだけ入れる）" : "未設定（省くと secret を送らない）"}
              description={unreadable ? "復号できないので、未設定として扱っている" : view.client_secret_set ? "設定済み" : "未設定"}
              autoComplete="off"
              style={{ flex: 1 }}
            />
            {view.client_secret_set && (
              <Button variant="default" color="red" disabled={busy} aria-label="client secret を消す" onClick={() => void save(true)}>
                消す
              </Button>
            )}
          </Group>
          <TextInput
            label="Web UI の URL"
            description="ブラウザで Web UI を開く URL（scheme と host。例: https://rajikura.example）"
            value={webUrl}
            onChange={(e) => setWebUrl(e.currentTarget.value)}
            autoComplete="off"
          />
          <Button type="submit" loading={busy}>
            保存
          </Button>
        </Stack>
      </form>

      {view.redirect_url !== "" && (
        <Stack gap={4}>
          <Text size="sm">IdP に登録する戻り先の URL</Text>
          <Group gap="xs" wrap="nowrap">
            <Code style={{ flex: 1, wordBreak: "break-all" }}>{view.redirect_url}</Code>
            <CopyButton value={view.redirect_url}>
              {({ copied, copy }) => (
                <Button variant="default" size="xs" onClick={copy}>
                  {copied ? "写した" : "写す"}
                </Button>
              )}
            </CopyButton>
          </Group>
        </Stack>
      )}

      {view.redirect_url !== "" && !view.web_host_allowed && (
        <Alert color="orange" role="status">
          Web UI の URL の名前（{view.web_host}）が、rajikura（serve）の -allowed-hosts に無い。IdP から戻る要求が断られるので、-allowed-hosts に足して起動し直してほしい
        </Alert>
      )}

      <LinkPart view={view} onChanged={onChanged} setError={setError} />
    </Stack>
  );
}

function LinkPart({ view, onChanged, setError }: BodyProps) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [openUrl, setOpenUrl] = useState<string | null>(null);

  async function link(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setOpenUrl(null);
    try {
      const url = await startIdpLink(password);
      setPassword("");
      window.location.assign(url);
    } catch (err) {
      if (err instanceof IdpApiError) {
        setError(err.message);
        if (err.webUrl) setOpenUrl(err.webUrl);
      } else {
        setError("紐づけを始められなかった。通信を確かめてほしい");
      }
      setBusy(false);
    }
  }

  async function unlink() {
    setBusy(true);
    setError(null);
    try {
      onChanged(await unlinkIdp());
      notifySuccess("IdP の紐づけを外した");
    } catch (err) {
      setError(err instanceof IdpApiError ? err.message : "紐づけを外せなかった。通信を確かめてほしい");
    } finally {
      setBusy(false);
    }
  }

  if (view.binding) {
    return (
      <Group justify="space-between">
        <Group gap="xs">
          <Text size="sm">紐づけた相手</Text>
          <Badge variant="light" size="lg" tt="none">
            {view.binding.email !== "" ? view.binding.email : view.binding.subject}
          </Badge>
        </Group>
        <Button variant="default" color="red" loading={busy} onClick={() => void unlink()}>
          紐づけを外す
        </Button>
      </Group>
    );
  }
  if (view.issuer === "" || view.client_id === "" || view.web_url === "") {
    return (
      <Text size="sm" c="dimmed">
        IdP の設定を保存すると、紐づけを始められる。
      </Text>
    );
  }
  return (
    <form onSubmit={(e) => void link(e)}>
      <Stack>
        {openUrl && (
          <Alert color="orange" role="status">
            紐づけは、Web UI の URL で開いた画面からだけ始められる。<a href={openUrl + "/settings"}>{openUrl}</a> で開き直してほしい
          </Alert>
        )}
        <PasswordInput
          label="今のパスワード（紐づけ）"
          description="IdP を紐づけるには、今のパスワードを入れる"
          value={password}
          onChange={(e) => setPassword(e.currentTarget.value)}
          autoComplete="current-password"
          required
        />
        <Button type="submit" loading={busy}>
          IdP を紐づける
        </Button>
      </Stack>
    </form>
  );
}
