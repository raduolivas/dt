import { describe, expect, it } from 'vitest';
import { AdaptiveQuality, QUALITY_LEVELS, isPhoneViewport, type Quality } from './quality';

function make(phone: boolean) {
  const applied: Quality[] = [];
  const q = new AdaptiveQuality(() => phone, (l) => applied.push(l));
  return { q, applied };
}
const feed = (q: AdaptiveQuality, fps: number, seconds: number) => {
  for (let i = 0; i < seconds; i++) q.sample(fps, 1000);
};

describe('AdaptiveQuality', () => {
  it('lowers the pixel ratio and the shadow map size after 5 s under 30 fps on a phone', () => {
    const { q, applied } = make(true);
    feed(q, 22, 4);
    expect(applied).toHaveLength(0);
    feed(q, 22, 1);
    expect(applied).toHaveLength(1);
    expect(applied[0]!.maxPixelRatio).toBeLessThan(QUALITY_LEVELS[0]!.maxPixelRatio);
    expect(applied[0]!.shadowMapSize).toBeLessThan(QUALITY_LEVELS[0]!.shadowMapSize);
  });

  it('counts only an unbroken slow stretch', () => {
    const { q, applied } = make(true);
    feed(q, 20, 4);
    feed(q, 45, 1);
    feed(q, 20, 4);
    expect(applied).toHaveLength(0);
  });

  it('steps down one level per slow stretch and stops at the lowest', () => {
    const { q, applied } = make(true);
    feed(q, 10, 60);
    expect(applied).toEqual(QUALITY_LEVELS.slice(1));
  });

  it('ignores the gap of a page that was in the background', () => {
    const { q, applied } = make(true);
    q.sample(0.01, 600_000);
    feed(q, 20, 4);
    expect(applied).toHaveLength(0);
  });

  it('leaves desktop alone', () => {
    const { q, applied } = make(false);
    feed(q, 10, 20);
    expect(applied).toHaveLength(0);
  });

  it('a phone is a coarse pointer or a viewport whose shorter side is under 768 px', () => {
    expect(isPhoneViewport(false, 375, 667)).toBe(true);
    expect(isPhoneViewport(false, 1280, 720)).toBe(true);
    expect(isPhoneViewport(false, 1440, 900)).toBe(false);
    expect(isPhoneViewport(true, 1024, 1366)).toBe(true);
  });
});
