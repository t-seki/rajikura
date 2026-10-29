// rajikura serve の /api/ を呼ぶ。作りは docs/adr/0008-web-ui-shape.md と docs/adr/0016-first-password-by-setup-code.md。

/**
 * ログインの状態。setup は、パスワードがまだ決まっていない（503。セットアップコードを入れてパスワードを決める）。
 * setupLocked は、セットアップコードを 5 回続けて間違えて、serve を起動し直すまで受け付けない。
 */
export type AuthState = "authenticated" | "unauthenticated" | "setup" | "setupLocked";

export class ApiError extends Error {}

/**
 * パスワードを続けて間違えたので、serve がログイン（と、パスワードを変えること）の受け付けを止めている（429）。
 * retryAfter は、受け付けを再び始めるまでの秒数。
 */
export class LoginLimitedError extends ApiError {
  constructor(readonly retryAfter: number) {
    super(`パスワードを続けて間違えたので、受け付けを止めている。あと ${retryAfter} 秒待ってほしい`);
  }
}

/** 429 の応答から、待つ秒数（本文の retry_after、無ければ Retry-After ヘッダ）を読んで LoginLimitedError にする。 */
async function limitedOf(res: Response): Promise<LoginLimitedError> {
  const body = (await res.json().catch(() => ({}))) as { retry_after?: unknown };
  let secs = typeof body.retry_after === "number" ? body.retry_after : Number(res.headers.get("Retry-After"));
  if (!Number.isFinite(secs) || secs < 0) secs = 0;
  return new LoginLimitedError(Math.ceil(secs));
}

async function stateOf(res: Response): Promise<AuthState | null> {
  if (res.status === 503) {
    const body = (await res.json().catch(() => ({}))) as { locked?: unknown };
    return body.locked === true ? "setupLocked" : "setup";
  }
  if (res.status === 401) return "unauthenticated";
  return null;
}

/** /api/me の結果。authenticated のときだけ rajikura の版（version）が付く。 */
export type Me = { state: AuthState; version: string };

export async function fetchMe(): Promise<Me> {
  const res = await fetch("/api/me", { credentials: "same-origin" });
  if (res.ok) {
    const body = (await res.json().catch(() => ({}))) as { version?: unknown };
    return { state: "authenticated", version: typeof body.version === "string" ? body.version : "" };
  }
  const s = await stateOf(res);
  if (s) return { state: s, version: "" };
  throw new ApiError(`/api/me が ${res.status} を返した`);
}

async function send(method: string, url: string, body: unknown): Promise<Response> {
  return fetch(url, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

async function errorOf(res: Response, fallback: string): Promise<string> {
  const body = (await res.json().catch(() => ({}))) as { error?: unknown };
  return typeof body.error === "string" && body.error !== "" ? body.error : fallback;
}

/**
 * パスワードでログインする。成功は authenticated、パスワード違いは unauthenticated。
 * 続けて間違えて受け付けを止めているとき（429）は LoginLimitedError を投げる。
 */
export async function login(password: string): Promise<AuthState> {
  const res = await send("POST", "/api/login", { password });
  if (res.ok) return "authenticated";
  if (res.status === 429) throw await limitedOf(res);
  const s = await stateOf(res);
  if (s) return s;
  throw new ApiError(`/api/login が ${res.status} を返した`);
}

/**
 * セットアップコードの結果。ok ならパスワードが決まり、ログインした状態になる。
 * wrong はコードの間違い（remaining はあと何回間違えてよいか）、locked は serve を起動し直すまで受け付けない、
 * decided はパスワードがもう決まっている、invalid はパスワードが使えない（message に理由）。
 */
export type SetupResult =
  | { kind: "ok" }
  | { kind: "wrong"; remaining: number }
  | { kind: "locked" }
  | { kind: "decided" }
  | { kind: "invalid"; message: string };

/** セットアップコードを入れてパスワードを決める（POST /api/setup）。 */
export async function setupPassword(code: string, password: string): Promise<SetupResult> {
  const res = await send("POST", "/api/setup", { code, password });
  if (res.ok) return { kind: "ok" };
  if (res.status === 401) {
    const body = (await res.json().catch(() => ({}))) as { remaining?: unknown; locked?: unknown };
    if (body.locked === true) return { kind: "locked" };
    return { kind: "wrong", remaining: typeof body.remaining === "number" ? body.remaining : 0 };
  }
  if (res.status === 429) return { kind: "locked" };
  if (res.status === 409) return { kind: "decided" };
  if (res.status === 400) return { kind: "invalid", message: await errorOf(res, "パスワードが使えない") };
  throw new ApiError(`/api/setup が ${res.status} を返した`);
}

/**
 * パスワードを変える（PUT /api/password）。今のパスワードが違うなどで変えられなければ、理由の文で ApiError を投げる。
 * 今のパスワードの間違いはログインと同じ数に入り、受け付けを止めているとき（429）は LoginLimitedError を投げる。
 */
export async function changePassword(current: string, password: string): Promise<void> {
  const res = await send("PUT", "/api/password", { current, password });
  if (res.ok) return;
  if (res.status === 429) throw await limitedOf(res);
  if (res.status === 401) throw new ApiError("ログインが切れた。再読み込みしてログインし直してほしい");
  throw new ApiError(await errorOf(res, `/api/password が ${res.status} を返した`));
}

export async function logout(): Promise<void> {
  const res = await fetch("/api/logout", { method: "POST", credentials: "same-origin" });
  // 401 はすでにログアウトしている
  if (!res.ok && res.status !== 401) throw new ApiError(`/api/logout が ${res.status} を返した`);
}
