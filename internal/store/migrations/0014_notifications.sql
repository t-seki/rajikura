-- 通知を 1 件ずつ DB の行にする（ADR 0011）。通知を作る処理はここに書くだけで、通知先には録音のループの周ごとに送る。
-- notifications:
--   kind: 通知の種類（一覧は notify.Kinds が正。種類を足しても列の形は変わらないので、ここには列挙しない）
--   anomaly: サービスの異常の行（始まりと復旧）なら、どの異常か（sync・out_of_area）。ジョブの出来事は空
--   message: 本文
--   created_at: 作った時刻
--   deliver: 通知先に送るか（0 = 送らない。通知先なしのときと、notify.success = false の録音済み）
--   sent_at: 通知先に送れた時刻（空ならまだ）
--   failed_at: 作ってから 1 時間を過ぎた後に送ろうとして送れず、諦めた時刻（「送れなかった」の印。空なら諦めていない）
--   read: 既読（Web UI の履歴で使う）
CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL,
    anomaly    TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    deliver    INTEGER NOT NULL,
    sent_at    TEXT NOT NULL DEFAULT '',
    failed_at  TEXT NOT NULL DEFAULT '',
    read       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notifications_pending ON notifications (deliver, sent_at, failed_at, id);
CREATE INDEX notifications_created_at ON notifications (created_at);
CREATE INDEX notifications_anomaly ON notifications (anomaly, id);

-- 日次サマリをやめた（ADR 0010）。最後に送った日（daily_summary_sent_on）だけを持っていた notify_state を消す。
DROP TABLE notify_state;
