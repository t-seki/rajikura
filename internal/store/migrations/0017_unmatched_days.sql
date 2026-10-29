-- 当たらない日（用語集）の記録。番組表の同期と予約を変えた直後の展開が、局・放送日ごとの展開と同じ番組表から求めて書く。
-- 予約の一覧（GET /api/reservations）に付け、まだ通知していないものを予約の出来事（unmatched_day）として通知する。
-- unmatched_days:
--   reservation_name: 予約の名前（予約の名前を変えたら付け替える）
--   station_id: 局
--   day: 放送日（YYYY-MM-DD）。放送日が過ぎた行は消す
--   start_at: 枠の開始時刻（毎週のスケジュールの開始時刻）
--   covering: その時刻を含む放送回の JSON（title・start・end）。無ければ空
--   titled: 同じ局・同じ放送日で、番組名が予約の名前で始まる放送回の JSON の配列（title・start・end）
--   active: 1 なら今も当たらない日。0 は、通知した後に当たるようになった枠（通知を重ねないために、放送日が過ぎるまで残す）
--   notified_at: 通知した時刻（空ならまだ）。通知を DB に書くのと同じトランザクションで立てる
CREATE TABLE unmatched_days (
    reservation_name TEXT NOT NULL,
    station_id       TEXT NOT NULL,
    day              TEXT NOT NULL,
    start_at         TEXT NOT NULL,
    covering         TEXT NOT NULL DEFAULT '',
    titled           TEXT NOT NULL DEFAULT '[]',
    active           INTEGER NOT NULL,
    notified_at      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (reservation_name, station_id, start_at)
);
CREATE INDEX unmatched_days_station_day ON unmatched_days (station_id, day);
