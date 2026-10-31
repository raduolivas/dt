import type { LatLng, TrackingSnapshot } from '@dt/contract';
import type { Vec2 } from '../engine/geo';
import { activeLeg, isFinal } from '../snapshot';

/** A fix within this distance of the active leg is drawn on the leg; further away it is drawn raw. */
export const SNAP_M = 25;
/** Dead reckoning stops when the snapshot is older than this by the client's clock. */
export const DEAD_RECKON_MAX_MS = 30_000;
/** Time constant of the glide to a new target, and of the turn to a new heading. */
export const GLIDE_TAU_MS = 300;
export const TURN_TAU_MS = 110;

type Signal = NonNullable<TrackingSnapshot['courier']>['signal'];

export interface Snapped {
  p: Vec2;
  /** The leg segment `p` lies on: between `leg[index]` and `leg[index + 1]`. */
  index: number;
  /** Metres from `leg[0]` to `p`, along the leg. */
  along: number;
}

/** The nearest point of `leg` to `p`, or `null` when it is further than `maxM` or the leg has no segment. */
export function snapToLeg(p: Vec2, leg: Vec2[], maxM = SNAP_M): Snapped | null {
  let best: Snapped | null = null;
  let bestD = Infinity;
  let before = 0; // length of the segments before i
  for (let i = 0; i < leg.length - 1; i++) {
    const a = leg[i]!;
    const b = leg[i + 1]!;
    const abx = b.x - a.x;
    const abz = b.z - a.z;
    const len2 = abx * abx + abz * abz;
    const len = Math.sqrt(len2);
    const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((p.x - a.x) * abx + (p.z - a.z) * abz) / len2));
    const qx = a.x + abx * t;
    const qz = a.z + abz * t;
    const d = Math.hypot(p.x - qx, p.z - qz);
    if (d < bestD) {
      bestD = d;
      best = { p: { x: qx, z: qz }, index: i, along: before + len * t };
    }
    before += len;
  }
  return best && bestD <= maxM ? best : null;
}

export interface MotionState {
  /** The fix as drawn (snapped when it was on the leg). */
  p: Vec2;
  leg: Vec2[];
  /** The leg segment `p` is on, or -1 when the fix was drawn raw. */
  index: number;
  speedKmh: number | null;
  /** The client clock when the snapshot arrived. */
  atMs: number;
  signal: Signal;
}

/**
 * Where the courier probably is now: only while `live`, at the last speed, along the active leg, never past the next
 * unreached route point, and not at all once the snapshot is more than 30 s old.
 */
export function deadReckon(state: MotionState, nowMs: number): Vec2 {
  const { p, leg, index } = state;
  const next = index >= 0 ? leg[index + 1] : undefined;
  if (state.signal !== 'live' || !next || !state.speedKmh || state.speedKmh <= 0) return p;
  const elapsed = Math.min(Math.max(0, nowMs - state.atMs), DEAD_RECKON_MAX_MS);
  const dist = ((state.speedKmh / 3.6) * elapsed) / 1000;
  const dx = next.x - p.x;
  const dz = next.z - p.z;
  const room = Math.hypot(dx, dz);
  if (room < 1e-6) return p;
  const f = Math.min(dist, room) / room;
  return { x: p.x + dx * f, z: p.z + dz * f };
}

export function angleDiff(target: number, current: number): number {
  return ((((target - current + Math.PI) % (Math.PI * 2)) + Math.PI * 2) % (Math.PI * 2)) - Math.PI;
}

export interface CourierFrame {
  p: Vec2;
  /** Rotation about the vertical axis for a model that faces +z. */
  yaw: number;
  faded: boolean;
  visible: boolean;
}

/**
 * The courier as the scene draws it. The snapshot decides visibility: no courier, no fix yet (`pending`) or a final
 * status means none is drawn. A new fix is glided to, never jumped to; `stale` and `lost` fade the courier and stop it.
 */
export class CourierMotion {
  private deliveryId: string | null = null;
  private last = -Infinity;
  private cur: Vec2 | null = null;
  private yaw = 0;
  private targetYaw = 0;
  private state: MotionState | null = null;
  private shown = false;
  private yawSet = false;

  constructor(private readonly toLocal: (p: LatLng) => Vec2) {}

  /** Whether a courier is drawn, as of the last snapshot. */
  get visible(): boolean {
    return this.shown;
  }

  /** The last fix, as drawn (snapped when on the leg); `null` while no courier is drawn. */
  get fix(): Vec2 | null {
    return this.shown && this.state ? this.state.p : null;
  }

  /** Forget the position: the next fix is placed, not glided to (a new delivery or a new map). */
  reset(): void {
    this.cur = null;
    this.state = null;
    this.shown = false;
    this.yawSet = false;
  }

  onSnapshot(s: TrackingSnapshot, nowMs: number): void {
    if (s.delivery_id !== this.deliveryId) {
      this.deliveryId = s.delivery_id;
      this.last = -Infinity;
      this.reset();
    }
    if (s.version <= this.last) return;
    this.last = s.version;

    const c = s.courier;
    if (isFinal(s.status) || !c || c.signal === 'pending' || !c.position) {
      this.reset(); // removed at once; a courier that comes back is placed where it is
      return;
    }
    const raw = this.toLocal(c.position);
    const leg = activeLeg(s).map((p) => this.toLocal(p));
    const snapped = snapToLeg(raw, leg);
    const p = snapped ? snapped.p : raw;
    const index = snapped ? snapped.index : -1;
    this.state = { p, leg, index, speedKmh: c.speed_kmh, atMs: nowMs, signal: c.signal };
    this.shown = true;
    this.cur ??= p;

    if (c.heading_deg !== null) {
      this.targetYaw = Math.PI - (c.heading_deg * Math.PI) / 180;
    } else if (snapped && leg[index + 1]) {
      const a = leg[index]!;
      const b = leg[index + 1]!;
      this.targetYaw = Math.PI - Math.atan2(b.x - a.x, -(b.z - a.z));
    }
    if (!this.yawSet) {
      this.yaw = this.targetYaw;
      this.yawSet = true;
    }
  }

  frame(dtMs: number, nowMs: number): CourierFrame {
    const st = this.state;
    if (!this.shown || !st || !this.cur) {
      return { p: this.cur ?? { x: 0, z: 0 }, yaw: this.yaw, faded: false, visible: false };
    }
    const faded = st.signal === 'stale' || st.signal === 'lost';
    if (!faded) {
      const target = deadReckon(st, nowMs);
      const k = 1 - Math.exp(-Math.max(0, dtMs) / GLIDE_TAU_MS);
      this.cur = { x: this.cur.x + (target.x - this.cur.x) * k, z: this.cur.z + (target.z - this.cur.z) * k };
      this.yaw += angleDiff(this.targetYaw, this.yaw) * (1 - Math.exp(-Math.max(0, dtMs) / TURN_TAU_MS));
    }
    return { p: this.cur, yaw: this.yaw, faded, visible: true };
  }
}

/** Speed changes this fast on screen (s): the wheels and the walk follow the drawn courier, not the last fix. */
const SPEED_TAU_S = 0.12;
/** A step longer than this in one frame is a placement, not motion. */
const SPEED_JUMP_M = 30;

/**
 * The speed the courier is drawn moving at (m/s, smoothed): it drives the wheels, pedals and steps, so they stop when
 * dead reckoning has reached its cap or the glide has settled.
 */
export class SpeedMeter {
  private has = false;
  private lx = 0;
  private lz = 0;
  private v = 0;

  reset(): void {
    this.has = false;
    this.v = 0;
  }

  update(p: Vec2, dtS: number): number {
    const had = this.has;
    const d = Math.hypot(p.x - this.lx, p.z - this.lz);
    this.has = true;
    this.lx = p.x;
    this.lz = p.z;
    if (!had || dtS <= 0 || d > SPEED_JUMP_M) return this.v;
    this.v += (d / dtS - this.v) * (1 - Math.exp(-dtS / SPEED_TAU_S));
    if (this.v < 0.02) this.v = 0;
    return this.v;
  }
}
