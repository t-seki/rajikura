-- 番組名ルールを取り下げ、ルールを時刻ルール（局・曜日・開始時刻）だけにする。
-- 番組名ルールの行を消し、rules 表を作り直して kind・title・weekdays の列を消す。
-- rules を指す外部キーは無いので、作り直しても他の表は変わらない。
DELETE FROM rules WHERE kind = 'title';

CREATE TABLE rules_new (
    id         INTEGER PRIMARY KEY,
    show_id    INTEGER NOT NULL REFERENCES shows (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    station_id TEXT    NOT NULL,
    -- 曜日（0 = 日曜。放送日で数える）と開始時刻（HH:MM）
    weekday    INTEGER NOT NULL,
    start_hm   TEXT    NOT NULL,
    UNIQUE (show_id, position)
);

INSERT INTO rules_new (id, show_id, position, station_id, weekday, start_hm)
    SELECT id, show_id, position, station_id, weekday, start_hm FROM rules;

DROP TABLE rules;
ALTER TABLE rules_new RENAME TO rules;
