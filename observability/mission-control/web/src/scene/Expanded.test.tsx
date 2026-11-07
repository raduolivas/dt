import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { store } from '../state/store';
import { mkEvent } from '../test-utils';
import { Breadcrumb } from './Expanded';
import { buildExpanded } from './expand';
import { PulseEngine } from './engine';
import { topologyFixture } from './fixture';
import { buildModel } from './model';
import { highlightWire, logEventId, pulseSelection } from './linking';

beforeEach(() => store.reset());

describe('Breadcrumb', () => {
  it('breadcrumbReturnsToOrder', () => {
    store.selectOrder('d-1');
    store.expand('tracking');
    render(<Breadcrumb label={(id) => id} />);
    expect(screen.getByRole('navigation')).toHaveTextContent('system › order › tracking');
    fireEvent.click(screen.getByRole('button', { name: 'order' }));
    expect(store.getState().expanded).toBeNull();
    expect(store.getState().orderId).toBe('d-1');
  });
  it('without an order the order segment is absent; system goes back', () => {
    store.expand('tracking');
    render(<Breadcrumb label={(id) => id} />);
    expect(screen.getByRole('navigation')).toHaveTextContent('system › tracking');
    expect(screen.queryByRole('button', { name: 'order' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'system' }));
    expect(store.getState().expanded).toBeNull();
  });
  it('renders nothing when not expanded', () => {
    const { container } = render(<Breadcrumb label={(id) => id} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe('buildExpanded', () => {
  const topo = topologyFixture();
  it('holds the parts of one component with their own wires only', () => {
    const x = buildExpanded(topo, 'tracking')!;
    expect(x.model.cubes.map((c) => c.id).sort()).toEqual(
      ['tracking.consumers', 'tracking.hub', 'tracking.ingest', 'tracking.outbox', 'tracking.simulator', 'tracking.state', 'tracking.sweeper'],
    );
    expect(x.model.wires.map((w) => w.id)).toContain('tracking.ingest>tracking.state');
    expect(x.model.wires.every((w) => w.a.startsWith('tracking.') && w.b.startsWith('tracking.'))).toBe(true);
    expect(x.layout.cubes.size).toBe(7);
  });
  it('a component without parts does not expand', () => {
    expect(buildExpanded(topo, 'redis')).toBeNull();
  });
  it('places events between parts only, nothing invented', () => {
    const x = buildExpanded(topo, 'tracking')!;
    expect(x.model.placeEvent(mkEvent(1, { source: 'tracking.ingest', target: 'tracking.state' }))?.wire).toBe('tracking.ingest>tracking.state');
    expect(x.model.placeEvent(mkEvent(2, { source: 'gateway.courier_api', target: 'tracking.ingest' }))).toBeNull();
    expect(x.model.placeEvent(mkEvent(3, { source: 'tracking.ingest', target: 'tracking.ingest' }))).toBeNull();
  });
});

describe('linking', () => {
  const model = buildModel(topologyFixture());
  const req = mkEvent(7, { traceId: 't', spanId: 's', source: 'gateway.courier_api', target: 'tracking.ingest' });
  it('a log resolves to the pulse event with the same span, else to itself', () => {
    const log = mkEvent(9, { kind: 'log', traceId: 't', spanId: 's' });
    expect(logEventId(log, [req, log])).toBe(7);
    expect(logEventId(mkEvent(10, { kind: 'log', traceId: 'x', spanId: 'y' }), [req])).toBe(10);
  });
  it('a pulse selects its event; a pulse without one selects nothing', () => {
    expect(pulseSelection({ eventId: 7 })).toEqual({ kind: 'event', id: '7' });
    expect(pulseSelection({})).toBeNull();
  });
  it('the selected event highlights its wire', () => {
    expect(highlightWire(model, [req], { kind: 'event', id: '7' })).toBe('gateway>tracking');
    expect(highlightWire(model, [req], { kind: 'event', id: '99' })).toBeNull();
    expect(highlightWire(model, [req], { kind: 'wire', id: 'gateway>tracking' })).toBeNull();
  });
});

describe('pulses carry their event', () => {
  const model = buildModel(topologyFixture());
  const NOW = 1_700_000_100_000;
  const at = new Date(NOW).toISOString();
  const ev = (id: number) => mkEvent(id, { observedAt: at, occurredAt: at, source: 'gateway.courier_api', target: 'tracking.ingest' });
  it('a single pulse carries its id; a bundle carries the newest id it bundles', () => {
    const eng = new PulseEngine(model, () => 8);
    eng.ingestEvents([ev(1)], NOW);
    expect(eng.frame(NOW + 100, false)[0].eventId).toBe(1);
    const eng2 = new PulseEngine(model, () => 8);
    eng2.ingestEvents(Array.from({ length: 25 }, (_, i) => ev(100 + i)), NOW);
    const f = eng2.frame(NOW + 100, false);
    expect(f).toHaveLength(1);
    expect(f[0].eventId).toBe(124);
  });
});
