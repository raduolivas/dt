import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { vi } from 'vitest';
import type { LatLng, TrackingSnapshot } from '@dt/contract';
import { fake2d, installFakeDom, type Fake2d, type FakeDom } from '../testing/fakeDom';
import { COLORS } from './colors';
import { FallbackView } from './fallback';

const STORE: LatLng = { lat: 52.3731, lng: 4.8926 };
const HOME: LatLng = { lat: 52.36, lng: 4.8852 };
const MID: LatLng = { lat: 52.367, lng: 4.889 };
const pair = (p: LatLng) => [p.lat, p.lng];
const hex = (c: number) => `#${c.toString(16).padStart(6, '0')}`;

function snap(over: Partial<TrackingSnapshot> = {}): TrackingSnapshot {
  return {
    delivery_id: 'del_fb',
    delivery_model: 'end_to_end',
    status: 'picked_up',
    courier: {
      display_name: 'Lucas M.',
      vehicle: 'bicycle',
      photo_url: null,
      position: MID,
      heading_deg: 0,
      speed_kmh: 0,
      position_at: null,
      signal: 'live',
    },
    pickup: { location: STORE, name: 'Food Hub' },
    dropoff: { location: HOME, label: 'Keizersgracht 1' },
    route: { to_pickup: null, to_dropoff: [pair(STORE), pair(MID), pair(HOME)] },
    eta_seconds: 300,
    eta_window: null,
    eta_state: 'live',
    eta_quality: 'estimated',
    distance_remaining_m: 900,
    progress: 0.5,
    timeline: [],
    updated_at: '2026-10-29T10:00:00Z',
    version: 1,
    ...over,
  } as TrackingSnapshot;
}

let dom: FakeDom;
let ctx: Fake2d;
beforeEach(() => {
  dom = installFakeDom();
  ctx = fake2d();
  dom.canvasContexts['2d'] = () => ctx;
});
afterEach(() => vi.unstubAllGlobals());

const ops = (op: string) => ctx.log.filter((o) => o.op === op);
const view = () => new FallbackView(dom.container as unknown as HTMLElement, { theme: 'light', clock: () => 0 });

describe('FallbackView', () => {
  it('draws the route, the pickup, the dropoff and the courier dot in the prototype palette', () => {
    const v = view();
    v.onSnapshot(snap());
    v.frame(0.016);
    const strokes = ops('stroke').map((o) => o.strokeStyle);
    expect(strokes).toContain('#2f6bff'); // the active leg
    const fills = ops('fill').map((o) => o.fillStyle);
    expect(fills).toContain(hex(COLORS.blue)); // store and courier
    expect(fills).toContain(hex(COLORS.coral)); // home
    // three round markers at least: store, home, courier
    expect(ops('arc').length).toBeGreaterThanOrEqual(3);
    // a calm map background, never an error look
    expect(ops('fillRect')[0]!.fillStyle).toBe(hex(COLORS.ground));
  });

  it('keeps everything on the canvas', () => {
    const v = view();
    v.onSnapshot(snap());
    v.frame(0.016);
    const W = dom.container.clientWidth * 2; // device pixels (devicePixelRatio 2)
    const H = dom.container.clientHeight * 2;
    for (const o of ops('arc')) {
      const [x, y] = o.args as number[];
      expect(x).toBeGreaterThan(0);
      expect(x).toBeLessThan(W);
      expect(y).toBeGreaterThan(0);
      expect(y).toBeLessThan(H);
    }
  });

  it('draws the next leg and the active one before pickup, in the accent when given', () => {
    const v = view();
    v.setAccent('#aa3300');
    v.onSnapshot(snap({ status: 'courier_assigned', route: { to_pickup: [pair(MID), pair(STORE)], to_dropoff: [pair(STORE), pair(HOME)] } }));
    v.frame(0.016);
    const strokes = ops('stroke').map((o) => o.strokeStyle);
    expect(strokes).toContain('#aa3300');
    expect(strokes.length).toBeGreaterThanOrEqual(2);
  });

  it('draws no courier the snapshot does not show', () => {
    const v = view();
    v.onSnapshot(snap({ status: 'confirmed', courier: null }));
    v.frame(0.016);
    expect(ops('arc').length).toBe(2); // store and home only
  });

  it('turns the home green on delivered and draws no route', () => {
    const v = view();
    v.onSnapshot(snap({ status: 'delivered', courier: null }));
    v.frame(0.016);
    expect(ops('stroke').filter((o) => o.strokeStyle === '#2f6bff')).toHaveLength(0);
    expect(ops('fill').map((o) => o.fillStyle)).toContain(hex(COLORS.green));
  });

  it('removes its canvas on dispose', () => {
    const v = view();
    expect(dom.container.children.some((c) => c.tagName === 'canvas')).toBe(true);
    v.dispose();
    expect(dom.container.children.some((c) => c.tagName === 'canvas')).toBe(false);
  });
});
