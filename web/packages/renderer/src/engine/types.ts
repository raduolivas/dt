/**
 * Public contract of the tracking module.
 *
 * The 3D view and the HUD ONLY consume `TrackingSnapshot`s. A real backend
 * (WebSocket, SSE, polling, Firebase...) just needs to produce the same shape
 * and implement `TrackingSource` — nothing in the view has to change.
 */

export interface LatLng {
  lat: number;
  lng: number;
}

export type OrderStatus =
  | 'requested' // customer placed the order
  | 'accepted' // a courier was assigned
  | 'to_store' // courier is riding to the store
  | 'at_store' // courier is waiting for the order to be prepared
  | 'picked_up' // order handed over to the courier
  | 'on_the_way' // courier is riding to the customer
  | 'arriving' // courier is close to the customer
  | 'delivered';

export interface CourierState {
  position: LatLng;
  /** Compass bearing in degrees, 0 = north, clockwise. */
  heading: number;
  speedKmh: number;
}

export interface TrackingSnapshot {
  orderId: string;
  status: OrderStatus;
  courier: CourierState;
  store: LatLng;
  destination: LatLng;
  /** Planned route, split in the two legs of the delivery. */
  route: {
    toStore: LatLng[];
    toCustomer: LatLng[];
  };
  /** Seconds until delivery, null when unknown. */
  etaSeconds: number | null;
  /** Meters left until the customer's door. */
  distanceRemainingM: number;
  /** 0..1 over the whole delivery. */
  progress: number;
  /** Seconds since the order was requested. */
  elapsedSec: number;
  /** Unix ms timestamp of this snapshot. */
  updatedAt: number;
}

export type SnapshotListener = (snapshot: TrackingSnapshot) => void;

/** Anything that can feed the tracking view: simulator, WebSocket client, etc. */
export interface TrackingSource {
  subscribe(listener: SnapshotListener): () => void;
}

export type DestinationPreset = 'near' | 'medium' | 'far';

export interface SimParams {
  /** Cruising speed of the bicycle. */
  courierSpeedKmh: number;
  /** How long the store needs to prepare the order once the courier arrived. */
  prepTimeSec: number;
  /** Simulated seconds per real second. */
  timeScale: number;
  destination: DestinationPreset;
}

export const DEFAULT_PARAMS: SimParams = {
  courierSpeedKmh: 18,
  prepTimeSec: 25,
  timeScale: 4,
  destination: 'medium',
};
