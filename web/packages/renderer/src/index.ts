import type { TrackingSnapshot } from '@dt/contract';
import { SnapshotGate } from './snapshot';
import type { TileSourceSpec } from './tiles/source';
import { CityScene } from './view/CityScene';

export type { TileSourceSpec } from './tiles/source';
export type { Insets } from './view/framing';
import type { Insets } from './view/framing';
export { activeLeg, activeLegKind, toLatLng, viewKind, type ViewKind } from './snapshot';

/** `light` is the day city, `dark` the night city; `auto` (partner branding) follows `prefers-color-scheme`. */
export type RendererTheme = 'light' | 'dark' | 'auto';

export interface RendererOptions {
  mode?: '3d' | '2d';
  /** Default `light`. Changing it later fades between day and night (at once under reduced motion). */
  theme?: RendererTheme;
  /** No camera moves, bounces, sway, shimmer or pulses (the courier still moves). Unset: `prefers-reduced-motion`. */
  reducedMotion?: boolean;
  /** Absent = the procedural grid city (dev only). */
  tiles?: TileSourceSpec;
  /** Partner primary color for the ribbon and pins. */
  accent?: string;
  /** Milliseconds; tests freeze it. */
  clock?: () => number;
  onFrameStats?: (s: { fps: number; drawCalls: number }) => void;
  /** The first tiles of a delivery are on screen: bytes fetched, time since the delivery started, tile count. */
  onTiles?: (s: { bytes: number; ms: number; tiles: number }) => void;
}

export interface Renderer {
  update(s: TrackingSnapshot): void;
  setMode(m: '3d' | '2d'): void;
  /** Day or night: a short fade, or at once under reduced motion. */
  setTheme(t: RendererTheme): void;
  setAccent(c: string | null): void;
  overview(): void;
  setFollow(on: boolean): void;
  /** Screen space the page's cards cover (CSS px); framing keeps store, home and courier clear of it. */
  setInsets(i: Insets): void;
  /** Overrides `prefers-reduced-motion` from now on. */
  setReducedMotion(on: boolean): void;
  destroy(): void;
}

/**
 * Draws a `TrackingSnapshot` in `container`. The snapshot is the only input: the renderer never
 * infers a hidden courier, never shows the courier photo and never shows the dropoff label.
 */
const resolveTheme = (t: RendererTheme | undefined): 'light' | 'dark' =>
  t === 'auto' ? (typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : (t ?? 'light');

export function createRenderer(container: HTMLElement, o: RendererOptions = {}): Renderer {
  const clock = o.clock ?? (() => performance.now());
  // Reduced motion: the option when given, else the system setting (followed while it changes).
  const media = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : null;
  let reducedSet = o.reducedMotion !== undefined;
  const scene = new CityScene(container, {
    accent: o.accent,
    tiles: o.tiles,
    theme: resolveTheme(o.theme),
    onTiles: o.onTiles,
    clock: o.clock,
    reducedMotion: o.reducedMotion ?? media?.matches ?? false,
  });
  const onMedia = (e: MediaQueryListEvent) => {
    if (!reducedSet) scene.setReducedMotion(e.matches);
  };
  media?.addEventListener?.('change', onMedia);
  const gate = new SnapshotGate();
  // `mode` is accepted now and used by the 2D task.
  const state = { mode: o.mode ?? '3d' };
  void state;

  let raf = 0;
  let last = clock();
  let statsFrom = last;
  let frames = 0;
  let alive = true;

  const loop = () => {
    if (!alive) return;
    const now = clock();
    const dt = Math.max(0, (now - last) / 1000);
    last = now;
    scene.frame(dt);
    frames++;
    if (o.onFrameStats && now - statsFrom >= 1000) {
      o.onFrameStats({ fps: (frames * 1000) / (now - statsFrom), drawCalls: scene.drawCalls });
      statsFrom = now;
      frames = 0;
    }
    raf = requestAnimationFrame(loop);
  };
  raf = requestAnimationFrame(loop);

  return {
    update(s) {
      if (alive && gate.accept(s)) scene.onSnapshot(s);
    },
    setMode(m) {
      state.mode = m;
    },
    setTheme(t) {
      scene.setTheme(resolveTheme(t));
    },
    setAccent(c) {
      scene.setAccent(c);
    },
    overview: () => scene.overview(),
    setFollow: (on) => scene.setFollow(on),
    setInsets: (i) => scene.setInsets(i),
    setReducedMotion(on) {
      reducedSet = true;
      scene.setReducedMotion(on);
    },
    destroy() {
      alive = false;
      media?.removeEventListener?.('change', onMedia);
      cancelAnimationFrame(raf);
      scene.dispose();
    },
  };
}
