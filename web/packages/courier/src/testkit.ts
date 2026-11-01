import { vi } from 'vitest';
import type { CourierDelivery, CourierStorage, CourierTokens } from './types';

export const T0 = Date.parse('2026-10-31T10:00:00Z');
export const ACCESS_MS = 15 * 60_000;
export const SESSION_MS = 12 * 3_600_000;

export interface Call {
  method: string;
  path: string;
  body: any; // eslint-disable-line @typescript-eslint/no-explicit-any
  auth: string | null;
}
type Handler = (c: Call) => Response | Promise<Response>;

export const json = (status: number, body?: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
export const problem = (status: number, code: string, headers?: Record<string, string>) =>
  json(status, { type: 'about:blank', title: code, status, code, request_id: 'r' }, headers);

export const delivery = (id = 'del_1', over: Partial<CourierDelivery> = {}): CourierDelivery =>
  ({
    id,
    reference: 'ref-' + id,
    model: 'direct',
    status: 'courier_assigned',
    ready_at: null,
    pickup: { lat: 52.37, lng: 4.9, label: 'Store' },
    dropoff: { lat: 52.38, lng: 4.91, label: null, notes: null },
    items_summary: null,
    item_count: null,
    pickup_confirmations: [],
    next_actions: ['at_pickup'],
    ...over,
  }) as CourierDelivery;

/** A scripted API. Default answers are the happy path; `handle` overrides one route, `offline` makes fetch throw. */
export function fakeApi() {
  const calls: Call[] = [];
  const overrides = new Map<string, Handler[]>();
  const api = {
    calls,
    offline: false,
    deliveries: [delivery()] as CourierDelivery[],
    tokenSeq: 0,
    /** event_id -> the delivery as it was answered: the server's duplicate detection (the real gateway, M5-R16). */
    applied: new Map<string, CourierDelivery>(),
    /**
     * What the events route does: a known event_id is `duplicate` whatever the delivery's status now; a new one is
     * applied, and a final one (delivered, failed) takes the delivery off the courier's list, as the real listing does.
     */
    event(call: Call): Response {
      const prev = api.applied.get(call.body.event_id);
      if (prev) return json(200, { outcome: 'duplicate', delivery: prev });
      const id = call.path.split('/')[3];
      const final = call.body.type === 'delivered' || call.body.type === 'failed';
      const d = delivery(id, final ? { status: call.body.type, next_actions: [] } : { status: 'at_pickup' });
      api.applied.set(call.body.event_id, d);
      if (final) api.deliveries = api.deliveries.filter((x) => x.id !== id);
      return json(200, { outcome: 'applied', delivery: d });
    },
    sessionEnd: T0 + SESSION_MS,
    /** Queue a one-shot answer for "METHOD /path" (the path without query or ids is matched by prefix). */
    handle(route: string, h: Handler) {
      overrides.set(route, [...(overrides.get(route) ?? []), h]);
    },
    tokens(): CourierTokens {
      const n = ++api.tokenSeq;
      return {
        access_token: `access-${n}`,
        access_expires_at: new Date(Math.min(Date.now() + ACCESS_MS, api.sessionEnd)).toISOString(),
        refresh_token: `refresh-${n}`,
        session_expires_at: new Date(api.sessionEnd).toISOString(),
      };
    },
    paths: (prefix: string) => calls.filter((c) => c.path.startsWith(prefix)),
    fetch: (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (api.offline) throw new TypeError('network');
      const path = new URL(String(input)).pathname.replace(/^\/v1/, '');
      const headers = (init?.headers ?? {}) as Record<string, string>;
      const call: Call = {
        method: init?.method ?? 'GET',
        path,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
        auth: headers.Authorization ?? null,
      };
      calls.push(call);
      const route = `${call.method} ${path.replace(/^(\/courier\/deliveries)\/[^/]+/, '$1/:id')}`;
      const queued = overrides.get(route);
      if (queued?.length) {
        // like a real fetch, an aborted request rejects even when the server never answers
        const signal = init?.signal;
        const answer = Promise.resolve(queued.shift()!(call));
        if (!signal) return answer;
        return Promise.race([
          answer,
          new Promise<Response>((_, reject) => {
            if (signal.aborted) reject(signal.reason);
            signal.addEventListener('abort', () => reject(signal.reason));
          }),
        ]);
      }
      switch (route) {
        case 'POST /courier/token':
          return json(200, api.tokens());
        case 'GET /courier/deliveries':
          return json(200, { deliveries: api.deliveries });
        case 'POST /courier/locations':
          return json(200, { accepted: call.body.fixes.length, dropped: 0 });
        case 'POST /courier/deliveries/:id/events':
          return api.event(call);
        case 'POST /courier/session/end':
          return json(204);
        default:
          return problem(404, 'not_found');
      }
    }) as typeof fetch,
  };
  return api;
}

export function fakeGeolocation() {
  let cb: PositionCallback | null = null;
  let errCb: PositionErrorCallback | null = null;
  const g = {
    watches: 0,
    clears: 0,
    asks: 0,
    /** What getCurrentPosition answers: a good fix, or a permission error. */
    askAnswer: 'fix' as 'fix' | 'denied',
    get watching() {
      return cb !== null;
    },
    geo: {
      watchPosition: (ok: PositionCallback, err?: PositionErrorCallback | null) => {
        g.watches++;
        cb = ok;
        errCb = err ?? null;
        return g.watches;
      },
      getCurrentPosition: (ok: PositionCallback, err?: PositionErrorCallback | null) => {
        g.asks++;
        if (g.askAnswer === 'denied') err?.({ code: 1 } as GeolocationPositionError);
        else
          ok({
            timestamp: Date.now(),
            coords: { latitude: 1, longitude: 1, accuracy: 5, speed: null, heading: null },
          } as unknown as GeolocationPosition);
      },
      clearWatch: () => {
        g.clears++;
        cb = null;
      },
    } as unknown as Geolocation,
    /** A reading taken now, unless `at` says otherwise. */
    fix(accuracy: number, at = Date.now(), lat = 52.37) {
      cb?.({
        timestamp: at,
        coords: { latitude: lat, longitude: 4.9, accuracy, speed: null, heading: null },
      } as unknown as GeolocationPosition);
    },
    deny() {
      errCb?.({ code: 1 } as GeolocationPositionError);
    },
  };
  return g;
}

export function memoryStorage(): CourierStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  };
}

export const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);
