import { Badge } from "@mantine/core";

/**
 * ライブ録音のジョブ（API の live）の印。番組表の「◎ライブで録る」と ◎ で対応を取り、色は状態のバッジ（StateBadge）と紛れないものにする。
 * 録る前のジョブの値は、その時点の番組表での見込み（番組表の同期で変わりうる）。
 */
export function LiveBadge() {
  return (
    <Badge color="violet" variant="light">
      ◎ライブ録音
    </Badge>
  );
}
