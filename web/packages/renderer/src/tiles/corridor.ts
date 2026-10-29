import type { LatLng } from '@dt/contract';

export interface TileXY {
  z: number;
  x: number;
  y: number;
}

const M_PER_DEG_LAT = 111320;
const RAD = Math.PI / 180;

export function lngLatToTile(p: LatLng, z: number): TileXY {
  const n = 2 ** z;
  const x = Math.floor(((p.lng + 180) / 360) * n);
  const y = Math.floor(((1 - Math.asinh(Math.tan(p.lat * RAD)) / Math.PI) / 2) * n);
  return { z, x: Math.min(n - 1, Math.max(0, x)), y: Math.min(n - 1, Math.max(0, y)) };
}

function tileLng(x: number, z: number): number {
  return (x / 2 ** z) * 360 - 180;
}

function tileLat(y: number, z: number): number {
  return Math.atan(Math.sinh(Math.PI * (1 - (2 * y) / 2 ** z))) / RAD;
}

/** Metres from p to the nearest point of the tile's lng/lat rectangle (0 inside). */
function distanceToTileM(p: LatLng, t: TileXY): number {
  const west = tileLng(t.x, t.z);
  const east = tileLng(t.x + 1, t.z);
  const north = tileLat(t.y, t.z);
  const south = tileLat(t.y + 1, t.z);
  const dx = (Math.max(west - p.lng, 0, p.lng - east) * M_PER_DEG_LAT) * Math.cos(p.lat * RAD);
  const dy = Math.max(south - p.lat, 0, p.lat - north) * M_PER_DEG_LAT;
  return Math.hypot(dx, dy);
}

function tilesAround(p: LatLng, z: number, radiusM: number, into: Map<string, TileXY>): void {
  const dLat = radiusM / M_PER_DEG_LAT;
  const dLng = radiusM / (M_PER_DEG_LAT * Math.cos(p.lat * RAD));
  const lo = lngLatToTile({ lat: p.lat + dLat, lng: p.lng - dLng }, z);
  const hi = lngLatToTile({ lat: p.lat - dLat, lng: p.lng + dLng }, z);
  for (let x = lo.x; x <= hi.x; x++) {
    for (let y = lo.y; y <= hi.y; y++) {
      const t = { z, x, y };
      const key = `${x}/${y}`;
      if (!into.has(key) && distanceToTileM(p, t) <= radiusM) into.set(key, t);
    }
  }
}

function sorted(m: Map<string, TileXY>): TileXY[] {
  return [...m.values()].sort((a, b) => a.y - b.y || a.x - b.x);
}

/** Tiles within `bufferM` of any leg segment or point. */
export function corridorTiles(legs: LatLng[][], points: LatLng[], z: number, bufferM: number): TileXY[] {
  const out = new Map<string, TileXY>();
  const step = Math.max(10, bufferM / 2);
  for (const p of points) tilesAround(p, z, bufferM, out);
  for (const leg of legs) {
    if (leg.length === 1) tilesAround(leg[0]!, z, bufferM, out);
    for (let i = 1; i < leg.length; i++) {
      const a = leg[i - 1]!;
      const b = leg[i]!;
      const lenM = Math.hypot(
        (b.lat - a.lat) * M_PER_DEG_LAT,
        (b.lng - a.lng) * M_PER_DEG_LAT * Math.cos(((a.lat + b.lat) / 2) * RAD),
      );
      const n = Math.max(1, Math.ceil(lenM / step));
      for (let k = 0; k <= n; k++) {
        const f = k / n;
        tilesAround({ lat: a.lat + (b.lat - a.lat) * f, lng: a.lng + (b.lng - a.lng) * f }, z, bufferM, out);
      }
    }
  }
  return sorted(out);
}

/** All tiles inside the bounding box of the legs and points, grown by `marginM` on every side. */
export function boxTiles(legs: LatLng[][], points: LatLng[], z: number, marginM = 300): TileXY[] {
  const all = [...legs.flat(), ...points];
  if (all.length === 0) return [];
  let minLat = Infinity;
  let maxLat = -Infinity;
  let minLng = Infinity;
  let maxLng = -Infinity;
  for (const p of all) {
    minLat = Math.min(minLat, p.lat);
    maxLat = Math.max(maxLat, p.lat);
    minLng = Math.min(minLng, p.lng);
    maxLng = Math.max(maxLng, p.lng);
  }
  const midLat = (minLat + maxLat) / 2;
  const dLat = marginM / M_PER_DEG_LAT;
  const dLng = marginM / (M_PER_DEG_LAT * Math.cos(midLat * RAD));
  const lo = lngLatToTile({ lat: maxLat + dLat, lng: minLng - dLng }, z);
  const hi = lngLatToTile({ lat: minLat - dLat, lng: maxLng + dLng }, z);
  const out: TileXY[] = [];
  for (let y = lo.y; y <= hi.y; y++) for (let x = lo.x; x <= hi.x; x++) out.push({ z, x, y });
  return out;
}
