import type { LatLng } from '@dt/contract';
import { BLOCK, placeOnStreet, projection as gridProjection, type Place } from '../view/grid';
import { GeoProjection, type Vec2 } from '../engine/geo';
import { routeBetween } from '../engine/router';
import { AMS_COURIER_START, AMS_HOMES, AMS_STORE, AMS_TO_STORE } from './amsterdam';

export { projection } from '../view/grid';

export type DestinationPreset = 'near' | 'medium' | 'far';

export const STORE: Place = placeOnStreet('store', 'Food Hub', -2, -2, -1);

export const DESTINATIONS: Record<DestinationPreset, Place> = {
  near: placeOnStreet('home', 'Home', -1, 0, -1),
  medium: placeOnStreet('home', 'Home', 1, 1, -1),
  far: placeOnStreet('home', 'Home', 2, 3, -1),
};

/** Where the courier starts the shift. */
export const COURIER_START: Vec2 = { x: 1 * BLOCK + BLOCK / 2, z: -BLOCK };

/** Where the simulated delivery happens: the places and the two legs, in the world's local metres. */
export interface DevWorld {
  projection: GeoProjection;
  storeName: string;
  storeLocation: LatLng;
  homeLocation(p: DestinationPreset): LatLng;
  /** Courier start to the store's stop. */
  toStore(): Vec2[];
  /** Store's stop to the home's stop. */
  toHome(p: DestinationPreset): Vec2[];
}

/** The prototype's procedural grid city (no tiles). */
export const GRID_WORLD: DevWorld = {
  projection: gridProjection,
  storeName: STORE.label,
  storeLocation: gridProjection.toLatLng(STORE.stop),
  homeLocation: (p) => gridProjection.toLatLng(DESTINATIONS[p].stop),
  toStore: () => routeBetween(COURIER_START, STORE.stop, 'a'),
  toHome: (p) => routeBetween(STORE.stop, DESTINATIONS[p].stop, 'b'),
};

/** A real Amsterdam delivery on the tile fixture (routes follow real streets, see amsterdam.ts). */
export function amsterdamWorld(): DevWorld {
  const projection = new GeoProjection(AMS_STORE.location);
  const local = (pts: number[][]) => pts.map((p) => projection.toLocal({ lat: p[0]!, lng: p[1]! }));
  return {
    projection,
    storeName: AMS_STORE.name,
    storeLocation: AMS_STORE.location,
    homeLocation: (p) => AMS_HOMES[p].location,
    toStore: () => {
      const start = projection.toLocal(AMS_COURIER_START);
      const leg = local(AMS_TO_STORE);
      return Math.hypot(leg[0]!.x - start.x, leg[0]!.z - start.z) > 0.5 ? [start, ...leg] : leg;
    },
    toHome: (p) => local(AMS_HOMES[p].route),
  };
}
