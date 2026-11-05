-- 保存先の移し（docs/adr/0039-saved-files-follow-show-rename.md）の移し先の控えを持たせる（0030_output_moves.sql と同じく、jobs を
-- 作り直さずに列を足す）。
--   jobs.move_to: 移し先の控え（無ければ ''）。録音のループが、移すファイルを書いて確かめた後、移し先に置く直前に書き、移しの結果を
--                 書くとき（store.FinishOutputMove。同じトランザクション）に '' に戻す。置かなかった移し（移し先にファイルがある
--                 など）では書かない。控えが残ったジョブを次に拾ったら、控えの先とファイルの様子から回復する（置いた後、保存先の
--                 記録を付け替える前に止まった移しを、保存先の記録とファイルのずれのまま残さないため）。
--   output_moves.result には 'album_rewritten'（移し先が今の保存先と同じなので、その場でアルバムの名前だけを書き換えた。from_path と
--   to_path は同じ）が加わる（列の型は変えない）。
ALTER TABLE jobs ADD COLUMN move_to TEXT NOT NULL DEFAULT '';
