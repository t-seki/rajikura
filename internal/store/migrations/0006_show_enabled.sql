-- 番組に「有効か」を足す。無効な番組のルールは、番組表の同期で放送回に当てない。既定は有効。
ALTER TABLE shows ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
