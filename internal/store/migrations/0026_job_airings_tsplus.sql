-- 放送回の tsplus_in_ng（番組表のフラグ。0 でなければタイムフリー30 の対象外として読む。domain.ExcludedFromTimeFree30）を持たせる。
-- 会員種別を保存したときに、まだ終わっていないジョブの録音期限を、DB の放送回から付け直すため（docs/adr/0030-radiko-login-follows-chosen-membership.md）。
-- これまでの放送回は 0（次の展開で、番組表の値に置き換わる。確定したジョブは置き換わらない）。
ALTER TABLE job_airings ADD COLUMN tsplus_in_ng INTEGER NOT NULL DEFAULT 0;
