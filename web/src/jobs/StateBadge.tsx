import { Badge } from "@mantine/core";
import { viewState, viewStateLabel, type JobState, type JobViewState } from "./jobsApi";

const stateColor: Record<JobViewState, string> = {
  waiting: "gray",
  skip_planned: "yellow",
  running: "blue",
  retrying: "orange",
  done: "green",
  expired: "red",
  skipped: "yellow",
};

/** ジョブの状態のバッジ。録らない予定（確定の前の skipped）は録らなかったと同じ色の枠だけで出す。 */
export function StateBadge({ job }: { job: { state: JobState; settled: boolean } }) {
  const s = viewState(job);
  return (
    <Badge color={stateColor[s]} variant={s === "skip_planned" ? "outline" : "filled"}>
      {viewStateLabel(s)}
    </Badge>
  );
}
