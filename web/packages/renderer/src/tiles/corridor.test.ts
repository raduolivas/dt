import { describe, expect, it } from 'vitest';
import type { LatLng } from '@dt/contract';
import { boxTiles, corridorTiles, lngLatToTile } from './corridor';
import { fixtureTiles } from './fixture';

const Z = 14;
const key = (t: { x: number; y: number }) => `${t.x}/${t.y}`;

describe('corridorTiles', () => {
  it('a straight 1 km leg at z14 selects the tiles it crosses and nothing far away', () => {
    const a: LatLng = { lat: 52.3702, lng: 4.8952 };
    const b: LatLng = { lat: 52.3702, lng: 4.8952 + 1000 / (111320 * Math.cos((52.3702 * Math.PI) / 180)) };
    const tiles = corridorTiles([[a, b]], [], Z, 10);
    const ta = lngLatToTile(a, Z);
    const tb = lngLatToTile(b, Z);
    const keys = new Set(tiles.map(key));
    expect(keys.has(key(ta))).toBe(true);
    expect(keys.has(key(tb))).toBe(true);
    expect(tiles.length).toBeLessThanOrEqual(4);
    expect(tiles.every((t) => t.z === Z)).toBe(true);
    // No duplicates.
    expect(keys.size).toBe(tiles.length);
  });

  it('a point near a tile edge pulls in the neighbour only when within the buffer', () => {
    const base = lngLatToTile({ lat: 52.37, lng: 4.9 }, Z);
    const n = 2 ** Z;
    const edgeLng = ((base.x + 1) / n) * 360 - 180;
    const mPerDegLng = 111320 * Math.cos((52.37 * Math.PI) / 180);
    const near: LatLng = { lat: 52.37, lng: edgeLng - 50 / mPerDegLng };
    expect(corridorTiles([], [near], Z, 150).map(key)).toContain(`${base.x + 1}/${lngLatToTile(near, Z).y}`);
    expect(corridorTiles([], [near], Z, 20).map(key)).not.toContain(`${base.x + 1}/${lngLatToTile(near, Z).y}`);
  });

  it('stays within the 2 MB first-load budget for a 12 km route', async () => {
    const tiles = await fixtureTiles();
    expect(tiles.length).toBeGreaterThan(0);
    const mean = tiles.reduce((s, t) => s + t.data.byteLength, 0) / tiles.length;
    // Synthetic L-shaped 12 km route: 7 km east, then 5 km north.
    const mLat = 111320;
    const mLng = 111320 * Math.cos((52.37 * Math.PI) / 180);
    const store: LatLng = { lat: 52.3, lng: 4.8 };
    const corner: LatLng = { lat: store.lat, lng: store.lng + 7000 / mLng };
    const home: LatLng = { lat: store.lat + 5000 / mLat, lng: corner.lng };
    const legs = [[store, corner, home]];
    const first = new Set([
      ...corridorTiles(legs, [], Z, 150).map(key),
      ...boxTiles([], [store], Z, 200).map(key),
      ...boxTiles([], [home], Z, 200).map(key),
    ]);
    expect(first.size * mean).toBeLessThanOrEqual(2 * 1024 * 1024);
  });
});

describe('boxTiles', () => {
  it('includes the 300 m margin', () => {
    const p: LatLng = { lat: 52.37, lng: 4.9 };
    const base = lngLatToTile(p, Z);
    const n = 2 ** Z;
    const mLng = 111320 * Math.cos((52.37 * Math.PI) / 180);
    const edge = ((base.x + 1) / n) * 360 - 180;
    const q: LatLng = { lat: p.lat, lng: edge - 250 / mLng };
    const withMargin = boxTiles([], [q], Z).map(key);
    expect(withMargin).toContain(`${base.x + 1}/${lngLatToTile(q, Z).y}`);
    expect(boxTiles([], [q], Z, 100).map(key)).not.toContain(`${base.x + 1}/${lngLatToTile(q, Z).y}`);
  });

  it('covers every point and leg vertex', () => {
    const pts: LatLng[] = [
      { lat: 52.36, lng: 4.87 },
      { lat: 52.37, lng: 4.92 },
    ];
    const keys = new Set(boxTiles([pts], [], Z).map(key));
    for (const p of pts) expect(keys.has(key(lngLatToTile(p, Z)))).toBe(true);
  });
});
