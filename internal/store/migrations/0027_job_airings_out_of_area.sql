-- 放送回の tsplus_out_ng（番組表のフラグ）と、エリアの外から録るかの印（out_of_area。domain.Airing.OutOfArea）を持たせる。
-- 印は、展開のたびにその時点の自分のエリアで決め、展開が触るジョブでは書き直す。印が立っていれば ts_out_ng・tsplus_out_ng、立っていなければ
-- ts_in_ng・tsplus_in_ng で、タイムフリーで録れるかと録音期限を決める（domain.Airing.TimeFreeExcluded・domain.ExcludedFromTimeFree30）。
-- これまでの放送回は 0（自分のエリアから録る扱い。次の展開で、番組表の値に置き換わる。確定したジョブは置き換わらない）。
ALTER TABLE job_airings ADD COLUMN tsplus_out_ng INTEGER NOT NULL DEFAULT 0;
ALTER TABLE job_airings ADD COLUMN out_of_area INTEGER NOT NULL DEFAULT 0;
