-- チャプターを、音声を取り直さずに保存したファイルに書き込む（docs/adr/0033-chapters-written-into-saved-file.md）ために、
-- チャプターの書き込みの履歴と、連結したファイルのパートの長さを持たせる。jobs・job_airings・attempts の列は変えない
-- （新しい表にする。0022_job_autoincrement.sql のように jobs を作り直す移行では、ON DELETE CASCADE で行が消えるので、
-- その移行で写し直すこと）。
--
--   chapter_writes: 書き込みのたびに 1 行（試行の履歴 attempts とは別）。
--     written_at:   書き込んだ（書かなかった・失敗した）時刻。
--     requested_by: 誰が。'refetch' はチャプターの取り直し（rajikura が自分で行う）、'redo' はチャプターの入れ直し（人の指示）。
--     result:       'written' は書き込んだ、'not_fetched' は取れなかったので書かなかった、'failed' は書き込めなかった。
--     count:        書き込んだチャプターの件数（written のときだけ）。
--     error:        書き込めなかった理由（failed のときだけ）。
--   job_part_lengths: 連結したファイル（放送回が 2 つ以上）を保存したときの、放送回（パート）ごとのファイルの中の長さ（ミリ秒）。
--     2 つ目以降のパートのチャプターを、前のパートの長さだけずらすのに使う。この移行より前に保存したジョブには無い
--     （番組表の放送時間で代える）。保存のたびに置き換える。
CREATE TABLE chapter_writes (
    id           INTEGER PRIMARY KEY,
    job_id       INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    written_at   TEXT    NOT NULL,
    requested_by TEXT    NOT NULL,
    result       TEXT    NOT NULL,
    count        INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX chapter_writes_job ON chapter_writes (job_id, written_at);

CREATE TABLE job_part_lengths (
    job_id     INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    airing_key TEXT    NOT NULL,
    length_ms  INTEGER NOT NULL,
    PRIMARY KEY (job_id, airing_key)
);

-- 前の版のチャプターの取り直しは、録り直しと同じく試行を起こしていた（jobs.chapters_refetch = 1 の retrying・running）。
-- この版はその印を読まないので、そのままだと人の録り直しと同じに扱われ、音声を取り直して前のファイルを上書きし、録音済みを
-- 通知してしまう。そこで、取り直しの試行の途中のジョブは録音済み（done）に戻し、保存した放送回（欠落の無いもの）を全部
-- 「取れるようになった」かを確かめ直す待ちにして、次の見回りですぐ API を引かせる（全部取れれば、保存したファイルに書き込む）。
-- 保存先・欠落の印は前のまま。試行回数は取り直しで 0 に戻した値のまま。列 chapters_refetch は残すが、この版からは読み書きしない。
UPDATE job_airings SET chapter_wait = 1
WHERE missing = 0 AND job_id IN (SELECT id FROM jobs WHERE chapters_refetch != 0 AND state IN ('retrying', 'running'));
UPDATE jobs SET state = 'done', chapters = 'waiting', chapters_check_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), chapters_refetch = 0
WHERE chapters_refetch != 0 AND state IN ('retrying', 'running');
