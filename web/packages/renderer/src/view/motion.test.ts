import { describe, expect, it } from 'vitest';
import type { LatLng, TrackingSnapshot } from '@dt/contract';
import { CourierMotion, SpeedMeter, deadReckon, snapToLeg, type MotionState } from './motion';

// local metres: x east, z south. 1 degree of "lat" is 1 m in this toy projection.
const toLocal = (p: LatLng) => ({ x: p.lng, z: p.lat });
const LEG = [
  { x: 0, z: 0 },
  { x: 100, z: 0 },
  { x: 100, z: 100 },
];

describe('snapToLeg', () => {
  it('snaps a point 10 m off the leg and refuses one 40 m off', () => {
    const r = snapToLeg({ x: 50, z: 10 }, LEG);
    expect(r).not.toBeNull();
    expect(r!.p).toEqual({ x: 50, z: 0 });
    expect(r!.index).toBe(0);
    expect(r!.along).toBeCloseTo(50);
    expect(snapToLeg({ x: 50, z: 40 }, LEG)).toBeNull();
  });
  it('reports the segment and the distance along the leg', () => {
    const r = snapToLeg({ x: 105, z: 60 }, LEG)!;
    expect(r.index).toBe(1);
    expect(r.along).toBeCloseTo(160);
  });
  it('refuses a leg without two points', () => {
    expect(snapToLeg({ x: 0, z: 0 }, [{ x: 0, z: 0 }])).toBeNull();
  });
});

describe('deadReckon', () => {
  const base: MotionState = { p: { x: 10, z: 0 }, leg: LEG, index: 0, speedKmh: 36, atMs: 1000, signal: 'live' };
  it('advances at speed_kmh along the leg', () => {
    const p = deadReckon(base, 3000); // 2 s at 10 m/s
    expect(p.x).toBeCloseTo(30);
    expect(p.z).toBeCloseTo(0);
  });
  it('never passes the next unreached route point', () => {
    const p = deadReckon({ ...base, speedKmh: 180 }, 11000); // would be 100 m
    expect(p).toEqual({ x: 100, z: 0 });
  });
  it('stops after 30 s', () => {
    const slow: MotionState = { ...base, speedKmh: 3.6 }; // 1 m/s
    expect(deadReckon(slow, 1000 + 30000).x).toBeCloseTo(40);
    expect(deadReckon(slow, 1000 + 90000).x).toBeCloseTo(40);
  });
  it('only moves while the signal is live and the fix is on the leg', () => {
    expect(deadReckon({ ...base, signal: 'stale' }, 3000)).toEqual(base.p);
    expect(deadReckon({ ...base, signal: 'lost' }, 3000)).toEqual(base.p);
    expect(deadReckon({ ...base, index: -1 }, 3000)).toEqual(base.p);
    expect(deadReckon({ ...base, speedKmh: null }, 3000)).toEqual(base.p);
  });
});

function snap(over: {
  version?: number;
  status?: TrackingSnapshot['status'];
  x?: number;
  z?: number;
  signal?: 'pending' | 'live' | 'stale' | 'lost';
  noCourier?: boolean;
  speed?: number | null;
  delivery?: string;
  heading?: number;
}): TrackingSnapshot {
  const signal = over.signal ?? 'live';
  return {
    delivery_id: over.delivery ?? 'del_1',
    delivery_model: 'direct',
    status: over.status ?? 'picked_up',
    courier: over.noCourier
      ? null
      : {
          display_name: 'A',
          vehicle: 'bicycle',
          photo_url: null,
          position: signal === 'pending' ? null : { lat: over.z ?? 0, lng: over.x ?? 10 },
          heading_deg: over.heading ?? 90,
          speed_kmh: over.speed ?? 0,
          position_at: null,
          signal,
        },
    pickup: { location: { lat: 0, lng: 0 }, name: 'S' },
    dropoff: { location: { lat: 100, lng: 100 }, label: null },
    route: { to_pickup: null, to_dropoff: [[0, 0], [0, 100], [100, 100]] },
    eta_seconds: null,
    eta_window: null,
    eta_state: 'calculating',
    eta_quality: 'estimated',
    distance_remaining_m: null,
    progress: null,
    timeline: [],
    updated_at: '2026-01-01T00:00:00Z',
    version: over.version ?? 1,
  } as unknown as TrackingSnapshot;
}

describe('CourierMotion', () => {
  it('glides to a fix 80 m ahead without a jump', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ version: 1, x: 10 }), 0);
    expect(m.frame(16, 0).p.x).toBeCloseTo(10);
    m.onSnapshot(snap({ version: 2, x: 90 }), 100);
    let prev = m.frame(0, 100).p.x;
    let maxStep = 0;
    for (let t = 116; t <= 4000; t += 16) {
      const x = m.frame(16, t).p.x;
      maxStep = Math.max(maxStep, Math.abs(x - prev));
      expect(x).toBeGreaterThanOrEqual(prev - 1e-9);
      prev = x;
    }
    expect(maxStep).toBeLessThan(6); // 80 m over several frames, never in one
    expect(prev).toBeCloseTo(90, 0);
  });

  it('places the first fix at once and drawn on the leg', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ x: 40, version: 1 }), 0);
    const f = m.frame(16, 0);
    expect(f.visible).toBe(true);
    expect(f.faded).toBe(false);
    expect(f.p).toEqual({ x: 40, z: 0 });
  });

  it('fades and stops when the signal is stale or lost', () => {
    for (const signal of ['stale', 'lost'] as const) {
      const m = new CourierMotion(toLocal);
      m.onSnapshot(snap({ version: 1, x: 10, speed: 36 }), 0);
      m.frame(16, 16);
      m.onSnapshot(snap({ version: 2, x: 10, speed: 36, signal }), 100);
      const a = m.frame(16, 200);
      const b = m.frame(16, 5000);
      expect(a.faded).toBe(true);
      expect(b.faded).toBe(true);
      expect(b.visible).toBe(true);
      expect(b.p).toEqual(a.p);
    }
  });

  it('dead-reckons while live, for at most 30 s', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ x: 10, speed: 3.6 }), 0);
    for (let t = 0; t <= 60000; t += 100) m.frame(100, t);
    expect(m.frame(100, 60100).p.x).toBeCloseTo(40, 0);
  });

  it('hides for pending, for no courier and for a final status', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ version: 1, signal: 'pending' }), 0);
    expect(m.frame(16, 0).visible).toBe(false);
    m.onSnapshot(snap({ version: 2, x: 10 }), 10);
    expect(m.frame(16, 10).visible).toBe(true);
    m.onSnapshot(snap({ version: 3, noCourier: true }), 20);
    expect(m.frame(16, 20).visible).toBe(false);
    m.onSnapshot(snap({ version: 4, x: 10 }), 30);
    expect(m.frame(16, 30).visible).toBe(true);
    m.onSnapshot(snap({ version: 5, x: 10, status: 'delivered' }), 40);
    expect(m.frame(16, 40).visible).toBe(false);
  });

  it('ignores a snapshot with a lower or equal version', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ version: 5, x: 10 }), 0);
    m.onSnapshot(snap({ version: 4, noCourier: true }), 10);
    m.onSnapshot(snap({ version: 5, noCourier: true }), 10);
    expect(m.frame(16, 10).visible).toBe(true);
    m.onSnapshot(snap({ version: 1, x: 10, delivery: 'del_2' }), 20); // another delivery starts over
    expect(m.frame(16, 20).visible).toBe(true);
  });

  it('draws a fix more than 25 m off the leg raw', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ x: 50, speed: 36 }), 0);
    m.onSnapshot(snap({ version: 2, x: 50 }), 0);
    const f = m.frame(16, 0);
    expect(f.p.z).toBe(0);
    const raw = new CourierMotion(toLocal);
    raw.onSnapshot(snap({ x: 50, z: 40, speed: 36 }), 0);
    expect(raw.frame(16, 5000).p).toEqual({ x: 50, z: 40 });
  });

  it('turns to the first heading at once again after a reset', () => {
    const m = new CourierMotion(toLocal);
    m.onSnapshot(snap({ version: 1, x: 10, heading: 90 }), 0);
    const first = m.frame(16, 0).yaw;
    m.reset();
    m.onSnapshot(snap({ version: 2, x: 10, heading: 270 }), 10);
    const yaw = m.frame(0, 10).yaw;
    expect(Math.abs(yaw - first)).toBeCloseTo(Math.PI); // placed facing the new heading, not turning towards it
  });
});

describe('SpeedMeter', () => {
  it('reads the speed the courier is drawn moving at, and zero once it stands still', () => {
    const s = new SpeedMeter();
    let v = 0;
    for (let i = 1; i <= 60; i++) v = s.update({ x: i * 0.16, z: 0 }, 0.016); // 10 m/s
    expect(v).toBeCloseTo(10, 0);
    for (let i = 0; i < 60; i++) v = s.update({ x: 9.6, z: 0 }, 0.016); // stalled (dead reckoning capped)
    expect(v).toBeLessThan(0.05);
  });
  it('ignores the jump when the courier is placed somewhere new', () => {
    const s = new SpeedMeter();
    s.update({ x: 0, z: 0 }, 0.016);
    s.reset();
    expect(s.update({ x: 500, z: 0 }, 0.016)).toBe(0);
  });
});
