/** What a call came back with. Callers branch on `kind`; nothing here throws. */
export type ApiResult<T> =
  | { kind: 'ok'; data: T }
  | { kind: 'unauthorized'; code?: string }
  | { kind: 'rate_limited'; retryAfterMs: number }
  | { kind: 'network' }
  | { kind: 'error'; status: number; code?: string };

const DEFAULT_RETRY_AFTER_MS = 5_000;
/** A request with no complete answer by then is given up as a network error (a stalled mobile connection). */
export const REQUEST_TIMEOUT_MS = 15_000;

export interface Client {
  call<T>(method: 'GET' | 'POST', path: string, opts?: { token?: string; body?: unknown }): Promise<ApiResult<T>>;
}

export function createClient(api: string, fetchFn?: typeof fetch): Client {
  const base = api.replace(/\/+$/, '');
  const f: typeof fetch = fetchFn ?? ((...a) => globalThis.fetch(...a));
  return {
    async call<T>(method: 'GET' | 'POST', path: string, opts: { token?: string; body?: unknown } = {}) {
      const headers: Record<string, string> = {};
      if (opts.token) headers.Authorization = `Bearer ${opts.token}`;
      if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
      // AbortController and our own timer rather than AbortSignal.timeout: older Safari lacks it. The timer covers
      // the body as well as the headers.
      const ctl = typeof AbortController === 'undefined' ? undefined : new AbortController();
      const timer = ctl && setTimeout(() => ctl.abort(), REQUEST_TIMEOUT_MS);
      try {
        return await send<T>(f, base + path, method, headers, opts.body, ctl?.signal);
      } finally {
        clearTimeout(timer);
      }
    },
  };
}

async function send<T>(
  f: typeof fetch,
  url: string,
  method: string,
  headers: Record<string, string>,
  body: unknown,
  signal: AbortSignal | undefined,
): Promise<ApiResult<T>> {
  let res: Response;
  try {
    res = await f(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'omit',
      signal,
    });
  } catch {
    return { kind: 'network' } as ApiResult<T>;
  }
  if (res.ok) {
    if (res.status === 204) return { kind: 'ok', data: undefined as T };
    try {
      return { kind: 'ok', data: (await res.json()) as T };
    } catch {
      return { kind: 'network' }; // a body cut off mid-flight is a lost answer, same as a lost request
    }
  }
  const code = await problemCode(res);
  if (res.status === 401) return { kind: 'unauthorized', code };
  if (res.status === 429) {
    const s = Number(res.headers.get('Retry-After'));
    return { kind: 'rate_limited', retryAfterMs: Number.isFinite(s) && s > 0 ? s * 1000 : DEFAULT_RETRY_AFTER_MS };
  }
  return { kind: 'error', status: res.status, code };
}

async function problemCode(res: Response): Promise<string | undefined> {
  try {
    const p = (await res.json()) as { code?: unknown };
    return typeof p.code === 'string' ? p.code : undefined;
  } catch {
    return undefined;
  }
}
