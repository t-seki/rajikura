import { Badge } from "@mantine/core";
import type { JobChapters } from "./jobsApi";

/**
 * 一覧で、チャプターを待っているジョブ（waiting）と入らなかったジョブ（not_added）に出す印（ADR 0027）。
 * 入った（added）と値なしには何も出さない。色は状態のバッジ（StateBadge）の色とライブ録音の印（LiveBadge、violet）と紛れないものにする。
 */
export function ChaptersBadge({ chapters }: { chapters: JobChapters | undefined }) {
  if (chapters === "waiting") {
    return (
      <Badge color="cyan" variant="light">
        チャプター待ち
      </Badge>
    );
  }
  if (chapters === "not_added") {
    return (
      <Badge color="pink" variant="light">
        チャプターなし
      </Badge>
    );
  }
  return null;
}
