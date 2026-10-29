-- 通知の重複を防ぐための記録を足す。
-- jobs.deadline_notified_at: 期限間近を通知した時刻（送れたときだけ立てる。空なら未通知）。
-- notify_state: 通知の記録（キーと値）。日次サマリを最後に送った日（daily_summary_sent_on）を持つ。
ALTER TABLE jobs ADD COLUMN deadline_notified_at TEXT NOT NULL DEFAULT '';

CREATE TABLE notify_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
