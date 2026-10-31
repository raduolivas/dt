import { randomInt, randomUUID } from 'node:crypto';
import type { BrowserContext } from '@playwright/test';

// The local stack as the tests see it (make dev; the cdn container serves make web-assets' output).
export const ORIGIN = 'http://localhost:8080';
export const API = `${ORIGIN}/v1`;
export const CDN = 'http://localhost:8090';
/** The SDK demo page at the origin the test tenant lets frame the hosted page, and at one it does not. */
export const EMBED_ORIGIN = 'https://localhost:5180';
export const OTHER_ORIGIN = 'https://localhost:5181';
/** The SDK demo page over plain http, for the SDK test (WebKit does not let an https page load http://localhost). */
export const DEMO_HTTP = 'http://localhost:5182';
/** The stack behind one TLS origin (serve-demo.mjs), for the hosted page inside an https embedding page. */
export const FRONT = 'https://localhost:8443';
/** A link URL served through the TLS front. */
export const viaFront = (url: string): string => url.replace(ORIGIN, FRONT);

export type Model = 'direct' | 'end_to_end';
export type Status = 'confirmed' | 'courier_assigned' | 'at_pickup' | 'picked_up' | 'arriving' | 'delivered';

export interface Delivery {
  id: string;
  token: string;
  /** The hosted page: <TRACKING_URL_BASE>/t/<token>. */
  url: string;
}

const STORE = { location: { lat: 52.3731, lng: 4.8922 }, name: 'E2E Kitchen' };
const DOOR = { location: { lat: 52.36, lng: 4.8852 }, label: 'E2E Door' };

function key(): string {
  const k = process.env.DT_E2E_KEY;
  if (!k) throw new Error('DT_E2E_KEY is not set (the global setup creates it)');
  return k;
}

async function call(
  method: string,
  path: string,
  body?: unknown,
): Promise<{ status: number; json: Record<string, unknown> }> {
  const res = await fetch(`${API}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${key()}`,
      'Idempotency-Key': `e2e-${randomUUID()}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  return { status: res.status, json: text ? (JSON.parse(text) as Record<string, unknown>) : {} };
}

/** A new sandbox delivery in Amsterdam (inside the tile fixture), with its tracking link. */
export async function createDelivery(model: Model): Promise<Delivery> {
  // the tenant's settings reach deliveries through NATS a moment after the global setup wrote them
  const deadline = Date.now() + 15_000;
  for (;;) {
    const r = await call('POST', '/deliveries', {
      external_id: `e2e-${randomUUID()}`,
      delivery_model: model,
      pickup: STORE,
      dropoff: DOOR,
      customer: { first_name: 'Ana' },
    });
    const tracking = r.json.tracking as { token: string; url: string } | null | undefined;
    if (r.status === 201 && tracking)
      return { id: r.json.id as string, token: tracking.token, url: tracking.url };
    if (Date.now() > deadline) throw new Error(`POST /deliveries: ${r.status} ${JSON.stringify(r.json)}`);
    await new Promise((res) => setTimeout(res, 500));
  }
}

/** Starts the sandbox simulator at its top speed (20x). */
export async function simulate(id: string): Promise<void> {
  const r = await call('POST', `/test/deliveries/${id}/simulate`, { speed_factor: 20 });
  if (r.status !== 202) throw new Error(`simulate: ${r.status} ${JSON.stringify(r.json)}`);
}

export async function status(id: string): Promise<string> {
  const r = await call('GET', `/deliveries/${id}`);
  if (r.status !== 200) throw new Error(`GET /deliveries/${id}: ${r.status}`);
  return r.json.status as string;
}

/** Rotates the tracking link: the old token stops working. */
export async function rotateLink(id: string): Promise<void> {
  const r = await call('POST', `/deliveries/${id}/tracking-link/rotate`);
  if (r.status !== 200) throw new Error(`rotate: ${r.status} ${JSON.stringify(r.json)}`);
}

/** Waits until a new delivery is known to tracking (the first sessions can answer 503 for a moment). */
export async function waitTrackable(token: string): Promise<void> {
  const deadline = Date.now() + 10_000;
  for (;;) {
    const res = await fetch(`${API}/tracking/session`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Forwarded-For': testClientIp() },
      body: JSON.stringify({ token }),
    });
    if (res.status === 200) return;
    if (res.status !== 503 || Date.now() > deadline) throw new Error(`POST /tracking/session: ${res.status}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

/** An address from 198.18.0.0/15 (benchmark testing), one per call. */
export function testClientIp(): string {
  return `198.${18 + randomInt(2)}.${randomInt(256)}.${1 + randomInt(254)}`;
}

/**
 * Gives the context its own client address on the viewer endpoints of the gateway (the page shell and the session
 * exchange are rate limited per client IP, 10 a minute). The stack's gateway trusts the edge proxy's
 * X-Forwarded-For, so each test counts as a different customer instead of all of them sharing one address.
 */
export async function ownClientIp(context: BrowserContext): Promise<void> {
  const ip = testClientIp();
  await context.route(
    /^(http:\/\/localhost:8080|https:\/\/localhost:8443)\/(t\/|v1\/tracking\/session)/,
    (route) => route.continue({ headers: { ...route.request().headers(), 'x-forwarded-for': ip } }),
  );
}
