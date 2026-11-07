import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '../api/client';
import type { OrderHit, OrderResponse, SystemResponse } from '../api/types';
import { store } from '../state/store';
import { mkEvent } from '../test-utils';
import { LeftPanel } from './OrderPanel';
import { eventsAt } from './replay';
import { Search } from './Search';
import { Timeline } from './Timeline';

const hit = (over: Partial<OrderHit> = {}): OrderHit => ({
  tenantId: 'acme', environment: 'prod', orderRef: 'A-1', deliveryId: 'd-1', origin: 'live',
  createdAt: '2026-11-05T10:00:00Z', ...over,
});
const fake = (over: Partial<Client>): Client => over as Client;
const orderResp = (over: Partial<OrderResponse> = {}): OrderResponse => ({
  order: hit(),
  statusSteps: [
    { status: 'confirmed', at: '2026-11-05T10:00:00Z', state: 'done' },
    { status: 'courier_assigned', at: '2026-11-05T10:01:00Z', state: 'warning' },
    { status: 'at_pickup', at: null, state: 'pending' },
  ],
  detectors: [],
  ...over,
});
const _emptySystem: SystemResponse = { traffic: [], recentOrders: [], warnings: [] };
void _emptySystem;

beforeEach(() => store.reset());
afterEach(() => vi.restoreAllMocks());

describe('Search', () => {
  it('searchShowsPickerForAmbiguous', async () => {
    const hits = [hit({ deliveryId: 'd-1', tenantId: 'acme' }), hit({ deliveryId: 'd-2', tenantId: 'globex', environment: 'staging' })];
    const client = fake({ searchOrders: vi.fn(async () => hits) });
    render(<Search client={client} />);
    fireEvent.change(screen.getByLabelText(/search orders/i), { target: { value: 'A-1' } });
    fireEvent.submit(screen.getByRole('search'));
    const items = await screen.findAllByRole('option');
    expect(items).toHaveLength(2);
    expect(items[1]).toHaveTextContent('globex · staging ·');
    expect(store.getState().orderId).toBeNull();
    fireEvent.click(items[1]);
    expect(store.getState().orderId).toBe('d-2');
    expect(screen.queryAllByRole('option')).toHaveLength(0);
  });

  it('a single hit opens the order, none says so', async () => {
    const searchOrders = vi.fn(async (): Promise<OrderHit[]> => [hit({ deliveryId: 'd-9' })]);
    render(<Search client={fake({ searchOrders })} />);
    fireEvent.change(screen.getByLabelText(/search orders/i), { target: { value: 'x' } });
    fireEvent.submit(screen.getByRole('search'));
    await waitFor(() => expect(store.getState().orderId).toBe('d-9'));
    searchOrders.mockResolvedValueOnce([]);
    store.selectOrder(null);
    fireEvent.submit(screen.getByRole('search'));
    expect(await screen.findByText(/no order found/i)).toBeInTheDocument();
    expect(store.getState().orderId).toBeNull();
  });
});

describe('LeftPanel', () => {
  it('rendersPartnerStringsAsText', async () => {
    const evil = '<img src=x onerror=alert(1)>';
    store.selectOrder('d-1');
    const client = fake({
      order: vi.fn(async () =>
        orderResp({
          order: hit({ orderRef: evil, tenantId: evil }),
          detectors: [mkEvent(5, { kind: 'detector', reason: evil, source: 'courier_stale' })],
        }),
      ),
    });
    const { container } = render(<LeftPanel client={client} />);
    await waitFor(() => expect(container.textContent).toContain(evil));
    expect(container.querySelector('img')).toBeNull();
    expect(document.querySelector('img')).toBeNull();
  });

  it('simulatedBadgeFromOrigin', async () => {
    store.selectOrder('d-1');
    const order = vi.fn(async () => orderResp({ order: hit({ origin: 'simulated' }) }));
    const { unmount } = render(<LeftPanel client={fake({ order })} />);
    expect(await screen.findByText('SIMULATED')).toBeInTheDocument();
    unmount();
    order.mockResolvedValue(orderResp({ order: hit({ origin: 'live' }) }));
    render(<LeftPanel client={fake({ order })} />);
    await screen.findByText('acme');
    expect(screen.queryByText('SIMULATED')).toBeNull();
  });

  it('shows done, warning, pending steps and the failed/cancelled ones', async () => {
    store.selectOrder('d-1');
    const client = fake({
      order: vi.fn(async () =>
        orderResp({
          statusSteps: [
            { status: 'confirmed', at: 'x', state: 'done' },
            { status: 'courier_assigned', at: 'x', state: 'warning' },
            { status: 'at_pickup', at: null, state: 'pending' },
            { status: 'cancelled', at: 'x', state: 'done' },
          ],
        }),
      ),
    });
    render(<LeftPanel client={client} />);
    const steps = await screen.findAllByRole('listitem');
    expect(steps[0]).toHaveTextContent('✓ confirmed');
    expect(steps[1]).toHaveTextContent('▲ courier_assigned');
    expect(steps[2]).toHaveTextContent('○ at_pickup');
    expect(steps[3]).toHaveTextContent('✓ cancelled');
  });

  it('shows the system view with no order: traffic, recent orders, warnings', async () => {
    const client = fake({
      system: vi.fn(async () => ({
        traffic: [{ wire: 'gateway>tracking', count: 40, failed: 2, rate: 1.5, errorRate: 0.05, p95Ms: 120 }],
        recentOrders: [hit({ orderRef: 'R-7', deliveryId: 'd-7' })],
        warnings: [mkEvent(3, { kind: 'detector', source: 'courier_stale', reason: 'no signal' })],
      })),
    });
    render(<LeftPanel client={client} />);
    expect(await screen.findByText('gateway>tracking')).toBeInTheDocument();
    expect(screen.getByText('no signal', { exact: false })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /R-7/ }));
    expect(store.getState().orderId).toBe('d-7');
  });
});

describe('Timeline', () => {
  it('gapBandsLabelled', () => {
    store.addEvents([mkEvent(1), mkEvent(2)]);
    const t0 = new Date(1_700_000_000_000).toISOString();
    const t1 = new Date(1_700_000_000_001).toISOString();
    store.setTimelineGaps([
      { from: t0, to: t1, kind: 'no_trace' },
      { from: t0, to: t1, kind: 'mc_down' },
    ]);
    render(<Timeline />);
    expect(screen.getByText('no trace data')).toBeInTheDocument();
    expect(screen.getByText('Mission Control was not running')).toBeInTheDocument();
  });

  it('colours ticks by outcome', () => {
    store.addEvents([mkEvent(1, { outcome: 'ok' }), mkEvent(2, { outcome: 'failed' }), mkEvent(3, { outcome: '' })]);
    const { container } = render(<Timeline />);
    const ticks = [...container.querySelectorAll('.mc-tick')];
    expect(ticks.map((t) => [...t.classList].find((c) => c.startsWith('is-')))).toEqual(['is-ok', 'is-fail', 'is-unknown']);
  });

  it('dragging enters replay with a banner and the cursor time', () => {
    store.addEvents([mkEvent(0), mkEvent(1000)]);
    const { container } = render(<Timeline />);
    const track = container.querySelector('.mc-track') as HTMLElement;
    track.getBoundingClientRect = () => ({ left: 0, width: 100, top: 0, height: 10, right: 100, bottom: 10, x: 0, y: 0, toJSON: () => ({}) });
    expect(screen.queryByText(/replay/i)).toBeNull();
    fireEvent.pointerDown(track, { clientX: 50, pointerId: 1 });
    expect(store.getState().mode).toBe('replay');
    expect(store.getState().replayAt).toBe(1_700_000_000_500);
    expect(screen.getByRole('status')).toHaveTextContent(/REPLAY/);
    fireEvent.pointerMove(track, { clientX: 100, pointerId: 1 });
    expect(store.getState().replayAt).toBe(1_700_000_001_000);
    act(() => store.setMode('live'));
    expect(store.getState().replayAt).toBeNull();
    expect(screen.queryByRole('status')).toBeNull();
  });
});

describe('replay', () => {
  it('replayFiltersByCursor', () => {
    const buf = [mkEvent(1), mkEvent(5), mkEvent(9)];
    const t = Date.parse(buf[1].occurredAt);
    expect(eventsAt(buf, t).map((e) => e.id)).toEqual([1, 5]);
    expect(eventsAt(buf, t - 10)).toEqual([]);
    expect(eventsAt(buf, Date.parse(buf[2].occurredAt))).toHaveLength(3);
    expect(buf).toHaveLength(3); // input untouched
  });
});
