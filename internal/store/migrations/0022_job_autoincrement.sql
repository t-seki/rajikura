-- ジョブの id を使い回さない（docs/adr/0026-job-id-not-reused.md）。jobs.id を AUTOINCREMENT にする。
-- AUTOINCREMENT の無い INTEGER PRIMARY KEY では、いちばん大きい id のジョブを消すと（展開がジョブを消して作り直す）、
-- 次に作るジョブが同じ id になり、Web UI の URL・CLI の <id>・通知のリンクが別のジョブを指しうる。
-- jobs だけを作り直すと、job_airings と attempts の行が ON DELETE CASCADE で消える（0012_drop_job_skip.sql）。
-- そこで子の表ごと作り直す。新しい表に写し、子（job_airings・attempts）→ jobs の順に消してから名前を戻すので、
-- jobs を消すときに消える子の行は無く、録音の記録は消えない。外部キーは有効のまま、このマイグレーションの
-- トランザクションの中で行う。ALTER TABLE ... RENAME は、子の表の REFERENCES も戻した名前に書き換える。
-- 列・CHECK・主キー・索引は作り直す前（版 21）の定義のまま写す（AUTOINCREMENT を付けるのは jobs.id だけ）。
-- 初めの値（sqlite_sequence の jobs）は、今あるジョブの最大の id と、通知が覚えているジョブの id
-- （notifications.job_id）の最大の大きい方にする。作り直す前に消えていたジョブの id を、通知のリンクが
-- 指したまま別のジョブに振らないため。

CREATE TABLE jobs_new (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    reservation_name     TEXT    NOT NULL,
    broadcast_day        TEXT    NOT NULL,
    part                 INTEGER NOT NULL DEFAULT 0,
    state                TEXT    NOT NULL CHECK (state IN ('waiting', 'running', 'retrying', 'done', 'expired', 'skipped')),
    next_attempt_at      TEXT    NOT NULL,
    attempts             INTEGER NOT NULL DEFAULT 0,
    deadline             TEXT    NOT NULL,
    output_path          TEXT    NOT NULL DEFAULT '',
    deadline_notified_at TEXT    NOT NULL DEFAULT '',
    by_person            INTEGER NOT NULL DEFAULT 0,
    settled              INTEGER NOT NULL DEFAULT 0
);
INSERT INTO jobs_new (id, reservation_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path,
                      deadline_notified_at, by_person, settled)
SELECT id, reservation_name, broadcast_day, part, state, next_attempt_at, attempts, deadline, output_path,
       deadline_notified_at, by_person, settled
FROM jobs;

CREATE TABLE job_airings_new (
    job_id      INTEGER NOT NULL REFERENCES jobs_new (id) ON DELETE CASCADE,
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
INSERT INTO job_airings_new (job_id, position, airing_key, program_id, station_id, start_at, end_at,
                             title, performer, description, image_url, ts_in_ng, ts_out_ng, missing)
SELECT job_id, position, airing_key, program_id, station_id, start_at, end_at,
       title, performer, description, image_url, ts_in_ng, ts_out_ng, missing
FROM job_airings;

CREATE TABLE attempts_new (
    id                INTEGER PRIMARY KEY,
    job_id            INTEGER NOT NULL REFERENCES jobs_new (id) ON DELETE CASCADE,
    started_at        TEXT    NOT NULL,
    ended_at          TEXT    NOT NULL DEFAULT '',
    failed_stage      TEXT    NOT NULL DEFAULT '',
    error             TEXT    NOT NULL DEFAULT '',
    -- 全セグメントの #EXTINF の合計と、ffprobe で測った長さ（ミリ秒）
    extinf_total_ms   INTEGER NOT NULL DEFAULT 0,
    probe_duration_ms INTEGER NOT NULL DEFAULT 0
);
INSERT INTO attempts_new (id, job_id, started_at, ended_at, failed_stage, error, extinf_total_ms, probe_duration_ms)
SELECT id, job_id, started_at, ended_at, failed_stage, error, extinf_total_ms, probe_duration_ms
FROM attempts;

DROP TABLE job_airings;
DROP TABLE attempts;
DROP TABLE jobs;

ALTER TABLE jobs_new RENAME TO jobs;
ALTER TABLE job_airings_new RENAME TO job_airings;
ALTER TABLE attempts_new RENAME TO attempts;

CREATE INDEX jobs_reservation_day ON jobs (reservation_name, broadcast_day);
CREATE INDEX jobs_day ON jobs (broadcast_day);
CREATE INDEX jobs_due ON jobs (state, next_attempt_at);
CREATE INDEX attempts_job ON attempts (job_id);

-- 上の INSERT … SELECT で、sqlite_sequence に jobs の行（写した最大の id。0 行を写したときは 0）ができる
-- （migrate_job_seq_test.go で確かめている）。通知の job_id の方が大きければ上げる。
UPDATE sqlite_sequence
SET seq = (SELECT MAX(job_id) FROM notifications)
WHERE name = 'jobs' AND (SELECT MAX(job_id) FROM notifications) > seq;
-- 万一 jobs の行が無ければ、通知の job_id があるときだけ行を作る（どちらも無ければ作らない）。
INSERT INTO sqlite_sequence (name, seq)
SELECT 'jobs', MAX(job_id) FROM notifications
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'jobs')
HAVING MAX(job_id) IS NOT NULL;
