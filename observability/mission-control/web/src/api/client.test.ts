import { describe, expect, it } from 'vitest';
import { createClient } from './client';

function fake(reply: unknown = {}, status = 200) {
  const urls: string[] = [];
  const f = (async (u: string) => {
    urls.push(u);
    return new Response(JSON.stringify(reply), { status });
  }) as unknown as typeof fetch;
  return { urls, api: createClient(f) };
}

describe('client', () => {
  it('builds one URL per endpoint and encodes path segments and params', async () => {
    const { urls, api } = fake();
    await api.topology();
    await api.searchOrders('ORD 1&x');
    await api.order('del/1');
    await api.timeline('del_1', { after: 0, limit: 500 });
    await api.component('tracking.state', 'del_1');
    await api.componentLogs('tracking', { deliveryId: 'del_1', level: 'warn', after: 3, limit: 50 });
    await api.event(42);
    await api.system();
    await api.self();
    expect(urls).toEqual([
      '/api/topology',
      '/api/orders?q=ORD%201%26x',
      '/api/orders/del%2F1',
      '/api/orders/del_1/timeline?after=0&limit=500',
      '/api/components/tracking.state?delivery_id=del_1',
      '/api/components/tracking/logs?delivery_id=del_1&level=warn&after=3&limit=50',
      '/api/events/42',
      '/api/system',
      '/api/self',
    ]);
  });

  it('throws an ApiError carrying the status and the server message on non-2xx', async () => {
    const { api } = fake({ error: 'order not found' }, 404);
    await expect(api.order('x')).rejects.toMatchObject({ status: 404, message: 'order not found' });
  });

  it('allTimeline follows next from after=0 until it is 0 and merges gaps', async () => {
    const pages = [
      { events: [{ id: 1 }, { id: 2 }], next: 2, gaps: [{ kind: 'mc_down' }] },
      { events: [{ id: 3 }], next: 0, gaps: [{ kind: 'mc_down' }] },
    ];
    const urls: string[] = [];
    const f = (async (u: string) => {
      urls.push(u);
      return new Response(JSON.stringify(pages.shift()));
    }) as unknown as typeof fetch;
    const r = await createClient(f).allTimeline('d');
    expect(urls[0]).toContain('after=0');
    expect(urls[1]).toContain('after=2');
    expect(r.events.map((e) => e.id)).toEqual([1, 2, 3]);
    expect(r.gaps).toHaveLength(1);
  });
});
