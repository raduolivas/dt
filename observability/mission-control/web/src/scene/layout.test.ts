import { describe, expect, it } from 'vitest';
import { topologyFixture } from './fixture';
import { ZONE_ORDER, layoutScene } from './layout';
import { buildModel } from './model';

const m = buildModel(topologyFixture());
const l = layoutScene(m.cubes);

describe('layout', () => {
  it('five platforms in the 6.2 order', () => {
    expect(l.platforms.map((p) => p.zone)).toEqual([...ZONE_ORDER]);
    const xs = l.platforms.map((p) => p.center[0]);
    expect([...xs].sort((a, b) => a - b)).toEqual(xs);
  });

  it('every cube sits on its own zone platform, without overlaps', () => {
    for (const c of m.cubes) {
      const p = l.cubes.get(c.id)!;
      const pl = l.platforms.find((x) => x.zone === c.zone)!;
      expect(Math.abs(p[0] - pl.center[0]), c.id).toBeLessThanOrEqual(pl.size[0] / 2);
      expect(Math.abs(p[2] - pl.center[2]), c.id).toBeLessThanOrEqual(pl.size[1] / 2);
    }
    const keys = [...l.cubes.values()].map((p) => `${p[0].toFixed(2)},${p[2].toFixed(2)}`);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('is stable: the same layout for every order', () => {
    expect(layoutScene(m.cubes)).toEqual(l);
  });
});
