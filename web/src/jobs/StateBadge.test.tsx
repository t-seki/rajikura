import { MantineProvider } from "@mantine/core";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ChaptersBadge } from "./ChaptersBadge";
import { LiveBadge } from "./LiveBadge";
import { StateBadge } from "./StateBadge";

const wrap = (ui: React.ReactElement) => render(<MantineProvider>{ui}</MantineProvider>);

describe("StateBadge", () => {
  it.each([
    ["retrying", "録音予定（失敗・再試行待ち）"],
    ["expired", "録音できなかった（期限切れ）"],
  ] as const)("%s の全文を、「…」で切らず折り返して出す", (state, text) => {
    wrap(<StateBadge job={{ state, settled: true }} />);
    const label = screen.getByText(text);
    expect(label).toHaveStyle({ whiteSpace: "normal", textOverflow: "clip" });
    expect(label).not.toHaveStyle({ whiteSpace: "nowrap" });
    expect(label).not.toHaveStyle({ textOverflow: "ellipsis" });
    // root の高さが 1 行分に決まったままだと、overflow: hidden で 2 行目が隠れる。高さは文字に合わせて伸びる。
    const root = label.closest(".mantine-Badge-root");
    expect(root).toBeInstanceOf(HTMLElement);
    // jsdom の computed の height は指定が無くても auto になるので、styles で付けた inline の指定を見る。
    expect((root as HTMLElement).style.height).toBe("auto");
  });

  it("ライブ録音とチャプターの印も、折り返す", () => {
    wrap(
      <>
        <LiveBadge />
        <ChaptersBadge chapters="not_added" />
      </>,
    );
    for (const t of ["◎ライブ録音", "チャプターなし"]) {
      expect(screen.getByText(t)).toHaveStyle({ whiteSpace: "normal", textOverflow: "clip" });
      expect(screen.getByText(t)).not.toHaveStyle({ whiteSpace: "nowrap" });
      expect(screen.getByText(t)).not.toHaveStyle({ textOverflow: "ellipsis" });
      expect((screen.getByText(t).closest(".mantine-Badge-root") as HTMLElement).style.height).toBe("auto");
    }
  });
});
