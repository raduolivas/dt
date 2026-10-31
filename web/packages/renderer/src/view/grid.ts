import type { LatLng } from '@dt/contract';
import { GeoProjection, type Vec2 } from '../engine/geo';

/**
 * The procedural city: a regular street lattice. Everything is defined in local
 * meters and converted to lat/lng through `projection`, so the rest of the app
 * only ever deals with real geographic coordinates. Dev only: real map tiles
 * replace it (spec §5.2).
 */
export const BLOCK = 110; // distance between two parallel streets (m)
export const ROAD_W = 20; // street width (m)
export const GRID = 3; // streets run from -GRID..GRID in both directions

/** Anchor of the prototype city. Change it to place the scene anywhere on Earth. */
export const ORIGIN: LatLng = { lat: 52.3676, lng: 4.9041 };
export const projection = new GeoProjection(ORIGIN);

export interface Place {
  id: string;
  label: string;
  /** Curb point on the street where the courier stops. */
  stop: Vec2;
  /** Center of the building next to that curb point. */
  building: Vec2;
  /** Which side of the (horizontal) street the building is on: +1 south, -1 north. */
  side: 1 | -1;
  /** City-block cell occupied by the building. */
  cell: { i: number; j: number };
}

export function placeOnStreet(id: string, label: string, i: number, j: number, side: 1 | -1): Place {
  const stop = { x: i * BLOCK + BLOCK / 2, z: j * BLOCK };
  return {
    id,
    label,
    stop,
    building: { x: stop.x, z: stop.z + side * (ROAD_W / 2 + 27) },
    side,
    cell: { i, j: side === 1 ? j : j - 1 },
  };
}

/** The grid place whose curb point is nearest to `ll` (the building stands on the north side of its street). */
export function placeNear(id: string, label: string, ll: LatLng): Place {
  const p = projection.toLocal(ll);
  const i = Math.round((p.x - BLOCK / 2) / BLOCK);
  const j = Math.round(p.z / BLOCK);
  return placeOnStreet(id, label, i, j, -1);
}
