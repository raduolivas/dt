import * as THREE from 'three';
import { describe, expect, it } from 'vitest';
import type { LatLng, TrackingSnapshot } from '@dt/contract';
import { AMS_COURIER_START, AMS_HOMES, AMS_STORE, AMS_TO_STORE } from '../dev/amsterdam';
import { GeoProjection } from '../engine/geo';
import { decodeTile } from '../tiles/build/decode';
import { FIXTURE_PATH, FileSource } from '../tiles/fixture';
import { openPmtiles } from '../tiles/pmtiles';
import type { TileSource } from '../tiles/source';
import { TILE_LAYERS, TileCity, placeSites, type TileBuilder } from './city';
import { frameCamera } from './framing';

// Headless: geometry only. The worker is replaced by a builder that calls `decodeTile` directly.
const builder: TileBuilder = { build: async (req) => decodeTile(req).layers };
const pair = (p: LatLng) => [p.lat, p.lng];

const CANAL: LatLng = { lat: 52.3694, lng: 4.8879 }; // Keizersgracht water
const DAM_SQUARE: LatLng = { lat: 52.3731, lng: 4.8926 };
const RIJKSMUSEUM: LatLng = { lat: 52.36, lng: 4.8852 };

let sourceP: Promise<TileSource> | undefined;
const fixture = () => (sourceP ??= openPmtiles('fixture', { source: new FileSource(FIXTURE_PATH) }));

function snap(pickup: LatLng, dropoff: LatLng, over: Partial<TrackingSnapshot> = {}): TrackingSnapshot {
  return {
    delivery_id: 'del_city',
    delivery_model: 'end_to_end',
    status: 'courier_assigned',
    courier: null,
    pickup: { location: pickup, name: 'Food Hub' },
    dropoff: { location: dropoff, label: null },
    route: { to_pickup: null, to_dropoff: [pair(pickup), pair(dropoff)] },
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

/** The dev simulator's Amsterdam delivery (end to end, courier at its start). */
function devSnap(preset: keyof typeof AMS_HOMES): TrackingSnapshot {
  const home = AMS_HOMES[preset];
  return snap(AMS_STORE.location, home.location, {
    courier: {
      display_name: 'Lucas M.',
      vehicle: 'bicycle',
      photo_url: null,
      position: AMS_COURIER_START,
      heading_deg: 0,
      speed_kmh: 0,
      position_at: null,
      signal: 'live',
    },
    route: { to_pickup: AMS_TO_STORE, to_dropoff: home.route },
  });
}

async function cityFor(s: TrackingSnapshot) {
  const group = new THREE.Group();
  const city = new TileCity(group, await fixture(), new GeoProjection(s.pickup.location), 'light', { builder });
  const first = await city.loadFirst(s);
  return { group, city, first };
}

describe('TileCity', { timeout: 30_000 }, () => {
  it('places standalone models when no footprint contains the point', async () => {
    const s = snap(CANAL, DAM_SQUARE);
    const { group, city } = await cityFor(s);
    expect(city.findFootprint(CANAL)).toBeNull();
    expect(city.findFootprint(DAM_SQUARE)).toBeNull();
    const sites = placeSites(city, s, group);
    expect(sites.store.kind).toBe('standalone');
    expect(sites.home.kind).toBe('standalone');
    expect(group.children).toContain(sites.store.object);
    expect(group.children).toContain(sites.home.object);
    city.dispose();
  });

  it('uses the footprint containing the pickup', async () => {
    const s = snap(RIJKSMUSEUM, AMS_HOMES.medium.location);
    const { group, city } = await cityFor(s);
    const fp = city.findFootprint(RIJKSMUSEUM);
    expect(fp).not.toBeNull();
    const local = new GeoProjection(RIJKSMUSEUM).toLocal(RIJKSMUSEUM);
    expect(Math.abs(local.x) + Math.abs(local.z)).toBeLessThan(1e-6); // the origin is the pickup
    expect(fp!.ring.length).toBeGreaterThanOrEqual(3);
    expect(fp!.height).toBeGreaterThan(0);
    const sites = placeSites(city, s, group);
    expect(sites.store.kind).toBe('footprint');
    expect(sites.home.kind).toBe('footprint'); // the dev home is inside a building too
    city.dispose();
  });

  it('first load reports bytes ≤ 2 MB on the fixture for the dev simulator route', async () => {
    for (const preset of ['near', 'medium', 'far'] as const) {
      const { city, first } = await cityFor(devSnap(preset));
      expect(first.bytes, preset).toBeGreaterThan(0);
      expect(first.bytes, preset).toBeLessThanOrEqual(2_000_000);
      city.dispose();
    }
  });

  it('loads the rest of the route box lazily', async () => {
    const s = devSnap('far');
    const { city } = await cityFor(s);
    const before = city.loadedTiles;
    expect(city.pendingTiles).toBeGreaterThan(0);
    const proj = new GeoProjection(s.pickup.location);
    // Nothing queued is near the store itself: a focus there loads nothing new.
    city.loadMore(new THREE.Vector3(0, 0, 0), 100);
    await city.idle();
    expect(city.loadedTiles).toBe(before);
    // The camera pulls back over the whole route: the rest of the box loads.
    const mid = proj.toLocal({ lat: (AMS_STORE.location.lat + AMS_HOMES.far.location.lat) / 2, lng: (AMS_STORE.location.lng + AMS_HOMES.far.location.lng) / 2 });
    city.loadMore(new THREE.Vector3(mid.x, 0, mid.z), 4000);
    await city.idle();
    expect(city.loadedTiles).toBeGreaterThan(before);
    expect(city.pendingTiles).toBe(0);
    city.dispose();
  });

  it('draws at most one draw call per layer per tile', async () => {
    const { group, city } = await cityFor(devSnap('medium'));
    const tiles = group.children.filter((c) => c.userData.tile);
    expect(tiles.length).toBeGreaterThan(0);
    for (const t of tiles) {
      const perLayer = new Map<string, number>();
      t.traverse((o) => {
        if (o instanceof THREE.Mesh) perLayer.set(o.userData.layer, (perLayer.get(o.userData.layer) ?? 0) + 1);
      });
      for (const [layer, n] of perLayer) {
        expect(TILE_LAYERS).toContain(layer);
        expect(n, `${t.userData.tile} ${layer}`).toBeLessThanOrEqual(1);
      }
    }
    city.dispose();
  });
});

describe('frameCamera', () => {
  const VIEWPORTS: [number, number][] = [
    [375, 667],
    [390, 844],
    [1440, 900],
  ];

  function expectInFrustum(points: LatLng[]) {
    const proj = new GeoProjection(points[0]!);
    const world = points.map((p) => {
      const l = proj.toLocal(p);
      return new THREE.Vector3(l.x, 0, l.z);
    });
    for (const [w, h] of VIEWPORTS) {
      const cam = new THREE.OrthographicCamera();
      frameCamera(cam, world, w, h);
      cam.updateMatrixWorld();
      for (const p of world) {
        const v = p.clone().project(cam);
        expect(Math.abs(v.x), `${w}x${h} x`).toBeLessThan(1);
        expect(Math.abs(v.y), `${w}x${h} y`).toBeLessThan(1);
        expect(Math.abs(v.z), `${w}x${h} depth`).toBeLessThan(1);
      }
    }
  }

  it('frames a 40 m route', () => {
    expectInFrustum([
      { lat: 52.37, lng: 4.89 },
      { lat: 52.37036, lng: 4.89 },
    ]);
  });

  it('frames a 12 km route', () => {
    expectInFrustum([
      { lat: 52.37, lng: 4.89 },
      { lat: 52.3, lng: 5.03 },
      { lat: 52.33, lng: 4.95 }, // the courier
    ]);
  });
});
