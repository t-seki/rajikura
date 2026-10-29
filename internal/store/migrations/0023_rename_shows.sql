-- 言葉の付け替え（docs/adr/0006-show-name-from-radiko.md）に合わせて、表と列の名前を新しい言葉に揃える。データは残す。
-- 録るもののまとまりを「番組」と呼ぶので、reservations → shows、番組を指す列（reservation_id・reservation_name・reservation）
-- → show_id・show_name・show にする。weekly・onces の表の名前はそのまま。
-- ALTER TABLE ... RENAME は、ほかの表の外部キーと索引が指す表と列の名前も書き換える（migrate_show_test.go の
-- TestMigrateRenameShows で確かめている）。索引の名前は書き換わらないので、作り直す。
ALTER TABLE reservations RENAME TO shows;

ALTER TABLE weekly RENAME COLUMN reservation_id TO show_id;
ALTER TABLE onces RENAME COLUMN reservation_id TO show_id;

ALTER TABLE jobs RENAME COLUMN reservation_name TO show_name;
DROP INDEX jobs_reservation_day;
CREATE INDEX jobs_show_day ON jobs (show_name, broadcast_day);

ALTER TABLE keyword_airings RENAME COLUMN reservation_name TO show_name;
ALTER TABLE unmatched_days RENAME COLUMN reservation_name TO show_name;
ALTER TABLE notifications RENAME COLUMN reservation TO show;
