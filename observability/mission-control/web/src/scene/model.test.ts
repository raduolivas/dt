import { describe, expect, it } from 'vitest';
import { mkEvent } from '../test-utils';
import { topologyFixture } from './fixture';
import { buildModel, derive } from './model';

const m = buildModel(topologyFixture());

describe('scene model', () => {
  it('shows only top-level components', () => {
    expect(m.cubes.map((c) => c.id)).toEqual([
      'partner', 'courier', 'viewer', 'gateway', 'deliveries', 'tracking', 'accounts', 'webhooks', 'nats', 'redis',
      'pg.deliveries', 'pg.tracking', 'pg.accounts', 'pg.gateway', 'osrm',
    ]);
  });

  it('projects part wires onto one wire per cube pair, without self wires', () => {
    const ids = m.wires.map((w) => w.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids).toContain('courier>gateway');
    expect(ids).toContain('gateway>nats');
    expect(ids).not.toContain('nats>gateway'); // the reverse direction shares the curve
    expect(ids).not.toContain('gateway>gateway');
    expect(m.wires.every((w) => w.a !== w.b)).toBe(true);
    expect(m.wires.find((w) => w.id === 'tracking>redis')?.channels).toEqual(['redis']);
  });

  it('rootOf walks parents and rejects unknown ids', () => {
    expect(m.rootOf('tracking.ingest')).toBe('tracking');
    expect(m.rootOf('nats.TRACKING')).toBe('nats');
    expect(m.rootOf('redis')).toBe('redis');
    expect(m.rootOf('nope')).toBeNull();
    expect(m.rootOf('')).toBeNull();
  });

  it('places events with direction', () => {
    expect(m.place('gateway.courier_api', 'tracking.ingest')).toEqual({ wire: 'gateway>tracking', forward: true, attributed: false });
    expect(m.place('nats.DELIVERIES', 'gateway.consumers')).toEqual({ wire: 'gateway>nats', forward: false, attributed: false });
    expect(m.place('tracking.ingest', 'tracking.state')).toBeNull(); // inside one cube
    expect(m.place('gateway', 'webhooks')).toBeNull(); // no such wire: never invented
    expect(m.place('unknown', 'redis')).toBeNull();
  });

  it('attributes an arrival at an entry part to its only outside wire, marked attributed', () => {
    expect(m.place('gateway.courier_api', 'gateway.courier_api', true)).toEqual({ wire: 'courier>gateway', forward: true, attributed: true });
    expect(m.place('gateway.partner_api', 'gateway.partner_api', true)).toEqual({ wire: 'partner>gateway', forward: true, attributed: true });
    // the hub pushes to viewers over WS/SSE
    expect(m.place('tracking.hub', 'tracking.hub', true)).toEqual({ wire: 'viewer>tracking', forward: false, attributed: true });
    // not without permission, and never for parts without an outside wire
    expect(m.place('gateway.courier_api', 'gateway.courier_api')).toBeNull();
    expect(m.place('tracking.ingest', 'tracking.ingest', true)).toBeNull();
  });

  it('derives activity, evidence and touched parts', () => {
    const t = (ms: number) => new Date(1_700_000_000_000 + ms).toISOString();
    const d = derive(m, [
      mkEvent(1, { source: 'gateway.courier_api', target: 'tracking.ingest', traceId: 'abc', observedAt: t(10) }),
      mkEvent(2, { kind: 'message_published', source: 'tracking', target: 'nats.TRACKING', traceId: '', observedAt: t(20) }),
      mkEvent(3, { source: 'gateway.courier_api', target: 'gateway.courier_api', traceId: 'abc', observedAt: t(30) }),
      mkEvent(4, { kind: 'detector', source: 'mc', target: 'redis', observedAt: t(40) }),
      mkEvent(5, { kind: 'log', source: 'deliveries', target: '', observedAt: t(50) }),
    ]);
    expect(d.evidence.get('gateway>tracking')).toEqual({ measured: 1, inferred: 0 });
    expect(d.evidence.get('tracking>nats')).toEqual({ measured: 0, inferred: 1 }); // no shared trace
    expect(d.evidence.get('courier>gateway')).toEqual({ measured: 0, inferred: 1 }); // attributed, caller not measured
    expect(d.activity.get('tracking')).toBe(1_700_000_000_020);
    expect(d.activity.get('gateway')).toBe(1_700_000_000_030);
    expect(d.activity.get('deliveries')).toBe(1_700_000_000_050);
    expect(d.activity.has('redis')).toBe(false); // detector findings are not activity
    expect(d.activity.get('courier')).toBe(1_700_000_000_030); // an arrival from outside is the caller's activity too
    expect([...d.touched].sort()).toEqual(
      ['courier', 'courier>gateway', 'deliveries', 'gateway', 'gateway>tracking', 'nats', 'tracking', 'tracking>nats'],
    );
  });
});
