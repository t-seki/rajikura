-- 毎週のスケジュールの任意の開始日（放送日、"2006-01-02"）。これより前の放送日の放送回には当たらない。NULL なら制限なし。
ALTER TABLE weekly ADD COLUMN from_day TEXT;
