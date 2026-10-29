-- セッションの ID を、DB には SHA-256 の hash で置く（API トークンと同じ置き方）。cookie には今までどおり元の ID を渡す。
-- DB のファイル（手で写した .db、NFS のスナップショットなど）が外に出ても、期限の切れていないセッションでログインした状態にならないようにする。
-- 元の ID を SQL の中で hash に置き換えることはできないので、sessions を作り直して今あるセッションは消す。この版に上げると、一度ログインし直す。
DROP TABLE sessions;
CREATE TABLE sessions (
    id_hash    TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
