import { useSyncExternalStore } from 'react';
import type { BundleData, GapData, McEvent } from '../api/types';

export type Mode = 'live' | 'paused' | 'replay';
export type Selection = { kind: 'component' | 'wire' | 'event'; id: string } | null;
export type PanelName = 'left' | 'right' | 'bottom';

/** The event buffer keeps the newest this many events. */
export const MAX_EVENTS = 20_000;
const MAX_BUNDLES = 500;
const MAX_GAPS = 500;
const MAX_FINDINGS = 500;

export interface McState {
  mode: Mode;
  /** Selected order (delivery id); null = whole-system view. */
  orderId: string | null;
  selection: Selection;
  /** Ascending by id, capped at MAX_EVENTS. */
  events: McEvent[];
  gaps: GapData[];
  bundles: BundleData[];
  findings: McEvent[];
  collapsed: Record<PanelName, boolean>;
  reducedMotion: boolean;
}

export interface McStore {
  getState(): McState;
  subscribe(fn: () => void): () => void;
  setMode(m: Mode): void;
  selectOrder(id: string | null): void;
  select(s: Selection): void;
  addEvents(evs: McEvent[]): void;
  addGap(g: GapData): void;
  addBundle(b: BundleData): void;
  addFinding(f: McEvent): void;
  togglePanel(p: PanelName): void;
  setPanels(collapsed: boolean): void;
  setReducedMotion(v: boolean): void;
  reset(): void;
}

function initial(): McState {
  return {
    mode: 'live', orderId: null, selection: null, events: [], gaps: [], bundles: [], findings: [],
    collapsed: { left: false, right: false, bottom: false },
    reducedMotion:
      typeof window !== 'undefined' && typeof window.matchMedia === 'function'
        ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
        : false,
  };
}

export function createStore(): McStore {
  let state = initial();
  let ids = new Set<number>();
  const subs = new Set<() => void>();
  const set = (patch: Partial<McState>) => {
    state = { ...state, ...patch };
    subs.forEach((f) => f());
  };
  const tail = <T,>(a: T[], max: number) => (a.length > max ? a.slice(a.length - max) : a);

  return {
    getState: () => state,
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    setMode: (mode) => set({ mode }),
    selectOrder(orderId) {
      ids = new Set();
      set({ orderId, selection: null, events: [], gaps: [], bundles: [], findings: [] });
    },
    select: (selection) => set({ selection }),
    addEvents(evs) {
      const fresh: McEvent[] = [];
      for (const e of evs) {
        if (ids.has(e.id)) continue;
        ids.add(e.id);
        fresh.push(e);
      }
      if (fresh.length === 0) return;
      let merged = state.events;
      const inOrder = fresh.every((e, i) => e.id > (i === 0 ? (merged[merged.length - 1]?.id ?? -1) : fresh[i - 1].id));
      merged = inOrder ? merged.concat(fresh) : merged.concat(fresh).sort((a, b) => a.id - b.id);
      if (merged.length > MAX_EVENTS) {
        for (const e of merged.slice(0, merged.length - MAX_EVENTS)) ids.delete(e.id);
        merged = merged.slice(merged.length - MAX_EVENTS);
      }
      set({ events: merged });
    },
    addGap: (g) => set({ gaps: tail([...state.gaps, g], MAX_GAPS) }),
    addBundle: (b) => set({ bundles: tail([...state.bundles, b], MAX_BUNDLES) }),
    addFinding(f) {
      if (state.findings.some((x) => x.id === f.id)) return;
      set({ findings: tail([...state.findings, f], MAX_FINDINGS) });
    },
    togglePanel: (p) => set({ collapsed: { ...state.collapsed, [p]: !state.collapsed[p] } }),
    setPanels: (c) => set({ collapsed: { left: c, right: c, bottom: c } }),
    setReducedMotion: (reducedMotion) => set({ reducedMotion }),
    reset() {
      ids = new Set();
      set(initial());
    },
  };
}

export const store = createStore();

export function useStore<T>(selector: (s: McState) => T, s: McStore = store): T {
  return useSyncExternalStore(s.subscribe, () => selector(s.getState()));
}
