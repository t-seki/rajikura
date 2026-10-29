import { MantineProvider, Table } from "@mantine/core";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { SortableTh } from "./SortableTh";
import type { SortState } from "./sort";

function Harness() {
  const [sort, setSort] = useState<SortState<"a" | "b">>(null);
  return (
    <Table>
      <Table.Thead>
        <Table.Tr>
          <SortableTh label="列A" sortKey="a" sort={sort} onSort={setSort} />
          <SortableTh label="列B" sortKey="b" sort={sort} onSort={setSort} />
        </Table.Tr>
      </Table.Thead>
    </Table>
  );
}

const th = (name: string) => screen.getByRole("columnheader", { name: new RegExp(name) });
const renderHarness = () =>
  render(
    <MantineProvider>
      <Harness />
    </MantineProvider>,
  );

describe("SortableTh", () => {
  it("押すたびに 昇順 → 降順 → 既定 と切り替わり、向きを見出しに出す", async () => {
    renderHarness();
    expect(th("列A")).toHaveAttribute("aria-sort", "none");
    expect(screen.getByRole("button", { name: "列A" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /列A/ }));
    expect(th("列A")).toHaveAttribute("aria-sort", "ascending");
    expect(th("列A")).toHaveTextContent("▲");
    await userEvent.click(screen.getByRole("button", { name: /列A/ }));
    expect(th("列A")).toHaveAttribute("aria-sort", "descending");
    expect(th("列A")).toHaveTextContent("▼");
    await userEvent.click(screen.getByRole("button", { name: /列A/ }));
    expect(th("列A")).toHaveAttribute("aria-sort", "none");
    expect(th("列A")).not.toHaveTextContent(/[▲▼]/);
  });

  it("別の列を押すと、その列の昇順から始める", async () => {
    renderHarness();
    await userEvent.click(screen.getByRole("button", { name: /列A/ }));
    await userEvent.click(screen.getByRole("button", { name: /列A/ }));
    await userEvent.click(screen.getByRole("button", { name: /列B/ }));
    expect(th("列A")).toHaveAttribute("aria-sort", "none");
    expect(th("列B")).toHaveAttribute("aria-sort", "ascending");
  });
});
