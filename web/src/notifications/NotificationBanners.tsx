import { Alert, Anchor, Stack, Text } from "@mantine/core";
import { formatDateTime } from "../jobs/format";
import type { NotificationStatus } from "./notificationsApi";

// どのページの上部にも出す帯。既読とは関係なく、直るまで出し続ける（直ったかはサーバーが決める）。
//   - まだ復旧していないサービスの異常（番組表の同期の失敗が続いている・エリア外・バックアップを取れない・スリープから起こせない・再スキャンを頼めない）: 異常ごとに 1 つ
//   - 通知先に送れなかった通知がある: 件数と、履歴のページへのリンク
// 足したら直す: サービスの異常
export function NotificationBanners({ status, onOpenHistory }: { status: NotificationStatus | null; onOpenHistory: () => void }) {
  if (!status || (status.anomalies.length === 0 && status.failed === 0)) return null;
  return (
    <Stack gap="xs" mb="md" data-testid="notification-banners">
      {status.anomalies.map((a) => (
        <Alert key={a.kind} color="red" title="まだ復旧していない異常" role="status">
          <Text size="sm" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
            {a.message}
          </Text>
          <Text size="xs" c="dimmed">
            {formatDateTime(a.since)} から
          </Text>
        </Alert>
      ))}
      {status.failed > 0 && (
        <Alert color="orange" title="通知先に送れなかった通知がある" role="status">
          <Text size="sm">
            {status.failed} 件の通知を通知先に送れなかった。{" "}
            <Anchor component="button" type="button" size="sm" onClick={onOpenHistory}>
              通知の履歴を開く
            </Anchor>
          </Text>
          <Text size="xs" c="dimmed">
            この帯は、次の通知を通知先に送れたら消える（通知の履歴のテスト通知で、すぐに確かめられる）。
          </Text>
        </Alert>
      )}
    </Stack>
  );
}
