import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { DOOR, STORE, call, type Delivery } from './stack';

// The courier side of the local stack: a courier, a direct delivery assigned to it and a link-mode session, the way
// scripts/courier-link.sh sets them up (the tenant of the global setup allows direct deliveries and pickup "any").

export interface CourierRide {
  delivery: Delivery;
  courierId: string;
  sessionId: string;
  /** The one-use courier link: <COURIER_PAGE_BASE>/s/<code>. */
  link: string;
}

async function must(
  want: number[],
  method: string,
  path: string,
  body?: unknown,
): Promise<Record<string, unknown>> {
  const r = await call(method, path, body);
  if (!want.includes(r.status)) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.json)}`);
  return r.json;
}

/** A courier, a direct delivery in Amsterdam assigned to it, and a link session for the courier page. */
export async function createRide(): Promise<CourierRide> {
  const courier = await must([200, 201], 'PUT', `/couriers/e2e-${randomUUID()}`, {
    display_name: 'E2E Courier',
    vehicle: 'e_bike',
  });
  const courierId = courier.id as string;

  // the tenant's settings reach deliveries through NATS a moment after the global setup wrote them
  const deadline = Date.now() + 15_000;
  let created: Record<string, unknown> | undefined;
  for (;;) {
    const r = await call('POST', '/deliveries', {
      external_id: `e2e-${randomUUID()}`,
      delivery_model: 'direct',
      pickup: STORE,
      dropoff: DOOR,
      customer: { first_name: 'Ana' },
    });
    if (r.status === 201 && r.json.tracking) {
      created = r.json;
      break;
    }
    if (Date.now() > deadline) throw new Error(`POST /deliveries: ${r.status} ${JSON.stringify(r.json)}`);
    await new Promise((res) => setTimeout(res, 500));
  }
  const tracking = created.tracking as { token: string; url: string };
  const id = created.id as string;

  // direct assignment leaves the delivery `confirmed`
  await must([200], 'POST', `/deliveries/${id}/assignment`, { courier_id: courierId });
  const session = await must([201], 'POST', '/courier-sessions', { courier_id: courierId, mode: 'link' });
  const s = session.session as { id: string };
  return {
    delivery: { id, token: tracking.token, url: tracking.url },
    courierId,
    sessionId: s.id,
    link: session.link_url as string,
  };
}

/** Revokes the session (the courier's next call is 401). */
export async function revokeSession(sessionId: string): Promise<void> {
  await must([200], 'DELETE', `/courier-sessions/${sessionId}`);
}

/** The fixture route from the pickup to the door (fixtures/look.json), thinned to `n` points. */
export function route(n: number): { latitude: number; longitude: number }[] {
  const fx = JSON.parse(readFileSync(resolve(import.meta.dirname, '../fixtures/look.json'), 'utf8')) as {
    preparing: { route: { to_dropoff: [number, number][] } };
  };
  const pts = fx.preparing.route.to_dropoff;
  return Array.from({ length: n }, (_, i) => {
    const [latitude, longitude] = pts[Math.round((i * (pts.length - 1)) / (n - 1))];
    return { latitude, longitude };
  });
}
