import type { DeliveryStatus, LatLng, TrackingSnapshot } from '@dt/contract';

export type ViewKind = 'preparing' | 'live' | 'final';

const FINAL: ReadonlySet<DeliveryStatus> = new Set(['delivered', 'failed', 'cancelled']);
const BEFORE_PICKUP: ReadonlySet<DeliveryStatus> = new Set(['confirmed', 'courier_assigned', 'at_pickup']);

/** The wire format sends route points as `[lat, lng]`. */
export function toLatLng(p: readonly number[]): LatLng {
  return { lat: p[0]!, lng: p[1]! };
}

export function isFinal(status: DeliveryStatus): boolean {
  return FINAL.has(status);
}

/** The leg the courier is on: to the pickup before `picked_up` when the snapshot has it, else to the dropoff. */
export function activeLeg(s: TrackingSnapshot): LatLng[] {
  const leg = BEFORE_PICKUP.has(s.status) && s.route.to_pickup ? s.route.to_pickup : s.route.to_dropoff;
  return leg.map(toLatLng);
}

/** Which view the snapshot calls for. The server decides visibility: no courier in the snapshot means none is drawn. */
export function viewKind(s: TrackingSnapshot): ViewKind {
  if (isFinal(s.status)) return 'final';
  return s.courier === null ? 'preparing' : 'live';
}

/** Ignores a snapshot whose `version` is not greater than the last applied one of the same delivery. */
export class SnapshotGate {
  private deliveryId: string | null = null;
  private last = -Infinity;

  accept(s: TrackingSnapshot): boolean {
    if (s.delivery_id !== this.deliveryId) {
      this.deliveryId = s.delivery_id;
      this.last = -Infinity;
    }
    if (s.version <= this.last) return false;
    this.last = s.version;
    return true;
  }
}
