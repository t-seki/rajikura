// IdP のログインの設定と紐づけの API（docs/api.md の「IdP のログイン」。作りは docs/adr/0040-idp-login-by-generic-oidc.md）を呼ぶ。
// client secret の値は返らない。

export interface IdpBinding {
  issuer: string;
  subject: string;
  /** ID トークンに無ければ空。 */
  email: string;
}

/** GET /api/idp の応答（PUT /api/idp・DELETE /api/idp/link も同じ形）。 */
export interface IdpView {
  issuer: string;
  client_id: string;
  web_url: string;
  client_secret_set: boolean;
  client_secret_unreadable: boolean;
  /** IdP に登録する戻り先の URL。設定していなければ空。 */
  redirect_url: string;
  web_host: string;
  /** Web UI の URL の host が -allowed-hosts にあるか（設定していなければ true）。 */
  web_host_allowed: boolean;
  binding: IdpBinding | null;
}

/** PUT /api/idp の本文。client_secret は、書けば変え、空の文字列なら消し、省けば今のまま。 */
export interface IdpValues {
  issuer: string;
  client_id: string;
  web_url: string;
  client_secret?: string;
}

export class IdpApiError extends Error {
  /** 紐づけを始めるとき、Web UI の URL で開き直すよう求められた（409 で web_url が付く）ときの URL。 */
  webUrl?: string;
}

async function errorOf(res: Response, fallback: string): Promise<{ message: string; webUrl?: string }> {
  const body = (await res.json().catch(() => ({}))) as { error?: unknown; web_url?: unknown };
  return {
    message: typeof body.error === "string" && body.error !== "" ? body.error : fallback,
    webUrl: typeof body.web_url === "string" && body.web_url !== "" ? body.web_url : undefined,
  };
}

async function call(method: string, url: string, body?: unknown): Promise<Response> {
  return fetch(url, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

async function viewOf(res: Response, what: string): Promise<IdpView> {
  if (res.ok) return (await res.json()) as IdpView;
  if (res.status === 401) throw new IdpApiError("ログインが切れた。再読み込みしてログインし直してほしい");
  throw new IdpApiError((await errorOf(res, `${what}が ${res.status} を返した`)).message);
}

export async function fetchIdp(): Promise<IdpView> {
  return viewOf(await call("GET", "/api/idp"), "/api/idp");
}

export async function saveIdp(values: IdpValues): Promise<IdpView> {
  return viewOf(await call("PUT", "/api/idp", values), "/api/idp");
}

export async function unlinkIdp(): Promise<IdpView> {
  return viewOf(await call("DELETE", "/api/idp/link"), "/api/idp/link");
}

/** 今のパスワードを入れて紐づけを始める。IdP へ移る URL を返す（画面は top-level で移る）。 */
export async function startIdpLink(password: string): Promise<string> {
  const res = await call("POST", "/api/idp/link", { password });
  if (res.ok) {
    const body = (await res.json().catch(() => ({}))) as { url?: unknown };
    if (typeof body.url === "string" && body.url !== "") return body.url;
    throw new IdpApiError("IdP へ移る URL を受け取れなかった");
  }
  if (res.status === 429) throw new IdpApiError("パスワードを続けて間違えたので、受け付けを止めている。しばらく待ってほしい");
  if (res.status === 401) throw new IdpApiError("ログインが切れた。再読み込みしてログインし直してほしい");
  const { message, webUrl } = await errorOf(res, `/api/idp/link が ${res.status} を返した`);
  const e = new IdpApiError(message);
  e.webUrl = webUrl;
  throw e;
}

/** ログインの画面と設定の画面に付く idp_error の理由（docs/api.md）を、画面に出す文にする。 */
export function idpErrorMessage(reason: string): string {
  switch (reason) {
    case "unavailable":
      return "IdP のログインを使えない（IdP を設定していないか、紐づけていない）";
    case "denied":
      return "IdP がログインを拒んだ";
    case "expired":
      return "ログインの途中の状態が無い（時間が過ぎたか、rajikura を起動し直した）。もう一度やり直してほしい";
    case "mismatch":
      return "紐づけた相手と違う利用者だった";
    default:
      return "IdP のログインに失敗した。IdP の設定を確かめてほしい";
  }
}

/** URL のクエリから key の値を取り、URL からそのキーだけを消す（ほかのクエリは残す）。無ければ null。 */
export function takeQuery(key: string): string | null {
  const url = new URL(window.location.href);
  const v = url.searchParams.get(key);
  if (v === null) return null;
  url.searchParams.delete(key);
  window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
  return v;
}
