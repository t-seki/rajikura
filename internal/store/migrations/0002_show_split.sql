-- 番組に「分割番組か」を足す（保存の track の決め方にだけ使う。連結するかとは独立）。
ALTER TABLE shows ADD COLUMN split INTEGER NOT NULL DEFAULT 0;
