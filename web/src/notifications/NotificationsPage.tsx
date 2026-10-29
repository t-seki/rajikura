import { Alert, Anchor, Badge, Button, Card, Code, Group, Loader, Stack, Text, Title } from "@mantine/core";
import { useEffect, useState } from "react";
import { formatDateTime } from "../jobs/format";
import {
  fetchNotificationStatus,
  kindLabel,
  linkLabel,
  listNotifications,
  markNotificationsRead,
  sendTestNotification,
  type AppNotification,
} from "./notificationsApi";

// 足したら直す: 通知の種類
const kindColor: Record<string, string> = {
  recorded: "green",
  missing: "yellow",
  expired: "red",
  skipped: "yellow",
  skip_planned: "yellow",
  unskipped: "blue",
  deadline_near: "orange",
  sync: "red",
  out_of_area: "red",
  backup: "red",
  wake: "red",
  rescan: "red",
  recovered: "teal",
  keyword_added: "indigo",
  keyword_limit: "grape",
  unmatched_day: "cyan",
  test: "gray",
};

// テスト通知を送った結果。noChannel は通知先なし（none）なので送らずに履歴にだけ入れたこと。
type TestResult = { sent: true; at: string } | { sent: false; error: string } | { noChannel: true };

// テスト通知のカード。通知先の種類（channel）が none でもボタンを出す（押すと履歴にだけ入れ、トレイが拾う）。
// 送っている間はボタンを押せなくし（連打の防ぎはこれだけ）、結果をカードの中に出す。onSent は送った後
// （送れても送れなくても、行を書いた後）に呼ぶ。
function TestNotificationCard({ channel, onSent }: { channel: string; onSent: () => void }) {
  const [sending, setSending] = useState(false);
  const [result, setResult] = useState<TestResult | null>(null);

  async function send() {
    setSending(true);
    setResult(null);
    try {
      const r = await sendTestNotification();
      setResult(
        r.noChannel
          ? { noChannel: true }
          : r.sent
            ? { sent: true, at: r.sentAt ?? r.notification.createdAt }
            : { sent: false, error: r.error ?? "" },
      );
      onSent();
    } catch (e) {
      setResult({ sent: false, error: (e instanceof Error && e.message) || "原因は分からない" });
    } finally {
      setSending(false);
    }
  }

  return (
    <Card withBorder padding="sm" data-testid="test-notification">
      <Stack gap="xs">
        <Title order={4}>テスト通知</Title>
        {channel === "none" ? (
          <Text size="sm">
            通知先が選ばれていない（<Code>notify.channel = "none"</Code>）。通知は Web UI の履歴にだけ残る。
          </Text>
        ) : (
          <Text size="sm">通知先（{channel}）に、テストの通知を 1 通送る。</Text>
        )}
        <Group>
          <Button size="xs" onClick={send} disabled={sending}>
            {sending ? "送っている…" : "テスト通知を送る"}
          </Button>
        </Group>
        {result && "noChannel" in result && (
          <Alert color="blue" role="status">
            履歴に入れた。トレイには 1 分以内に届く。
          </Alert>
        )}
        {result && "sent" in result && result.sent && (
          <Alert color="green" role="status">
            送れた（{formatDateTime(result.at)}）。通知先に届いたかを確かめて。
          </Alert>
        )}
        {result && "sent" in result && !result.sent && (
          <Alert color="red" role="alert">
            送れなかった: {result.error}
          </Alert>
        )}
      </Stack>
    </Card>
  );
}

// 通知が指す画面（ジョブの詳細・番組の編集・キーワード）を開くリンク。ふつうに押したら onOpen で画面の中で移り
// （履歴に積む）、中クリック・Ctrl+クリックなどはブラウザに任せる（別タブで開く）。行全体は押せるようにしない（本文を選べるように）。
function TargetLink({ link, onOpen }: { link: string; onOpen?: (link: string) => void }) {
  const label = linkLabel(link);
  if (!label) return null;
  return (
    <Anchor
      href={link}
      size="sm"
      onClick={(e: React.MouseEvent<HTMLAnchorElement>) => {
        if (!onOpen || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        onOpen(link);
      }}
    >
      {label}
    </Anchor>
  );
}

// 通知の履歴の画面。通知を新しい順に出し、開いたら未読の通知を全部既読にする（この画面では 1 件ずつは既読にしない。トーストから通知が指す画面を開いたときの 1 件の既読は Shell が受ける）。
// 「未読」の印は、開いたときに未読だったものに付ける（既読にした後も、この画面を開いている間は残す）。
// 上部にテスト通知のカード（帯の下、説明文の上）を置く。通知先の種類は /api/notifications/status で読む。
// onRead は既読にできた後と、テスト通知を送った後に呼ぶ（ナビの未読の数と帯を読み直すため）。
// 指す画面（link）のある通知には、それを開くリンクを置く。onOpenLink はそのリンクをふつうに押したときに呼ぶ（画面を移る）。
// リンクから画面を移っても、通知の既読は変えない。
export function NotificationsPage({ onRead, onOpenLink }: { onRead?: () => void; onOpenLink?: (link: string) => void }) {
  const [items, setItems] = useState<AppNotification[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // 通知先の種類。読めるまで（読めなければずっと）テスト通知のカードを出さない。
  const [channel, setChannel] = useState<string | null>(null);

  // テスト通知を送った後に、履歴（テスト通知の行が先頭に出る）と帯を読み直す。
  async function afterTestSent() {
    onRead?.();
    try {
      setItems(await listNotifications());
    } catch (e) {
      setError(e instanceof Error ? e.message : "通知を読めなかった");
    }
  }

  useEffect(() => {
    let cancelled = false;
    fetchNotificationStatus().then(
      (s) => {
        if (!cancelled) setChannel(s.channel);
      },
      () => {},
    );
    (async () => {
      try {
        const ns = await listNotifications();
        if (cancelled) return;
        setItems(ns);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : "通知を読めなかった");
        return;
      }
      try {
        await markNotificationsRead();
        if (!cancelled) onRead?.();
      } catch (e) {
        if (!cancelled) setError("通知を既読にできなかった: " + (e instanceof Error ? e.message : ""));
      }
    })();
    return () => {
      cancelled = true;
    };
    // 開いたときに 1 度だけ読む（onRead が変わっても読み直さない）
  }, []);

  return (
    <Stack>
      {channel !== null && <TestNotificationCard channel={channel} onSent={afterTestSent} />}
      <Text size="sm" c="dimmed">
        録音の結果・サービスの異常・テスト通知を新しい順に出す。通知先に送れなかった通知には「送れなかった」の印を付ける。通知は 90 日で消える。
      </Text>
      {error && (
        <Alert color="red" role="alert">
          {error}
        </Alert>
      )}
      {items === null && !error && <Loader size="sm" />}
      {items?.length === 0 && <Text c="dimmed">通知は無い。</Text>}
      {items?.map((n) => (
        <Card key={n.id} withBorder padding="sm" data-testid="notification">
          <Stack gap={4}>
            <Group gap="xs" wrap="wrap">
              <Badge color={kindColor[n.kind] ?? "gray"}>{kindLabel(n.kind)}</Badge>
              {!n.read && (
                <Badge variant="outline" color="blue">
                  未読
                </Badge>
              )}
              {n.failed && (
                <Badge variant="outline" color="red">
                  送れなかった
                </Badge>
              )}
              <Text size="sm" c="dimmed">
                {formatDateTime(n.createdAt)}
              </Text>
            </Group>
            <Text size="sm" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
              {n.message}
            </Text>
            {n.link && <TargetLink link={n.link} onOpen={onOpenLink} />}
          </Stack>
        </Card>
      ))}
    </Stack>
  );
}
