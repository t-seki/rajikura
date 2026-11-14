import { describe, expect, it } from "vitest";
import { currentYearJst, dayLabel } from "../guide/format";
import { formatDateTime } from "./format";

const at = (s: string) => new Date(s);

describe("dayLabel（放送日の日付）", () => {
  it("今年の放送日には年が付かず、曜日が付く", () => {
    expect(dayLabel("2026-04-15", at("2026-06-01T00:00:00+09:00"))).toBe("4/15(水)");
  });

  it("今年でない放送日にだけ年が付く（月・日は 0 で埋めない）", () => {
    expect(dayLabel("2025-12-30", at("2026-01-01T00:10:00+09:00"))).toBe("2025/12/30(火)");
    expect(dayLabel("2025-12-31", at("2026-01-01T00:10:00+09:00"))).toBe("2025/12/31(水)");
    expect(dayLabel("2026-01-01", at("2026-01-01T00:10:00+09:00"))).toBe("1/1(木)");
    // 今が 2025 年なら、2026-01-01 に年が付き、2025-12-31 には付かない。
    expect(dayLabel("2026-01-01", at("2025-12-31T12:00:00+09:00"))).toBe("2026/1/1(木)");
    expect(dayLabel("2025-12-31", at("2025-12-31T12:00:00+09:00"))).toBe("12/31(水)");
  });

  it("「今年」は日本時間で決める", () => {
    // 2025-12-31T15:30:00Z は日本時間 2026-01-01 00:30。
    const now = at("2025-12-31T15:30:00Z");
    expect(currentYearJst(now)).toBe(2026);
    expect(dayLabel("2026-01-01", now)).toBe("1/1(木)");
    expect(dayLabel("2025-12-31", now)).toBe("2025/12/31(水)");
  });
});

describe("formatDateTime（暦の日時）", () => {
  const now = at("2026-01-01T00:30:00+09:00");

  it("日本時間の「M/D(曜) HH:MM」で出す", () => {
    expect(formatDateTime("2026-04-15T01:55:00Z", at("2026-06-01T00:00:00+09:00"))).toBe("4/15(水) 10:55");
    expect(formatDateTime("2026-01-01T00:10:00+09:00", now)).toBe("1/1(木) 00:10");
  });

  it("今年（日本時間）でなければ年が付く。年をまたぐ例", () => {
    expect(formatDateTime("2025-12-31T23:10:00+09:00", now)).toBe("2025/12/31(水) 23:10");
    expect(formatDateTime("2025-12-30T23:10:00+09:00", now)).toBe("2025/12/30(火) 23:10");
    // 今が 2025-12-31 なら、2026-01-01 に年が付き、2025-12-31 には付かない。
    const before = at("2025-12-31T12:00:00+09:00");
    expect(formatDateTime("2026-01-01T00:10:00+09:00", before)).toBe("2026/1/1(木) 00:10");
    expect(formatDateTime("2025-12-31T23:10:00+09:00", before)).toBe("12/31(水) 23:10");
  });

  it("日付は日本時間の暦で決める（UTC の 15:30 は翌日）", () => {
    expect(formatDateTime("2025-12-31T15:30:00Z", now)).toBe("1/1(木) 00:30");
  });

  it("日時でない値は - にする", () => {
    expect(formatDateTime("x")).toBe("-");
  });
});
