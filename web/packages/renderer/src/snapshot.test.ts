import { describe, expect, it } from 'vitest';
import type { DeliveryStatus, TrackingSnapshot } from '@dt/contract';
import { SnapshotGate, activeLeg, toLatLng, viewKind } from './snapshot';
import { DeliverySimulator } from './dev/simulator';

const ALL: DeliveryStatus[] = [
  'confirmed',
  'courier_assigned',
  'at_pickup',
  'picked_up',
  'arriving',
  'delivered',
  'cancelled',
  'failed',
];

const PICKUP_LEG = [
  [52.0, 4.0],
  [52.001, 4.001],
];
const DROPOFF_LEG = [
  [52.001, 4.001],
  [52.002, 4.002],
];

function snap(over: Partial<TrackingSnapshot>): TrackingSnapshot {
  return {
    delivery_id: 'del_1',
    delivery_model: 'end_to_end',
    status: 'confirmed',
    courier: null,
    pickup: { location: { lat: 52, lng: 4 }, name: 'Food Hub' },
    dropoff: { location: { lat: 52.002, lng: 4.002 }, label: null },
    route: { to_pickup: PICKUP_LEG, to_dropoff: DROPOFF_LEG },
    eta_seconds: null,
    eta_window: null,
    eta_state: 'calculating',
    eta_quality: 'estimated',
    distance_remaining_m: null,
    progress: null,
    timeline: [],
    updated_at: '2026-10-29T10:00:00Z',
    version: 1,
    ...over,
  };
}

const courier: NonNullable<TrackingSnapshot['courier']> = {
  display_name: 'Lucas M.',
  vehicle: 'bicycle',
  photo_url: null,
  position: { lat: 52, lng: 4 },
  heading_deg: 0,
  speed_kmh: 0,
  position_at: null,
  signal: 'live',
};

describe('toLatLng', () => {
  it('turns a [lat, lng] pair into a LatLng', () => {
    expect(toLatLng([52.1, 4.9])).toEqual({ lat: 52.1, lng: 4.9 });
  });
});

describe('activeLeg', () => {
  const before: DeliveryStatus[] = ['confirmed', 'courier_assigned', 'at_pickup'];
  const after: DeliveryStatus[] = ['picked_up', 'arriving', 'delivered', 'cancelled', 'failed'];

  it('end_to_end: to_pickup before picked_up, to_dropoff from then on', () => {
    for (const status of before) {
      expect(activeLeg(snap({ status })), status).toEqual(PICKUP_LEG.map(toLatLng));
    }
    for (const status of after) {
      expect(activeLeg(snap({ status, route: { to_pickup: null, to_dropoff: DROPOFF_LEG } })), status).toEqual(
        DROPOFF_LEG.map(toLatLng),
      );
    }
  });

  it('direct: to_pickup is null, so every status uses to_dropoff', () => {
    for (const status of ALL) {
      const s = snap({ status, delivery_model: 'direct', route: { to_pickup: null, to_dropoff: DROPOFF_LEG } });
      expect(activeLeg(s), status).toEqual(DROPOFF_LEG.map(toLatLng));
    }
  });
});

describe('viewKind', () => {
  it('is final for delivered, failed and cancelled', () => {
    for (const status of ['delivered', 'failed', 'cancelled'] as const) {
      expect(viewKind(snap({ status, courier })), status).toBe('final');
      expect(viewKind(snap({ status, courier: null })), status).toBe('final');
    }
  });

  it('is preparing when there is no courier and the delivery is not final', () => {
    for (const status of ALL.filter((st) => !['delivered', 'failed', 'cancelled'].includes(st))) {
      expect(viewKind(snap({ status, courier: null })), status).toBe('preparing');
      expect(viewKind(snap({ status, courier })), status).toBe('live');
    }
  });
});

describe('SnapshotGate', () => {
  it('ignores a snapshot whose version is not greater than the last applied one', () => {
    const g = new SnapshotGate();
    expect(g.accept(snap({ version: 5 }))).toBe(true);
    expect(g.accept(snap({ version: 5 }))).toBe(false);
    expect(g.accept(snap({ version: 4 }))).toBe(false);
    expect(g.accept(snap({ version: 6 }))).toBe(true);
  });

  it('starts over for a different delivery', () => {
    const g = new SnapshotGate();
    g.accept(snap({ version: 9 }));
    expect(g.accept(snap({ delivery_id: 'del_2', version: 1 }))).toBe(true);
  });
});

describe('dev simulator', () => {
  function run(model: 'direct' | 'end_to_end') {
    const sim = new DeliverySimulator();
    sim.setParams({ model, timeScale: 30, prepTimeSec: 5 });
    sim.reset();
    const seen: TrackingSnapshot[] = [];
    sim.subscribe((s) => seen.push(s));
    sim.start();
    for (let i = 0; i < 20000 && sim.isRunning; i++) sim.update(0.1);
    return seen;
  }

  it('a direct run never emits a courier before picked_up', () => {
    const seen = run('direct');
    expect(seen.at(-1)?.status).toBe('delivered');
    for (const s of seen) {
      expect(s.delivery_model).toBe('direct');
      expect(s.route.to_pickup).toBeNull();
      const early = s.status === 'confirmed' || s.status === 'courier_assigned' || s.status === 'at_pickup';
      if (early) expect(s.courier, s.status).toBeNull();
    }
    expect(seen.some((s) => s.status === 'picked_up' && s.courier !== null)).toBe(true);
  });

  it('an end_to_end run shows the courier from courier_assigned', () => {
    const seen = run('end_to_end');
    expect(seen.filter((s) => s.status === 'confirmed').every((s) => s.courier === null)).toBe(true);
    const assigned = seen.filter((s) => s.status === 'courier_assigned');
    expect(assigned.length).toBeGreaterThan(0);
    expect(assigned.every((s) => s.courier !== null)).toBe(true);
  });

  it('versions only increase, also across a restart', () => {
    const sim = new DeliverySimulator();
    const versions: number[] = [];
    sim.subscribe((s) => versions.push(s.version));
    sim.start();
    for (let i = 0; i < 50; i++) sim.update(0.1);
    sim.reset();
    sim.start();
    for (let i = 0; i < 50; i++) sim.update(0.1);
    for (let i = 1; i < versions.length; i++) expect(versions[i]).toBeGreaterThan(versions[i - 1]!);
  });

  it('dropping the signal flips the courier signal to stale then lost, restoring makes it live', () => {
    const sim = new DeliverySimulator();
    sim.start();
    for (let i = 0; i < 100; i++) sim.update(0.1);
    expect(sim.snapshot().courier?.signal).toBe('live');
    sim.setSignal('stale');
    expect(sim.snapshot().courier?.signal).toBe('stale');
    sim.setSignal('lost');
    expect(sim.snapshot().courier?.signal).toBe('lost');
    sim.setSignal('live');
    expect(sim.snapshot().courier?.signal).toBe('live');
  });
});
