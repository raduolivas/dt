import { BLOCK, placeOnStreet, type Place } from '../view/grid';
import type { Vec2 } from '../engine/geo';

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
