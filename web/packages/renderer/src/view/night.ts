import * as THREE from 'three';
import { LIT_CELLS, mulberry32 } from '../tiles/build/common';
import { NIGHT } from './colors';

export { LIT_CELLS } from '../tiles/build/common';

/**
 * The night city's extras, all baked or emissive: lit windows (an emissive map on the building walls), streetlight
 * pools and the courier's headlight (additive glow decals). None of them is a `THREE.Light`, and by day none of them is
 * drawn, so the day keeps its draw calls and the night adds no real-time light.
 */

const LIT_SHARE = 0.32;
const LIT_SEED = 0x1e7_9a11;
/** Warm interiors, now and then a cool one (a screen). */
const LIT_COLORS = ['#ffd27a', '#ffc35e', '#ffe2a6', '#ffb85a', '#ffd9a0', '#bcd4ff'];

/** Which windows of the LIT_CELLS × LIT_CELLS pattern are lit (row major). Seeded: the same on every load. */
export function litWindowCells(): boolean[] {
  const rnd = mulberry32(LIT_SEED);
  return Array.from({ length: LIT_CELLS * LIT_CELLS }, () => rnd() < LIT_SHARE);
}

function canvas2d(w: number, h: number): { c: HTMLCanvasElement; g: CanvasRenderingContext2D } | null {
  if (typeof document === 'undefined') return null;
  const c = document.createElement('canvas');
  c.width = w;
  c.height = h;
  const g = c.getContext('2d');
  return g ? { c, g } : null;
}

let litBase: THREE.Texture | null = null;
/**
 * The lit-window variant of the prototype's window texture, as an emissive map: black, with the lit windows of the
 * seeded pattern drawn where the day texture has its glass. One UV unit is one window (repeat 1 / LIT_CELLS). Shared,
 * do not dispose.
 */
export function litWindowTexture(): THREE.Texture {
  if (litBase) return litBase;
  const cell = 64;
  const n = LIT_CELLS;
  const cv = canvas2d(cell * n, cell * n);
  if (!cv) {
    litBase = new THREE.Texture();
  } else {
    const { c, g } = cv;
    g.fillStyle = '#000000';
    g.fillRect(0, 0, c.width, c.height);
    const rnd = mulberry32(LIT_SEED ^ 0x5eed);
    litWindowCells().forEach((on, i) => {
      const tint = LIT_COLORS[Math.floor(rnd() * LIT_COLORS.length)]!;
      if (!on) return;
      const x = (i % n) * cell;
      const y = Math.floor(i / n) * cell;
      const grad = g.createLinearGradient(0, y + 12, 0, y + 52);
      grad.addColorStop(0, tint);
      grad.addColorStop(1, '#a8682a');
      g.fillStyle = grad;
      g.fillRect(x + 12, y + 12, 40, 40);
      g.fillStyle = '#000000';
      g.fillRect(x + 30.5, y + 12, 3, 40); // the window's mullion, as on the day texture
    });
    const t = new THREE.CanvasTexture(c);
    t.colorSpace = THREE.SRGBColorSpace;
    t.anisotropy = 4;
    litBase = t;
  }
  litBase.wrapS = litBase.wrapT = THREE.RepeatWrapping;
  litBase.repeat.set(1 / n, 1 / n);
  return litBase;
}

let glowBase: THREE.Texture | null = null;
/** A soft round falloff (white in the middle, black at the rim) for additive glow decals. Shared, do not dispose. */
export function glowTexture(): THREE.Texture {
  if (glowBase) return glowBase;
  const cv = canvas2d(128, 128);
  if (!cv) {
    glowBase = new THREE.Texture();
    return glowBase;
  }
  const { c, g } = cv;
  const grad = g.createRadialGradient(64, 64, 0, 64, 64, 64);
  grad.addColorStop(0, '#ffffff');
  grad.addColorStop(0.35, '#9a9a9a');
  grad.addColorStop(0.7, '#2e2e2e');
  grad.addColorStop(1, '#000000');
  g.fillStyle = grad;
  g.fillRect(0, 0, 128, 128);
  glowBase = new THREE.CanvasTexture(c);
  return glowBase;
}

/** An unlit additive glow material (never written to depth, so glows overlap softly). */
function glowMaterial(color: number): THREE.MeshBasicMaterial {
  const m = new THREE.MeshBasicMaterial({
    color,
    map: glowTexture(),
    transparent: true,
    opacity: 0,
    blending: THREE.AdditiveBlending,
    depthWrite: false,
  });
  m.userData.dtColor = false;
  return m;
}

/** A flat quad on the ground (y up), centred at (x, z), `w` × `d` metres, with the full glow texture on it. */
function groundQuad(x: number, y: number, z: number, w: number, d: number): THREE.BufferGeometry {
  const g = new THREE.PlaneGeometry(w, d);
  g.rotateX(-Math.PI / 2);
  g.translate(x, y, z);
  return g;
}

function merge(parts: THREE.BufferGeometry[]): THREE.BufferGeometry {
  const out = new THREE.BufferGeometry();
  for (const name of ['position', 'normal', 'uv']) {
    const size = parts[0]!.getAttribute(name).itemSize;
    const a = new Float32Array(parts.reduce((s, p) => s + p.getAttribute(name).count * size, 0));
    let o = 0;
    for (const p of parts) {
      const src = p.getAttribute(name).array as Float32Array;
      a.set(src, o);
      o += src.length;
    }
    out.setAttribute(name, new THREE.BufferAttribute(a, size));
  }
  const idx: number[] = [];
  let base = 0;
  for (const p of parts) {
    for (const i of p.getIndex()!.array) idx.push(i + base);
    base += p.getAttribute('position').count;
  }
  out.setIndex(idx);
  parts.forEach((p) => p.dispose());
  return out;
}

// -----------------------------------------------------------------------------
// Streetlights
// -----------------------------------------------------------------------------

const POLE_H = 7;
const ARM_M = 2.6;
const POOL_M = 17;
const LAMP_GLOW_M = 3.2;
/** Pools sit just over the street surface (above the block top the poles stand on), under the route ribbons. */
const POOL_Y = 0.42;
const POLE_COLOR = 0x343d63;
const LAMP_COLOR = 0xffc777;

/**
 * The streetlights of the city: shared geometry and materials, one instanced pole mesh and one instanced glow mesh
 * (pool on the street + the lamp's own glow) per tile. Shown only at night.
 */
export class Streetlights {
  private readonly poleGeo: THREE.BufferGeometry;
  private readonly glowGeo: THREE.BufferGeometry;
  private readonly poleMat = new THREE.MeshStandardMaterial({ color: POLE_COLOR, roughness: 0.7, metalness: 0.1 });
  private readonly glowMat = glowMaterial(LAMP_COLOR);
  private mix = 0;

  constructor() {
    this.poleMat.userData.dtColor = false;
    const pole = new THREE.BoxGeometry(0.4, POLE_H, 0.4);
    pole.translate(0, POLE_H / 2, 0);
    const arm = new THREE.BoxGeometry(0.3, 0.3, ARM_M + 0.2);
    arm.translate(0, POLE_H - 0.15, ARM_M / 2);
    const head = new THREE.BoxGeometry(0.9, 0.35, 1.1);
    head.translate(0, POLE_H - 0.4, ARM_M);
    this.poleGeo = merge([pole, arm, head]);
    // The pool on the street under the lamp and a glow around the lamp itself (two crossed upright quads).
    const lampA = new THREE.PlaneGeometry(LAMP_GLOW_M, LAMP_GLOW_M);
    lampA.translate(0, POLE_H - 0.5, ARM_M);
    const lampB = new THREE.PlaneGeometry(LAMP_GLOW_M, LAMP_GLOW_M);
    lampB.rotateY(Math.PI / 2);
    lampB.translate(0, POLE_H - 0.5, ARM_M);
    this.glowGeo = merge([groundQuad(0, POOL_Y, ARM_M, POOL_M, POOL_M), lampA, lampB]);
    this.glowMat.side = THREE.DoubleSide;
  }

  /**
   * The two meshes of one tile (`lights` stride 3 `[x, z, yaw]`, poles standing at `y`), or nothing when it has no
   * lit street. The tile owns and disposes them; `isStreetlight` finds them for `setNight`.
   */
  build(lights: Float32Array, y: number): { poles: THREE.InstancedMesh; glow: THREE.InstancedMesh } | null {
    const n = lights.length / 3;
    if (n === 0) return null;
    const poles = new THREE.InstancedMesh(this.poleGeo, this.poleMat, n);
    const glow = new THREE.InstancedMesh(this.glowGeo, this.glowMat, n);
    const m = new THREE.Matrix4();
    const q = new THREE.Quaternion();
    const up = new THREE.Vector3(0, 1, 0);
    const p = new THREE.Vector3();
    const one = new THREE.Vector3(1, 1, 1);
    for (let i = 0; i < n; i++) {
      m.compose(p.set(lights[i * 3]!, y, lights[i * 3 + 1]!), q.setFromAxisAngle(up, lights[i * 3 + 2]!), one);
      poles.setMatrixAt(i, m);
      glow.setMatrixAt(i, m);
    }
    for (const mesh of [poles, glow]) {
      mesh.instanceMatrix.needsUpdate = true;
      mesh.computeBoundingSphere();
      mesh.visible = this.mix > 0;
      mesh.userData.streetlight = true;
    }
    poles.castShadow = false;
    glow.renderOrder = 1; // after the opaque city, with the route ribbons
    return { poles, glow };
  }

  /** 0 = day, 1 = night. The caller shows the meshes only while `k > 0` (`isStreetlight`). */
  setNight(k: number): void {
    this.mix = k;
    this.glowMat.opacity = k * NIGHT.streetlights * 0.7;
  }

  dispose(): void {
    this.poleGeo.dispose();
    this.glowGeo.dispose();
    this.poleMat.dispose();
    this.glowMat.dispose();
  }
}

export const isStreetlight = (o: THREE.Object3D): o is THREE.InstancedMesh => o.userData.streetlight === true;

// -----------------------------------------------------------------------------
// Courier
// -----------------------------------------------------------------------------

const HEADLIGHT_COLOR = 0xfff0c8;

/**
 * The courier's night glow, for every vehicle: a soft warm halo on the street around it and a headlight beam ahead
 * (+Z, the way every courier model faces), as one additive mesh. Child of the courier root (it scales with the
 * courier); by day it is not drawn. A `userData.dtNight` hook: `applyTheme` drives it.
 */
export function makeCourierGlow(): THREE.Mesh {
  const halo = groundQuad(0, 0.06, 0, 4.4, 4.4);
  // The beam: the glow texture's far half stretched ahead, narrow at the lamp and wide down the street.
  const beam = new THREE.BufferGeometry();
  const y = 0.07;
  beam.setAttribute(
    'position',
    new THREE.Float32BufferAttribute([-0.35, y, 0.7, 0.35, y, 0.7, 1.6, y, 5.6, -1.6, y, 5.6], 3),
  );
  beam.setAttribute('normal', new THREE.Float32BufferAttribute([0, 1, 0, 0, 1, 0, 0, 1, 0, 0, 1, 0], 3));
  beam.setAttribute('uv', new THREE.Float32BufferAttribute([0.45, 0.5, 0.55, 0.5, 0.95, 0.98, 0.05, 0.98], 2));
  beam.setIndex([0, 2, 1, 0, 3, 2]);
  const material = glowMaterial(HEADLIGHT_COLOR);
  const mesh = new THREE.Mesh(merge([halo, beam]), material);
  mesh.name = 'courier-glow';
  mesh.renderOrder = 3;
  mesh.visible = false;
  mesh.userData.dtNight = (k: number) => {
    material.opacity = k * NIGHT.streetlights * 0.6;
    mesh.visible = k > 0;
  };
  return mesh;
}
