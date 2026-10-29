// バックアップの API（internal/server/backup.go の GET /api/backup）を呼び、写しをブラウザにファイルとして保存させる。

export class BackupApiError extends Error {}

/** 答えの Content-Disposition の filename（無ければ rajikura-backup.db）。 */
export function backupFileName(disposition: string | null): string {
  const m = disposition?.match(/filename="([^"]+)"/);
  return m ? m[1] : "rajikura-backup.db";
}

/** バックアップを受け取り、ファイルとして保存させる。保存させたファイルの名前を返す。断られたら理由を BackupApiError で投げる。 */
export async function downloadBackup(): Promise<string> {
  const res = await fetch("/api/backup", { credentials: "same-origin" });
  if (!res.ok) {
    let msg = `/api/backup が ${res.status} を返した`;
    try {
      const b = (await res.json()) as { error?: string };
      if (b.error) msg = b.error;
    } catch {
      // 本文が JSON でなければ、上の既定の文のまま
    }
    throw new BackupApiError(msg);
  }
  const name = backupFileName(res.headers.get("Content-Disposition"));
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // click の直後に URL を捨てると、保存が始まる前に消えて保存に失敗しうるので、少し待ってから捨てる（ブラウザごとには確かめていない）。
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  return name;
}
