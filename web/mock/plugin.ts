// pnpm dev:mock（vite --mode mock）のときだけ、/api をダミーのデータで答える Vite のプラグイン。
// rajikura serve にも radiko にもつながらない。読み出し（GET）だけ答え、書き込みは 405 で断る。
// pnpm build の成果物には入らない（vite.config.ts が mode が mock のときだけ足し、apply も serve に限る）。
import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";
import { guideOf, jobDetail, jobs, mockImage, shows, stations } from "./data.ts";

function json(res: ServerResponse, body: unknown, status = 200) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(body));
}

function answer(req: IncomingMessage, res: ServerResponse): boolean {
  const url = new URL(req.url ?? "/", "http://localhost");
  const path = url.pathname;
  const img = /^\/mock-images\/(\d+)\.svg$/.exec(path);
  if (img) {
    res.setHeader("Content-Type", "image/svg+xml");
    res.end(mockImage(img[1]));
    return true;
  }
  if (!path.startsWith("/api/")) return false;
  if (req.method !== "GET") {
    json(res, { error: "ダミーのデータの口は読み出しだけ答える" }, 405);
    return true;
  }
  if (path === "/api/me") return json(res, { version: "0.0.0-mock" }), true;
  if (path === "/api/guide") {
    const day = url.searchParams.get("day") ?? "";
    return json(res, { day, stations: guideOf(day) }), true;
  }
  if (path === "/api/guide/search") return json(res, { q: url.searchParams.get("q") ?? "", airings: [], truncated: false }), true;
  if (path === "/api/stations") return json(res, stations), true;
  if (path === "/api/shows") return json(res, shows), true;
  const show = /^\/api\/shows\/([^/]+)$/.exec(path);
  if (show) {
    const s = shows.find((x) => x.name === decodeURIComponent(show[1]));
    return s ? (json(res, { ...s, onces: [] }), true) : (json(res, { error: "番組が無い" }, 404), true);
  }
  if (path === "/api/jobs") {
    const states = (url.searchParams.get("state") ?? "").split(",").filter(Boolean);
    return json(res, { jobs: states.length === 0 ? jobs : jobs.filter((j) => states.includes(j.state)) }), true;
  }
  const job = /^\/api\/jobs\/(\d+)$/.exec(path);
  if (job) {
    const j = jobs.find((x) => x.id === Number(job[1]));
    return j ? (json(res, jobDetail(j)), true) : (json(res, { error: "ジョブが無い" }, 404), true);
  }
  if (path === "/api/settings") {
    return json(res, { settings: { library: "D:\\Radio", preset: "jellyfin" }, secrets: {}, library_set: true, unreadable_secrets: [], last_backup_at: null }), true;
  }
  if (path === "/api/notifications/status") return json(res, { unread: 0, failed: 0, anomalies: [], channel: "none" }), true;
  if (path === "/api/notifications") return json(res, []), true;
  json(res, { error: "ダミーのデータの口に無い API" }, 404);
  return true;
}

export function mockApi(): Plugin {
  return {
    name: "rajikura-mock-api",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!answer(req, res)) next();
      });
    },
  };
}
