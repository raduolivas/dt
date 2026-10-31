// Test-only: snapshots and stand-ins for the renderer and the viewer client, so the SDK runs headless.
import type { Branding, TrackingCourier, TrackingSnapshot } from '@dt/contract';
import type { Insets, Renderer, RendererOptions } from '@dt/renderer';
import type { ViewerClient, ViewerError, ViewerOptions, ViewerState } from '@dt/viewer';

/** 2026-10-29 12:38:20 in Amsterdam (CET): ETA windows are counted from here. */
export const NOW = Date.parse('2026-10-29T11:38:20Z');

export function snapshot(over: Partial<TrackingSnapshot> = {}): TrackingSnapshot {
  return {
    delivery_id: 'del_1',
    delivery_model: 'end_to_end',
    status: 'confirmed',
    courier: null,
    pickup: { location: { lat: 52.3731, lng: 4.8926 }, name: 'Food Hub' },
    dropoff: { location: { lat: 52.3705, lng: 4.8895 }, label: null },
    route: { to_pickup: null, to_dropoff: [[52.3731, 4.8926], [52.3705, 4.8895]] },
    eta_seconds: null,
    eta_window: null,
    eta_state: 'calculating',
    eta_quality: 'osrm',
    distance_remaining_m: null,
    progress: null,
    timeline: [{ status: 'confirmed', at: '2026-10-29T11:30:00Z' }],
    updated_at: '2026-10-29T11:38:20Z',
    version: 1,
    ...over,
  };
}

export function courier(over: Partial<TrackingCourier> = {}): TrackingCourier {
  return {
    display_name: 'Lucas M.',
    vehicle: 'bicycle',
    photo_url: 'https://partner.example/photos/lucas.jpg',
    position: { lat: 52.372, lng: 4.891 },
    heading_deg: 90,
    speed_kmh: 16,
    position_at: '2026-10-29T11:38:15Z',
    signal: 'live',
    ...over,
  };
}

export const BRANDING: Branding = {
  display_name: 'Pizza Piazza',
  primary_color: '#ffeb3b',
  theme: 'light',
  logo_url: 'https://track.example/logos/t1/logo.webp',
};

export const live = (s: TrackingSnapshot, branding: Branding | null = null): ViewerState => ({ kind: 'live', snapshot: s, branding });

// ---- renderer stand-in -------------------------------------------------------------------------------------------

export interface FakeRenderer extends Renderer {
  container: HTMLElement;
  options: RendererOptions;
  calls: { setMode: string[]; setTheme: string[]; setAccent: (string | null)[]; setInsets: Insets[]; update: TrackingSnapshot[]; overview: number };
  /** Every call in order, by name. */
  log: string[];
  destroyed: boolean;
}

/** What the fake renderers hold right now: WebGL contexts and tile workers (released by `destroy`). */
export const gpu = { contexts: 0, workers: 0 };
export const renderers: FakeRenderer[] = [];

export function createRenderer(container: HTMLElement, options: RendererOptions = {}): Renderer {
  gpu.contexts++;
  gpu.workers++;
  const canvas = document.createElement('canvas');
  canvas.className = 'scene-canvas';
  const credit = document.createElement('div');
  credit.className = 'dt-attribution';
  credit.textContent = '© OpenStreetMap contributors · © OpenMapTiles';
  container.append(canvas, credit);
  const r: FakeRenderer = {
    container,
    options,
    calls: { setMode: [], setTheme: [], setAccent: [], setInsets: [], update: [], overview: 0 },
    log: [],
    destroyed: false,
    update: (s) => void (r.log.push('update'), r.calls.update.push(s)),
    setMode: (m) => void r.calls.setMode.push(m),
    setTheme: (t) => void r.calls.setTheme.push(t),
    setAccent: (c) => void r.calls.setAccent.push(c),
    setInsets: (i) => void (r.log.push('setInsets'), r.calls.setInsets.push(i)),
    overview() {
      r.log.push('overview');
      r.calls.overview++;
    },
    setFollow() {},
    setReducedMotion() {},
    destroy() {
      if (r.destroyed) return;
      r.destroyed = true;
      gpu.contexts--;
      gpu.workers--;
      canvas.remove();
      credit.remove();
    },
  };
  renderers.push(r);
  return r;
}

// ---- viewer client stand-in --------------------------------------------------------------------------------------

export interface FakeViewer extends ViewerClient {
  options: ViewerOptions;
  running: boolean;
  listeners(): number;
  push(s: ViewerState): void;
  fail(e: ViewerError): void;
}

export const viewers: FakeViewer[] = [];

export function createViewer(options: ViewerOptions): ViewerClient {
  const states = new Set<(s: ViewerState) => void>();
  const errors = new Set<(e: ViewerError) => void>();
  const v: FakeViewer = {
    options,
    running: false,
    listeners: () => states.size + errors.size,
    push: (s) => [...states].forEach((cb) => cb(s)),
    fail: (e) => [...errors].forEach((cb) => cb(e)),
    start() {
      v.running = true;
      v.push({ kind: 'connecting' });
    },
    stop() {
      v.running = false;
    },
    onState(cb) {
      states.add(cb);
      return () => void states.delete(cb);
    },
    onError(cb) {
      errors.add(cb);
      return () => void errors.delete(cb);
    },
  };
  viewers.push(v);
  return v;
}

export function resetFakes(): void {
  renderers.length = 0;
  viewers.length = 0;
  gpu.contexts = 0;
  gpu.workers = 0;
}

export const lastRenderer = (): FakeRenderer => renderers[renderers.length - 1]!;
export const lastViewer = (): FakeViewer => viewers[viewers.length - 1]!;

// ---- listeners and observers -------------------------------------------------------------------------------------

/** Counts listeners added and not yet removed on any EventTarget, and live ResizeObservers. Call `restore` after. */
export function trackListeners() {
  const live = new Set<string>();
  const ids = new WeakMap<object, number>();
  let next = 1;
  const id = (o: object) => {
    if (!ids.has(o)) ids.set(o, next++);
    return ids.get(o)!;
  };
  const key = (t: object, type: string, l: unknown, opts?: boolean | EventListenerOptions) =>
    `${id(t)}|${type}|${l && typeof l === 'object' ? id(l) : l ? id(l as object) : 0}|${typeof opts === 'boolean' ? opts : !!opts?.capture}`;
  const proto = EventTarget.prototype;
  const add = proto.addEventListener;
  const remove = proto.removeEventListener;
  proto.addEventListener = function (this: EventTarget, type: string, l: EventListenerOrEventListenerObject | null, o?: boolean | AddEventListenerOptions) {
    if (l) live.add(key(this, type, l, o));
    return add.call(this, type, l, o);
  };
  proto.removeEventListener = function (this: EventTarget, type: string, l: EventListenerOrEventListenerObject | null, o?: boolean | EventListenerOptions) {
    if (l) live.delete(key(this, type, l, o));
    return remove.call(this, type, l, o);
  };
  const RO = globalThis.ResizeObserver;
  const observers = new Set<object>();
  globalThis.ResizeObserver = class {
    observe() {
      observers.add(this);
    }
    unobserve() {}
    disconnect() {
      observers.delete(this);
    }
  } as unknown as typeof ResizeObserver;
  return {
    listeners: () => live.size,
    observers: () => observers.size,
    restore() {
      proto.addEventListener = add;
      proto.removeEventListener = remove;
      globalThis.ResizeObserver = RO;
    },
  };
}

/** A controllable `prefers-color-scheme: dark` query; other queries never match. */
export function fakeColorScheme(dark: boolean) {
  const mql = new EventTarget() as EventTarget & { matches: boolean; media: string };
  mql.matches = dark;
  mql.media = '(prefers-color-scheme: dark)';
  const other = () => Object.assign(new EventTarget(), { matches: false, media: '' });
  const matchMedia = (q: string) => (q.includes('prefers-color-scheme') ? mql : other());
  return {
    matchMedia: matchMedia as unknown as typeof window.matchMedia,
    set(d: boolean) {
      mql.matches = d;
      mql.dispatchEvent(Object.assign(new Event('change'), { matches: d }));
    },
  };
}
