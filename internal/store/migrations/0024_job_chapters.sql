-- チャプターの取り直し（docs/adr/0027-chapters-refetched-by-rerecording.md）のために、ジョブにチャプターの値と待ちを持たせる。
-- 状態の値は増やさない（待っている間も done のまま。0018_job_by_person.sql と同じく、jobs を作り直さずに列を足す）。
--   chapters:          保存したファイルにチャプターが入ったか（'' は値なし、'added' は入った、'waiting' は待っている、
--                      'not_added' は入らなかった）。これまでのジョブは値なし。
--   chapters_check_at: 待っているジョブが次にチャプターの API を引く時刻（待っていなければ ''）。
--   chapters_refetch:  今の試行がチャプターの取り直しで起こしたものである印（1 なら、録れなくても録音済みに戻し、通知しない）。
--                      DB に持つので、起動時の回復で retrying に戻しても消えない。
--   job_airings.chapter_wait: 待っている放送回の様子（0 は待っていない、1 はまだ取れていない、2 は取れるようになった）。
ALTER TABLE jobs ADD COLUMN chapters TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN chapters_check_at TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN chapters_refetch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE job_airings ADD COLUMN chapter_wait INTEGER NOT NULL DEFAULT 0;
