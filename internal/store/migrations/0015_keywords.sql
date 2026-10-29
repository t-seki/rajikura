-- キーワード（用語集の「キーワード」、ADR 0014）。予約の外にあり、当たった放送回を予約の 1 回のスケジュールとして足す。
-- keywords:
--   words: 語（空白で区切る）。words_key は語を見分ける形（domain.KeywordKey）で、同じ形のキーワードは 1 つだけ
--   exclude: 除外する語（空なら外さない）
--   stations: 当てる局の ID をカンマでつないだもの（空ならエリアの全局）
--   enabled: 0 なら無効なキーワード（当てない）
--   limit_notified: 上限で足すのを止めたことを通知した印（上限を下回ったら下ろす）
-- id は AUTOINCREMENT で、消したキーワードの ID を使い回さない（キーワードどうしの重なりで、ID の小さい方＝先に作った方が足す）。
CREATE TABLE keywords (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    words          TEXT    NOT NULL,
    words_key      TEXT    NOT NULL UNIQUE,
    exclude        TEXT    NOT NULL DEFAULT '',
    stations       TEXT    NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    limit_notified INTEGER NOT NULL DEFAULT 0
);

-- keyword_airings: キーワードが足した放送回の記録（局と開始時刻）。記録にある放送回には、もう足さない
-- （人が消した・別の予約へ移した回を足し直さない）。キーワードを消すと一緒に消える。
-- station_name・end_at・title・reservation_name は、足したときの番組表と足した先の予約の名前（一覧に出すため）。
CREATE TABLE keyword_airings (
    id               INTEGER PRIMARY KEY,
    keyword_id       INTEGER NOT NULL REFERENCES keywords (id) ON DELETE CASCADE,
    station_id       TEXT    NOT NULL,
    station_name     TEXT    NOT NULL DEFAULT '',
    start_at         TEXT    NOT NULL,
    end_at           TEXT    NOT NULL,
    title            TEXT    NOT NULL,
    reservation_name TEXT    NOT NULL,
    added_at         TEXT    NOT NULL,
    UNIQUE (keyword_id, station_id, start_at)
);
CREATE INDEX keyword_airings_airing ON keyword_airings (station_id, start_at);

-- onces.keyword_id: その 1 回のスケジュールを足したキーワード。人が足したものは NULL。キーワードを消したら NULL に戻す。
ALTER TABLE onces ADD COLUMN keyword_id INTEGER REFERENCES keywords (id) ON DELETE SET NULL;
