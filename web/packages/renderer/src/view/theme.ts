import * as THREE from 'three';
import { COLORS, DAY, NIGHT, type ColorKey, type GlowKey, type Palette } from './colors';

/** The renderer's theme: `light` is the day city, `dark` the night city. */
export type Theme = 'light' | 'dark';

/** How long a theme change fades (ms); 0 under reduced motion. */
export const THEME_FADE_MS = 1400;

export const paletteOf = (t: Theme): Palette => (t === 'dark' ? NIGHT : DAY);

/**
 * The scene's only lights: the prototype's hemisphere and its warm sun with soft shadows. At night the sun is the
 * moon (moved and recolored by `applyTheme`); nothing else in the city is a real-time light. The sun's offset from
 * what the camera looks at is `sun.userData.offset` (the scene moves the sun with its target every frame).
 */
export function createLights(): { hemi: THREE.HemisphereLight; sun: THREE.DirectionalLight } {
  const hemi = new THREE.HemisphereLight(DAY.hemiSky, DAY.hemiGround, DAY.hemiIntensity);
  const sun = new THREE.DirectionalLight(DAY.sunColor, DAY.sunIntensity);
  sun.userData.offset = new THREE.Vector3(...DAY.sunOffset);
  sun.position.copy(sun.userData.offset as THREE.Vector3);
  sun.castShadow = true;
  sun.shadow.mapSize.set(2048, 2048);
  const sc = sun.shadow.camera;
  sc.left = -520;
  sc.right = 520;
  sc.top = 520;
  sc.bottom = -520;
  sc.near = 10;
  sc.far = 1400;
  sun.shadow.bias = -0.0006;
  sun.shadow.normalBias = 0.6;
  return { hemi, sun };
}

export interface ThemeFade {
  readonly theme: Theme;
  /** The fade reached its theme. */
  readonly done: boolean;
  /** Advances the fade by `ms`. */
  step(ms: number): void;
  /** Jumps to the end (reduced motion switched on mid-fade). */
  finish(): void;
}

/**
 * Turns `scene` to theme `t`: background, the hemisphere and sun (moon) lights, the colors and glows of its materials
 * and every `userData.dtNight(k)` hook (0 = day, 1 = night). With `fadeMs` 0 (reduced motion) the final colors are set
 * in this call; otherwise the fade runs as `step` is called, from wherever the scene is now (a reversed fade turns
 * back smoothly).
 *
 * Materials: a color equal to one of the prototype's `COLORS` takes that key's night color, an emissive equal to a
 * day glow (or tagged `userData.dtGlow`) takes the night glow. Their own day values are kept in `userData.dtDay`, so
 * the day comes back exactly. `userData.dtColor = false` leaves the color alone (partner-colored materials, vertex
 * colored ones are skipped anyway).
 */
export function applyTheme(scene: THREE.Scene, t: Theme, o: { fadeMs: number }): ThemeFade {
  const to = t === 'dark' ? 1 : 0;
  const from = nightMix(scene);
  const total = Math.max(0, o.fadeMs) * Math.abs(to - from);
  let elapsed = 0;
  const set = (u: number) => mixTheme(scene, from + (to - from) * (u * u * (3 - 2 * u)));
  if (total <= 0) set(1);
  return {
    theme: t,
    get done() {
      return total <= 0 || elapsed >= total;
    },
    step(ms) {
      if (total <= 0 || elapsed >= total) return;
      elapsed = Math.min(total, elapsed + ms);
      set(elapsed / total);
    },
    finish() {
      if (total <= 0 || elapsed >= total) return;
      elapsed = total;
      set(1);
    },
  };
}

/** How far `scene` is into the night (0 = day, 1 = night). */
export function nightMix(scene: THREE.Scene): number {
  return (scene.userData.dtNight as number | undefined) ?? 0;
}

/** Applies the scene's current mix to objects added since (a new courier model, store, home, pins). */
export function refreshTheme(scene: THREE.Scene, root: THREE.Object3D = scene): void {
  mixObjects(root, nightMix(scene));
}

const lerp = (a: number, b: number, k: number) => a + (b - a) * k;
const ca = new THREE.Color();
const cb = new THREE.Color();
/** Sets `out` between two sRGB hexes (blended in the linear working space). */
const mixHex = (out: THREE.Color, a: number, b: number, k: number) => out.lerpColors(ca.setHex(a), cb.setHex(b), k);

const COLOR_KEYS = new Map<number, ColorKey>();
for (const [k, v] of Object.entries(COLORS) as [ColorKey, number][]) if (!COLOR_KEYS.has(v)) COLOR_KEYS.set(v, k);
const GLOW_KEYS = new Map<number, GlowKey>();
for (const [k, g] of Object.entries(DAY.glow) as [GlowKey, (typeof DAY.glow)[GlowKey]][]) {
  if (g.color !== null) GLOW_KEYS.set(g.color, k);
}

function mixTheme(scene: THREE.Scene, k: number): void {
  scene.userData.dtNight = k;
  if (scene.background instanceof THREE.Color) mixHex(scene.background, DAY.sky, NIGHT.sky, k);
  mixObjects(scene, k);
}

interface DayRecord {
  color: number;
  emissive: number | null;
  emissiveIntensity: number;
  colorKey: ColorKey | null;
  glowKey: GlowKey | null;
}

type Themed = THREE.Material & {
  color?: THREE.Color;
  emissive?: THREE.Color;
  emissiveIntensity?: number;
  vertexColors: boolean;
};

function mixObjects(root: THREE.Object3D, k: number): void {
  const seen = new Set<THREE.Material>();
  root.traverse((o) => {
    if (o instanceof THREE.HemisphereLight) {
      mixHex(o.color, DAY.hemiSky, NIGHT.hemiSky, k);
      mixHex(o.groundColor, DAY.hemiGround, NIGHT.hemiGround, k);
      o.intensity = lerp(DAY.hemiIntensity, NIGHT.hemiIntensity, k);
    } else if (o instanceof THREE.DirectionalLight) {
      mixHex(o.color, DAY.sunColor, NIGHT.sunColor, k);
      o.intensity = lerp(DAY.sunIntensity, NIGHT.sunIntensity, k);
      const off = o.userData.offset as THREE.Vector3 | undefined;
      off?.set(...(DAY.sunOffset.map((d, i) => lerp(d, NIGHT.sunOffset[i]!, k)) as [number, number, number]));
    }
    const hook = o.userData.dtNight as ((k: number) => void) | undefined;
    if (typeof hook === 'function') hook(k);
    const mats = (o as THREE.Mesh).material;
    if (!mats) return;
    for (const m of Array.isArray(mats) ? mats : [mats]) {
      if (seen.has(m)) continue;
      seen.add(m);
      mixMaterial(m as Themed, k);
    }
  });
}

function dayRecord(m: Themed): DayRecord {
  let r = m.userData.dtDay as DayRecord | undefined;
  if (r) return r;
  const color = m.color?.getHex() ?? 0xffffff;
  const emissive = m.emissive ? m.emissive.getHex() : null;
  const tagged = m.userData.dtGlow as GlowKey | undefined;
  r = {
    color,
    emissive,
    emissiveIntensity: m.emissiveIntensity ?? 0,
    colorKey: m.color && !m.vertexColors && m.userData.dtColor !== false ? (COLOR_KEYS.get(color) ?? null) : null,
    glowKey: tagged ?? (emissive !== null && m.emissiveIntensity ? (GLOW_KEYS.get(emissive) ?? null) : null),
  };
  m.userData.dtDay = r;
  return r;
}

function mixMaterial(m: Themed, k: number): void {
  const r = dayRecord(m);
  if (r.colorKey && m.color) mixHex(m.color, r.color, NIGHT[r.colorKey], k);
  if (r.glowKey && m.emissive) {
    const g = NIGHT.glow[r.glowKey];
    if (g.color !== null && r.emissive !== null) mixHex(m.emissive, r.emissive, g.color, k);
    m.emissiveIntensity = lerp(r.emissiveIntensity, g.intensity, k);
  }
}
