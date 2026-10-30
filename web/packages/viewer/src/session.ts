import type { TrackingSession, TrackingSnapshot } from '@dt/contract';

/** Outcome of a viewer API call. Never carries a token. */
export type CallResult<T> =
  | { ok: true; value: T }
  | { ok: false; reason: 'unauthorized' | 'unavailable' | 'failed'; status: number; retryAfterMs?: number };

async function call<T>(f: typeof fetch, url: string, init: RequestInit): Promise<CallResult<T>> {
  let res: Response;
  try {
    res = await f(url, init);
  } catch {
    return { ok: false, reason: 'failed', status: 0 };
  }
  if (res.ok) {
    try {
      return { ok: true, value: (await res.json()) as T };
    } catch {
      return { ok: false, reason: 'failed', status: res.status };
    }
  }
  if (res.status === 401) return { ok: false, reason: 'unauthorized', status: 401 };
  if (res.status === 503) {
    const secs = Number(res.headers.get('Retry-After'));
    return {
      ok: false,
      reason: 'unavailable',
      status: 503,
      retryAfterMs: Number.isFinite(secs) && secs > 0 ? secs * 1000 : 1000,
    };
  }
  return { ok: false, reason: 'failed', status: res.status };
}

export function createSession(f: typeof fetch, api: string, linkToken: string) {
  return call<TrackingSession>(f, `${api}/tracking/session`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: linkToken }),
  });
}

export function refreshSession(f: typeof fetch, api: string, viewerToken: string) {
  return call<{ viewer_token: string; expires_at: string }>(f, `${api}/tracking/session/refresh`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${viewerToken}` },
  });
}

export function getSnapshot(f: typeof fetch, api: string, viewerToken: string) {
  return call<TrackingSnapshot>(f, `${api}/tracking/snapshot`, {
    headers: { Authorization: `Bearer ${viewerToken}` },
  });
}
