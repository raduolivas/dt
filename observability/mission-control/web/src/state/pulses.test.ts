import { describe, expect, it } from 'vitest';
import { toPulses } from './pulses';
import { mkEvent } from '../test-utils';

const NOW = 1_700_000_010_000;
const at = (ms: number, over = {}) => {
  const t = new Date(ms).toISOString();
  return mkEvent(Math.floor(ms), { occurredAt: t, ...over });
};

describe('toPulses', () => {
  it('bundlesBurstsAboveThreshold', () => {
    const burst = Array.from({ length: 25 }, (_, i) => at(NOW - 900 + i * 10, { outcome: i === 3 ? 'failed' : 'ok' }));
    const few = Array.from({ length: 5 }, (_, i) => at(NOW - 500 + i, { source: 'redis', target: 'tracking.hub' }));
    const ps = toPulses([...burst, ...few], NOW);
    const bundles = ps.filter((p) => p.count !== undefined);
    expect(bundles).toHaveLength(1);
    expect(bundles[0].count).toBe(25);
    expect(bundles[0].wire).toBe('gateway>tracking.ingest');
    expect(bundles[0].color).toBe('fail'); // the worst outcome in the bundle
    expect(ps.filter((p) => p.wire === 'redis>tracking.hub')).toHaveLength(5);
    expect(ps).toHaveLength(6);
  });

  it('exactly 20 events per wire stay individual pulses', () => {
    const evs = Array.from({ length: 20 }, (_, i) => at(NOW - 900 + i));
    const ps = toPulses(evs, NOW);
    expect(ps).toHaveLength(20);
    expect(ps.every((p) => p.count === undefined)).toBe(true);
  });

  it('maps outcomes to colours', () => {
    const ps = toPulses(
      [at(NOW - 5, { outcome: 'ok' }), at(NOW - 4, { outcome: 'slow' }), at(NOW - 3, { outcome: 'retried' }),
        at(NOW - 2, { outcome: 'dropped' }), at(NOW - 1, { outcome: 'rejected' }), at(NOW, { outcome: 'failed' })],
      NOW,
    );
    expect(ps.map((p) => p.color)).toEqual(['ok', 'warn', 'warn', 'warn', 'warn', 'fail']);
  });

  it('never invents pulses: no target, other kinds and events outside the window give none', () => {
    const ps = toPulses(
      [at(NOW - 100, { target: '' }), at(NOW - 100, { kind: 'health' }), at(NOW - 100, { kind: 'log' }),
        at(NOW - 5000), at(NOW + 5000)],
      NOW,
    );
    expect(ps).toEqual([]);
  });

  it('marks simulated events', () => {
    const [p] = toPulses([at(NOW - 10, { origin: 'simulated' })], NOW);
    expect(p.simulated).toBe(true);
  });

  it('R32: unknown outcomes are their own colour and rank between ok and warn in a bundle', () => {
    expect(toPulses([at(NOW - 100, { outcome: 'unknown' })], NOW)[0].color).toBe('unknown');
    const burst = (bad: string) =>
      Array.from({ length: 25 }, (_, i) => at(NOW - 900 + i * 10, { outcome: i === 1 ? 'unknown' : i === 2 ? bad : 'ok' }));
    expect(toPulses(burst('ok'), NOW)[0].color).toBe('unknown');
    expect(toPulses(burst('slow'), NOW)[0].color).toBe('warn');
  });
});
