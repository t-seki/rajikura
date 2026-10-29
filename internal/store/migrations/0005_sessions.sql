-- Web UI のログインのセッション。id はランダムな文字列で、cookie に載せて渡す。
-- expires_at を過ぎたセッションは認証で弾き、見つけたときに消す。
CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
