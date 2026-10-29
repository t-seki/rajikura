-- API トークン（CLI や AI が API を叩くための鍵）。DB には sha256 の hash だけを置き、トークンそのものは残さない。
-- 期限は持たない（人が失効させる）。last_used_at は最後に使った日時で、まだ使っていなければ NULL。
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);
