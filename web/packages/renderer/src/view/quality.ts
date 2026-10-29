/** What the 3D scene may spend per frame: the device pixel ratio cap and the sun's shadow map size. */
export interface Quality {
  maxPixelRatio: number;
  shadowMapSize: number;
}

/** From full quality down. The first steps only lower pixel ratio and shadow map size, before anything else. */
export const QUALITY_LEVELS: readonly Quality[] = [
  { maxPixelRatio: 2, shadowMapSize: 2048 },
  { maxPixelRatio: 1.5, shadowMapSize: 1024 },
  { maxPixelRatio: 1, shadowMapSize: 512 },
];

/** The frame budget on a phone (global constraints: ≥ 30 fps on the reference phones). */
export const PHONE_MIN_FPS = 30;
/** How long the frame rate must stay under budget before quality steps down. */
export const SLOW_FOR_MS = 5000;
/** A sample longer than this is a paused page (a background tab), not a slow frame rate: it is ignored. */
const MAX_SAMPLE_MS = 2000;

/** "On a phone": a coarse pointer, or a viewport whose shorter side is under 768 CSS px. */
export function isPhoneViewport(coarsePointer: boolean, width: number, height: number): boolean {
  return coarsePointer || Math.min(width, height) < 768;
}

/** The browser's answer to `isPhoneViewport`. */
export function isPhone(): boolean {
  const coarse = typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches;
  const w = typeof innerWidth === 'number' ? innerWidth : Infinity;
  const h = typeof innerHeight === 'number' ? innerHeight : Infinity;
  return isPhoneViewport(coarse, w, h);
}

/**
 * Adaptive quality: when the frame rate stays under 30 fps for 5 s on a phone, steps down one level (lower pixel
 * ratio and shadow map size), then waits another 5 s before the next step. It never steps back up.
 */
export class AdaptiveQuality {
  private level = 0;
  private slowMs = 0;

  constructor(
    private readonly phone: () => boolean,
    private readonly apply: (q: Quality) => void,
  ) {}

  /** One frame-rate sample covering `ms` milliseconds (the renderer takes one per second). */
  sample(fps: number, ms: number): void {
    if (ms > MAX_SAMPLE_MS) return;
    if (fps >= PHONE_MIN_FPS) {
      this.slowMs = 0;
      return;
    }
    this.slowMs += ms;
    if (this.slowMs < SLOW_FOR_MS || this.level >= QUALITY_LEVELS.length - 1) return;
    this.slowMs = 0;
    if (!this.phone()) return;
    this.level++;
    this.apply(QUALITY_LEVELS[this.level]!);
  }
}
