import { Alert, Button, Card, Collapse, Group, NumberInput, PasswordInput, Select, Stack, Switch, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useCallback, useEffect, useState } from "react";
import { notifySuccess } from "../ui/toast";
import { BackupApiError, downloadBackup } from "./backupApi";
import { formatDateTime } from "../jobs/format";
import { FolderPicker } from "./FolderPicker";
import { fetchSettings, patchSettings, secretLabels, SettingsApiError, type SettingsValues, type SettingsView } from "./settingsApi";

// 設定の画面。区切りは「保存先」「通知」「Jellyfin との連携」「バックアップ」「詳しい設定」。区切りごとに保存し、保存したらすぐに効く
// （番組表のキャッシュの大きさだけは、serve の再起動まで効かない）。「バックアップ」は自動のバックアップの取る先と残す数を保存し、最後に自動で取れた日時を出す。DB の写しをダウンロードもさせる。
// 秘密の欄は値を返さない。空のまま保存したら変えず、「消す」を押したときだけ空の文字列を送る。
// onChanged は保存した後の設定を、画面の上の帯に渡す。
export function SettingsPage({ onChanged }: { onChanged: (view: SettingsView) => void }) {
  const [view, setView] = useState<SettingsView | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchSettings().then(setView, (e) => setError(e instanceof Error ? e.message : "設定を読めなかった"));
  }, []);

  const changed = useCallback(
    (v: SettingsView) => {
      setView(v);
      onChanged(v);
    },
    [onChanged],
  );

  if (!view) {
    return error ? (
      <Alert color="red" role="alert">
        {error}
      </Alert>
    ) : null;
  }
  return (
    <Stack>
      <LibrarySection view={view} onChanged={changed} />
      <NotifySection view={view} onChanged={changed} />
      <JellyfinSection view={view} onChanged={changed} />
      <BackupSection view={view} onChanged={changed} />
      <AdvancedSection view={view} onChanged={changed} />
    </Stack>
  );
}

interface SectionProps {
  view: SettingsView;
  onChanged: (view: SettingsView) => void;
}

// 区切りごとの保存（busy・失敗の理由・成功のトースト）。save は成功したら true。
function useSaver(onChanged: (view: SettingsView) => void, what: string) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const save = async (patch: SettingsValues): Promise<boolean> => {
    setBusy(true);
    setError(null);
    try {
      onChanged(await patchSettings(patch));
      notifySuccess(`${what}を保存した`);
      return true;
    } catch (e) {
      setError(e instanceof SettingsApiError ? e.message : `${what}を保存できなかった。通信を確かめてほしい`);
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, save };
}

function Section({ title, hint, error, children }: { title: string; hint?: string; error: string | null; children: React.ReactNode }) {
  return (
    <Card withBorder padding="md" component="section" aria-label={title}>
      <Stack>
        <Title order={4}>{title}</Title>
        {hint && (
          <Text size="sm" c="dimmed">
            {hint}
          </Text>
        )}
        {error && (
          <Alert color="red" role="alert">
            {error}
          </Alert>
        )}
        {children}
      </Stack>
    </Card>
  );
}

// 秘密の欄。値は返らないので、設定済みかどうかだけを出す。空のまま保存したら変えない。
function SecretField({
  name,
  view,
  value,
  onChange,
  onClear,
  busy,
}: {
  name: string;
  view: SettingsView;
  value: string;
  onChange: (v: string) => void;
  onClear: () => void;
  busy: boolean;
}) {
  const label = secretLabels[name] ?? name;
  const state = view.secrets[name];
  const unreadable = state?.unreadable === true;
  return (
    <Group align="flex-end" wrap="nowrap">
      <PasswordInput
        label={label}
        value={value}
        onChange={(e) => onChange(e.currentTarget.value)}
        placeholder={unreadable ? "復号できない。入れ直して" : state?.set ? "設定済み（変えるときだけ入れる）" : "未設定"}
        description={unreadable ? "復号できないので、未設定として扱っている" : state?.set ? "設定済み" : "未設定"}
        autoComplete="off"
        style={{ flex: 1 }}
      />
      {state?.set && (
        <Button variant="default" color="red" onClick={onClear} disabled={busy} aria-label={`${label}を消す`}>
          消す
        </Button>
      )}
    </Group>
  );
}

// フォルダ（絶対パス）の欄と、serve のマシンのフォルダを選ぶダイアログを開くボタン。選んでも保存はせず、欄に入れるだけ。
function FolderField({
  label,
  description,
  value,
  onChange,
  placeholder,
}: {
  label: string;
  description?: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  const [opened, setOpened] = useState(false);
  return (
    <>
      <Group align="flex-end" wrap="nowrap">
        <TextInput
          label={label}
          description={description}
          value={value}
          onChange={(e) => onChange(e.currentTarget.value)}
          placeholder={placeholder}
          style={{ flex: 1 }}
        />
        <Button variant="default" onClick={() => setOpened(true)} aria-label={`${label}を選ぶ`}>
          選ぶ…
        </Button>
      </Group>
      <FolderPicker opened={opened} initial={value} onClose={() => setOpened(false)} onSelect={onChange} />
    </>
  );
}

function LibrarySection({ view, onChanged }: SectionProps) {
  const [library, setLibrary] = useState(view.settings.library ?? "");
  const { busy, error, save } = useSaver(onChanged, "保存先");
  return (
    <Section
      title="保存先"
      hint="録音したファイルを置くフォルダ（絶対パス）。保存するときに、フォルダを作り、書けることを確かめる。決めるまで、録音は待たされる。"
      error={error}
    >
      <FolderField label="保存先のフォルダ" value={library} onChange={setLibrary} placeholder="/library" />
      <Group>
        <Button onClick={() => save({ library: library.trim() })} loading={busy} disabled={library.trim() === ""}>
          保存
        </Button>
      </Group>
    </Section>
  );
}

const channelOptions = [
  { value: "none", label: "通知先なし（履歴にだけ残す）" },
  { value: "discord", label: "Discord" },
  { value: "slack", label: "Slack" },
  { value: "email", label: "メール" },
];

function NotifySection({ view, onChanged }: SectionProps) {
  const n = view.settings.notify;
  const [channel, setChannel] = useState(n?.channel ?? "none");
  const [success, setSuccess] = useState(n?.success ?? true);
  const [discord, setDiscord] = useState("");
  const [slack, setSlack] = useState("");
  const [host, setHost] = useState(n?.smtp?.host ?? "");
  const [port, setPort] = useState<number | string>(n?.smtp?.port || 587);
  const [username, setUsername] = useState(n?.smtp?.username ?? "");
  const [from, setFrom] = useState(n?.smtp?.from ?? "");
  const [to, setTo] = useState((n?.smtp?.to ?? []).join("\n"));
  const [smtpPassword, setSmtpPassword] = useState("");
  const { busy, error, save } = useSaver(onChanged, "通知");

  async function submit() {
    const notify: NonNullable<SettingsValues["notify"]> = { channel, success };
    if (channel === "discord" && discord !== "") notify.discord_webhook = discord;
    if (channel === "slack" && slack !== "") notify.slack_webhook = slack;
    if (channel === "email") {
      notify.smtp = {
        host: host.trim(),
        port: Number(port) || 0,
        username: username.trim(),
        from: from.trim(),
        to: to.split(/[\s,]+/).filter((s) => s !== ""),
      };
      if (smtpPassword !== "") notify.smtp.password = smtpPassword;
    }
    if (await save({ notify })) {
      setDiscord("");
      setSlack("");
      setSmtpPassword("");
    }
  }

  // 秘密を消す。保存してある通知先が使っている秘密を消すと、サーバーの確かめで断られる（選んだ通知先の秘密が無い）ので、
  // 通知先を「なし」にして一緒に送る。使っていない秘密は、秘密だけを送る。
  async function clearSecret(name: string, usedBy: string, secret: NonNullable<SettingsValues["notify"]>) {
    const inUse = n?.channel === usedBy;
    const msg = inUse
      ? `${secretLabels[name]}を消しますか。今の通知先がこの秘密を使っているので、通知先を「なし」にして消します。`
      : `${secretLabels[name]}を消しますか。`;
    if (!window.confirm(msg)) return;
    if (await save({ notify: inUse ? { channel: "none", ...secret } : secret })) {
      if (inUse) setChannel("none");
    }
  }

  return (
    <Section title="通知" hint="録音の結果などの通知を送る先。通知は、どの通知先でも履歴には残る。" error={error}>
      <Select label="通知先" data={channelOptions} value={channel} onChange={(v) => v && setChannel(v)} allowDeselect={false} />
      <Switch label="録音済みの通知を送る" checked={success} onChange={(e) => setSuccess(e.currentTarget.checked)} />
      {channel === "discord" && (
        <SecretField
          name="notify.discord_webhook"
          view={view}
          value={discord}
          onChange={setDiscord}
          onClear={() => clearSecret("notify.discord_webhook", "discord", { discord_webhook: "" })}
          busy={busy}
        />
      )}
      {channel === "slack" && (
        <SecretField
          name="notify.slack_webhook"
          view={view}
          value={slack}
          onChange={setSlack}
          onClear={() => clearSecret("notify.slack_webhook", "slack", { slack_webhook: "" })}
          busy={busy}
        />
      )}
      {channel === "email" && (
        <Stack gap="xs">
          <TextInput label="SMTP のホスト" value={host} onChange={(e) => setHost(e.currentTarget.value)} placeholder="smtp.gmail.com" />
          <NumberInput label="SMTP のポート" value={port} onChange={setPort} min={1} max={65535} hideControls />
          <TextInput label="SMTP のユーザー名" value={username} onChange={(e) => setUsername(e.currentTarget.value)} />
          <SecretField
            name="notify.smtp.password"
            view={view}
            value={smtpPassword}
            onChange={setSmtpPassword}
            onClear={() => clearSecret("notify.smtp.password", "email", { smtp: { password: "" } })}
            busy={busy}
          />
          <TextInput label="送り元のアドレス" value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
          <Textarea
            label="宛先のアドレス"
            description="複数なら、改行かカンマで区切る"
            value={to}
            onChange={(e) => setTo(e.currentTarget.value)}
            rows={2}
          />
        </Stack>
      )}
      <Group>
        <Button onClick={submit} loading={busy}>
          保存
        </Button>
      </Group>
    </Section>
  );
}

function JellyfinSection({ view, onChanged }: SectionProps) {
  const j = view.settings.jellyfin;
  const [enabled, setEnabled] = useState((j?.url ?? "") !== "");
  const [url, setUrl] = useState(j?.url ?? "");
  const [library, setLibrary] = useState(j?.library ?? "");
  const [apiKey, setApiKey] = useState("");
  const { busy, error, save } = useSaver(onChanged, "Jellyfin との連携");

  async function submit() {
    // 使わないと決めたら、URL・ライブラリ・API キーを全部空にする（連携をやめる）
    const patch: SettingsValues = enabled
      ? { jellyfin: { url: url.trim(), library: library.trim(), ...(apiKey !== "" ? { api_key: apiKey } : {}) } }
      : { jellyfin: { url: "", library: "", api_key: "" } };
    if (await save(patch)) setApiKey("");
  }

  return (
    <Section
      title="Jellyfin との連携"
      hint="任意。使うと、録音を保存するたびに、指定したライブラリだけを Jellyfin に再スキャンさせる。"
      error={error}
    >
      <Switch label="Jellyfin と連携する" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />
      {enabled && (
        <Stack gap="xs">
          <TextInput label="Jellyfin の URL" value={url} onChange={(e) => setUrl(e.currentTarget.value)} placeholder="http://jellyfin:8096" />
          <TextInput label="再スキャンするライブラリの名前" value={library} onChange={(e) => setLibrary(e.currentTarget.value)} />
          <SecretField
            name="jellyfin.api_key"
            view={view}
            value={apiKey}
            onChange={setApiKey}
            onClear={async () => {
              // 保存してある連携（URL がある）は API キーが要るので、連携をやめて消す。連携を使っていなければ、API キーだけを消す
              const inUse = (j?.url ?? "") !== "";
              const msg = inUse
                ? `${secretLabels["jellyfin.api_key"]}を消しますか。連携が API キーを使っているので、Jellyfin との連携をやめて消します。`
                : `${secretLabels["jellyfin.api_key"]}を消しますか。`;
              if (!window.confirm(msg)) return;
              if (await save({ jellyfin: inUse ? { url: "", library: "", api_key: "" } : { api_key: "" } }) && inUse) {
                setEnabled(false);
                setUrl("");
                setLibrary("");
              }
            }}
            busy={busy}
          />
        </Stack>
      )}
      <Group>
        <Button onClick={submit} loading={busy}>
          保存
        </Button>
      </Group>
    </Section>
  );
}

// バックアップ。serve が取る先（backup_dir）と残す数（backup_keep）を保存し、最後に自動で取れた日時を出す。
// 「ダウンロード」（GET /api/backup）は、押すと、その場で取った DB の写しをこの PC にファイルとして保存させる。
function BackupSection({ view, onChanged }: SectionProps) {
  const [busy, setBusy] = useState(false);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const [dir, setDir] = useState(view.settings.backup_dir ?? "");
  const [keep, setKeep] = useState<number | string>(view.settings.backup_keep ?? 7);
  const saver = useSaver(onChanged, "バックアップの設定");
  const error = saver.error ?? downloadError;

  async function download() {
    setBusy(true);
    setDownloadError(null);
    try {
      const name = await downloadBackup();
      notifySuccess(`バックアップ（${name}）をダウンロードした`);
    } catch (e) {
      setDownloadError(e instanceof BackupApiError ? e.message : "バックアップをダウンロードできなかった。通信を確かめてほしい");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="バックアップ"
      hint="動いたまま取る DB の写し。番組・設定・ジョブの記録などを含み、ログインのセッションは含まない。戻すときは、serve を止めてデータのフォルダの DB をこれで置き換える。"
      error={error}
    >
      <FolderField
        label="自動のバックアップを取る先のフォルダ"
        description="絶対パス。空なら自動では取らない。保存するときに、フォルダを作り、書けることを確かめる。1 時間ごとに見て、前に取ってから 24 時間が過ぎていれば取る"
        value={dir}
        onChange={setDir}
        placeholder="/backups"
      />
      <NumberInput label="残す数" description="取る先に、新しいものからこの数だけ残す（1 以上）" value={keep} onChange={setKeep} min={1} hideControls />
      <Text size="sm">
        最後に自動で取れた日時: {view.last_backup_at ? formatDateTime(view.last_backup_at) : "まだ取れていない"}
      </Text>
      <Group>
        <Button
          onClick={() => saver.save({ backup_dir: dir.trim(), backup_keep: typeof keep === "number" ? keep : Number(keep) })}
          loading={saver.busy}
          disabled={typeof keep !== "number" && (keep === "" || Number.isNaN(Number(keep)))}
        >
          保存
        </Button>
      </Group>
      <Group>
        <Button onClick={download} loading={busy}>
          ダウンロード
        </Button>
        <Text size="sm">この PC にファイルとして保存する。秘密の鍵は含まない</Text>
      </Group>
    </Section>
  );
}

function AdvancedSection({ view, onChanged }: SectionProps) {
  const s = view.settings;
  const [open, setOpen] = useState(false);
  const [muxer, setMuxer] = useState(s.muxer ?? "builtin");
  const [ffmpeg, setFfmpeg] = useState(s.ffmpeg ?? "");
  const [ffprobe, setFfprobe] = useState(s.ffprobe ?? "");
  const [truncate, setTruncate] = useState(s.truncate_artist ?? true);
  const [chapters, setChapters] = useState(s.chapters ?? true);
  const [maxAttempts, setMaxAttempts] = useState<number | string>(s.max_attempts_free ?? 10);
  const [keywordMax, setKeywordMax] = useState<number | string>(s.keyword_max_pending ?? 10);
  const [cacheMB, setCacheMB] = useState<number | string>(s.radiko?.program_cache_mb ?? 64);
  const { busy, error, save } = useSaver(onChanged, "詳しい設定");

  return (
    <Stack gap="xs">
      <Group>
        <Button variant="subtle" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          詳しい設定
        </Button>
      </Group>
      <Collapse expanded={open}>
        <Section title="詳しい設定" error={error}>
          <Select
            label="m4a への格納"
            description="builtin が既定で、ffmpeg・ffprobe を使わない。ffmpeg を選ぶと、ffmpeg・ffprobe が要る"
            data={["builtin", "ffmpeg"]}
            value={muxer}
            onChange={(v) => v && setMuxer(v)}
            allowDeselect={false}
          />
          <TextInput label="ffmpeg のパス" description="空なら PATH から探す" value={ffmpeg} onChange={(e) => setFfmpeg(e.currentTarget.value)} />
          <TextInput label="ffprobe のパス" description="空なら PATH から探す" value={ffprobe} onChange={(e) => setFfprobe(e.currentTarget.value)} />
          <Switch label="出演者を 250 バイトに切り詰める" checked={truncate} onChange={(e) => setTruncate(e.currentTarget.checked)} />
          <Switch
            label="タイムフリーの録音にチャプターを入れる"
            description="radiko が放送の後に作るチャプターを取って m4a に入れる。取れなければチャプター無しで保存し、まだできていないだけなら、番組の終了から 6 時間まで待ち、できたらもう一度録音して入れる"
            checked={chapters}
            onChange={(e) => setChapters(e.currentTarget.checked)}
          />
          <NumberInput label="非会員の試行回数の上限" value={maxAttempts} onChange={setMaxAttempts} min={1} hideControls />
          <NumberInput
            label="キーワードごとの、録音を終えていない放送回の上限"
            description="1 以上"
            value={keywordMax}
            onChange={setKeywordMax}
            min={1}
            hideControls
          />
          <NumberInput
            label="番組表のキャッシュの大きさ（MB）"
            description="保存しても、serve を再起動するまで効かない"
            value={cacheMB}
            onChange={setCacheMB}
            min={1}
            hideControls
          />
          <Group>
            <Button
              loading={busy}
              onClick={() =>
                save({
                  muxer,
                  ffmpeg: ffmpeg.trim(),
                  ffprobe: ffprobe.trim(),
                  truncate_artist: truncate,
                  chapters,
                  max_attempts_free: Number(maxAttempts),
                  keyword_max_pending: Number(keywordMax),
                  radiko: { program_cache_mb: Number(cacheMB) },
                })
              }
            >
              保存
            </Button>
          </Group>
        </Section>
      </Collapse>
    </Stack>
  );
}
