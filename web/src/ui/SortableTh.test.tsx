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
const renderSingle = (label: string) =>
  render(
    <MantineProvider>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <SortableTh label={label} sortKey="a" sort={null} onSort={() => {}} />
          </Table.Tr>
        </Table.Thead>
      </Table>
    </MantineProvider>,
  );

describe("SortableTh", () => {
  it("押すたびに 昇順 → 降順 → 既定 と切り替わり、向きを見出しに出す", async () => {
    renderHarness();
    expect(th("列A")).toHaveAttribute("aria-sort", "none");
    expect(th("列A")).toHaveTextContent("↕");
    expect(th("列B")).toHaveTextContent("↕");
    await userEvent.click(screen.getByRole("button", { name: "列A" }));
    expect(th("列A")).toHaveAttribute("aria-sort", "ascending");
    expect(th("列A")).toHaveTextContent("↑");
    expect(th("列A")).not.toHaveTextContent(/[↕↓]/);
    await userEvent.click(screen.getByRole("button", { name: "列A" }));
    expect(th("列A")).toHaveAttribute("aria-sort", "descending");
    expect(th("列A")).toHaveTextContent("↓");
    expect(th("列A")).not.toHaveTextContent(/[↕↑]/);
    await userEvent.click(screen.getByRole("button", { name: "列A" }));
    expect(th("列A")).toHaveAttribute("aria-sort", "none");
    expect(th("列A")).toHaveTextContent("↕");
    expect(th("列A")).not.toHaveTextContent(/[↑↓]/);
    expect(th("列B")).toHaveTextContent("↕");
  });

  it("短い見出しは、見出しの全体と印が、折り返さない 1 つのかたまりに入る", () => {
    renderHarness();
    const tails = screen.getAllByTestId("sort-tail");
    expect(tails).toHaveLength(2);
    expect(tails[0]).toHaveStyle({ whiteSpace: "nowrap" });
    expect(tails[0].textContent).toBe("列A↕");
    expect(tails[1].textContent).toBe("列B↕");
  });

  it("長い見出しは、最後の文字と印だけがかたまりに入り、残りの文字は途中で折り返せる", () => {
    renderSingle("次の試行・録音期限");
    const tail = screen.getByTestId("sort-tail");
    expect(tail).toHaveStyle({ whiteSpace: "nowrap" });
    // 印の前で改行できないよう、最後の文字と印が同じかたまりにある
    expect(tail.textContent).toBe("限↕");
    // かたまりの外の文字（ボタン）は nowrap にせず、途中で折り返せる
    const button = screen.getByRole("button", { name: "次の試行・録音期限" });
    expect(button).not.toHaveStyle({ whiteSpace: "nowrap" });
    expect(button.textContent).toBe("次の試行・録音期限↕");
  });

  it("別の列を押すと、その列の昇順から始める", async () => {
    renderHarness();
    await userEvent.click(screen.getByRole("button", { name: "列A" }));
    await userEvent.click(screen.getByRole("button", { name: "列A" }));
    await userEvent.click(screen.getByRole("button", { name: "列B" }));
    expect(th("列A")).toHaveAttribute("aria-sort", "none");
    expect(th("列B")).toHaveAttribute("aria-sort", "ascending");
  });
});
