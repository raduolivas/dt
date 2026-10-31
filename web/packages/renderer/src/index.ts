import type * as THREE from 'three';
import type { TrackingSnapshot } from '@dt/contract';
import { SnapshotGate } from './snapshot';
import type { TileSourceSpec } from './tiles/source';
import { CityScene, type SceneLabels } from './view/CityScene';
import { FallbackView } from './view/fallback';
import { AdaptiveQuality, isPhone } from './view/quality';
import './view/scene.css';

export type { TileSourceSpec } from './tiles/source';
export type { Insets } from './view/framing';
export type { SceneLabels } from './view/CityScene';
import type { Insets } from './view/framing';
export { activeLeg, activeLegKind, toLatLng, viewKind, type ViewKind } from './snapshot';

/** `light` is the day city, `dark` the night city; `auto` (partner branding) follows `prefers-color-scheme`. */
export type RendererTheme = 'light' | 'dark' | 'auto';

/** Map data credit, always in the container (3D and fallback). */
export const ATTRIBUTION = '© OpenStreetMap contributors · © OpenMapTiles';
/** With no tile on screen this long after the delivery started, the renderer draws the 2D fallback map instead. */
export const TILE_FALLBACK_MS = 10_000;
/** A lost WebGL context not restored within this long (iOS may never give it back): the 2D fallback map takes over. */
export const CONTEXT_FALLBACK_MS = 3_000;

export interface RendererOptions {
  /** `3d` (default): the isometric toy city; `2d`: the same scene seen straight down. */
  mode?: '3d' | '2d';
  /** Default `light`. Changing it later fades between day and night (at once under reduced motion). */
  theme?: RendererTheme;
  /** No camera moves, bounces, sway, shimmer or pulses (the courier still moves). Unset: `prefers-reduced-motion`. */
  reducedMotion?: boolean;
  /** Absent = the procedural grid city (dev only). */
  tiles?: TileSourceSpec;
  /** Partner primary color for the ribbon and pins. */
  accent?: string;
  /** The scene's label text in the page's language (the home label); default English. */
  labels?: Partial<SceneLabels>;
  /** Milliseconds; tests freeze it. */
  clock?: () => number;
  /** Once per second. */
  onFrameStats?: (s: { fps: number; drawCalls: number }) => void;
  /** The first tiles of a delivery are on screen: bytes fetched, time since the delivery started, tile count. */
  onTiles?: (s: { bytes: number; ms: number; tiles: number }) => void;
}

export interface Renderer {
  update(s: TrackingSnapshot): void;
  /** 2D looks straight down on the same scene (no rotation, zoom kept); smooth unless reduced motion. */
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

/** @internal Test seams. */
export interface RendererDeps {
  /** Makes the WebGL renderer (a mock in tests); skips the WebGL probe. */
  webgl?: () => THREE.WebGLRenderer;
}

const resolveTheme = (t: RendererTheme | undefined): 'light' | 'dark' =>
  t === 'auto' ? (typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : (t ?? 'light');

/** Whether the browser can give us WebGL at all (the probe context is released at once). */
function webglAvailable(): boolean {
  if (typeof document === 'undefined') return false;
  const c = document.createElement('canvas');
  const gl = (c.getContext('webgl2') ?? c.getContext('webgl')) as WebGLRenderingContext | null;
  gl?.getExtension('WEBGL_lose_context')?.loseContext();
  return gl !== null;
}

/** What draws: the 3D city scene, or the 2D fallback map. */
interface View {
  onSnapshot(s: TrackingSnapshot): void;
  frame(dt: number): void;
  setTheme(t: 'light' | 'dark'): void;
  setAccent(c: string | null): void;
  setInsets(i: Insets): void;
  dispose(): void;
  readonly drawCalls: number;
}

/**
 * Draws a `TrackingSnapshot` in `container`. The snapshot is the only input: the renderer never
 * infers a hidden courier, never shows the courier photo and never shows the dropoff label.
 *
 * Without WebGL (or when WebGL fails to start) it draws the 2D fallback map from the start; when no map tile is on
 * screen 10 s after the delivery started, the same handle switches to it and keeps drawing the latest snapshot.
 */
export function createRenderer(container: HTMLElement, o: RendererOptions = {}, deps: RendererDeps = {}): Renderer {
  const clock = o.clock ?? (() => performance.now());
  // Reduced motion: the option when given, else the system setting (followed while it changes).
  const media = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : null;
  let reducedSet = o.reducedMotion !== undefined;
  let theme = resolveTheme(o.theme);
  let accent = o.accent ?? null;
  let insets: Insets = { top: 0, right: 0, bottom: 0, left: 0 };
  let latest: TrackingSnapshot | null = null;
  let tilesOnScreen = false;
  let tilesTimer: ReturnType<typeof setTimeout> | null = null;
  let contextTimer: ReturnType<typeof setTimeout> | null = null;
  let alive = true;
  const clearTimers = () => {
    if (tilesTimer !== null) clearTimeout(tilesTimer);
    if (contextTimer !== null) clearTimeout(contextTimer);
    tilesTimer = contextTimer = null;
  };

  const attribution = document.createElement('div');
  attribution.className = 'dt-attribution';
  attribution.textContent = ATTRIBUTION;

  const makeScene = (): CityScene | null => {
    if (!deps.webgl && !webglAvailable()) return null;
    try {
      return new CityScene(container, {
        accent: o.accent,
        tiles: o.tiles,
        theme,
        mode: o.mode,
        labels: o.labels,
        clock: o.clock,
        webgl: deps.webgl,
        reducedMotion: o.reducedMotion ?? media?.matches ?? false,
        onTiles: (t) => {
          tilesOnScreen = true;
          if (tilesTimer !== null) clearTimeout(tilesTimer);
          tilesTimer = null;
          o.onTiles?.(t);
        },
        onContext: (lost) => {
          if (contextTimer !== null) clearTimeout(contextTimer);
          contextTimer = lost ? setTimeout(toFallback, CONTEXT_FALLBACK_MS) : null;
        },
      });
    } catch (err) {
      // e.g. WebGL 1 only (three.js needs WebGL 2), or a context that will not start
      console.warn('renderer: WebGL did not start, drawing the 2D map', err);
      return null;
    }
  };
  const makeFallback = (): FallbackView => {
    const f = new FallbackView(container, { theme, accent, clock: o.clock });
    f.setInsets(insets);
    if (latest) f.onSnapshot(latest);
    return f;
  };
  let scene = makeScene();
  let view: View = scene ?? makeFallback();
  container.appendChild(attribution); // over the canvas

  /** No WebGL picture to show (the context did not come back): the same handle draws the 2D map from now on. */
  const toFallback = () => {
    clearTimers();
    if (!scene || !alive) return;
    scene.dispose();
    scene = null;
    view = makeFallback();
    container.appendChild(attribution);
  };

  /** The map tiles did not come. */
  const tilesMissing = () => {
    tilesTimer = null;
    if (!tilesOnScreen) toFallback();
  };

  const onMedia = (e: MediaQueryListEvent) => {
    if (!reducedSet) scene?.setReducedMotion(e.matches);
  };
  media?.addEventListener?.('change', onMedia);
  const gate = new SnapshotGate();
  // Slow phone: lower the pixel ratio and the shadow map size first.
  const quality = new AdaptiveQuality(isPhone, (q) => scene?.setQuality(q));

  let raf = 0;
  let last = clock();
  let statsFrom = last;
  let frames = 0;

  const loop = () => {
    if (!alive) return;
    const now = clock();
    const dt = Math.max(0, (now - last) / 1000);
    last = now;
    view.frame(dt);
    frames++;
    const span = now - statsFrom;
    if (span >= 1000) {
      const fps = (frames * 1000) / span;
      if (scene) quality.sample(fps, span);
      o.onFrameStats?.({ fps, drawCalls: view.drawCalls });
      statsFrom = now;
      frames = 0;
    }
    raf = requestAnimationFrame(loop);
  };
  raf = requestAnimationFrame(loop);

  return {
    update(s) {
      if (!alive || !gate.accept(s)) return;
      latest = s;
      view.onSnapshot(s);
      // the delivery started: its first tiles have 10 s to show up
      if (scene && o.tiles && !tilesOnScreen && tilesTimer === null) tilesTimer = setTimeout(tilesMissing, TILE_FALLBACK_MS);
    },
    setMode(m) {
      scene?.setMode(m); // the fallback map is 2D already
    },
    setTheme(t) {
      theme = resolveTheme(t);
      view.setTheme(theme);
    },
    setAccent(c) {
      accent = c;
      view.setAccent(c);
    },
    overview: () => scene?.overview(),
    setFollow: (on) => scene?.setFollow(on),
    setInsets(i) {
      insets = { ...i };
      view.setInsets(i);
      // the credit stays clear of the page's cards
      attribution.style.left = `${Math.round(i.left) + 8}px`;
      attribution.style.bottom = `${Math.round(i.bottom) + 4}px`;
    },
    setReducedMotion(on) {
      reducedSet = true;
      scene?.setReducedMotion(on);
    },
    destroy() {
      alive = false;
      clearTimers();
      media?.removeEventListener?.('change', onMedia);
      cancelAnimationFrame(raf);
      view.dispose();
      attribution.remove();
    },
  };
}
