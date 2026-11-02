import type { CourierFix, CourierEventType, ReportResult } from './types';
import type { BufferStore, Entry } from './store';

export const FIX_MAX_AGE_MS = 10 * 60_000;
export const BATCH_MAX = 50;

/** A status tap. `eventId` is made once and sent on every retry, so the server applies it once. */
export interface Tap {
  eventId: string;
  deliveryId: string;
  type: CourierEventType;
  reason?: string;
  occurredAt: string;
}
export interface PendingTap extends Entry<Tap> {
  /** Callers of `report()` waiting for the answer. Not persisted: after a reload the tap is still sent. */
  waiters: { resolve: (r: ReportResult) => void; reject: (e: Error) => void }[];
}

/** The offline buffer: fixes (droppable, ageing) and taps (never dropped, sent first and in order). */
export function createQueue(store: BufferStore, now: () => number) {
  let fixes: Entry<CourierFix>[] = [];
  let taps: PendingTap[] = [];
  let seq = 0;

  return {
    async load() {
      const s = await store.load();
      fixes = s.fixes as Entry<CourierFix>[];
      taps = (s.taps as Entry<Tap>[]).map((e) => ({ ...e, waiters: [] }));
      seq = Math.max(0, ...fixes.map((e) => e.key), ...taps.map((e) => e.key));
    },
    get fixCount() {
      return fixes.length;
    },
    get tapCount() {
      return taps.length;
    },
    addFix(fix: CourierFix) {
      const e = { key: ++seq, value: fix };
      fixes.push(e);
      store.put('fixes', e.key, fix);
    },
    /** Drops fixes older than 10 min; returns how many went. */
    prune() {
      const cutoff = now() - FIX_MAX_AGE_MS;
      const old = fixes.filter((e) => Date.parse(e.value.t) < cutoff);
      if (old.length) this.ackFixes(old);
      return old.length;
    },
    nextBatch(): Entry<CourierFix>[] {
      return fixes.slice(0, BATCH_MAX);
    },
    ackFixes(done: Entry<CourierFix>[]) {
      const keys = new Set(done.map((e) => e.key));
      fixes = fixes.filter((e) => !keys.has(e.key));
      store.remove('fixes', [...keys]);
    },
    addTap(tap: Tap): PendingTap {
      const e: PendingTap = { key: ++seq, value: tap, waiters: [] };
      taps.push(e);
      store.put('taps', e.key, tap);
      return e;
    },
    /** A tap for the same delivery and type that is still waiting: a double tap joins it instead of making another. */
    findTap(deliveryId: string, type: CourierEventType) {
      return taps.find((e) => e.value.deliveryId === deliveryId && e.value.type === type);
    },
    firstTap: () => taps[0] as PendingTap | undefined,
    ackTap(e: PendingTap) {
      taps = taps.filter((t) => t !== e);
      store.remove('taps', [e.key]);
    },
    /** Empties everything and returns the taps that were still waiting, for the caller to fail. */
    clear(): PendingTap[] {
      const left = taps;
      fixes = [];
      taps = [];
      store.clear();
      return left;
    },
  };
}
export type Queue = ReturnType<typeof createQueue>;
