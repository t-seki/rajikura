// 通知の API（internal/server/notifications.go）を呼ぶ。

export interface AppNotification {
  id: number;
  // 足したら直す: 通知の種類
  /** 通知の種類（recorded・missing・expired・skipped・skip_planned・unskipped・deadline_near・sync・out_of_area・backup・wake・rescan・recovered・keyword_added・keyword_limit・unmatched_day・test）。 */
  kind: string;
  message: string;
  createdAt: string;
  /** 「送れなかった」の印（通知先に送ろうとして送れず、諦めた）。 */
  failed: boolean;
  read: boolean;
  /**
   * 通知が指す画面の Web UI のパス。ジョブの出来事は `/jobs/{id}`、キーワードで録音予定にした・当たらない日は
   * `/shows/{名前}`（名前はエンコード済み）、キーワードの上限で止めたは `/keywords`。指す先が無い通知には付かない。
   */
  link?: string;
}

/** link が指す画面を開くリンクの文。知らない形の link は null（リンクを出さない）。 */
export function linkLabel(link: string): string | null {
  if (link.startsWith("/jobs/")) return "ジョブを開く";
  if (link.startsWith("/shows/")) return "番組を開く";
  if (link === "/keywords") return "キーワードを開く";
  return null;
}

/** まだ復旧していないサービスの異常。message は始まりの通知の本文、since はその通知を作った時刻。 */
export interface Anomaly {
  kind: string;
  message: string;
  since: string;
}

/**
 * ナビの未読の数と、帯の元と、通知先の種類。failed は、最後に通知先に送れた時刻より後に送れなかった通知の数
 * （通知先が none の間は 0）。channel は notify.channel の値（discord・slack・email・none）。
 */
export interface NotificationStatus {
  unread: number;
  failed: number;
  anomalies: Anomaly[];
  channel: string;
}

/**
 * テスト通知を送った結果。sent が false なら error に通知先のエラー（URL は含まない）。notification は書いた行。
 * 通知先が none なら、送らずに行だけ書き、sent が false で noChannel が true（error も sentAt も付かない）。
 */
export interface TestNotificationResult {
  sent: boolean;
  sentAt?: string;
  error?: string;
  noChannel?: boolean;
  notification: AppNotification;
}

export class NotificationsApiError extends Error {}

async function call(method: string, url: string): Promise<Response> {
  const res = await fetch(url, { method, credentials: "same-origin" });
  if (!res.ok) {
    let msg = `${url} が ${res.status} を返した`;
    try {
      const b = (await res.json()) as { error?: string };
      if (b.error) msg = b.error;
    } catch {
      // 本文が JSON でなければ、上の既定の文のまま
    }
    throw new NotificationsApiError(msg);
  }
  return res;
}

export async function listNotifications(): Promise<AppNotification[]> {
  return ((await (await call("GET", "/api/notifications")).json()) as { notifications: AppNotification[] }).notifications;
}

/** 未読の通知を全部既読にする。 */
export async function markNotificationsRead(): Promise<void> {
  await call("POST", "/api/notifications/read");
}

/** 通知 1 件を既読にする（トーストから通知が指す画面を開いたとき）。通知が無ければ NotificationsApiError。 */
export async function markNotificationRead(id: number): Promise<void> {
  await call("POST", `/api/notifications/${id}/read`);
}

export async function fetchNotificationStatus(): Promise<NotificationStatus> {
  const b = (await (await call("GET", "/api/notifications/status")).json()) as Partial<NotificationStatus>;
  return { unread: b.unread ?? 0, failed: b.failed ?? 0, anomalies: b.anomalies ?? [], channel: b.channel ?? "none" };
}

/** テスト通知をその場で通知先に送る。送れなかったときと、通知先が none（履歴にだけ入れる）のときも結果を返す（例外にしない）。 */
export async function sendTestNotification(): Promise<TestNotificationResult> {
  return (await (await call("POST", "/api/notifications/test")).json()) as TestNotificationResult;
}

// 足したら直す: 通知の種類
const kindLabels: Record<string, string> = {
  recorded: "録音済み",
  missing: "欠落あり",
  expired: "録音できなかった",
  skipped: "録らなかった",
  skip_planned: "録らない予定にした",
  unskipped: "録音予定に戻した",
  deadline_near: "期限間近",
  sync: "番組表の同期の失敗",
  out_of_area: "エリア外",
  backup: "バックアップの失敗",
  wake: "スリープから起こせない",
  rescan: "再スキャンの失敗",
  recovered: "復旧",
  keyword_added: "キーワードで録音予定にした",
  keyword_limit: "キーワードの上限で止めた",
  unmatched_day: "当たらない日",
  test: "テスト",
};

/** 通知の種類の画面の言葉。知らない値はそのまま返す。 */
export function kindLabel(kind: string): string {
  return kindLabels[kind] ?? kind;
}
