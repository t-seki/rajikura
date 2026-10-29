-- 設定と秘密を DB に置く（ADR 0015。config.toml と秘密の環境変数をやめた）。
-- settings: 設定の値（秘密を除く）。行は 1 つだけ（id = 1）。
--   body: 設定の値の JSON（キーは設定の TOML と同じ。internal/config の Values の形）。行が無ければ既定値（保存先は未定）
-- secrets: 秘密（notify.discord_webhook・notify.slack_webhook・notify.smtp.password・jellyfin.api_key）。
--   name: 秘密の名前
--   sealed: 暗号化した値（internal/secret。鍵はデータのフォルダの rajikura.key にあり、DB には置かない）
CREATE TABLE settings (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    body TEXT NOT NULL
);
CREATE TABLE secrets (
    name   TEXT PRIMARY KEY,
    sealed BLOB NOT NULL
);
