import { Table, Text, UnstyledButton } from "@mantine/core";
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
  const chars = Array.from(label);
  // 短い見出しは全体を折り返さない。長い見出しは最後の文字と印だけをまとめ、途中では折り返せる
  const tail = chars.length <= 6 ? chars.splice(0).join("") : (chars.pop() ?? "");
  const head = chars.join("");
  return (
    <Table.Th aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}>
      <UnstyledButton fw="inherit" onClick={() => onSort(nextSort(sort, sortKey))}>
        {head}
        {/* 見出しの最後の文字と印を 1 つのかたまりにして、印だけが次の行に落ちないようにする */}
        <span data-testid="sort-tail" style={{ whiteSpace: "nowrap" }}>
          {tail}
          <Text span aria-hidden="true" ml={4} c={active ? undefined : "dimmed"} opacity={active ? 1 : 0.5}>
            {sortMark(sort, sortKey)}
          </Text>
        </span>
      </UnstyledButton>
    </Table.Th>
  );
}
