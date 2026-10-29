import * as THREE from 'three';
import { COLORS } from './colors';
import { mulberry32 } from './models';

/**
 * The game-feel touches (spec §5.3). Every one is off under reduced motion (`prefers-reduced-motion: reduce`): no camera
 * moves, bounces, sway, shimmer or pulses; the courier itself still moves.
 */
export const EFFECTS = [
  /** Steam rising over the store while the order is prepared. */
  'preparing',
  'treeSway',
  'waterShimmer',
  /** The prototype's gentle bob of the store and home pins. */
  'pinBob',
  /** The curb rings' ripple and the courier halo's pulse. */
  'ringPulse',
  'haloPulse',
  /** A soft breathing of the home pin. */
  'homePulse',
  /** A squash-and-stretch of the pin (and a hop of the courier) when the courier arrives at the store or the door. */
  'arrivalBounce',
  'parcelDrop',
  /** A short confetti burst over the home on `delivered`. */
  'celebration',
  /** The camera's gentle move in when the page opens. */
  'cameraIntro',
  /** Reframing glides; under reduced motion the camera snaps. */
  'reframeGlide',
] as const;
export type EffectName = (typeof EFFECTS)[number];

export type BounceTarget = 'store' | 'home' | 'courier';
const BOUNCE_TARGETS: readonly BounceTarget[] = ['store', 'home', 'courier'];

const BOUNCE_S = 0.9;
const HOP_S = 0.7;
const HOP_M = 3;
const DROP_S = 0.7;

const STEAM_PUFFS = 4;
const STEAM_PERIOD_S = 2.6;
const STEAM_RISE_M = 16;

const CONFETTI = 36;
const CONFETTI_S = 1.8;
const GRAVITY = 42; // m/s², toy gravity: a quick, snappy burst

export class Effects {
  private reduced = false;
  private time = 0;
  private sway = 0;

  private readonly bounces: Record<BounceTarget, number> = { store: -1, home: -1, courier: -1 };
  private readonly squashOut: Record<'store' | 'home', { xz: number; y: number }> = {
    store: { xz: 1, y: 1 },
    home: { xz: 1, y: 1 },
  };
  private drop = -1;

  private readonly steamAt = new THREE.Vector3();
  private steamOn = false;
  private steamFade = 0;
  private readonly puffs: THREE.Mesh[] = [];
  private readonly puffMats: THREE.MeshStandardMaterial[] = [];
  private readonly puffGeo = new THREE.IcosahedronGeometry(1, 0);

  private burst: THREE.InstancedMesh | null = null;
  private burstT = 0;
  private readonly burstState = new Float32Array(CONFETTI * 9); // position, velocity, spin axis·rate
  private readonly dummy = new THREE.Object3D();
  private readonly burstColor = new THREE.Color();

  constructor(private readonly parent: THREE.Object3D) {
    for (let i = 0; i < STEAM_PUFFS; i++) {
      const m = new THREE.MeshStandardMaterial({
        color: 0xffffff,
        emissive: 0xffffff,
        emissiveIntensity: 0.35,
        roughness: 1,
        flatShading: true,
        transparent: true,
        opacity: 0,
        depthWrite: false,
      });
      const puff = new THREE.Mesh(this.puffGeo, m);
      puff.visible = false;
      puff.renderOrder = 3;
      this.puffs.push(puff);
      this.puffMats.push(m);
      parent.add(puff);
    }
  }

  setReducedMotion(on: boolean): void {
    this.reduced = on;
    if (on) {
      for (const k of BOUNCE_TARGETS) this.bounces[k] = -1;
      this.drop = -1;
      this.endBurst();
    }
  }

  get reducedMotion(): boolean {
    return this.reduced;
  }

  /** Every effect runs unless reduced motion is on. */
  isEnabled(e: EffectName): boolean {
    return !this.reduced && EFFECTS.includes(e);
  }

  /** Steam over the store at `at` (top of the store) while preparing; `null` stops it (the puffs fade out). */
  setPreparing(at: THREE.Vector3 | null): void {
    this.steamOn = at !== null;
    if (at) this.steamAt.copy(at);
  }

  bounce(target: BounceTarget): void {
    if (this.reduced) return;
    this.bounces[target] = 0;
  }

  /** The parcel falls onto the doorstep and bounces; under reduced motion it is simply there. */
  dropParcel(): void {
    this.drop = this.reduced ? -1 : 0;
  }

  /** Confetti over the home. Small and short: one instanced mesh, removed and disposed when it lands. */
  celebrate(at: THREE.Vector3): void {
    if (this.reduced) return;
    this.endBurst();
    const geo = new THREE.BoxGeometry(1.3, 0.22, 0.8);
    const mat = new THREE.MeshBasicMaterial({ side: THREE.DoubleSide });
    const burst = new THREE.InstancedMesh(geo, mat, CONFETTI);
    burst.frustumCulled = false;
    burst.renderOrder = 4;
    const rand = mulberry32(0x5eed);
    const palette = [COLORS.blue, COLORS.coral, COLORS.green, COLORS.shirt, 0xffd34d, COLORS.blueLight];
    const st = this.burstState;
    for (let i = 0; i < CONFETTI; i++) {
      const a = rand() * Math.PI * 2;
      const out = 6 + rand() * 12;
      const o = i * 9;
      st[o] = at.x;
      st[o + 1] = at.y;
      st[o + 2] = at.z;
      st[o + 3] = Math.cos(a) * out;
      st[o + 4] = 22 + rand() * 20;
      st[o + 5] = Math.sin(a) * out;
      st[o + 6] = rand() * 6;
      st[o + 7] = rand() * 6;
      st[o + 8] = 6 + rand() * 10;
      burst.setColorAt(i, this.burstColor.setHex(palette[i % palette.length]!));
    }
    this.burst = burst;
    this.burstT = 0;
    this.parent.add(burst);
    this.writeBurst();
  }

  get activeParticles(): number {
    return this.burst ? this.burst.count : 0;
  }

  get visibleSteam(): number {
    let n = 0;
    for (const p of this.puffs) if (p.visible) n++;
    return n;
  }

  /** Time for the tree sway and the water shimmer; it stands still under reduced motion. */
  get swayTime(): number {
    return this.sway;
  }

  /** Scale of the store or home pin (squash and stretch after an arrival). */
  squash(target: 'store' | 'home'): { xz: number; y: number } {
    const out = this.squashOut[target];
    const t = this.bounces[target];
    if (t < 0) {
      out.xz = 1;
      out.y = 1;
    } else {
      const s = Math.sin(t * 16) * Math.exp(-t * 5) * 0.38;
      out.y = 1 + s;
      out.xz = 1 - s * 0.5;
    }
    return out;
  }

  /** How high the courier hops (world m) after arriving at the store. */
  hop(): number {
    const t = this.bounces.courier;
    if (t < 0) return 0;
    return Math.abs(Math.sin((t / HOP_S) * Math.PI * 2)) * HOP_M * (1 - t / HOP_S);
  }

  /** Scale of the soft pulse: the home pin breathes, the courier halo pulses. */
  pulse(target: 'home' | 'courier'): number {
    if (target === 'home') return this.isEnabled('homePulse') ? 1 + Math.sin(this.time * 3) * 0.07 : 1;
    return this.isEnabled('haloPulse') ? 1 + Math.sin(this.time * 4) * 0.12 : 1;
  }

  /** The halo's opacity (pulses with its scale). */
  haloOpacity(): number {
    return this.isEnabled('haloPulse') ? 0.4 + Math.sin(this.time * 4) * 0.15 : 0.45;
  }

  /** The prototype's pin bob (world m). */
  bob(target: 'store' | 'home'): number {
    if (!this.isEnabled('pinBob')) return 0;
    return Math.sin(this.time * 2.4 + (target === 'home' ? 0.8 : 0)) * 1.2;
  }

  /** Ripple phase 0..1 of curb ring `i`, or -1 for a still ring. */
  ringPhase(i: number): number {
    return this.isEnabled('ringPulse') ? (this.time * 0.9 + i * 0.5) % 1 : -1;
  }

  /** Height of the dropped parcel above its rest on the doorstep (world m). */
  parcelLift(): number {
    const t = this.drop;
    if (t < 0) return 0;
    const k = Math.min(t / DROP_S, 1);
    return 10 * (1 - k) * (1 - k) + Math.abs(Math.sin(k * Math.PI * 2.5)) * (1 - k) * 1.6;
  }

  frame(dt: number): void {
    if (this.reduced) {
      this.updateSteam(dt);
      return;
    }
    this.time += dt;
    this.sway += dt;
    for (const k of BOUNCE_TARGETS) {
      if (this.bounces[k] < 0) continue;
      this.bounces[k] += dt;
      if (this.bounces[k] > (k === 'courier' ? HOP_S : BOUNCE_S)) this.bounces[k] = -1;
    }
    if (this.drop >= 0) {
      this.drop += dt;
      if (this.drop > DROP_S) this.drop = -1;
    }
    this.updateSteam(dt);
    if (this.burst) {
      this.burstT += dt;
      if (this.burstT >= CONFETTI_S) this.endBurst();
      else this.stepBurst(dt);
    }
  }

  dispose(): void {
    this.endBurst();
    for (const p of this.puffs) this.parent.remove(p);
    this.puffMats.forEach((m) => m.dispose());
    this.puffGeo.dispose();
  }

  private updateSteam(dt: number): void {
    const on = this.steamOn && this.isEnabled('preparing');
    // fade in and out over a third of a second, so the loop never pops; under reduced motion it is simply gone
    this.steamFade = this.reduced ? 0 : Math.max(0, Math.min(1, this.steamFade + (on ? dt : -dt) * 3));
    if (!on && this.steamFade === 0) {
      for (const p of this.puffs) p.visible = false;
      return;
    }
    for (let i = 0; i < STEAM_PUFFS; i++) {
      const t = (this.time / STEAM_PERIOD_S + i / STEAM_PUFFS) % 1;
      const p = this.puffs[i]!;
      p.visible = true;
      p.position.set(
        this.steamAt.x + Math.sin(t * 5 + i) * 1.5,
        this.steamAt.y + 1 + t * STEAM_RISE_M,
        this.steamAt.z + Math.cos(t * 4 + i * 2) * 1.5,
      );
      p.scale.setScalar(1.6 + t * 3.2);
      p.rotation.set(t * 2 + i, t * 3, 0);
      this.puffMats[i]!.opacity = 0.9 * Math.min(1, t * 6) * (1 - t) * this.steamFade;
    }
  }

  private stepBurst(dt: number): void {
    const st = this.burstState;
    for (let i = 0; i < CONFETTI; i++) {
      const o = i * 9;
      st[o + 4]! -= GRAVITY * dt;
      // air drag: confetti floats down rather than falling like a stone
      const drag = Math.exp(-dt * 1.6);
      st[o + 3]! *= drag;
      st[o + 5]! *= drag;
      if (st[o + 4]! < -14) st[o + 4] = -14;
      st[o]! += st[o + 3]! * dt;
      st[o + 1]! += st[o + 4]! * dt;
      st[o + 2]! += st[o + 5]! * dt;
    }
    this.writeBurst();
  }

  private writeBurst(): void {
    const b = this.burst!;
    const st = this.burstState;
    const t = this.burstT;
    const shrink = Math.min(1, (CONFETTI_S - t) / 0.4);
    for (let i = 0; i < CONFETTI; i++) {
      const o = i * 9;
      this.dummy.position.set(st[o]!, st[o + 1]!, st[o + 2]!);
      this.dummy.rotation.set(st[o + 6]! * t, st[o + 7]! * t + i, st[o + 8]! * t);
      this.dummy.scale.setScalar(Math.max(0.001, shrink));
      this.dummy.updateMatrix();
      b.setMatrixAt(i, this.dummy.matrix);
    }
    b.instanceMatrix.needsUpdate = true;
  }

  private endBurst(): void {
    const b = this.burst;
    if (!b) return;
    this.burst = null;
    this.parent.remove(b);
    b.geometry.dispose();
    (b.material as THREE.Material).dispose();
    b.dispose();
  }
}
