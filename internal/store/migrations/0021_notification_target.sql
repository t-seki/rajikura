-- 通知に、該当のジョブ・予約を持たせる（docs/adr/0024-web-ui-url-per-screen.md の「通知から画面を開く」）。Web UI の通知の画面とトレイの
-- トーストが開く画面（GET /api/notifications の link）は、server がこの 2 つの列と通知の種類から組み立てる。
--   job_id: ジョブの出来事の通知なら、そのジョブの id。それ以外は NULL
--   reservation: キーワードの出来事（keyword_added）と当たらない日（unmatched_day）の通知なら、その予約の名前（予約の名前を
--                変えたら付け替える）。それ以外は NULL
-- 外部キーは張らない（指す先のジョブ・予約が消えても通知の行は残す）。既にある行は NULL のままにし、本文から埋め戻さない。
ALTER TABLE notifications ADD COLUMN job_id INTEGER NULL;
ALTER TABLE notifications ADD COLUMN reservation TEXT NULL;
