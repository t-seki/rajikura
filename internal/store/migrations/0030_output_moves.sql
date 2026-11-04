-- 保存先の移し（番組の名前を変えたとき、または人が頼んだときに、ジョブの保存したファイルを今の番組の名前のフォルダ・ファイル名に
-- 移すこと。docs/adr/0039-saved-files-follow-show-rename.md）の頼みの印と、移しの履歴を持たせる。状態の値は増やさない
-- （頼んでいる間もジョブの状態は変えない。0029_job_chapters_redo.sql と同じく、jobs を作り直さずに列を足す）。
--   jobs.move_requested_at: 保存先の移しを頼んだ時刻（頼みが無ければ ''）。名前の変更（store.ReplaceShow）と人の頼み（API）が書き、
--                           録音のループが拾って移したら（移さなかった・移せなかったときも）'' に戻す。拾った後に頼み直されていれば
--                           （時刻が変わっていれば）消さない。
--   output_moves: 移しのたびに 1 行（試行の履歴 attempts・チャプターの書き込みの履歴 chapter_writes とは別）。
--     moved_at:  移した（移さなかった・移せなかった）時刻。
--     from_path: 移す前の保存先。
--     to_path:   移し先（保存先のパスが番組の名前の形でなく、移し先を組めなかったときは ''）。
--     result:    'moved' は移した、'target_exists' は移し先に同じ名前のファイルがあるので移さなかった、'no_file' は保存先にファイルが
--                無いので移さなかった、'not_show_path' は保存先のパスが番組の名前の形でないので移さなかった、'failed' は移せなかった。
--     error:     移せなかった理由（failed のときだけ）。
ALTER TABLE jobs ADD COLUMN move_requested_at TEXT NOT NULL DEFAULT '';

CREATE TABLE output_moves (
    id        INTEGER PRIMARY KEY,
    job_id    INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    moved_at  TEXT    NOT NULL,
    from_path TEXT    NOT NULL,
    to_path   TEXT    NOT NULL DEFAULT '',
    result    TEXT    NOT NULL,
    error     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX output_moves_job ON output_moves (job_id, moved_at);
