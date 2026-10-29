import type { CourierSignal, DeliveryStatus, TrackingSnapshot, Vehicle } from '@dt/contract';

type DeliveryModel = TrackingSnapshot['delivery_model'];
import { Path } from '../engine/path';
import type { Vec2 } from '../engine/geo';
import { GRID_WORLD, type DestinationPreset, type DevWorld } from './world';

/** The simulator's own finer-grained phases; the v1 `DeliveryStatus` is derived from them. */
export type OrderStatus =
  | 'requested'
  | 'accepted'
  | 'to_store'
  | 'at_store'
  | 'picked_up'
  | 'on_the_way'
  | 'arriving'
  | 'delivered';

export type SnapshotListener = (snapshot: TrackingSnapshot) => void;

export interface SimParams {
  /** Cruising speed of the bicycle. */
  courierSpeedKmh: number;
  /** How long the store needs to prepare the order once the courier arrived. */
  prepTimeSec: number;
  /** Simulated seconds per real second. */
  timeScale: number;
  destination: DestinationPreset;
  /** `direct` hides the courier until `picked_up`; `end_to_end` shows it from `courier_assigned`. */
  model: DeliveryModel;
  vehicle: Vehicle;
}

export const DEFAULT_PARAMS: SimParams = {
  courierSpeedKmh: 18,
  prepTimeSec: 25,
  timeScale: 4,
  destination: 'medium',
  model: 'end_to_end',
  vehicle: 'bicycle',
};

const V1_STATUS: Record<OrderStatus, DeliveryStatus> = {
  requested: 'confirmed',
  accepted: 'courier_assigned',
  to_store: 'courier_assigned',
  at_store: 'at_pickup',
  picked_up: 'picked_up',
  on_the_way: 'picked_up',
  arriving: 'arriving',
  delivered: 'delivered',
};

const ISO = (ms: number) => new Date(ms).toISOString();

// Versions only ever grow, also across restarts: a new order is a new delivery id anyway.
let nextVersion = 1;

const CONFIRM_SEC = 3; // order confirmed -> courier assigned
const ASSIGN_SEC = 3; // courier assigned -> starts riding
const HANDOFF_SEC = 3; // store hands the order over
const ARRIVING_DIST_M = 45;
const ACCEL = 1.6; // m/s²
const DECEL = 2.8; // m/s²
const MAX_STEP_SEC = 0.25;

const toKmh = (ms: number) => ms * 3.6;
const pair = (ll: { lat: number; lng: number }): number[] => [ll.lat, ll.lng];

/**
 * Fake GPS + order state machine. It plays the role of the backend: in a real
 * app you replace this class with a client that emits the same snapshots.
 */
export class DeliverySimulator {
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
  private signal: CourierSignal = 'live';
  private frozen: { lat: number; lng: number } | null = null; // last known position while the signal is down
  private timeline: { status: DeliveryStatus; at: string }[] = [];

  /** The grid world by default; the dev page passes the Amsterdam world when it draws real tiles. */
  constructor(private readonly world: DevWorld = GRID_WORLD) {
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

  get elapsedSec(): number {
    return this.elapsed;
  }

  /** Simulate the courier's phone losing (or regaining) its connection. */
  setSignal(signal: CourierSignal): void {
    if (signal !== 'live' && this.signal === 'live') this.frozen = this.world.projection.toLatLng(this.courierLocal());
    this.signal = signal;
    this.emit();
  }

  get courierSignal(): CourierSignal {
    return this.signal;
  }

  setParams(patch: Partial<SimParams>): void {
    this.params = { ...this.params, ...patch };
  }

  /** Rebuild routes from the current params and go back to "order requested". */
  reset(): void {
    this.leg1 = new Path(this.world.toStore());
    this.leg2 = new Path(this.world.toHome(this.params.destination));
    this.status = 'requested';
    this.stateTime = 0;
    this.elapsed = 0;
    this.legDist = 0;
    this.traveled = 0;
    this.speedMs = 0;
    this.bearing = this.leg1.at(0).bearing;
    this.running = false;
    this.orderId = `#DLV-${Math.floor(10000 + Math.random() * 89999)}`;
    this.signal = 'live';
    this.frozen = null;
    this.timeline = [{ status: 'confirmed', at: ISO(Date.now()) }];
    this.emit();
  }

  start(): void {
    if (this.status === 'delivered') this.reset();
    this.running = true;
  }

  pause(): void {
    this.running = false;
  }

  /** Jump `simSec` simulated seconds ahead (dev page `?t=`, screenshots), then emit once. */
  advance(simSec: number): void {
    this.running = true;
    for (let t = simSec; t > 0 && this.status !== 'delivered'; t -= MAX_STEP_SEC) this.step(Math.min(t, MAX_STEP_SEC));
    this.emit();
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
    const before = V1_STATUS[this.status];
    this.status = next;
    this.stateTime = 0;
    if (V1_STATUS[next] !== before) this.timeline.push({ status: V1_STATUS[next], at: ISO(Date.now()) });
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

  private courierVisible(): boolean {
    if (this.params.model === 'end_to_end') return this.status !== 'requested';
    return this.status === 'picked_up' || this.status === 'on_the_way' || this.status === 'arriving' || this.status === 'delivered';
  }

  snapshot(): TrackingSnapshot {
    const total = this.leg1.length + this.leg2.length;
    const projection = this.world.projection;
    const toLL = (pts: Vec2[]) => pts.map((p) => pair(projection.toLatLng(p)));
    let bearingDeg = (this.bearing * 180) / Math.PI;
    bearingDeg = ((bearingDeg % 360) + 360) % 360;

    const direct = this.params.model === 'direct';
    const beforePickup = this.status === 'requested' || this.status === 'accepted' || this.status === 'to_store' || this.status === 'at_store';
    const eta = this.eta();
    const now = Date.now();
    const lost = this.signal === 'lost' || this.signal === 'stale';

    const courier: TrackingSnapshot['courier'] = this.courierVisible()
      ? {
          display_name: 'Lucas M.',
          vehicle: this.params.vehicle,
          photo_url: null,
          position: lost && this.frozen ? this.frozen : projection.toLatLng(this.courierLocal()),
          heading_deg: bearingDeg,
          speed_kmh: lost ? 0 : toKmh(this.speedMs),
          position_at: ISO(now),
          signal: this.signal,
        }
      : null;

    return {
      delivery_id: this.orderId,
      delivery_model: this.params.model,
      status: V1_STATUS[this.status],
      courier,
      pickup: { location: this.world.storeLocation, name: this.world.storeName },
      dropoff: { location: this.world.homeLocation(this.params.destination), label: 'Home' },
      route: {
        // Direct has no courier leg; after pickup the leg is gone in both models.
        to_pickup: direct || !beforePickup ? null : toLL(this.leg1.points),
        to_dropoff: toLL(this.leg2.points),
      },
      eta_seconds: eta,
      eta_window: beforePickup && eta !== null ? { min_seconds: Math.round(eta * 0.85), max_seconds: Math.round(eta * 1.15) } : null,
      eta_state: eta === null ? 'calculating' : beforePickup ? 'estimated' : 'live',
      eta_quality: lost ? 'estimated' : 'osrm',
      distance_remaining_m: this.distanceRemaining(),
      progress: total > 0 ? Math.min(1, this.traveled / total) : 0,
      timeline: this.timeline.slice(),
      updated_at: ISO(now),
      version: nextVersion++,
    };
  }

  private emit(): void {
    const snap = this.snapshot();
    this.listeners.forEach((l) => l(snap));
  }
}
