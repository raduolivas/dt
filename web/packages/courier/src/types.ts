import type { CourierDelivery, CourierTokens, DeliveryEventType, DeliveryEventOutcome } from '@dt/contract';

export type { CourierDelivery, CourierFix, CourierTokens } from '@dt/contract';
export type CourierEventType = DeliveryEventType;

/** The part of `Storage` the SDK uses; `localStorage` fits, and so can an async store (a native app's keychain). */
export interface CourierStorage {
  getItem(key: string): string | null | Promise<string | null>;
  setItem(key: string, value: string): void | Promise<void>;
  removeItem(key: string): void | Promise<void>;
}

export interface CourierOptions {
  /** The API base including the version, e.g. `https://api.example.com/v1`. */
  api: string;
  /** SDK partners: the pair from `POST /courier-sessions`. Ignored when storage holds a newer pair of the same session. */
  tokens?: CourierTokens;
  /** Link page: the code from the link. Exchanged once; a reload resumes from storage instead. */
  linkCode?: string;
  /** Where the token pair lives so a reload keeps the shift. Default: `localStorage`, else memory. */
  storage?: CourierStorage;
  fetch?: typeof fetch;
  geolocation?: Geolocation;
  now?: () => number;
  indexedDB?: IDBFactory;
  /**
   * Web Locks, so two tabs of one session never refresh at once. Default `navigator.locks`; `null` turns it off (the
   * SDK then adopts a pair another tab rotated from the `storage` event, best effort).
   */
  locks?: LockManager | null;
}

export interface CourierState {
  gps: 'off' | 'asking' | 'denied' | 'on';
  network: 'online' | 'offline';
  /** GPS fixes waiting to be sent. */
  buffered: number;
  /** Status taps waiting to be sent. */
  pendingTaps: number;
  session: 'starting' | 'active' | 'ended' | 'expired';
}

export type ReportResult = {
  outcome: DeliveryEventOutcome | 'not_yours';
  delivery?: CourierDelivery;
};
// 'recorded' = stored without a state change (e.g. one confirmation under courier_and_store); 'not_yours' = 404.

/**
 * 'reassigned': a tap got 404. 'ended': the session is over. 'rejected': the server refused a tap that nobody is
 * waiting on (one restored after a reload), so the page can tell the courier it did not count.
 */
export type CourierNotice = { kind: 'reassigned' | 'ended' | 'rejected'; deliveryId?: string; type?: CourierEventType };

export interface Courier {
  start(): Promise<void>;
  end(): Promise<void>;
  readonly deliveries: readonly CourierDelivery[];
  readonly state: CourierState;
  report(deliveryId: string, type: CourierEventType, opts?: { reason?: string }): Promise<ReportResult>;
  on(ev: 'deliveries', fn: (d: readonly CourierDelivery[]) => void): () => void;
  on(ev: 'state', fn: (s: CourierState) => void): () => void;
  on(ev: 'notice', fn: (n: CourierNotice) => void): () => void;
}

/** Why `start()` or `report()` failed. `code` is a problem code (`link_code_used`, `link_code_invalid`) or one of ours. */
export class CourierError extends Error {
  constructor(
    readonly code: 'link_code_used' | 'link_code_invalid' | 'no_session' | 'network' | 'session_ended' | 'rejected' | 'not_started' | string,
    message?: string,
  ) {
    super(message ?? code);
    this.name = 'CourierError';
  }
}
