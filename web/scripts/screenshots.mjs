// README のスクリーンショット（docs/images/ の PNG 3 枚）を撮り直す。
// ダミーのデータで答える Vite の開発サーバー（--mode mock。mock/plugin.ts）を立て、手元の Edge か Chrome をヘッドレスで動かして撮る。
// rajikura serve にも radiko にもつながらない。使い方は docs/development.md の「README のスクリーンショットを撮り直す」。
// ブラウザは playwright-core の channel で選ぶ（既定 msedge。環境変数 SCREENSHOT_CHANNEL で chrome などに変える）。ブラウザは別に入れない。
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";
import { createServer } from "vite";
import { MOCK_NOW } from "../mock/data.ts";

const webDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const outDir = join(webDir, "..", "docs", "images");
const channel = process.env.SCREENSHOT_CHANNEL || "msedge";
const port = 5199;

// 撮る画面。waitFor は、その画面のデータが出たことを見分ける文字（ダミーの番組名）。
const shots = [
  { file: "guide.png", path: "/", waitFor: "星降る深夜ラジオ", scrollUp: 560 },
  { file: "jobs.png", path: "/jobs", waitFor: "つむぎ夜の読み物" },
  { file: "shows.png", path: "/shows", waitFor: "波打ちぎわの昭和歌謡" },
];

mkdirSync(outDir, { recursive: true });

const server = await createServer({
  root: webDir,
  mode: "mock",
  server: { host: "127.0.0.1", port, strictPort: true },
  logLevel: "warn",
});
await server.listen();

let browser;
try {
  browser = await chromium.launch({ channel, headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "ja-JP", timezoneId: "Asia/Tokyo" });
  const page = await context.newPage();
  // 「今」を固定する（番組表の現在位置、ジョブの時刻が毎回同じになる）。
  await page.clock.setFixedTime(new Date(MOCK_NOW));
  for (const s of shots) {
    await page.goto(`http://127.0.0.1:${port}${s.path}`, { waitUntil: "networkidle" });
    await page.getByText(s.waitFor).first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(500);
    // 番組表は「今」の位置まで自動でスクロールしている。午前の録音済みの放送回も入るよう、少し戻す。
    if (s.scrollUp) {
      await page.mouse.move(600, 600);
      await page.mouse.wheel(0, -s.scrollUp);
      await page.waitForTimeout(300);
    }
    await page.screenshot({ path: join(outDir, s.file) });
    console.log(`wrote docs/images/${s.file}`);
  }
} finally {
  await browser?.close();
  await server.close();
}
