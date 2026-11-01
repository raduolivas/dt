import type { ApiResult, Client } from './client';
import { CourierError } from './types';
import type { CourierStorage, CourierTokens } from './types';

export const STORAGE_KEY = 'dt.courier.session';
/** The Web Locks name every tab of this origin refreshes under. */
export const REFRESH_LOCK = 'dt.courier.refresh';
/** Refresh this long before the access token expires. */
export const REFRESH_LEAD_MS = 60_000;
const RETRY_BASE_MS = 1_000;
const RETRY_MAX_MS = 30_000;

interface Stored {
  tokens: CourierTokens;
  /** Fingerprint of the link code, or of the first refresh token, this session came from: tells a reload from a new link. */
  origin?: string;
  /** Server clock minus device clock in ms, measured when the pair arrived (0 when the answer had no `expires_in`). */
  skew?: number;
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

/** Equality check only: stored in place of the code or first refresh token, so no usable secret is kept for it. */
async function fingerprint(v: string): Promise<string> {
  const bytes = new TextEncoder().encode(v);
  try {
    const h = await crypto.subtle.digest('SHA-256', bytes);
    return [...new Uint8Array(h)].map((b) => b.toString(16).padStart(2, '0')).join('');
  } catch {
    // no crypto.subtle (an insecure origin): FNV-1a is enough to tell one code from another
    let x = 0x811c9dc5;
    for (const b of bytes) x = Math.imul(x ^ b, 0x01000193) >>> 0;
    return 'fnv' + x.toString(16);
  }
}

export interface SessionDeps {
  client: Client;
  storage: CourierStorage;
  now: () => number;
  tokens?: CourierTokens;
  linkCode?: string;
  /** Web Locks; undefined means `navigator.locks` where there is one, null means none. */
  locks?: LockManager | null;
  /** The session is over (a 401 the one refresh could not cure, or past its expiry). Called once. */
  onDead: (why: 'ended' | 'expired') => void;
  onNetwork: (ok: boolean) => void;
}

/** Owns the token pair: exchange, resume, refresh. Everything that needs a bearer token asks `token()`. */
export function createSession(d: SessionDeps) {
  let cur: Stored | null = null;
  let inflight: Promise<void> | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let dead = false;
  /** The access token a 401-driven refresh produced: a 401 on it too ends the session (one refresh per 401 chain). */
  let recovered: string | null = null;
  const locks = d.locks !== undefined ? d.locks : typeof navigator !== 'undefined' ? (navigator.locks ?? null) : null;

  /**
   * Timestamps in the pair are the server's. The device clock can be minutes off, so every comparison runs on the
   * server's clock as measured when the pair arrived.
   */
  const serverNow = () => d.now() + (cur?.skew ?? 0);
  const accessExpiry = () => Date.parse(cur!.tokens.access_expires_at);
  const sessionExpiry = () => Date.parse(cur!.tokens.session_expires_at);
  const fresh = () => serverNow() < accessExpiry() - REFRESH_LEAD_MS;
  const skewOf = (t: CourierTokens, receivedAt: number, prev: number | undefined) =>
    typeof t.expires_in === 'number' ? Date.parse(t.access_expires_at) - t.expires_in * 1000 - receivedAt : (prev ?? 0);

  const stopListening = () => {
    if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
  };
  const finish = (why: 'ended' | 'expired') => {
    if (dead) return;
    dead = true;
    clearTimeout(timer);
    stopListening();
    cur = null;
    d.onDead(why);
  };
  const killIfExpired = () => {
    if (cur && serverNow() >= sessionExpiry()) finish('expired');
    return dead;
  };
  /** The server refused us for good: it does not say whether the session was ended, revoked or expired. */
  const ended = () => finish(cur && serverNow() >= sessionExpiry() ? 'expired' : 'ended');

  const persist = async (s: Stored) => {
    cur = s;
    try {
      await d.storage.setItem(STORAGE_KEY, JSON.stringify(s));
    } catch {
      /* the pair still works in memory; only a reload is lost */
    }
    schedule();
  };
  const readStored = async (): Promise<Stored | null> => {
    try {
      const raw = await d.storage.getItem(STORAGE_KEY);
      const s = raw ? (JSON.parse(raw) as Stored) : null;
      return s?.tokens ? s : null;
    } catch {
      return null;
    }
  };
  /** Another tab of the same session rotated the pair: ours is superseded and must never be presented again. */
  const rotatedElsewhere = (s: Stored | null): s is Stored =>
    !!s && !!cur && s.origin === cur.origin && s.tokens.refresh_token !== cur.tokens.refresh_token;
  const adopt = (s: Stored) => {
    cur = s;
    schedule();
  };

  /** Without Web Locks: best effort, take the pair another tab wrote as soon as the browser tells us. */
  function onStorage(e: StorageEvent) {
    if (dead || e.key !== STORAGE_KEY || !e.newValue) return;
    try {
      const s = JSON.parse(e.newValue) as Stored;
      if (s.tokens && rotatedElsewhere(s)) adopt(s);
    } catch {
      /* not ours */
    }
  }
  const listen = () => {
    if (typeof window !== 'undefined') window.addEventListener('storage', onStorage);
  };

  function schedule() {
    clearTimeout(timer);
    if (!cur || dead) return;
    const delay = Math.max(0, accessExpiry() - REFRESH_LEAD_MS - serverNow());
    timer = setTimeout(() => void refresh(), delay);
  }

  /**
   * One attempt, under the cross-tab lock where there is one. Inside it, storage is read first: a second tab of the
   * same session (the courier opened the link again) may have rotated the pair while we waited, and then we take
   * that pair instead of presenting our superseded refresh token, which would end the shift as refresh_reuse.
   */
  async function attempt(force: boolean): Promise<ApiResult<CourierTokens> | 'adopted'> {
    const run = async () => {
      const s = await readStored();
      if (rotatedElsewhere(s)) {
        adopt(s);
        if (fresh()) return 'adopted' as const;
      }
      if (!force && fresh()) return 'adopted' as const; // a storage event brought a new pair while we backed off
      const sent = cur!.tokens.refresh_token;
      const r = await d.client.call<CourierTokens>('POST', '/courier/token', {
        body: { grant_type: 'refresh_token', refresh_token: sent },
      });
      // persist before the lock is let go, so the next tab in line reads the new pair
      if (r.kind === 'ok' && !dead && cur) await persist({ tokens: r.data, origin: cur.origin, skew: skewOf(r.data, d.now(), cur.skew) });
      return r;
    };
    if (!locks) return run();
    try {
      return await locks.request(REFRESH_LOCK, run);
    } catch {
      return run(); // a lock manager that refuses (an opaque origin): refresh as if there were none
    }
  }

  /**
   * Single-flight inside this tab: a timer, a visibility resume, a request that found the token stale and a 401 all
   * share one call. Across tabs the Web Lock above does the same job.
   *
   * A network error leaves us not knowing whether the server saw the request. If it did, it already rotated the token
   * and our retry (the SAME token, we have no other) will be read as reuse and end the session. If it did not, the
   * retry is simply right. There is no way to tell the two apart, so we retry the same token and accept that
   * rare case: the session ends and the caller sees 'ended'.
   * The new pair is written to storage before anything uses it or the next retry is made.
   */
  function refresh(force = false): Promise<void> {
    if (inflight) return inflight;
    const run = async () => {
      let n = 0;
      while (cur && !dead) {
        if (killIfExpired()) return;
        const r = await attempt(force);
        if (dead) return;
        if (r === 'adopted') return;
        if (r.kind === 'ok') {
          d.onNetwork(true);
          return;
        }
        if (r.kind === 'unauthorized') return ended();
        let wait = Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** n++);
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
    /** Resolves true when it resumed the stored session, false when it started a new one (the caller drops leftovers). */
    async init(): Promise<boolean> {
      const stored = await readStored();
      const given = d.linkCode ?? d.tokens?.refresh_token;
      const origin = given === undefined ? undefined : await fingerprint(given);
      if (stored && (origin === undefined || stored.origin === origin)) {
        cur = stored;
        if (killIfExpired()) throw new CourierError('session_ended');
        listen();
        schedule();
        return true;
      }
      if (d.linkCode !== undefined) {
        const r = await d.client.call<CourierTokens>('POST', '/courier/token', {
          body: { grant_type: 'link_code', code: d.linkCode },
        });
        if (r.kind === 'ok') {
          await persist({ tokens: r.data, origin, skew: skewOf(r.data, d.now(), undefined) });
          listen();
          return false;
        }
        if (r.kind === 'unauthorized') throw new CourierError(r.code ?? 'link_code_invalid');
        if (r.kind === 'network') throw new CourierError('network');
        throw new CourierError(r.kind === 'error' ? (r.code ?? 'rejected') : 'rate_limited');
      }
      if (d.tokens) {
        await persist({ tokens: d.tokens, origin, skew: skewOf(d.tokens, d.now(), undefined) });
        listen();
        return false;
      }
      throw new CourierError('no_session');
    },
    /** A token that is good to use now, or null when the session is over. Waits for a refresh only if it must. */
    async token(): Promise<string | null> {
      if (dead || !cur || killIfExpired()) return null;
      if (!fresh()) {
        const p = refresh();
        if (serverNow() >= accessExpiry()) await p; // still good for a while: let the refresh run behind this call
      }
      return dead || !cur ? null : cur.tokens.access_token;
    },
    /** The access token if it is still valid, else null. Never waits and never refreshes. */
    readyToken(): string | null {
      return cur && !dead && serverNow() < accessExpiry() ? cur.tokens.access_token : null;
    },
    /** Call on resume from background: renews now if the timer was throttled while the tab slept. */
    ensureFresh() {
      if (cur && !dead && !fresh()) void refresh();
    },
    /**
     * A bearer route answered 401 to `used`. Before giving up, try ONE refresh: a token the server will not take (a
     * device clock far off, a key rotation) is not a dead session. A refresh that is refused too ends it, as does a
     * 401 on the pair that refresh produced, so there is never a loop. A revoked session therefore ends after one
     * refresh attempt, with the buffer discarded by the caller.
     */
    async unauthorized(used?: string): Promise<void> {
      if (dead || !cur || killIfExpired()) return;
      if (used !== undefined && used !== cur.tokens.access_token) return; // already replaced: the next call uses the new one
      if (used === undefined || used === recovered) return ended();
      await refresh(true);
      if (!dead && cur) recovered = cur.tokens.access_token;
    },
    /** Forget the pair (end of session). */
    async clear() {
      dead = true;
      clearTimeout(timer);
      stopListening();
      cur = null;
      try {
        await d.storage.removeItem(STORAGE_KEY);
      } catch {
        /* nothing to do */
      }
    },
  };
}
export type Session = ReturnType<typeof createSession>;
