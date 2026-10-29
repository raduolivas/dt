import type { TrackingSnapshot } from '@dt/contract';
import { SnapshotGate } from './snapshot';
import type { TileSourceSpec } from './tiles/source';
import { CityScene } from './view/CityScene';

export type { TileSourceSpec } from './tiles/source';
export type { Insets } from './view/framing';
import type { Insets } from './view/framing';
export { activeLeg, activeLegKind, toLatLng, viewKind, type ViewKind } from './snapshot';

export interface RendererOptions {
  mode?: '3d' | '2d';
  theme?: 'light' | 'dark';
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
  setTheme(t: 'light' | 'dark'): void;
  setAccent(c: string | null): void;
  overview(): void;
  setFollow(on: boolean): void;
  /** Screen space the page's cards cover (CSS px); framing keeps store, home and courier clear of it. */
  setInsets(i: Insets): void;
  destroy(): void;
}

/**
 * Draws a `TrackingSnapshot` in `container`. The snapshot is the only input: the renderer never
 * infers a hidden courier, never shows the courier photo and never shows the dropoff label.
 */
export function createRenderer(container: HTMLElement, o: RendererOptions = {}): Renderer {
  const clock = o.clock ?? (() => performance.now());
  const scene = new CityScene(container, { accent: o.accent, tiles: o.tiles, theme: o.theme, onTiles: o.onTiles, clock: o.clock });
  const gate = new SnapshotGate();
  // `mode` and `reducedMotion` are accepted now and used by the 2D and motion tasks.
  const state = { mode: o.mode ?? '3d', theme: o.theme ?? 'light', reducedMotion: o.reducedMotion ?? false };
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
      state.theme = t;
      scene.setTheme(t);
    },
    setAccent(c) {
      scene.setAccent(c);
    },
    overview: () => scene.overview(),
    setFollow: (on) => scene.setFollow(on),
    setInsets: (i) => scene.setInsets(i),
    destroy() {
      alive = false;
      cancelAnimationFrame(raf);
      scene.dispose();
    },
  };
}
