-- 予約の分割番組の設定を 1 つの値（分割番組でない・分割番組でつなぐ・分割番組でつながない）にする（
-- docs/adr/0003-job-per-show-and-broadcast-day.md）。列は split と join_airings の 2 つのまま使い回す
-- （reservations を作り直すと、weekly と onces の子の行が消えうる。0012_drop_job_skip.sql と同じ事情）。
--   split = 0                     : 分割番組でない（連結しない）
--   split = 1, join_airings = 1   : 分割番組でつなぐ
--   split = 1, join_airings = 0   : 分割番組でつながない
-- これまでの予約は、split がオフなら join_airings によらず分割番組でない（連結しない）に移す。
UPDATE reservations SET join_airings = 0 WHERE split = 0;
