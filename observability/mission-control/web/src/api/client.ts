import type {
  ComponentResponse, EventResponse, LogsResponse, McEvent, OrderHit, OrderResponse, SelfRow, SystemResponse,
  TimelineGap, TimelineResponse, TopologyResponse,
} from './types';

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

export interface Client {
  topology(): Promise<TopologyResponse>;
  searchOrders(q: string): Promise<OrderHit[]>;
  order(deliveryId: string): Promise<OrderResponse>;
  timeline(deliveryId: string, p?: { after?: number; limit?: number }): Promise<TimelineResponse>;
  /** Every page from after=0 (a trace sibling stored before a cursor is only seen that way). */
  allTimeline(deliveryId: string): Promise<{ events: McEvent[]; gaps: TimelineGap[] }>;
  component(id: string, deliveryId?: string): Promise<ComponentResponse>;
  componentLogs(
    id: string,
    p?: { deliveryId?: string; level?: string; after?: number; limit?: number },
  ): Promise<LogsResponse>;
  event(id: number): Promise<EventResponse>;
  system(): Promise<SystemResponse>;
  self(): Promise<SelfRow[]>;
}

const seg = encodeURIComponent;

function qs(params: Record<string, string | number | undefined>): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') parts.push(`${k}=${seg(String(v))}`);
  }
  return parts.length ? '?' + parts.join('&') : '';
}

export function createClient(f: typeof fetch = (...a) => fetch(...a)): Client {
  async function get<T>(path: string): Promise<T> {
    const res = await f(path, { headers: { Accept: 'application/json' } });
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try {
        const body = (await res.json()) as { error?: string };
        if (typeof body.error === 'string') msg = body.error;
      } catch {
        // keep the status text
      }
      throw new ApiError(res.status, msg);
    }
    return (await res.json()) as T;
  }
  const timeline = (id: string, p: { after?: number; limit?: number } = {}) =>
    get<TimelineResponse>(`/api/orders/${seg(id)}/timeline` + qs({ after: p.after, limit: p.limit }));
  return {
    topology: () => get('/api/topology'),
    searchOrders: (q) => get('/api/orders' + qs({ q })),
    order: (id) => get(`/api/orders/${seg(id)}`),
    timeline,
    async allTimeline(id) {
      const events: McEvent[] = [];
      const gaps = new Map<string, TimelineGap>();
      let after = 0;
      for (;;) {
        const page = await timeline(id, { after, limit: 1000 });
        events.push(...page.events);
        for (const g of page.gaps) gaps.set(`${g.kind}|${g.from}|${g.to}`, g);
        if (!page.next || page.next <= after) break;
        after = page.next;
      }
      return { events, gaps: [...gaps.values()] };
    },
    component: (id, deliveryId) => get(`/api/components/${seg(id)}` + qs({ delivery_id: deliveryId })),
    componentLogs: (id, p = {}) =>
      get(`/api/components/${seg(id)}/logs` + qs({ delivery_id: p.deliveryId, level: p.level, after: p.after, limit: p.limit })),
    event: (id) => get(`/api/events/${id}`),
    system: () => get('/api/system'),
    self: () => get('/api/self'),
  };
}

export const api = createClient();
