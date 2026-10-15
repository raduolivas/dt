import { GeoProjection, type Vec2 } from './geo';
import type { DestinationPreset, LatLng } from './types';

/**
 * The prototype city: a regular street lattice. Everything is defined in local
 * meters and converted to lat/lng through `projection`, so the rest of the app
 * only ever deals with real geographic coordinates.
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

function placeOnStreet(id: string, label: string, i: number, j: number, side: 1 | -1): Place {
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

export const STORE: Place = placeOnStreet('store', 'Food Hub', -2, -2, -1);

export const DESTINATIONS: Record<DestinationPreset, Place> = {
  near: placeOnStreet('home', 'Home', -1, 0, -1),
  medium: placeOnStreet('home', 'Home', 1, 1, -1),
  far: placeOnStreet('home', 'Home', 2, 3, -1),
};

/** Where the courier starts the shift. */
export const COURIER_START: Vec2 = { x: 1 * BLOCK + BLOCK / 2, z: -BLOCK };
