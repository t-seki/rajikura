// X などのカード（OGP）の画像 docs/images/card.png を作る。元は web/scripts/card.html（1200x630）。
// 手元の Edge か Chrome をヘッドレスで動かして撮る（既定 msedge。環境変数 SCREENSHOT_CHANNEL で変える）。CI や Pages の中では撮らない。
// 画面の画像（docs/images/ の 3 枚）を撮り直したら、これも作り直す。使い方は docs/development.md の「README のスクリーンショットを撮り直す」。
// 書体 BIZ UDPGothic は Google Fonts から読むので、ネットにつながる必要がある。
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium } from "playwright-core";

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, "..", "..", "docs", "images", "card.png");
const channel = process.env.SCREENSHOT_CHANNEL || "msedge";

const browser = await chromium.launch({ channel, headless: true });
try {
  const page = await (await browser.newContext({ viewport: { width: 1200, height: 630 }, locale: "ja-JP" })).newPage();
  await page.goto(pathToFileURL(join(here, "card.html")).href, { waitUntil: "networkidle" });
  await page.evaluate(() => document.fonts.load('700 60px "BIZ UDPGothic"', "聴きたいラジオ番組を"));
  await page.evaluate(() => document.fonts.ready);
  // fonts.check() は一致する FontFace が無いと true を返すので、読み込み済みの FontFace があるかを直接見る。
  const loaded = await page.evaluate(() =>
    [...document.fonts].some((f) => f.family.replace(/["']/g, "") === "BIZ UDPGothic" && f.status === "loaded"),
  );
  if (!loaded) {
    throw new Error("書体 BIZ UDPGothic が読み込めなかった");
  }
  await page.screenshot({ path: out });
  console.log("wrote docs/images/card.png");
} finally {
  await browser.close();
}
