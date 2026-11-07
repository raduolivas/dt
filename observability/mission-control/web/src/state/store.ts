import { useSyncExternalStore } from 'react';
import type { BundleData, GapData, McEvent, TimelineGap } from '../api/types';

export type Mode = 'live' | 'paused' | 'replay';
export type Selection = { kind: 'component' | 'wire' | 'event'; id: string } | null;
export type PanelName = 'left' | 'right' | 'bottom';

/** The event buffer keeps the newest this many events. */
export const MAX_EVENTS = 20_000;
const MAX_BUNDLES = 500;
const MAX_GAPS = 500;
const MAX_FINDINGS = 500;

/** A server bundle as kept: seq orders bundles locally; streamId is the SSE id it came with (the high-water mark). */
export type StoredBundle = BundleData & { seq: number; streamId?: number };

export interface McState {
  mode: Mode;
  /** Selected order (delivery id); null = whole-system view. */
  orderId: string | null;
  selection: Selection;
  /** Ascending by id, capped at MAX_EVENTS. */
  events: McEvent[];
  gaps: GapData[];
  bundles: StoredBundle[];
  findings: McEvent[];
  collapsed: Record<PanelName, boolean>;
  reducedMotion: boolean;
  /** Newest stream message id seen (events, bundles, gaps); the resume point for the next stream. */
  lastEventId: number | null;
  /** Replay cursor (epoch ms) while mode is replay; null otherwise. */
  replayAt: number | null;
  /** Time gaps of the selected order's timeline (Mission Control down, no trace data). */
  timelineGaps: TimelineGap[];
  /** Findings the left panel loaded from the API (order detectors or system warnings); the top-bar count reads them too. */
  panelFindings: McEvent[];
}

export interface McStore {
  getState(): McState;
  subscribe(fn: () => void): () => void;
  setMode(m: Mode): void;
  selectOrder(id: string | null): void;
  select(s: Selection): void;
  addEvents(evs: McEvent[]): void;
  /** Like addEvents, but coalesced into one update per animation frame (the stream path). */
  queueEvents(evs: McEvent[]): void;
  /** Like addBundle, through the same per-frame queue as queueEvents, in stream-id order. */
  queueBundle(b: BundleData, streamId: number): void;
  addGap(g: GapData): void;
  addBundle(b: BundleData, streamId?: number): void;
  addFinding(f: McEvent): void;
  togglePanel(p: PanelName): void;
  setPanels(collapsed: boolean): void;
  setReducedMotion(v: boolean): void;
  noteStreamId(id: number): void;
  setReplayAt(t: number | null): void;
  setTimelineGaps(g: TimelineGap[]): void;
  setPanelFindings(f: McEvent[]): void;
  reset(): void;
}

function initial(): McState {
  return {
    mode: 'live', orderId: null, selection: null, events: [], gaps: [], bundles: [], findings: [], replayAt: null, timelineGaps: [], panelFindings: [],
    collapsed: { left: false, right: false, bottom: false }, lastEventId: null,
    reducedMotion:
      typeof window !== 'undefined' && typeof window.matchMedia === 'function'
        ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
        : false,
  };
}

/** Longest a queued stream message waits when no animation frame comes (a hidden tab pauses rAF). */
const FLUSH_FALLBACK_MS = 250;

const nextFrame = (cb: () => void) => {
  let done = false;
  const run = () => {
    if (done) return;
    done = true;
    cb();
  };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(run);
  setTimeout(run, FLUSH_FALLBACK_MS);
};

type Queued = { kind: 'events'; at: number; evs: McEvent[] } | { kind: 'bundle'; at: number; b: BundleData; streamId: number };

/**
 * frame schedules the flush of queueEvents / queueBundle (default: the next
 * animation frame, or FLUSH_FALLBACK_MS when frames are paused). Batching
 * matters: addEvents copies the buffer and notifies every subscriber, so one
 * update per frame instead of one per stream message. Events and bundles share
 * the queue and are applied together in stream-id order, so a bundle never
 * lands before the individual events the stream sent ahead of it. A queue that
 * reaches MAX_EVENTS is flushed at once.
 */
export function createStore(frame: (cb: () => void) => void = nextFrame): McStore {
  let state = initial();
  let ids = new Set<number>();
  let queued: Queued[] = [];
  let queuedCount = 0;
  let scheduled = false;
  let bundleSeq = 0;
  const subs = new Set<() => void>();
  const set = (patch: Partial<McState>) => {
    state = { ...state, ...patch };
    subs.forEach((f) => f());
  };
  const tail = <T,>(a: T[], max: number) => (a.length > max ? a.slice(a.length - max) : a);

  /** The buffer with evs merged in (deduplicated, ascending, capped), or null when nothing is new. */
  const merge = (evs: McEvent[]): McEvent[] | null => {
    const fresh: McEvent[] = [];
    for (const e of evs) {
      if (ids.has(e.id)) continue;
      ids.add(e.id);
      fresh.push(e);
    }
    if (fresh.length === 0) return null;
    let merged = state.events;
    const inOrder = fresh.every((e, i) => e.id > (i === 0 ? (merged[merged.length - 1]?.id ?? -1) : fresh[i - 1].id));
    merged = inOrder ? merged.concat(fresh) : merged.concat(fresh).sort((a, b) => a.id - b.id);
    if (merged.length > MAX_EVENTS) {
      for (const e of merged.slice(0, merged.length - MAX_EVENTS)) ids.delete(e.id);
      merged = merged.slice(merged.length - MAX_EVENTS);
    }
    return merged;
  };
  const withBundles = (bs: { b: BundleData; streamId: number | undefined }[]) =>
    tail([...state.bundles, ...bs.map(({ b, streamId }) => ({ ...b, seq: ++bundleSeq, streamId }))], MAX_BUNDLES);

  const flush = () => {
    const items = queued;
    queued = [];
    queuedCount = 0;
    if (items.length === 0) return;
    // stream-id order; a bundle carries the high-water mark, so it sorts after events with that id
    const rank = (q: Queued) => (q.kind === 'bundle' ? 1 : 0);
    items.sort((x, y) => x.at - y.at || rank(x) - rank(y));
    const evs: McEvent[] = [];
    const bs: { b: BundleData; streamId: number }[] = [];
    for (const q of items) {
      if (q.kind === 'events') evs.push(...q.evs);
      else bs.push({ b: q.b, streamId: q.streamId });
    }
    const patch: Partial<McState> = {};
    const merged = merge(evs);
    if (merged) patch.events = merged;
    if (bs.length) patch.bundles = withBundles(bs);
    if (patch.events || patch.bundles) set(patch);
  };
  const enqueue = (q: Queued, n: number) => {
    queued.push(q);
    queuedCount += n;
    if (queuedCount >= MAX_EVENTS) {
      flush();
      return;
    }
    if (scheduled) return;
    scheduled = true;
    frame(() => {
      scheduled = false;
      flush();
    });
  };

  const api: McStore = {
    getState: () => state,
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    // Leaving replay drops the cursor, so a live scene never inherits a stale one.
    setMode: (mode) => set(mode === 'replay' ? { mode } : { mode, replayAt: null }),
    setReplayAt: (replayAt) => set({ replayAt }),
    setTimelineGaps: (timelineGaps) => set({ timelineGaps }),
    setPanelFindings: (panelFindings) => set({ panelFindings }),
    selectOrder(orderId) {
      ids = new Set();
      queued = [];
      queuedCount = 0;
      // a replay cursor belongs to one order's buffer: back to live with the new order
      set({ orderId, selection: null, events: [], gaps: [], bundles: [], findings: [], timelineGaps: [], panelFindings: [], replayAt: null, mode: state.mode === 'replay' ? 'live' : state.mode });
    },
    select: (selection) => set({ selection }),
    addEvents(evs) {
      const merged = merge(evs);
      if (merged) set({ events: merged });
    },
    queueEvents(evs) {
      if (evs.length === 0) return;
      enqueue({ kind: 'events', at: evs[evs.length - 1].id, evs }, evs.length);
    },
    queueBundle(b, streamId) {
      enqueue({ kind: 'bundle', at: streamId, b, streamId }, 1);
    },
    addGap: (g) => set({ gaps: tail([...state.gaps, g], MAX_GAPS) }),
    addBundle: (b, streamId) => set({ bundles: withBundles([{ b, streamId }]) }),
    addFinding(f) {
      if (state.findings.some((x) => x.id === f.id)) return;
      set({ findings: tail([...state.findings, f], MAX_FINDINGS) });
    },
    togglePanel: (p) => set({ collapsed: { ...state.collapsed, [p]: !state.collapsed[p] } }),
    setPanels: (c) => set({ collapsed: { left: c, right: c, bottom: c } }),
    setReducedMotion: (reducedMotion) => set({ reducedMotion }),
    noteStreamId(id) {
      if (Number.isFinite(id) && (state.lastEventId === null || id > state.lastEventId)) set({ lastEventId: id });
    },
    reset() {
      ids = new Set();
      queued = [];
      queuedCount = 0;
      set(initial());
    },
  };
  return api;
}

export const store = createStore();

export function useStore<T>(selector: (s: McState) => T, s: McStore = store): T {
  return useSyncExternalStore(s.subscribe, () => selector(s.getState()));
}
