import { describe, expect, it } from 'vitest';
import { createStore } from '../state/store';
import { mkEvent } from '../test-utils';
import { LIVE_WINDOW_MS, PulseEngine } from './engine';
import { topologyFixture } from './fixture';
import { buildModel } from './model';

const NOW = 1_700_000_100_000;
const iso = (ms: number) => new Date(ms).toISOString();
const ev = (id: number, ms: number, over = {}) =>
  mkEvent(id, { observedAt: iso(ms), occurredAt: iso(ms), source: 'gateway.courier_api', target: 'tracking.ingest', ...over });
const model = buildModel(topologyFixture());

describe('PulseEngine', () => {
  it('one pulse per observed event, travelling at a fixed speed whatever the latency', () => {
    const eng = new PulseEngine(model, (w) => (w === 'gateway>tracking' ? 8 : 16));
    eng.ingestEvents([ev(1, NOW, { attrs: { duration_ms: 5 } }), ev(2, NOW, { attrs: { duration_ms: 5000 } })], NOW);
    const f = eng.frame(NOW + 500, false);
    expect(f).toHaveLength(2);
    expect(f[0].t).toBeCloseTo(f[1].t); // latency does not change speed
    // a longer wire takes proportionally longer
    eng.ingestEvents([ev(3, NOW, { source: 'tracking.outbox', target: 'pg.tracking' })], NOW);
    const g = eng.frame(NOW + 500, false);
    const short = g.find((i) => i.wire === 'gateway>tracking')!;
    const long = g.find((i) => i.wire === 'pg.tracking>tracking' || i.wire === 'tracking>pg.tracking')!;
    expect(long.t).toBeCloseTo(short.t / 2);
  });

  it('never invents: unplaceable events, logs and health give no pulse; pulses end', () => {
    const eng = new PulseEngine(model, () => 8);
    const n = eng.ingestEvents([
      ev(1, NOW, { kind: 'log' }),
      ev(2, NOW, { kind: 'health' }),
      ev(3, NOW, { source: 'tracking.ingest', target: 'tracking.state' }), // inside one cube
      ev(4, NOW, { source: 'gateway', target: '' }),
    ], NOW);
    expect(n).toBe(0);
    expect(eng.frame(NOW + 10, false)).toEqual([]);
    eng.ingestEvents([ev(5, NOW)], NOW);
    expect(eng.frame(NOW + 10, false)).toHaveLength(1);
    expect(eng.frame(NOW + 60_000, false)).toEqual([]);
    expect(eng.frame(NOW + 60_000, true)).toEqual([]);
  });

  it('reverse direction travels from b to a', () => {
    const eng = new PulseEngine(model, () => 8);
    eng.ingestEvents([ev(1, NOW, { kind: 'message_consumed', source: 'nats.DELIVERIES', target: 'gateway.consumers' })], NOW);
    const [p] = eng.frame(NOW + 100, false);
    expect(p.wire).toBe('gateway>nats');
    expect(p.t).toBeGreaterThan(0.85); // starts at the nats end
    expect(eng.frame(NOW + 300, false)[0].t).toBeLessThan(p.t);
  });

  it('bursts above 20 per wire per second become one bundle pulse with a count', () => {
    const eng = new PulseEngine(model, () => 8);
    const burst = Array.from({ length: 30 }, (_, i) => ev(i + 1, NOW - 900 + i * 10, { outcome: i === 5 ? 'slow' : 'ok' }));
    expect(eng.ingestEvents(burst, NOW)).toBe(1);
    const [p] = eng.frame(NOW + 100, false);
    expect(p.count).toBe(30);
    expect(p.bundle).toBe(true);
    expect(p.color).toBe('warn');
  });

  it('server bundles render once with their count; self or unknown wires are skipped', () => {
    const eng = new PulseEngine(model, () => 8);
    expect(eng.ingestBundle({ wire: 'gateway.courier_api>tracking.ingest', count: 42, outcome: 'failed' }, NOW)).toBe(true);
    expect(eng.ingestBundle({ wire: 'gateway', count: 9, outcome: 'ok' }, NOW)).toBe(false); // logs have no target
    expect(eng.ingestBundle({ wire: 'gateway.courier_api>gateway.courier_api', count: 9, outcome: 'ok' }, NOW)).toBe(false);
    const f = eng.frame(NOW + 100, false);
    expect(f).toHaveLength(1);
    expect(f[0]).toMatchObject({ wire: 'gateway>tracking', count: 42, bundle: true, color: 'fail' });
  });

  it('simulated origin is carried to the pulse', () => {
    const eng = new PulseEngine(model, () => 8);
    eng.ingestEvents([ev(1, NOW, { origin: 'simulated' })], NOW);
    expect(eng.frame(NOW + 10, false)[0].simulated).toBe(true);
  });

  it('sync animates only new live events, once, and not events a server bundle may have folded', () => {
    const s = createStore();
    const eng = new PulseEngine(model, () => 8);
    s.addEvents([ev(1, NOW - 100)]);
    eng.sync(s.getState(), NOW); // first sync: history already in the buffer is not animated
    expect(eng.frame(NOW, false)).toEqual([]);

    s.addEvents([ev(2, NOW), ev(3, NOW - LIVE_WINDOW_MS - 1000)]); // 3 was observed long ago (history)
    eng.sync(s.getState(), NOW);
    eng.sync(s.getState(), NOW); // idempotent
    expect(eng.frame(NOW + 10, false)).toHaveLength(1);

    s.addBundle({ wire: 'gateway.courier_api>tracking.ingest', count: 5, outcome: 'ok' }, 50);
    eng.sync(s.getState(), NOW);
    eng.sync(s.getState(), NOW);
    expect(eng.frame(NOW + 10, false).filter((i) => i.bundle)).toHaveLength(1);

    s.addEvents([ev(40, NOW)]); // id <= the bundle's high-water mark: possibly already counted in it
    s.addEvents([ev(60, NOW)]);
    eng.sync(s.getState(), NOW);
    expect(eng.frame(NOW + 10, false)).toHaveLength(3);
  });
});
