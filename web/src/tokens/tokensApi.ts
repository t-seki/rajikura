// API トークンの API（internal/server/tokens.go）を呼ぶ。

export interface ApiToken {
  id: number;
  name: string;
  createdAt: string;
  /** 最後に使った日時。まだ使っていなければ null。 */
  lastUsedAt: string | null;
}

/** 発行直後の応答。token（トークンそのもの）はこの 1 回だけ得られる。 */
export interface CreatedApiToken extends ApiToken {
  token: string;
}

export class TokensApiError extends Error {}

async function call(method: string, url: string, body?: unknown): Promise<Response> {
  const res = await fetch(url, {
    method,
    credentials: "same-origin",
    ...(body === undefined ? {} : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
  });
  if (!res.ok) {
    let msg = `${url} が ${res.status} を返した`;
    try {
      const b = (await res.json()) as { error?: string };
      if (b.error) msg = b.error;
    } catch {
      // 本文が JSON でなければ、上の既定の文のまま
    }
    throw new TokensApiError(msg);
  }
  return res;
}

export async function listTokens(): Promise<ApiToken[]> {
  return ((await (await call("GET", "/api/tokens")).json()) as { tokens: ApiToken[] }).tokens;
}

export async function createToken(name: string): Promise<CreatedApiToken> {
  return (await (await call("POST", "/api/tokens", { name })).json()) as CreatedApiToken;
}

export async function deleteToken(id: number): Promise<void> {
  await call("DELETE", `/api/tokens/${id}`);
}
