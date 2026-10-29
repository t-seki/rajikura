// 設定の API（internal/server/settings.go）を呼ぶ。キーは config.Patch の JSON のキー（internal/config/config.go）。秘密の値は返らない。

/** 設定の値（GET /api/settings の settings。PATCH の本文も同じ形で、書いた項目だけが変わる）。 */
export interface SettingsValues {
  library?: string;
  preset?: string;
  truncate_artist?: boolean;
  membership?: string;
  max_attempts_free?: number;
  keyword_max_pending?: number;
  muxer?: string;
  ffmpeg?: string;
  ffprobe?: string;
  chapters?: boolean;
  backup_dir?: string;
  backup_keep?: number;
  radiko?: { program_cache_mb?: number };
  notify?: {
    channel?: string;
    success?: boolean;
    discord_webhook?: string;
    slack_webhook?: string;
    smtp?: { host?: string; port?: number; username?: string; from?: string; to?: string[]; password?: string };
  };
  jellyfin?: { url?: string; library?: string; api_key?: string };
}

export interface SecretState {
  set: boolean;
  unreadable: boolean;
}

export interface SettingsView {
  settings: SettingsValues;
  /** 秘密の名前（notify.discord_webhook など）ごとの状態。 */
  secrets: Record<string, SecretState>;
  /** 保存先が決まっているか（決まるまで録音が待たされる）。 */
  library_set: boolean;
  /** 復号できない秘密の名前。 */
  unreadable_secrets: string[];
  /** 最後に自動のバックアップを取れた時刻（ISO 8601）。まだ取れていないなら null。 */
  last_backup_at: string | null;
}

export const secretLabels: Record<string, string> = {
  "notify.discord_webhook": "Discord の Webhook の URL",
  "notify.slack_webhook": "Slack の Webhook の URL",
  "notify.smtp.password": "SMTP のパスワード",
  "jellyfin.api_key": "Jellyfin の API キー",
};

export class SettingsApiError extends Error {}

async function call(method: string, body?: unknown): Promise<SettingsView> {
  const res = await fetch("/api/settings", {
    method,
    credentials: "same-origin",
    ...(body === undefined ? {} : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
  });
  if (!res.ok) {
    let msg = `/api/settings が ${res.status} を返した`;
    try {
      const b = (await res.json()) as { error?: string };
      if (b.error) msg = b.error;
    } catch {
      // 本文が JSON でなければ、上の既定の文のまま
    }
    throw new SettingsApiError(msg);
  }
  const v = (await res.json()) as Partial<SettingsView>;
  return {
    settings: v.settings ?? {},
    secrets: v.secrets ?? {},
    library_set: v.library_set ?? false,
    unreadable_secrets: v.unreadable_secrets ?? [],
    last_backup_at: v.last_backup_at ?? null,
  };
}

export const fetchSettings = () => call("GET");

/** 書いた項目だけを変える。誤りがあれば 400 の理由を SettingsApiError で投げ、何も当たらない。 */
export const patchSettings = (patch: SettingsValues) => call("PATCH", patch);
