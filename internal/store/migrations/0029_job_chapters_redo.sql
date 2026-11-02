-- チャプターの入れ直し（人の指示で、録音済みのジョブの保存したファイルにチャプターを書き込ませる。
-- docs/adr/0033-chapters-written-into-saved-file.md）の頼みの印を、ジョブに持たせる。状態の値は増やさない（頼んでいる間も
-- done のまま。0024_job_chapters.sql と同じく、jobs を作り直さずに列を足す）。
--   chapters_redo_at: 人がチャプターの入れ直しを頼んだ時刻（頼みが無ければ ''）。API が書き、録音のループが拾って書き込んだら
--                     （取れずに書かなかった・書けなかったときも）'' に戻す。拾った後に頼み直されていれば（時刻が変わっていれば）
--                     消さない。
ALTER TABLE jobs ADD COLUMN chapters_redo_at TEXT NOT NULL DEFAULT '';
