# 保存の形は Jellyfin の音楽ライブラリに合わせたプリセットで決める

## 決定

保存先のパスとタグは、プリセットで決める。今あるのは Jellyfin の音楽ライブラリ向けのプリセットだけ。

- パスは `{放送局}/{番組}/{番組} YYYY-MM-DD.m4a`。パートがあれば放送日の後に `-N` を付ける。
- タグは、albumartist=放送局、album=番組の名前、title=放送日、date=放送日、disc=放送年、track=放送日の月日、genre=ラジオ、artist=出演者、comment=放送回の説明。
- 局名は radiko の局名（[ADR 0004](0004-station-name-from-radiko.md)）、番組の名前は radiko の番組名から写したもの（[ADR 0006](0006-show-name-from-radiko.md)）。
- title は `YYYY-MM-DD` で、放送回の番組名が番組の名前と違えば、その番組名を足す（[ADR 0007](0007-airing-title-in-title-tag.md)）。
- track は、分割番組でない番組では `MMDD`、分割番組の番組では `MMDD×10`（パートがあれば `MMDD×10+N`）。分割番組の設定は [ADR 0003](0003-job-per-show-and-broadcast-day.md) にある。

## 理由

- Jellyfin の音楽ライブラリは、albumartist と album でアルバムをまとめる。局を albumartist、番組を album にすれば、局の下に番組が 1 つのアルバムとして並ぶ。
- disc を年、track を月日にすれば、アルバムの中で放送日の順に並ぶ。
- 分割番組の track を 10 倍にすれば、つないだ日のファイル（`MMDD×10`）とつながなかった日のファイル（`MMDD×10+N`）が、同じアルバムで放送日の順に並ぶ。
- comment に録音の方式などを入れても、聴く人には役に立たない。放送回の説明の方が、何の回かが分かる。

## 検討した案

- 番組ごとのフォルダを作らず、局のフォルダに全部置く。Jellyfin のアルバムが局ごとに 1 つになり、番組で辿れない。
- 局名と番組名を、人が書いた対応表で決める。設定が増え、radiko の名前と離れる（[ADR 0004](0004-station-name-from-radiko.md)・[ADR 0006](0006-show-name-from-radiko.md)）。

## 帰結

- 保存済みのファイルのタグは、後から書き直さない。
- Jellyfin のほかの読み手（ファイルをそのまま読むプレイヤーなど）も、同じタグで並ぶ。ほかの構成のプリセットは、要るときに足す。
