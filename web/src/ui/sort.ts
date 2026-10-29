export type SortDirection = "asc" | "desc";
// null は、画面が決めた既定の並び。
export type SortState<K extends string> = { key: K; dir: SortDirection } | null;

// 同じ列を押すたびに 昇順 → 降順 → 既定 と進む。別の列を押したら、その列の昇順から始める。
export function nextSort<K extends string>(current: SortState<K>, key: K): SortState<K> {
  if (current?.key !== key) return { key, dir: "asc" };
  if (current.dir === "asc") return { key, dir: "desc" };
  return null;
}

// 並べ替えの矢印。いま並べている列だけに付く。
export function sortMark<K extends string>(current: SortState<K>, key: K): string {
  if (current?.key !== key) return "";
  return current.dir === "asc" ? "▲" : "▼";
}
