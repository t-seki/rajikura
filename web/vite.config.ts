import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// 開発サーバー（pnpm dev）は /api を rajikura serve に中継する。つなぎ先は環境変数 RAJIKURA_API（既定 http://localhost:8080）。
const api = process.env.RAJIKURA_API ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    // dist/.gitkeep を残すため false。古い成果物は pnpm build の前段（scripts/clean-dist.mjs）で消す。
    emptyOutDir: false,
    // バンドルした依存のライセンスの全文を dist/.vite/license.md に出す。go:embed でバイナリに入り、
    // rajikura licenses が Go の依存の一覧の後に出す（cmd/rajikura/licenses.go）。
    license: true,
  },
  server: {
    proxy: { "/api": { target: api, changeOrigin: true } },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    // CI の遅い回に、userEvent を重ねた画面のテスト（ShowsPage の「番組を作る」など）が既定の 5000ms で落ちることがある。
    testTimeout: 15000,
  },
});
