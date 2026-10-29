-- 言葉の付け替えに合わせて、表と列の名前を新しい言葉に揃える。データは残す。
-- 番組 → 予約（shows → reservations）、ルール → 毎週（rules → weekly）、単発予約 → 1 回（oneshots → onces）、
-- 番組を指す列（show_id・show_name）→ 予約を指す列（reservation_id・reservation_name）。
-- ALTER TABLE ... RENAME は、ほかの表の外部キーと索引が指す表と列の名前も書き換える（migrate_rename_test.go の TestMigrateRenameReservations で確かめている）。
-- 索引の名前は書き換わらないので、作り直す。
ALTER TABLE shows RENAME TO reservations;

ALTER TABLE rules RENAME TO weekly;
ALTER TABLE weekly RENAME COLUMN show_id TO reservation_id;

ALTER TABLE oneshots RENAME TO onces;
ALTER TABLE onces RENAME COLUMN show_id TO reservation_id;
DROP INDEX oneshots_start;
CREATE INDEX onces_start ON onces (start_at);

ALTER TABLE jobs RENAME COLUMN show_name TO reservation_name;
DROP INDEX jobs_show_day;
CREATE INDEX jobs_reservation_day ON jobs (reservation_name, broadcast_day);
