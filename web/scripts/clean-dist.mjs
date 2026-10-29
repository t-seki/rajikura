// web/dist の中身を消す。.gitkeep は残す。
// Vite の build.emptyOutDir は false にしてある（dist/.gitkeep を入れて go:embed を通しているため。web/embed.go）ので、
// 古いビルドの成果物が溜まらないよう、ビルドの前にこれで消す。
import { mkdirSync, readdirSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = join(dirname(fileURLToPath(import.meta.url)), "..", "dist");
mkdirSync(dist, { recursive: true });
for (const name of readdirSync(dist)) {
  if (name === ".gitkeep") continue;
  rmSync(join(dist, name), { recursive: true, force: true });
}
