-- 今日から先の放送回も番組名で録らない予定にし、人も録る・録らないを決められるようにする（docs/adr/0019-skip-by-title-and-by-person.md）。
-- 状態の値は増やさない（jobs.state の CHECK を変えるには jobs を作り直す必要があり、job_airings と attempts が
-- ON DELETE CASCADE で消える。0012_drop_job_skip.sql）。代わりに真偽値の列を 2 つ足す。
--   by_person: 人が録る・録らないを決めた印（1 なら、展開が番組名で状態を変えない）。一度付けたら外さない。
--   settled:   skipped のジョブが確定したか（0 なら録らない予定、1 なら録らなかった）。skipped でないジョブでは使わない。
-- これまでの skipped のジョブは、今日より前の放送日で作った時点で確定しているので settled を 1 にする。
ALTER TABLE jobs ADD COLUMN by_person INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN settled INTEGER NOT NULL DEFAULT 0;
UPDATE jobs SET settled = 1 WHERE state = 'skipped';
