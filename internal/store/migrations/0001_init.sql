-- 番組・ルール・ジョブ・ジョブの放送回・試行の表を作る。
-- 時刻は UTC の RFC 3339（ミリ秒まで、固定幅）の文字列で持ち、文字列の比較で前後が分かるようにする。

CREATE TABLE shows (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    join_airings INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE rules (
    id         INTEGER PRIMARY KEY,
    show_id    INTEGER NOT NULL REFERENCES shows (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('time', 'title')),
    station_id TEXT    NOT NULL,
    -- 時刻ルール: 曜日（0 = 日曜）と開始時刻（HH:MM）
    weekday    INTEGER NOT NULL DEFAULT 0,
    start_hm   TEXT    NOT NULL DEFAULT '',
    -- 番組名ルール: 番組名と曜日の集合（"1,5" の形。空なら全曜日）
    title      TEXT    NOT NULL DEFAULT '',
    weekdays   TEXT    NOT NULL DEFAULT '',
    UNIQUE (show_id, position)
);

CREATE TABLE jobs (
    id              INTEGER PRIMARY KEY,
    show_name       TEXT    NOT NULL,
    broadcast_day   TEXT    NOT NULL,
    part            INTEGER NOT NULL DEFAULT 0,
    state           TEXT    NOT NULL CHECK (state IN ('waiting', 'running', 'retrying', 'done', 'expired', 'skipped')),
    next_attempt_at TEXT    NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    deadline        TEXT    NOT NULL,
    output_path     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX jobs_show_day ON jobs (show_name, broadcast_day);
CREATE INDEX jobs_day ON jobs (broadcast_day);
CREATE INDEX jobs_due ON jobs (state, next_attempt_at);

CREATE TABLE job_airings (
    job_id      INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    airing_key  TEXT    NOT NULL,
    program_id  TEXT    NOT NULL DEFAULT '',
    station_id  TEXT    NOT NULL,
    start_at    TEXT    NOT NULL,
    end_at      TEXT    NOT NULL,
    -- 番組表のメタデータの写し
    title       TEXT    NOT NULL DEFAULT '',
    performer   TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    image_url   TEXT    NOT NULL DEFAULT '',
    ts_in_ng    INTEGER NOT NULL DEFAULT 0,
    ts_out_ng   INTEGER NOT NULL DEFAULT 0,
    -- 欠落（取得期限までに取れなかった）
    missing     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (job_id, position)
);

CREATE TABLE attempts (
    id                INTEGER PRIMARY KEY,
    job_id            INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    started_at        TEXT    NOT NULL,
    ended_at          TEXT    NOT NULL DEFAULT '',
    failed_stage      TEXT    NOT NULL DEFAULT '',
    error             TEXT    NOT NULL DEFAULT '',
    -- 全セグメントの #EXTINF の合計と、ffprobe で測った長さ（ミリ秒）
    extinf_total_ms   INTEGER NOT NULL DEFAULT 0,
    probe_duration_ms INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX attempts_job ON attempts (job_id);
