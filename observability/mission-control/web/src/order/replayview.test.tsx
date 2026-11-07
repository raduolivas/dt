import { act, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '../api/client';
import type { McEvent, OrderHit, OrderResponse, StatusStep } from '../api/types';
import { Cockpit } from '../layout/Cockpit';
import { store } from '../state/store';
import { mkEvent } from '../test-utils';
import { activeFindings, stepsAt } from './atTime';
import { LeftPanel } from './OrderPanel';

const T0 = 1_700_000_000_000;
const iso = (ms: number) => new Date(ms).toISOString();
const hit: OrderHit = { tenantId: 'acme', environment: 'prod', orderRef: 'A-1', deliveryId: 'd-1', origin: 'live', createdAt: iso(T0) };
const fake = (over: Partial<Client>): Client => over as Client;
const finding = (id: number, ms: number, over: Partial<McEvent> = {}) =>
  mkEvent(id, { kind: 'detector', source: 'mc', target: 'courier', attrs: { rule: 'courier_stale' }, outcome: 'warning', reason: 'no signal', occurredAt: iso(ms), observedAt: iso(ms), ...over });
const lifecycle = (id: number, ms: number, type: string, over: Partial<McEvent> = {}) =>
  mkEvent(id, { kind: 'message_consumed', occurredAt: iso(ms), observedAt: iso(ms), attrs: { event_type: type }, ...over });

const steps: StatusStep[] = [
  { status: 'confirmed', at: iso(T0), state: 'done' },
  { status: 'courier_assigned', at: iso(T0 + 60_000), state: 'done' },
  { status: 'at_pickup', at: null, state: 'pending' },
];
const marks = (s: StatusStep[]) => s.map((x) => `${x.state}:${x.status}`);

beforeEach(() => store.reset());

describe('stepsAt (R14 amended, evaluated at the cursor)', () => {
  it('a step reached after the cursor is pending', () => {
    expect(marks(stepsAt(steps, [], [], T0 + 30_000))).toEqual(['done:confirmed', 'pending:courier_assigned', 'pending:at_pickup']);
    expect(marks(stepsAt(steps, [], [], T0 + 90_000))).toEqual(['done:confirmed', 'done:courier_assigned', 'pending:at_pickup']);
  });

  it('warning only while the newest lost/restored at the cursor is a loss', () => {
    const evs = [lifecycle(1, T0 + 70_000, 'tracking.courier_signal_lost'), lifecycle(2, T0 + 100_000, 'tracking.courier_signal_restored')];
    expect(marks(stepsAt(steps, evs, [], T0 + 65_000))[1]).toBe('done:courier_assigned');
    expect(marks(stepsAt(steps, evs, [], T0 + 80_000))[1]).toBe('warning:courier_assigned');
    expect(marks(stepsAt(steps, evs, [], T0 + 110_000))[1]).toBe('done:courier_assigned');
  });

  it('a courier_stale finding warns until cleared or a later accepted fix', () => {
    const f = finding(5, T0 + 80_000);
    expect(marks(stepsAt(steps, [], [f], T0 + 70_000))[1]).toBe('done:courier_assigned'); // finding not yet there
    expect(marks(stepsAt(steps, [], [f], T0 + 90_000))[1]).toBe('warning:courier_assigned');
    const cleared = finding(6, T0 + 95_000, { outcome: 'cleared' });
    expect(marks(stepsAt(steps, [], [f, cleared], T0 + 99_000))[1]).toBe('done:courier_assigned');
    const ingest = mkEvent(7, { kind: 'request', traceId: 'tr', occurredAt: iso(T0 + 85_000), attrs: { span_name: 'tracking.ingest', 'tracking.fixes.accepted': 2 } });
    const apply = mkEvent(8, { kind: 'state_change', outcome: 'ok', traceId: 'tr', deliveryId: 'd-1', occurredAt: iso(T0 + 85_000), attrs: { span_name: 'tracking.apply' } });
    expect(marks(stepsAt(steps, [ingest, apply], [f], T0 + 90_000))[1]).toBe('done:courier_assigned'); // fix newer than the finding
    expect(marks(stepsAt(steps, [ingest, apply], [f], T0 + 82_000))[1]).toBe('warning:courier_assigned'); // fix not yet happened
  });

  it('no warning once the delivery ended; cancelled shows only when reached', () => {
    const withEnd = [...steps, { status: 'cancelled', at: iso(T0 + 120_000), state: 'done' as const }];
    const lost = [lifecycle(1, T0 + 70_000, 'tracking.courier_signal_lost')];
    expect(marks(stepsAt(withEnd, lost, [], T0 + 100_000))).toEqual(['done:confirmed', 'warning:courier_assigned', 'pending:at_pickup']);
    expect(marks(stepsAt(withEnd, lost, [], T0 + 130_000))).toEqual(['done:confirmed', 'done:courier_assigned', 'pending:at_pickup', 'done:cancelled']);
  });
});

describe('activeFindings', () => {
  it('drops findings after the cursor and findings cleared by it', () => {
    const f = finding(5, T0 + 80_000);
    const cleared = finding(6, T0 + 95_000, { outcome: 'cleared' });
    expect(activeFindings([f, cleared], T0 + 70_000)).toEqual([]);
    expect(activeFindings([f, cleared], T0 + 90_000)).toEqual([f]);
    expect(activeFindings([f, cleared], T0 + 100_000)).toEqual([]);
    expect(activeFindings([f, cleared], null)).toEqual([]);
    expect(activeFindings([f], null)).toEqual([f]);
  });
});

describe('activeFindings identity', () => {
  it('a clear for one delivery does not hide another delivery\'s open finding', () => {
    const base = { attrs: { rule: 'courier_stale' }, target: 'courier', reason: 'no_accepted_fix' };
    const a = finding(1, T0 + 1000, { ...base, deliveryId: 'd-1' });
    const b = finding(2, T0 + 2000, { ...base, deliveryId: 'd-2' });
    const clearA = finding(3, T0 + 3000, { ...base, deliveryId: 'd-1', outcome: 'cleared' });
    expect(activeFindings([a, b, clearA], null)).toEqual([b]);
  });
});

describe('LeftPanel in replay', () => {
  const client = (): Client =>
    fake({
      order: vi.fn(async (): Promise<OrderResponse> => ({ order: hit, statusSteps: steps, detectors: [finding(5, T0 + 80_000)] })),
    });

  it('shows only what is known at the cursor', async () => {
    store.selectOrder('d-1');
    store.setMode('replay');
    store.setReplayAt(T0 + 30_000);
    const { unmount } = render(<LeftPanel client={client()} />);
    const items = await screen.findAllByRole('listitem');
    expect(items[0]).toHaveTextContent('✓ confirmed');
    expect(items[1]).toHaveTextContent('○ courier_assigned');
    expect(screen.queryByText('courier')).toBeNull();
    unmount();

    store.setReplayAt(T0 + 90_000);
    render(<LeftPanel client={client()} />);
    await screen.findByText('courier');
    expect(screen.getAllByRole('listitem')[1]).toHaveTextContent('▲ courier_assigned');
  });
});

describe('top bar agrees with the panel', () => {
  const sys = fake({
    system: vi.fn(async () => ({ traffic: [], recentOrders: [], warnings: [finding(3, T0 + 80_000)] })),
  });
  const ui = () => render(<Cockpit left={<LeftPanel client={sys} />} />);

  it('warning count follows the cursor in replay and matches live', async () => {
    ui();
    await screen.findByText('courier');
    expect(screen.getByText('1 warning')).toBeInTheDocument();
    act(() => {
      store.setMode('replay');
      store.setReplayAt(T0 + 70_000);
    });
    await waitFor(() => expect(screen.getByText('0 warnings')).toBeInTheDocument());
    expect(screen.queryByText('courier')).toBeNull();
    act(() => store.setReplayAt(T0 + 90_000));
    expect(screen.getByText('1 warning')).toBeInTheDocument();
  });

  it('SIMULATED badge looks only at events up to the cursor', () => {
    store.addEvents([mkEvent(1, { occurredAt: iso(T0) }), mkEvent(2, { occurredAt: iso(T0 + 10_000), origin: 'simulated' })]);
    ui();
    expect(screen.getByText('SIMULATED')).toBeInTheDocument();
    act(() => {
      store.setMode('replay');
      store.setReplayAt(T0 + 5_000);
    });
    expect(screen.queryByText('SIMULATED')).toBeNull();
    act(() => store.setReplayAt(T0 + 10_000));
    expect(screen.getByText('SIMULATED')).toBeInTheDocument();
  });
});
