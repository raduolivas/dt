import { Path } from './path';
import { routeBetween } from './router';
import { COURIER_START, DESTINATIONS, STORE, projection } from './world';
import type { Vec2 } from './geo';
import {
  DEFAULT_PARAMS,
  type OrderStatus,
  type SimParams,
  type SnapshotListener,
  type TrackingSnapshot,
  type TrackingSource,
} from './types';

const CONFIRM_SEC = 3; // order confirmed -> courier assigned
const ASSIGN_SEC = 3; // courier assigned -> starts riding
const HANDOFF_SEC = 3; // store hands the order over
const ARRIVING_DIST_M = 45;
const ACCEL = 1.6; // m/s²
const DECEL = 2.8; // m/s²
const MAX_STEP_SEC = 0.25;

const toKmh = (ms: number) => ms * 3.6;

/**
 * Fake GPS + order state machine. It plays the role of the backend: in a real
 * app you replace this class with a client that emits the same snapshots.
 */
export class DeliverySimulator implements TrackingSource {
  params: SimParams = { ...DEFAULT_PARAMS };

  private listeners = new Set<SnapshotListener>();
  private status: OrderStatus = 'requested';
  private running = false;
  private stateTime = 0; // sim seconds spent in the current status
  private elapsed = 0;
  private legDist = 0; // meters ridden on the current leg
  private traveled = 0; // meters ridden overall
  private speedMs = 0;
  private bearing = 0;
  private leg1!: Path;
  private leg2!: Path;
  private orderId = '';

  constructor() {
    this.reset();
  }

  subscribe(listener: SnapshotListener): () => void {
    this.listeners.add(listener);
    listener(this.snapshot());
    return () => this.listeners.delete(listener);
  }

  get isRunning(): boolean {
    return this.running;
  }

  get currentStatus(): OrderStatus {
    return this.status;
  }

  setParams(patch: Partial<SimParams>): void {
    this.params = { ...this.params, ...patch };
  }

  /** Rebuild routes from the current params and go back to "order requested". */
  reset(): void {
    const dest = DESTINATIONS[this.params.destination];
    this.leg1 = new Path(routeBetween(COURIER_START, STORE.stop, 'a'));
    this.leg2 = new Path(routeBetween(STORE.stop, dest.stop, 'b'));
    this.status = 'requested';
    this.stateTime = 0;
    this.elapsed = 0;
    this.legDist = 0;
    this.traveled = 0;
    this.speedMs = 0;
    this.bearing = this.leg1.at(0).bearing;
    this.running = false;
    this.orderId = `#DLV-${Math.floor(10000 + Math.random() * 89999)}`;
    this.emit();
  }

  start(): void {
    if (this.status === 'delivered') this.reset();
    this.running = true;
  }

  pause(): void {
    this.running = false;
  }

  /** Advance the simulation by `realDtSec` real seconds (scaled by timeScale). */
  update(realDtSec: number): void {
    if (!this.running) return;
    let dt = Math.min(realDtSec, 0.1) * this.params.timeScale;
    while (dt > 0) {
      const h = Math.min(dt, MAX_STEP_SEC);
      this.step(h);
      dt -= h;
    }
    this.emit();
  }

  // ---------------------------------------------------------------------------

  private go(next: OrderStatus): void {
    this.status = next;
    this.stateTime = 0;
    if (next === 'to_store') this.legDist = 0;
    if (next === 'picked_up') this.bearing = this.leg2.at(0).bearing;
    if (next === 'on_the_way') this.legDist = 0;
    if (next === 'delivered') this.running = false;
  }

  private get nominalMs(): number {
    return this.params.courierSpeedKmh / 3.6;
  }

  private step(dt: number): void {
    if (this.status !== 'delivered') this.elapsed += dt;
    this.stateTime += dt;

    switch (this.status) {
      case 'requested':
        if (this.stateTime >= CONFIRM_SEC) this.go('accepted');
        break;
      case 'accepted':
        if (this.stateTime >= ASSIGN_SEC) this.go('to_store');
        break;
      case 'to_store':
        if (this.ride(this.leg1, dt)) {
          this.speedMs = 0;
          this.go('at_store');
        }
        break;
      case 'at_store':
        this.brake(dt);
        if (this.stateTime >= this.params.prepTimeSec) this.go('picked_up');
        break;
      case 'picked_up':
        this.brake(dt);
        if (this.stateTime >= HANDOFF_SEC) this.go('on_the_way');
        break;
      case 'on_the_way':
      case 'arriving': {
        const arrived = this.ride(this.leg2, dt);
        if (this.status === 'on_the_way' && this.leg2.length - this.legDist < ARRIVING_DIST_M) {
          this.go('arriving');
        }
        if (arrived) {
          this.speedMs = 0;
          this.go('delivered');
        }
        break;
      }
      case 'delivered':
        break;
    }
  }

  private brake(dt: number): void {
    this.speedMs = Math.max(0, this.speedMs - DECEL * dt);
  }

  /** Move along `leg`; returns true once its end is reached. */
  private ride(leg: Path, dt: number): boolean {
    const remaining = leg.length - this.legDist;
    let target = this.nominalMs;
    // slow down for corners and for the final approach
    const corner = leg.distanceToNextCorner(this.legDist);
    target *= Math.min(1, 0.45 + 0.55 * (corner / 16));
    target *= Math.min(1, 0.25 + 0.75 * (remaining / 25));

    const dv = target - this.speedMs;
    this.speedMs += Math.sign(dv) * Math.min(Math.abs(dv), (dv > 0 ? ACCEL : DECEL) * dt);

    const move = Math.min(this.speedMs * dt, remaining);
    this.legDist += move;
    this.traveled += move;
    this.bearing = leg.at(this.legDist).bearing;
    return leg.length - this.legDist < 0.05;
  }

  private courierLocal(): Vec2 {
    switch (this.status) {
      case 'requested':
      case 'accepted':
        return this.leg1.at(0).pos;
      case 'to_store':
        return this.leg1.at(this.legDist).pos;
      case 'at_store':
      case 'picked_up':
        return this.leg1.at(this.leg1.length).pos;
      default:
        return this.leg2.at(this.legDist).pos;
    }
  }

  private distanceRemaining(): number {
    switch (this.status) {
      case 'requested':
      case 'accepted':
        return this.leg1.length + this.leg2.length;
      case 'to_store':
        return this.leg1.length - this.legDist + this.leg2.length;
      case 'at_store':
      case 'picked_up':
        return this.leg2.length;
      case 'delivered':
        return 0;
      default:
        return this.leg2.length - this.legDist;
    }
  }

  private eta(): number | null {
    // effective speed is ~8% below cruising speed because of corners and braking
    const v = this.nominalMs * 0.92;
    if (v <= 0) return null;
    const { prepTimeSec } = this.params;
    switch (this.status) {
      case 'requested':
        return CONFIRM_SEC - this.stateTime + ASSIGN_SEC + this.etaFromStore(v) + this.leg1.length / v;
      case 'accepted':
        return ASSIGN_SEC - this.stateTime + this.etaFromStore(v) + this.leg1.length / v;
      case 'to_store':
        return (this.leg1.length - this.legDist) / v + this.etaFromStore(v);
      case 'at_store':
        return Math.max(0, prepTimeSec - this.stateTime) + HANDOFF_SEC + this.leg2.length / v;
      case 'picked_up':
        return Math.max(0, HANDOFF_SEC - this.stateTime) + this.leg2.length / v;
      case 'delivered':
        return 0;
      default:
        return (this.leg2.length - this.legDist) / v;
    }
  }

  private etaFromStore(v: number): number {
    return this.params.prepTimeSec + HANDOFF_SEC + this.leg2.length / v;
  }

  snapshot(): TrackingSnapshot {
    const dest = DESTINATIONS[this.params.destination];
    const total = this.leg1.length + this.leg2.length;
    const toLL = (pts: Vec2[]) => pts.map((p) => projection.toLatLng(p));
    let bearingDeg = (this.bearing * 180) / Math.PI;
    bearingDeg = ((bearingDeg % 360) + 360) % 360;

    return {
      orderId: this.orderId,
      status: this.status,
      courier: {
        position: projection.toLatLng(this.courierLocal()),
        heading: bearingDeg,
        speedKmh: toKmh(this.speedMs),
      },
      store: projection.toLatLng(STORE.stop),
      destination: projection.toLatLng(dest.stop),
      route: { toStore: toLL(this.leg1.points), toCustomer: toLL(this.leg2.points) },
      etaSeconds: this.eta(),
      distanceRemainingM: this.distanceRemaining(),
      progress: total > 0 ? Math.min(1, this.traveled / total) : 0,
      elapsedSec: this.elapsed,
      updatedAt: Date.now(),
    };
  }

  private emit(): void {
    const snap = this.snapshot();
    this.listeners.forEach((l) => l(snap));
  }
}
