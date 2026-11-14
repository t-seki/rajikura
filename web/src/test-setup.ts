import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

afterEach(cleanup);

// Mantine が使う。jsdom には無い。
window.matchMedia ??= ((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener() {},
  removeEventListener() {},
  addListener() {},
  removeListener() {},
  dispatchEvent: () => false,
})) as typeof window.matchMedia;

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub;

// Mantine の Select が選択肢を見えるところまで送る。jsdom には無い。
Element.prototype.scrollIntoView ??= () => {};

// 日付の年は「今年（日本時間）」で付け外しするので、テストの「今」を 2026 年に固定する（Date だけ。タイマーは本物）。
// 年の付け外しを確かめるテストは、今を引数で渡すか vi.setSystemTime で変える。
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"], now: new Date("2026-11-14T12:00:00+09:00") });
});
afterEach(() => {
  vi.useRealTimers();
});
