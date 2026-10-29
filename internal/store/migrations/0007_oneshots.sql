-- 単発予約。特定の放送回 1 つを局と開始時刻で指定し、番組に属する（番組を消したら一緒に消える）。
-- 開始時刻は jobs と同じく UTC の RFC 3339（ミリ秒まで、固定幅）の文字列で持つ。
CREATE TABLE oneshots (
    id         INTEGER PRIMARY KEY,
    show_id    INTEGER NOT NULL REFERENCES shows (id) ON DELETE CASCADE,
    station_id TEXT    NOT NULL,
    start_at   TEXT    NOT NULL,
    UNIQUE (show_id, station_id, start_at)
);

CREATE INDEX oneshots_start ON oneshots (start_at);
