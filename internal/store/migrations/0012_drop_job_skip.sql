-- ジョブのスキップを無くす。skipped のジョブは expired（録音できなかった）にする。
-- jobs.state の CHECK には 'skipped' を残す。CHECK を外すには jobs を作り直す（DROP TABLE jobs）必要があるが、
-- job_airings と attempts は jobs を ON DELETE CASCADE で参照していて、store は外部キーを有効にして開くので、
-- 作り直すと録音の記録が消える。Go のコードは skipped を作らず、読まない。
UPDATE jobs SET state = 'expired' WHERE state = 'skipped';
