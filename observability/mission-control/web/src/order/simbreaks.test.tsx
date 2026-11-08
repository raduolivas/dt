import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '../api/client';
import type { SimClient, SimRun } from '../api/sim';
import type { OrderHit, OrderResponse } from '../api/types';
import { store } from '../state/store';
import { LeftPanel } from './OrderPanel';

const hit = (origin: 'live' | 'simulated'): OrderHit => ({
  tenantId: 'acme', environment: 'test', orderRef: 'mc-sim-1', deliveryId: 'd-1', origin, createdAt: '2026-11-05T10:00:00Z',
});
const orderClient = (origin: 'live' | 'simulated'): Client =>
  ({
    order: vi.fn(async (): Promise<OrderResponse> => ({ order: hit(origin), statusSteps: [], detectors: [] })),
  }) as unknown as Client;
const simOf = (runs: SimRun[] | null): SimClient => ({
  status: vi.fn(async () => runs),
  breakRun: vi.fn(async () => {}),
  heal: vi.fn(async () => {}),
});

beforeEach(() => {
  store.reset();
  store.selectOrder('d-1');
});
afterEach(() => vi.restoreAllMocks());

describe('SimBreaks', () => {
  it('showsBreakButtonsOnlyWhenSimEnabledAndRunKnown', async () => {
    const sim = simOf([{ delivery_id: 'd-1', order_ref: 'mc-sim-1', scenario: 0, picked_up: true }]);
    render(<LeftPanel client={orderClient('simulated')} sim={sim} />);
    fireEvent.click(await screen.findByRole('button', { name: /3\. Rate limit/ }));
    await waitFor(() => expect(sim.breakRun).toHaveBeenCalledWith('d-1', 3));
    expect(screen.getByText(/make mc-break SCENARIO=5/)).toBeInTheDocument();
  });

  it('hidesBreakButtonsWhenSimIsOff', async () => {
    const sim = simOf(null);
    render(<LeftPanel client={orderClient('simulated')} sim={sim} />);
    await screen.findByText('Detectors');
    await waitFor(() => expect(sim.status).toHaveBeenCalled());
    expect(screen.queryByRole('button', { name: /Rate limit/ })).toBeNull();
  });

  it('hidesBreakButtonsForLiveOrders', async () => {
    const sim = simOf([{ delivery_id: 'd-1', order_ref: 'x', scenario: 0, picked_up: true }]);
    render(<LeftPanel client={orderClient('live')} sim={sim} />);
    await screen.findByText('Detectors');
    expect(screen.queryByRole('button', { name: /Rate limit/ })).toBeNull();
  });
});
