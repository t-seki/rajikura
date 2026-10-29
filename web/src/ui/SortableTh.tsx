import { Table, UnstyledButton } from "@mantine/core";
import { nextSort, sortMark, type SortState } from "./sort";

type Props<K extends string> = {
  label: string;
  sortKey: K;
  sort: SortState<K>;
  onSort: (next: SortState<K>) => void;
};

// 押すと並べ替えが 昇順 → 降順 → 既定 と切り替わる表の見出し。
export function SortableTh<K extends string>({ label, sortKey, sort, onSort }: Props<K>) {
  const active = sort?.key === sortKey;
  return (
    <Table.Th aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}>
      <UnstyledButton fw="inherit" onClick={() => onSort(nextSort(sort, sortKey))}>
        {active ? `${label} ${sortMark(sort, sortKey)}` : label}
      </UnstyledButton>
    </Table.Th>
  );
}
