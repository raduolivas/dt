import type { Branding, TrackingSession, TrackingSnapshot } from '@dt/contract';
import { createSession, getSnapshot, refreshSession } from './session';
import { openSse, openWebSocket } from './stream';
import type { ServerMessage, StreamConn } from './stream';
import type { ViewerClient, ViewerError, ViewerOptions, ViewerState } from './types';

const REFRESH_LEAD_MS = 120_000;
const WARMING_LIMIT_MS = 5_000;
const BACKOFF_BASE_MS = 500;
const BACKOFF_MAX_MS = 30_000;

export function createViewer(o: ViewerOptions): ViewerClient {
  const api = o.api.replace(/\/+$/, '');
  const f: typeof fetch = o.fetch ?? ((...a) => globalThis.fetch(...a));
  const WS = o.WebSocket ?? globalThis.WebSocket;
  const now = o.now ?? Date.now;
  const linkToken = o.token; // memory only: never stored, logged or put in an error

  const stateCbs = new Set<(s: ViewerState) => void>();
  const errorCbs = new Set<(e: ViewerError) => void>();
  const timers = new Set<{ id: ReturnType<typeof setTimeout>; resolve: (v: boolean) => void }>();

  let gen = 0; // bumped on start, stop and inactive: stale async work checks it and quits
  let running = false;
  let viewerToken = '';
  let expiresAt = 0;
  let branding: Branding | null = null;
  let lastSnapshot: TrackingSnapshot | null = null;
  let conn: StreamConn | null = null;
  let transport: 'ws' | 'sse' = 'ws';
  let attempt = 0;
  let recovering: Promise<boolean> | null = null;
  let refreshSeq = 0;
  let reconnecting = false; // a backoff sleep is pending; only reconnect() may open the next stream

  const setState = (s: ViewerState) => {
    for (const cb of [...stateCbs]) cb(s);
  };
  const fail = (code: string, detail?: string) => {
    for (const cb of [...errorCbs]) cb({ code, detail });
  };
  /** Resolves true after `ms`, or false if the run it belongs to ended first. */
  const sleep = (ms: number, g: number) =>
    new Promise<boolean>((resolve) => {
      const t = {
        id: setTimeout(() => {
          timers.delete(t);
          resolve(g === gen);
        }, ms),
        resolve,
      };
      timers.add(t);
    });
  const backoff = () => Math.random() * Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** attempt++);

  const teardown = () => {
    gen++;
    running = false;
    conn?.close();
    conn = null;
    recovering = null;
    reconnecting = false;
    for (const t of timers) {
      clearTimeout(t.id);
      t.resolve(false);
    }
    timers.clear();
  };
  const goInactive = () => {
    teardown();
    setState({ kind: 'inactive', last: lastSnapshot });
  };

  const apply = (s: TrackingSnapshot) => {
    if (lastSnapshot && s.version <= lastSnapshot.version) return;
    lastSnapshot = s;
    setState({ kind: 'live', snapshot: s, branding });
  };

  const adopt = (token: string, expires: string, g: number) => {
    viewerToken = token;
    expiresAt = Date.parse(expires);
    conn?.sendToken(token);
    void scheduleRefresh(g);
  };

  /** Exchange the link token for a viewer token, retrying a 503 for up to 5 s. Returns false when it ended. */
  async function startSession(g: number): Promise<boolean> {
    let warmingSince: number | null = null;
    for (;;) {
      const r = await createSession(f, api, linkToken);
      if (g !== gen) return false;
      if (r.ok) {
        const s: TrackingSession = r.value;
        branding = s.branding ?? null;
        adopt(s.viewer_token, s.expires_at, g);
        apply(s.snapshot);
        attempt = 0;
        return true;
      }
      if (r.reason === 'unauthorized') {
        goInactive();
        return false;
      }
      if (r.reason === 'unavailable') {
        warmingSince ??= now();
        if (now() - warmingSince >= WARMING_LIMIT_MS) {
          goInactive();
          return false;
        }
        setState({ kind: 'warming' });
        if (!(await sleep(r.retryAfterMs ?? 1000, g))) return false;
        continue;
      }
      fail('session_failed', `status ${r.status}`);
      if (!(await sleep(backoff(), g))) return false;
    }
  }

  /** Renew the viewer token; when the viewer token is refused, start a new session from the link token. */
  function recover(g: number): Promise<boolean> {
    recovering ??= (async () => {
      try {
        const r = await refreshSession(f, api, viewerToken);
        if (g !== gen) return false;
        if (r.ok) {
          adopt(r.value.viewer_token, r.value.expires_at, g);
          return true;
        }
        if (r.reason === 'unauthorized') return await startSession(g);
        fail('refresh_failed', `status ${r.status}`);
        return false;
      } finally {
        recovering = null;
      }
    })();
    return recovering;
  }

  async function scheduleRefresh(g: number) {
    // A newer schedule (after a token swap) supersedes this one.
    const mine = ++refreshSeq;
    if (!(await sleep(Math.max(0, expiresAt - REFRESH_LEAD_MS - now()), g)) || mine !== refreshSeq) return;
    while (g === gen && mine === refreshSeq && !(await recover(g))) {
      if (g !== gen || now() >= expiresAt) return; // an expired token is handled when the stream reconnects
      if (!(await sleep(backoff(), g))) return;
    }
  }
  async function resync(g: number, retry = true): Promise<void> {
    const r = await getSnapshot(f, api, viewerToken);
    if (g !== gen) return;
    if (r.ok) return apply(r.value);
    if (r.reason === 'unauthorized' && retry) {
      if (await recover(g)) return resync(g, false);
      return;
    }
    fail('resync_failed', `status ${r.status}`);
  }

  function onMessage(m: ServerMessage) {
    if (m.type === 'snapshot') apply(m.data);
    else if (m.type === 'revoked') goInactive();
  }

  function openStream(g: number) {
    conn?.close(); // single-flight: never leave an older stream open
    const url = `${api}/tracking/stream`;
    const handlers = {
      onOpen: () => void resync(g),
      onMessage: (m: ServerMessage) => {
        if (g !== gen || c !== conn) return;
        attempt = 0;
        onMessage(m);
      },
      onClose: ({ code, opened }: { code: number | null; opened: boolean }) => {
        if (g !== gen || c !== conn) return;
        conn = null;
        if (code === 4401) return goInactive();
        if (transport === 'ws' && !opened) {
          transport = 'sse';
          fail('websocket_unavailable');
          return openStream(g);
        }
        void reconnect(g);
      },
    };
    const c: StreamConn =
      transport === 'ws'
        ? openWebSocket(wsUrl(url), WS, viewerToken, handlers)
        : openSse(`${url}/sse`, f, viewerToken, handlers);
    conn = c;
  }

  async function reconnect(g: number) {
    reconnecting = true;
    const proceed = await sleep(backoff(), g);
    if (!proceed) return void (reconnecting = false);
    if (now() >= expiresAt && !(await recover(g))) {
      reconnecting = false;
      if (g === gen) void reconnect(g);
      return;
    }
    reconnecting = false;
    if (g === gen) openStream(g);
  }

  async function begin(g: number) {
    if (await startSession(g)) openStream(g);
  }

  async function onVisible() {
    // Nothing to resume until the first session was adopted (begin owns that).
    if (!running || !viewerToken || globalThis.document?.visibilityState === 'hidden') return;
    const g = gen;
    const wasSse = transport === 'sse';
    transport = 'ws'; // back in view: try the WebSocket again
    if (now() >= expiresAt && !(await recover(g))) return;
    if (g !== gen) return;
    await resync(g);
    if (g !== gen || reconnecting) return; // a pending reconnect opens the stream itself
    if (!conn || wasSse) openStream(g);
  }

  const visibility = () => void onVisible();

  return {
    start() {
      if (running) return;
      teardown();
      running = true;
      lastSnapshot = null;
      viewerToken = '';
      reconnecting = false;
      transport = 'ws';
      attempt = 0;
      setState({ kind: 'connecting' });
      globalThis.document?.addEventListener('visibilitychange', visibility);
      void begin(gen);
    },
    stop() {
      teardown();
      globalThis.document?.removeEventListener('visibilitychange', visibility);
    },
    onState(cb) {
      stateCbs.add(cb);
      return () => void stateCbs.delete(cb);
    },
    onError(cb) {
      errorCbs.add(cb);
      return () => void errorCbs.delete(cb);
    },
  };
}

function wsUrl(httpUrl: string): string {
  return httpUrl.replace(/^http/, 'ws');
}
