-- app_state: アプリの状態（キーと値）。最後にジョブを保存した時刻（last_saved_at、UTC の RFC 3339）を持ち、
-- 再起動しても /metrics の rajikura_last_saved_timestamp_seconds を 0 に戻さないために使う。
CREATE TABLE app_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
