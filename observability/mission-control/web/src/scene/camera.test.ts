import { describe, expect, it } from 'vitest';
import { ISO_AZIMUTH, ISO_ELEVATION, TWEEN_MS, fitZoom, isoPosition, tween, type CameraPose } from './camera';

const A: CameraPose = { position: [10, 10, 10], target: [0, 0, 0], zoom: 10 };
const B: CameraPose = { position: [20, 10, 0], target: [5, 0, 0], zoom: 30 };

describe('camera', () => {
  it('isometric position looks down at the iso elevation and azimuth', () => {
    const [x, y, z] = isoPosition([0, 0, 0], 50);
    expect(Math.hypot(x, y, z)).toBeCloseTo(50);
    expect(Math.asin(y / 50)).toBeCloseTo(ISO_ELEVATION);
    expect(Math.atan2(x, z)).toBeCloseTo(ISO_AZIMUTH);
  });

  it('tweens over TWEEN_MS, and does not tween with reduced motion', () => {
    const mid = tween(A, B, TWEEN_MS / 2, false);
    expect(mid.done).toBe(false);
    expect(mid.pose.zoom).toBeGreaterThan(10);
    expect(mid.pose.zoom).toBeLessThan(30);
    expect(tween(A, B, TWEEN_MS, false)).toEqual({ pose: B, done: true });
    expect(tween(A, B, 0, true)).toEqual({ pose: B, done: true });
  });

  it('fitZoom fits the bounds and grows with the viewport', () => {
    const b = { min: [-10, 0, -5] as [number, number, number], max: [10, 2, 5] as [number, number, number] };
    const z1 = fitZoom(b, 800, 600);
    const z2 = fitZoom(b, 1600, 1200);
    expect(z1).toBeGreaterThan(0);
    expect(z2).toBeCloseTo(z1 * 2);
  });
});
