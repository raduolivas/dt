import type { Client } from './client';
import { CourierError } from './types';
import type { CourierStorage, CourierTokens } from './types';

export const STORAGE_KEY = 'dt.courier.session';
/** Refresh this long before the access token expires. */
export const REFRESH_LEAD_MS = 60_000;
const RETRY_BASE_MS = 1_000;
const RETRY_MAX_MS = 30_000;

interface Stored {
  tokens: CourierTokens;
  /** The link code, or the first refresh token, this session came from: tells a reload from a new link. */
  origin?: string;
}

export function defaultStorage(): CourierStorage {
  try {
    if (typeof localStorage !== 'undefined') {
      localStorage.getItem(STORAGE_KEY); // throws where storage is blocked
      return localStorage;
    }
  } catch {
    /* fall through to memory */
  }
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
    removeItem: (k) => void m.delete(k),
  };
}

export interface SessionDeps {
  client: Client;
  storage: CourierStorage;
  now: () => number;
  tokens?: CourierTokens;
  linkCode?: string;
  /** The session is over (any 401, or past its expiry). Called once. */
  onDead: (why: 'ended' | 'expired') => void;
  onNetwork: (ok: boolean) => void;
}

/** Owns the token pair: exchange, resume, refresh. Everything that needs a bearer token asks `token()`. */
export function createSession(d: SessionDeps) {
  let cur: Stored | null = null;
  let inflight: Promise<void> | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let dead = false;

  const accessExpiry = () => Date.parse(cur!.tokens.access_expires_at);
  const sessionExpiry = () => Date.parse(cur!.tokens.session_expires_at);

  const finish = (why: 'ended' | 'expired') => {
    if (dead) return;
    dead = true;
    clearTimeout(timer);
    cur = null;
    d.onDead(why);
  };
  const killIfExpired = () => {
    if (cur && d.now() >= sessionExpiry()) finish('expired');
    return dead;
  };
  /** A 401 from any bearer route: the server does not say whether it was ended, revoked or expired. */
  const unauthorized = () => finish(cur && d.now() >= sessionExpiry() ? 'expired' : 'ended');

  const persist = async (s: Stored) => {
    cur = s;
    try {
      await d.storage.setItem(STORAGE_KEY, JSON.stringify(s));
    } catch {
      /* the pair still works in memory; only a reload is lost */
    }
    schedule();
  };

  function schedule() {
    clearTimeout(timer);
    if (!cur || dead) return;
    const delay = Math.max(0, accessExpiry() - REFRESH_LEAD_MS - d.now());
    timer = setTimeout(() => void refresh(), delay);
  }

  /**
   * Single-flight: a timer, a visibility resume and a request that found the token stale all share one call. Refresh
   * tokens are single use, and presenting a superseded one ends the whole session (refresh_reuse), so two calls would
   * kill our own shift.
   *
   * A network error leaves us not knowing whether the server saw the request. If it did, it already rotated the token
   * and our retry (the SAME token, we have no other) will be read as reuse and end the session. If it did not, the
   * retry is simply right. There is no way to tell the two apart, so we retry the same token and accept that
   * rare case: the session ends and the caller sees 'ended'.
   * The new pair is written to storage before anything uses it or the next retry is made.
   */
  function refresh(): Promise<void> {
    if (inflight) return inflight;
    const run = async () => {
      let attempt = 0;
      while (cur && !dead) {
        if (killIfExpired()) return;
        const r = await d.client.call<CourierTokens>('POST', '/courier/token', {
          body: { grant_type: 'refresh_token', refresh_token: cur.tokens.refresh_token },
        });
        if (dead) return;
        if (r.kind === 'ok') {
          d.onNetwork(true);
          await persist({ tokens: r.data, origin: cur.origin });
          return;
        }
        if (r.kind === 'unauthorized') return unauthorized();
        let wait = Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** attempt++);
        if (r.kind === 'network') d.onNetwork(false);
        else {
          d.onNetwork(true);
          if (r.kind === 'rate_limited') wait = r.retryAfterMs;
        }
        await new Promise((res) => setTimeout(res, wait));
      }
    };
    inflight = run().finally(() => {
      inflight = null;
    });
    return inflight;
  }

  return {
    /**
     * Resume from storage, else use the given pair, else exchange the link code. A stored pair wins when it came from
     * the same code or pair: the code is single use, and the stored refresh token is newer than the one we were given.
     */
    async init(): Promise<void> {
      let stored: Stored | null = null;
      try {
        const raw = await d.storage.getItem(STORAGE_KEY);
        if (raw) stored = JSON.parse(raw) as Stored;
      } catch {
        stored = null;
      }
      const origin = d.linkCode ?? d.tokens?.refresh_token;
      if (stored?.tokens && (origin === undefined || stored.origin === origin)) {
        cur = stored;
        if (killIfExpired()) throw new CourierError('session_ended');
        schedule();
        return;
      }
      if (d.linkCode !== undefined) {
        const r = await d.client.call<CourierTokens>('POST', '/courier/token', {
          body: { grant_type: 'link_code', code: d.linkCode },
        });
        if (r.kind === 'ok') return persist({ tokens: r.data, origin: d.linkCode });
        if (r.kind === 'unauthorized') throw new CourierError(r.code ?? 'link_code_invalid');
        if (r.kind === 'network') throw new CourierError('network');
        throw new CourierError(r.kind === 'error' ? (r.code ?? 'rejected') : 'rate_limited');
      }
      if (d.tokens) {
        return persist({ tokens: d.tokens, origin: d.tokens.refresh_token });
      }
      throw new CourierError('no_session');
    },
    /** A token that is good to use now, or null when the session is over. Waits for a refresh only if it must. */
    async token(): Promise<string | null> {
      if (dead || !cur || killIfExpired()) return null;
      if (d.now() >= accessExpiry() - REFRESH_LEAD_MS) {
        const p = refresh();
        if (d.now() >= accessExpiry()) await p; // still good for a while: let the refresh run behind this call
      }
      return dead || !cur ? null : cur.tokens.access_token;
    },
    /** Call on resume from background: renews now if the timer was throttled while the tab slept. */
    ensureFresh() {
      if (cur && !dead && d.now() >= accessExpiry() - REFRESH_LEAD_MS) void refresh();
    },
    unauthorized,
    /** Forget the pair (end of session). */
    async clear() {
      dead = true;
      clearTimeout(timer);
      cur = null;
      try {
        await d.storage.removeItem(STORAGE_KEY);
      } catch {
        /* nothing to do */
      }
    },
    get sessionExpiresAt() {
      return cur ? sessionExpiry() : null;
    },
  };
}
export type Session = ReturnType<typeof createSession>;
