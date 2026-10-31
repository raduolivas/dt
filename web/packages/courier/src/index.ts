import { createClient } from './client';
import type { ApiResult } from './client';
import { createGps } from './gps';
import { createQueue } from './queue';
import type { PendingTap } from './queue';
import { createSession, defaultStorage } from './session';
import { createBufferStore } from './store';
import { CourierError } from './types';
import type {
  Courier,
  CourierDelivery,
  CourierNotice,
  CourierOptions,
  CourierState,
  ReportResult,
} from './types';

export { CourierError } from './types';
export type {
  Courier,
  CourierDelivery,
  CourierEventType,
  CourierFix,
  CourierNotice,
  CourierOptions,
  CourierState,
  CourierStorage,
  CourierTokens,
  ReportResult,
} from './types';

const FLUSH_MS = 3_000;
const POLL_MS = 10_000;
const POLL_EMPTY_MS = 20_000;
const BACKLOG_GAP_MS = 1_000; // locations allow 1 batch/s: a backlog drains at that pace
const BATCH_MAX = 50;

type Listeners = {
  deliveries: Set<(d: readonly CourierDelivery[]) => void>;
  state: Set<(s: CourierState) => void>;
  notice: Set<(n: CourierNotice) => void>;
};

export function createCourier(o: CourierOptions): Courier {
  const now = o.now ?? Date.now;
  const client = createClient(o.api, o.fetch);
  const queue = createQueue(createBufferStore(o.indexedDB ?? globalThis.indexedDB), now);
  const storage = o.storage ?? defaultStorage();
  const listeners: Listeners = { deliveries: new Set(), state: new Set(), notice: new Set() };

  let state: CourierState = { gps: 'off', network: 'online', buffered: 0, pendingTaps: 0, session: 'starting' };
  let deliveries: readonly CourierDelivery[] = [];
  let started = false;
  let over = false; // ended, expired or revoked: nothing runs any more
  let wantGps = false;
  let flushing = false;
  let flushAgain = false; // something was queued while a flush ran
  let flushTimer: ReturnType<typeof setInterval> | undefined;
  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  let polling = false;
  let pollAgain = false;
  let tapsBlockedUntil = 0;
  let fixesBlockedUntil = 0;
  let pollBlockedUntil = 0;
  let cleanup: (() => void) | undefined;

  const emit = <K extends keyof Listeners>(k: K, v: Parameters<Parameters<Listeners[K]['add']>[0]>[0]) => {
    for (const fn of [...listeners[k]]) (fn as (x: typeof v) => void)(v);
  };
  const setState = (patch: Partial<CourierState>) => {
    const next = { ...state, ...patch, buffered: queue.fixCount, pendingTaps: queue.tapCount };
    if ((Object.keys(next) as (keyof CourierState)[]).every((k) => next[k] === state[k])) return;
    state = next;
    emit('state', state);
  };
  const setDeliveries = (next: readonly CourierDelivery[]) => {
    if (JSON.stringify(next) === JSON.stringify(deliveries)) return;
    deliveries = next;
    emit('deliveries', deliveries);
    syncGps();
  };
  const net = (ok: boolean) => setState({ network: ok ? 'online' : 'offline' });

  const gps = createGps(
    o.geolocation ?? (typeof navigator === 'undefined' ? undefined : navigator.geolocation),
    (fix) => {
      if (over) return;
      queue.addFix(fix);
      setState({});
      if (queue.fixCount >= BATCH_MAX) void flush();
    },
    (g) => setState({ gps: g }),
  );

  const session = createSession({
    client,
    storage,
    now,
    tokens: o.tokens,
    linkCode: o.linkCode,
    onNetwork: net,
    onDead: (why) => void finish(why, true),
  });

  /** GPS runs only while the session is live and the courier has at least one open delivery. */
  function syncGps() {
    const want = !over && started && deliveries.length > 0;
    if (want === wantGps) return;
    wantGps = want;
    if (want) gps.start();
    else gps.stop();
    schedulePoll();
  }

  /** The session is over: stop collecting, drop everything held, keep nothing. */
  async function finish(why: 'ended' | 'expired', notify: boolean) {
    if (over) return;
    over = true;
    wantGps = false;
    gps.stop();
    clearInterval(flushTimer);
    clearTimeout(pollTimer);
    cleanup?.();
    for (const t of queue.clear()) for (const w of t.waiters) w.reject(new CourierError('session_ended'));
    await session.clear();
    deliveries = [];
    emit('deliveries', deliveries);
    setState({ session: why, gps: 'off' });
    if (notify) emit('notice', { kind: 'ended' });
  }

  /** Runs a bearer call; a 401 anywhere ends the session (ended, revoked and expired look the same to us). */
  async function authed<T>(f: (token: string) => Promise<ApiResult<T>>): Promise<ApiResult<T>> {
    const token = await session.token();
    if (token === null) return { kind: 'unauthorized' };
    const r = await f(token);
    if (r.kind === 'unauthorized') session.unauthorized();
    else if (r.kind === 'network') net(false);
    else net(true);
    return r;
  }

  // ---- deliveries ----------------------------------------------------------------------------------------

  const visible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';

  function schedulePoll() {
    clearTimeout(pollTimer);
    if (over || !started) return;
    const empty = deliveries.length === 0;
    // A hidden page with work stops polling (and wakes on resume); an empty list keeps its slow check.
    if (!empty && !visible()) return;
    const wait = Math.max(empty ? POLL_EMPTY_MS : POLL_MS, pollBlockedUntil - now());
    pollTimer = setTimeout(() => void poll(), wait);
  }

  async function poll() {
    if (over) return;
    if (polling) {
      pollAgain = true;
      return;
    }
    polling = true;
    try {
      const r = await authed((t) => client.call<{ deliveries: CourierDelivery[] }>('GET', '/courier/deliveries', { token: t }));
      if (r.kind === 'ok') setDeliveries(r.data.deliveries);
      else if (r.kind === 'rate_limited') pollBlockedUntil = now() + r.retryAfterMs;
    } finally {
      polling = false;
    }
    if (pollAgain) {
      pollAgain = false;
      return poll();
    }
    schedulePoll();
  }

  // ---- sending -------------------------------------------------------------------------------------------

  /** Sends waiting taps in order, then one batch of fixes. One flush at a time. */
  async function flush() {
    if (over || !started) return;
    if (flushing) {
      flushAgain = true;
      return;
    }
    flushing = true;
    let more = false;
    try {
      queue.prune();
      setState({});
      for (let tap = queue.firstTap(); tap && !over && now() >= tapsBlockedUntil; tap = queue.firstTap()) {
        if (!(await sendTap(tap))) break;
      }
      if (!over && now() >= fixesBlockedUntil) more = await sendFixes();
    } finally {
      flushing = false;
      if (!over) setState({});
    }
    if (flushAgain && !over) {
      flushAgain = false;
      return flush();
    }
    if (more) setTimeout(() => void flush(), BACKLOG_GAP_MS);
  }

  /** True when the tap is settled (answered or dropped) and the next may go. */
  async function sendTap(tap: PendingTap): Promise<boolean> {
    const { eventId, deliveryId, type, reason, occurredAt } = tap.value;
    const r = await authed((t) =>
      client.call<{ outcome: Exclude<ReportResult['outcome'], 'not_yours'>; delivery: CourierDelivery }>(
        'POST',
        `/courier/deliveries/${encodeURIComponent(deliveryId)}/events`,
        { token: t, body: { event_id: eventId, type, occurred_at: occurredAt, ...(reason ? { reason } : {}) } },
      ),
    );
    if (over) return false; // a 401: finish() already failed the waiters
    const settle = (res: ReportResult | Error) => {
      queue.ackTap(tap);
      for (const w of tap.waiters) {
        if (res instanceof Error) w.reject(res);
        else w.resolve(res);
      }
      setState({});
    };
    switch (r.kind) {
      case 'ok':
        settle({ outcome: r.data.outcome, delivery: r.data.delivery });
        applyDelivery(r.data.delivery);
        void poll(); // the list is refreshed after every tap
        return true;
      case 'rate_limited':
        tapsBlockedUntil = now() + r.retryAfterMs;
        return false;
      case 'network':
        return false; // stays queued with the same event_id
      case 'error':
        if (r.status === 404) {
          settle({ outcome: 'not_yours' });
          emit('notice', { kind: 'reassigned', deliveryId });
          void poll(); // the next poll removes the card
          return true;
        }
        if (r.status === 409) {
          settle({ outcome: 'conflict' });
          void poll();
          return true;
        }
        if (r.status >= 500) return false;
        settle(new CourierError(r.code ?? 'rejected')); // a tap the server refuses outright must not block the ones behind it
        return true;
      default:
        return false;
    }
  }

  /** True when more full batches are waiting. */
  async function sendFixes(): Promise<boolean> {
    const batch = queue.nextBatch();
    if (batch.length === 0) return false;
    const r = await authed((t) =>
      client.call<{ accepted: number; dropped: number }>('POST', '/courier/locations', {
        token: t,
        body: { fixes: batch.map((e) => e.value) },
      }),
    );
    if (over) return false;
    if (r.kind === 'ok' || (r.kind === 'error' && r.status < 500)) {
      // A 4xx other than 429 means this batch will never be accepted: drop it rather than loop on it.
      queue.ackFixes(batch);
      setState({});
      return queue.fixCount >= BATCH_MAX;
    }
    if (r.kind === 'rate_limited') fixesBlockedUntil = now() + r.retryAfterMs;
    return false;
  }

  function applyDelivery(d: CourierDelivery) {
    setDeliveries(deliveries.map((x) => (x.id === d.id ? d : x)));
  }

  // ---- public ------------------------------------------------------------------------------------------

  const onVisible = () => {
    if (!visible() || over) return;
    session.ensureFresh();
    void poll();
    void flush();
  };
  const onOnline = () => void flush();

  return {
    async start() {
      if (started || over) return;
      started = true;
      try {
        await queue.load();
        await session.init();
      } catch (e) {
        started = false;
        if (!over) {
          // A used or invalid code means this link is finished; a network error leaves start() to be called again.
          if (e instanceof CourierError && e.code !== 'network') await finish('ended', false);
        }
        throw e;
      }
      setState({ session: 'active' });
      if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisible);
      if (typeof window !== 'undefined') window.addEventListener('online', onOnline);
      cleanup = () => {
        if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisible);
        if (typeof window !== 'undefined') window.removeEventListener('online', onOnline);
      };
      flushTimer = setInterval(() => void flush(), FLUSH_MS);
      void poll();
      void flush(); // taps and fixes left over from before a reload
    },

    async end() {
      if (over) return;
      wantGps = false;
      gps.stop();
      if (started) {
        await flush(); // best effort: taps still waiting go out before the session closes
        const token = await session.token();
        if (token !== null) await client.call('POST', '/courier/session/end', { token });
      }
      await finish('ended', false);
    },

    get deliveries() {
      return deliveries;
    },
    get state() {
      return state;
    },

    report(deliveryId, type, opts) {
      if (over) return Promise.reject(new CourierError('session_ended'));
      return new Promise<ReportResult>((resolve, reject) => {
        // A double tap joins the tap still waiting: one event_id, one transition.
        const tap =
          queue.findTap(deliveryId, type) ??
          queue.addTap({
            eventId: newId(),
            deliveryId,
            type,
            reason: opts?.reason,
            occurredAt: new Date(now()).toISOString(),
          });
        tap.waiters.push({ resolve, reject });
        setState({});
        void flush();
      });
    },

    on(ev: keyof Listeners, fn: never) {
      (listeners[ev] as Set<unknown>).add(fn);
      return () => void (listeners[ev] as Set<unknown>).delete(fn);
    },
  } as Courier;
}

function newId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}${Math.random().toString(36).slice(2)}`;
}
