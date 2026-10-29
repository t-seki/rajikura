-- 番組に「番組名の食い違いを調べるか」を足す（日次サマリにだけ使う。既定は調べる）。
ALTER TABLE shows ADD COLUMN check_title INTEGER NOT NULL DEFAULT 1;
