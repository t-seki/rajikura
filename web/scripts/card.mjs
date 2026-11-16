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
  // 日本語の書体は字の範囲（unicode-range）ごとの区画に分けて配られるので、カードに出る字を全部読み込ませる。
  await page.evaluate(() => document.fonts.load('700 60px "BIZ UDPGothic"', document.body.innerText));
  await page.evaluate(() => document.fonts.ready);
  // fonts.check() は一致する FontFace が無いと true を返すので、FontFace を直接見る。
  // 読み込み済みの区画が 1 つも無いか、読み込みに失敗した区画が 1 つでもあれば止める（代わりの書体が混じったまま撮らない）。
  const status = await page.evaluate(() => {
    const faces = [...document.fonts].filter((f) => f.family.replace(/["']/g, "") === "BIZ UDPGothic");
    return { loaded: faces.some((f) => f.status === "loaded"), error: faces.some((f) => f.status === "error") };
  });
  if (!status.loaded || status.error) {
    throw new Error("書体 BIZ UDPGothic が読み込めなかった");
  }
  await page.screenshot({ path: out });
  console.log("wrote docs/images/card.png");
} finally {
  await browser.close();
}
