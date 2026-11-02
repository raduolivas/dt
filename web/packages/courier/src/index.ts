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
const END_WAIT_MS = 5_000; // end() waits this long at most for the last flush and session/end

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
  let ready = false; // session established and the buffer loaded: report() may queue
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
    locks: o.locks,
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

  /**
   * Runs a bearer call. A 401 earns one refresh; when that is refused too the session is over (ended, revoked and
   * expired look the same to us) and `over` is set before this returns. The refused call itself is not retried here:
   * a tap or a batch stays queued for the next flush.
   */
  async function authed<T>(f: (token: string) => Promise<ApiResult<T>>): Promise<ApiResult<T>> {
    const token = await session.token();
    if (token === null) return { kind: 'unauthorized' };
    const r = await f(token);
    if (r.kind === 'unauthorized') await session.unauthorized(token);
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
    if (over) return false; // a 401 the refresh could not cure: finish() already failed the waiters
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
          if (tap.waiters.length === 0) emit('notice', { kind: 'rejected', deliveryId, type });
          settle({ outcome: 'conflict' });
          void poll();
          return true;
        }
        if (r.status >= 500) return false;
        // A tap the server refuses outright must not block the ones behind it. With nobody waiting (it was restored
        // after a reload) the page would never learn it did not count, so say so.
        if (tap.waiters.length === 0) emit('notice', { kind: 'rejected', deliveryId, type });
        settle(new CourierError(r.code ?? 'rejected'));
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
        const resumed = await session.init();
        await queue.load();
        // Leftovers from an older shift must not go out under this one.
        if (!resumed) for (const t of queue.clear()) for (const w of t.waiters) w.reject(new CourierError('session_ended'));
      } catch (e) {
        started = false;
        // Only a used or invalid link is final. Anything else (network, 429, 5xx) can be tried again.
        if (!over && e instanceof CourierError && (e.code === 'link_code_used' || e.code === 'link_code_invalid')) {
          await finish('ended', false);
        }
        throw e;
      }
      ready = true;
      setState({ session: 'active' });
      if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisible);
      if (typeof window !== 'undefined') window.addEventListener('online', onOnline);
      cleanup = () => {
        if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisible);
        if (typeof window !== 'undefined') window.removeEventListener('online', onOnline);
      };
      flushTimer = setInterval(() => void flush(), FLUSH_MS);
      gps.ask(); // the permission prompt, once, even with nothing assigned yet
      void poll();
      void flush(); // taps and fixes left over from before a reload
    },

    async end() {
      if (over) return;
      wantGps = false;
      gps.stop();
      // Closing must never wait for the network: only try the farewell when we are online with a usable token.
      const token = ready ? session.readyToken() : null;
      if (token !== null && state.network === 'online') {
        let timer: ReturnType<typeof setTimeout> | undefined;
        const giveUp = new Promise<void>((r) => (timer = setTimeout(r, END_WAIT_MS)));
        await Promise.race([
          (async () => {
            await flush(); // best effort: taps still waiting go out before the session closes
            if (!over) await client.call('POST', '/courier/session/end', { token });
          })(),
          giveUp,
        ]);
        clearTimeout(timer);
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
      if (!ready) return Promise.reject(new CourierError('not_started'));
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
