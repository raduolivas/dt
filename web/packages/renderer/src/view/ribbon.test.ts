import { describe, expect, it } from 'vitest';
import { buildRibbon, fillRibbon, trimLegFrom } from './ribbon';

const LEG = [
  { x: 0, z: 0 },
  { x: 100, z: 0 },
  { x: 100, z: 100 },
];

describe('trimLegFrom', () => {
  it('starts at the courier snapped onto the leg and keeps the rest', () => {
    expect(trimLegFrom(LEG, { x: 40, z: 8 })).toEqual([{ x: 40, z: 0 }, LEG[1], LEG[2]]);
    expect(trimLegFrom(LEG, { x: 104, z: 60 })).toEqual([{ x: 100, z: 60 }, LEG[2]]);
  });
  it('leaves the whole leg when the courier is drawn raw, more than 25 m off', () => {
    expect(trimLegFrom(LEG, { x: 50, z: 60 })).toBe(LEG);
  });
});

describe('fillRibbon', () => {
  it('reuses its buffers while the route fits and draws only the part in use', () => {
    const g = buildRibbon(LEG, 4, 1);
    const pos = g.getAttribute('position');
    const arr = pos.array;
    expect(g.drawRange.count).toBe(12); // two segments
    fillRibbon(g, trimLegFrom(LEG, { x: 40, z: 0 }), 4, 1);
    expect(g.getAttribute('position')).toBe(pos);
    expect(pos.array).toBe(arr);
    fillRibbon(g, [], 4, 1);
    expect(g.drawRange.count).toBe(0);
  });
  it('grows when a longer route arrives', () => {
    const g = buildRibbon(LEG, 4, 1);
    const long = Array.from({ length: 50 }, (_, i) => ({ x: i * 10, z: 0 }));
    fillRibbon(g, long, 4, 1);
    expect(g.drawRange.count).toBe(49 * 6);
    expect(g.getAttribute('position').count).toBeGreaterThanOrEqual(49 * 4);
  });
});
