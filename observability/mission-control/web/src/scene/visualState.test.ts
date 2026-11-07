import { describe, expect, it } from 'vitest';
import { mkEvent } from '../test-utils';
import { theme } from '../theme';
import { PulseEngine } from './engine';
import { comp, topologyFixture } from './fixture';
import { buildModel } from './model';
import {
  HEALTH_NOT_PROBED, PULSE_HEX, STATE_LOOK, VISUAL_STATES, WIRE_LOOK, caption, pulseColor, stateFrom, visualState, wireStyle,
  type Evidence,
} from './visualState';

const NOW = 1_700_000_100_000;
const iso = (ms: number) => new Date(ms).toISOString();
const seen = (ms: number, over = {}) => mkEvent(Math.floor(ms), { observedAt: iso(ms), occurredAt: iso(ms), ...over });

describe('visual states', () => {
  it('everyVisualStateDistinct', () => {
    const triples = VISUAL_STATES.map((s) => {
      const l = STATE_LOOK[s];
      expect(l, s).toBeDefined();
      return `${l.color}|${l.dash}|${l.opacity}`;
    });
    expect(new Set(triples).size).toBe(VISUAL_STATES.length);
    // Cyan is the accent: only the "active" (lit) look may use it.
    for (const s of VISUAL_STATES) if (s !== 'active') expect(STATE_LOOK[s].color, s).not.toBe(theme.accent);
    expect(STATE_LOOK.unhealthy.color).toBe(theme.fail);
    expect(STATE_LOOK.warning.color).toBe(theme.warn);
    expect(STATE_LOOK.not_instrumented.dash).toBe('dashed');
    expect(STATE_LOOK.stale.dash).toBe('hatched');
  });

  it('notInstrumentedIsNotIdle', () => {
    const courier = comp('courier', { state: 'not_instrumented' });
    // Even with traffic seen on its wire, an uninstrumented part never looks idle or active.
    const evs = [seen(NOW - 1000, { source: 'courier', target: 'gateway.courier_api' })];
    expect(visualState(courier, evs, NOW)).toBe('not_instrumented');
    expect(visualState(courier, [], NOW)).toBe('not_instrumented');
    expect(STATE_LOOK.not_instrumented).not.toEqual(STATE_LOOK.idle);
    // An instrumented part with nothing for 2 minutes is idle, not not_instrumented.
    const gw = comp('gateway', { state: 'idle' });
    expect(visualState(gw, [seen(NOW - 120_000, { source: 'gateway.partner_api', target: 'deliveries.rpc' })], NOW)).toBe('idle');
  });

  it('activity comes from the newest event and the backend lastSeen', () => {
    const tr = comp('tracking', { state: 'stale', lastSeen: null });
    expect(visualState(tr, [], NOW)).toBe('stale');
    expect(visualState(tr, [seen(NOW - 5000, { source: 'tracking.ingest', target: 'redis' })], NOW)).toBe('active');
    expect(visualState(tr, [seen(NOW - 400_000, { source: 'tracking.ingest', target: 'redis' })], NOW)).toBe('stale');
    expect(visualState(comp('tracking', { lastSeen: iso(NOW - 10_000) }), [], NOW)).toBe('active');
    // health findings are not activity
    expect(visualState(tr, [seen(NOW - 5000, { kind: 'health', source: 'tracking', target: '' })], NOW)).toBe('stale');
    // a different component's events do not count (prefix match is on whole ids)
    expect(visualState(tr, [seen(NOW - 5000, { source: 'trackingx', target: '' })], NOW)).toBe('stale');
  });

  it('backend health and findings win over activity; not_built is its own look', () => {
    expect(stateFrom(comp('gateway', { state: 'unreachable' }), NOW, NOW)).toBe('unhealthy');
    expect(stateFrom(comp('gateway', { state: 'warning' }), NOW, NOW)).toBe('warning');
    expect(stateFrom(comp('webhooks'), NOW, NOW)).toBe('not_built');
    expect(STATE_LOOK.not_built).not.toEqual(STATE_LOOK.not_instrumented);
  });

  it('R22: health not probed never looks unhealthy or stale by itself', () => {
    expect(HEALTH_NOT_PROBED.has('deliveries')).toBe(true);
    expect(HEALTH_NOT_PROBED.has('accounts')).toBe(true);
    // Even if a state ever said unreachable for them, Mission Control never probed: it comes from activity.
    expect(stateFrom(comp('deliveries', { state: 'unreachable' }), NOW - 1000, NOW)).toBe('active');
    expect(stateFrom(comp('accounts', { state: 'idle' }), NOW - 60_000, NOW)).toBe('idle');
    // No activity is not staleness for them: no traffic seen, still not stale (the label says so).
    expect(stateFrom(comp('deliveries', { state: 'stale' }), null, NOW)).toBe('idle');
    expect(stateFrom(comp('accounts', { state: 'stale' }), NOW - 3_600_000, NOW)).toBe('idle');
    expect(caption(comp('deliveries', { state: 'stale' }), 'idle', null, NOW)).toEqual(['idle · no traffic seen', 'health not probed']);
    // A detector finding still shows amber.
    expect(stateFrom(comp('deliveries', { state: 'warning' }), null, NOW)).toBe('warning');
  });
});

describe('wires', () => {
  it('inferredWireIsDotted', () => {
    const ev = new Map<string, Evidence>([
      ['a>b', { measured: 0, inferred: 3 }],
      ['c>d', { measured: 2, inferred: 5 }],
    ]);
    expect(wireStyle({ id: 'a>b' }, ev)).toBe('inferred');
    expect(wireStyle({ id: 'c>d' }, ev)).toBe('measured');
    expect(wireStyle({ id: 'x>y' }, ev)).toBe('quiet');
    expect(WIRE_LOOK.inferred.dash).toBe('dotted');
    expect(WIRE_LOOK.measured.dash).toBe('solid');
    expect(WIRE_LOOK.quiet).not.toEqual(WIRE_LOOK.measured);
  });
});

describe('pulses', () => {
  it('pulseColorsTrafficLight', () => {
    expect(pulseColor('ok')).toBe('ok');
    for (const o of ['slow', 'retried', 'dropped', 'rejected']) expect(pulseColor(o), o).toBe('warn');
    expect(pulseColor('failed')).toBe('fail');
    expect(PULSE_HEX).toEqual({ ok: theme.ok, warn: theme.warn, fail: theme.fail });
    expect(Object.values(PULSE_HEX)).not.toContain(theme.accent);
  });

  it('reducedMotionHasNoTravellingPulses', () => {
    const model = buildModel(topologyFixture());
    const eng = new PulseEngine(model, () => 10);
    const evs = [
      seen(NOW - 200, { id: 1, source: 'gateway.courier_api', target: 'tracking.ingest' }),
      seen(NOW - 150, { id: 2, source: 'gateway.courier_api', target: 'tracking.ingest', outcome: 'failed' }),
      seen(NOW - 100, { id: 3, source: 'gateway.courier_api', target: 'tracking.ingest' }),
    ];
    expect(eng.ingestEvents(evs, NOW)).toBe(3);
    const moving1 = eng.frame(NOW + 300, false);
    const moving2 = eng.frame(NOW + 500, false);
    expect(moving1.length).toBeGreaterThan(0);
    expect(moving1.every((i) => !i.static)).toBe(true);
    expect(moving2[0].t).not.toBe(moving1[0].t);

    const a = eng.frame(NOW + 300, true);
    const b = eng.frame(NOW + 900, true);
    expect(a.length).toBeGreaterThan(0);
    expect(a.every((i) => i.static)).toBe(true);
    expect(a.map((i) => i.t)).toEqual(b.map((i) => i.t)); // nothing travels
    expect(a.reduce((n, i) => n + i.count, 0)).toBe(3); // markers carry the counts
    expect(a.find((i) => i.color === 'ok')?.count).toBe(2);
    expect(a.find((i) => i.color === 'fail')?.count).toBe(1);
  });
});
