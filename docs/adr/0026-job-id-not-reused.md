# ジョブの id は使い回さない

## 決定

`jobs.id` は `AUTOINCREMENT` の `INTEGER PRIMARY KEY` にし、一度振った id を別のジョブに振らない。

- `AUTOINCREMENT` の無い表から移るときは、マイグレーション 1 本で、`jobs`・`job_airings`・`attempts` を新しい表に写し、子（`job_airings`・`attempts`）→ 親（`jobs`）の順に消してから新しい表の名前を戻す。外部キーは有効のまま、1 つのトランザクションで行う。`AUTOINCREMENT` を付けるのは `jobs` だけで、列・CHECK・索引は作り直す前の定義のまま写す。
- `sqlite_sequence` の `jobs` の値は、作り直した時点のジョブの最大の id と、通知が覚えているジョブの id（`notifications.job_id`）の最大の大きい方にする。
- マイグレーションを当てる仕組み（`internal/store/store.go` の `migrate`。1 本ずつトランザクションで当てる）は変えない。

## 理由

- `AUTOINCREMENT` の無い `INTEGER PRIMARY KEY` では、SQLite は新しい行に「いまある最大の id + 1」を振る。展開はジョブを消して同じトランザクションで作り直すので、いちばん大きい id のジョブを消すと、作り直したジョブが同じ id になる。
- ジョブの id は Web UI の URL（`/jobs/{id}`。[ADR 0024](0024-web-ui-url-per-screen.md)）、CLI の `<id>`、通知の `link` で人の目に触れるので、使い回すと古い通知のリンクが別のジョブを開きうる。
- `sqlite_sequence` を通知の覚えている id まで進めるのは、作り直す前に消えていたジョブの id を、通知のリンクが指したまま別のジョブに振らないため。
- 子の表を先に消せば、`jobs` を消すときに `ON DELETE CASCADE` で消える行が無いので、外部キーを止めずに作り直せる。新しい表の名前を戻すと、子の表の `REFERENCES` も戻した名前に書き換わる。
- `PRAGMA foreign_keys` はトランザクションの中では変えられないので、外部キーを止めて `jobs` だけを作り直すには、`migrate` にトランザクションの外で止める仕組みを足すことになる。
- `AUTOINCREMENT` は、キーワード・通知・API トークンの表と同じやり方。

## 検討した案

- 外部キーを止めて `jobs` だけを作り直す。`migrate` に、専用の接続でトランザクションの外から外部キーを止める仕組みが要る。
- 作り直さず、使った最大の id を別の行に覚え、ジョブを作るときに id を明示して振る。ジョブを作る道を足したときに、覚えた値を通し忘れうる。

## 帰結

- 消したジョブの id は欠番になり、id は飛び飛びになる。
- `jobs` の定義（CHECK など）を変えるには、この手順のように子の表ごと作り直すマイグレーションが要る。
