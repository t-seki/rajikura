-- 毎週の予約の任意の終了日（放送日、"2006-01-02"）。これより後の放送日の放送回には当たらない（終了日の放送日は当たる）。NULL なら制限なし。
ALTER TABLE weekly ADD COLUMN until_day TEXT;
