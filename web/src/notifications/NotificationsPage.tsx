import { Alert, Anchor, Badge, Card, Group, Loader, Stack, Text, Title } from "@mantine/core";
import { useEffect, useState } from "react";
import { formatDateTime } from "../jobs/format";
import {
  kindLabel,
  linkLabel,
  listNotifications,
  markNotificationsRead,
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
  login: "red",
  backup: "red",
  wake: "red",
  rescan: "red",
  recovered: "teal",
  keyword_added: "indigo",
  keyword_limit: "grape",
  unmatched_day: "cyan",
  area_changed: "violet",
  test: "gray",
};

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
// onRead は既読にできた後に呼ぶ（ナビの未読の数と帯を読み直すため）。
// 指す画面（link）のある通知には、それを開くリンクを置く。onOpenLink はそのリンクをふつうに押したときに呼ぶ（画面を移る）。
// リンクから画面を移っても、通知の既読は変えない。
export function NotificationsPage({ onRead, onOpenLink }: { onRead?: () => void; onOpenLink?: (link: string) => void }) {
  const [items, setItems] = useState<AppNotification[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
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
      <Title order={2}>通知</Title>
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
