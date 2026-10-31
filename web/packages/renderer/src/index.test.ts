import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TrackingSnapshot } from '@dt/contract';
import { createRenderer } from './index';
import { fake2d, installFakeDom, mockWebGL, type FakeDom } from './testing/fakeDom';

const ATTRIBUTION = '© OpenStreetMap contributors · © OpenMapTiles';

const snap = {
  delivery_id: 'del_idx',
  delivery_model: 'end_to_end',
  status: 'courier_assigned',
  courier: null,
  pickup: { location: { lat: 52.3731, lng: 4.8926 }, name: 'Food Hub' },
  dropoff: { location: { lat: 52.3705, lng: 4.8895 }, label: null },
  route: { to_pickup: null, to_dropoff: [[52.3731, 4.8926], [52.3705, 4.8895]] },
  eta_seconds: null,
  eta_window: null,
  eta_state: 'calculating',
  eta_quality: 'estimated',
  distance_remaining_m: null,
  progress: null,
  timeline: [],
  updated_at: '2026-10-29T10:00:00Z',
  version: 1,
} as TrackingSnapshot;

let dom: FakeDom;
beforeEach(() => {
  dom = installFakeDom();
  dom.canvasContexts['2d'] = () => fake2d();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const container = () => dom.container as unknown as HTMLElement;
const fallbackCanvas = () => dom.container.children.find((c) => c.className.includes('dt-fallback'));
const attribution = () => dom.container.children.find((c) => c.className.includes('dt-attribution'));

describe('createRenderer', () => {
  it('returns the 2D fallback when getContext("webgl2") and getContext("webgl") return null', () => {
    dom.canvasContexts.webgl2 = () => null;
    dom.canvasContexts.webgl = () => null;
    const r = createRenderer(container());
    expect(fallbackCanvas()).toBeDefined();
    r.update(snap);
    dom.frame();
    r.destroy();
    expect(fallbackCanvas()).toBeUndefined();
    expect(attribution()).toBeUndefined();
  });

  it('shows the attribution in the fallback renderer', () => {
    const r = createRenderer(container());
    expect(attribution()?.textContent).toBe(ATTRIBUTION);
    r.destroy();
  });

  it('shows the attribution in the 3D renderer', () => {
    const gl = mockWebGL();
    const r = createRenderer(container(), { reducedMotion: true }, { webgl: () => gl });
    expect(fallbackCanvas()).toBeUndefined();
    expect(dom.container.children).toContain(gl.domElement);
    expect(attribution()?.textContent).toBe(ATTRIBUTION);
    // above the canvas, clear of the page's cards
    expect(dom.container.children.indexOf(attribution()!)).toBeGreaterThan(dom.container.children.indexOf(gl.domElement));
    r.setInsets({ top: 0, right: 0, bottom: 120, left: 0 });
    expect(attribution()!.style.bottom).toBe('124px');
    r.destroy();
  });

  it('swaps to the fallback, on the same handle, when no tile has loaded 10 s after the delivery started', async () => {
    vi.useFakeTimers();
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    vi.stubGlobal('fetch', () => Promise.reject(new Error('offline')));
    const gl = mockWebGL();
    const r = createRenderer(
      container(),
      { reducedMotion: true, tiles: { kind: 'pmtiles', url: 'https://tiles.invalid/nl.pmtiles' } },
      { webgl: () => gl },
    );
    r.update(snap);
    await vi.advanceTimersByTimeAsync(9_000);
    expect(fallbackCanvas()).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(fallbackCanvas()).toBeDefined();
    expect(dom.container.children).not.toContain(gl.domElement);
    expect(attribution()?.textContent).toBe(ATTRIBUTION);
    // the latest snapshot is drawn, and later ones too
    r.update({ ...snap, version: 2 });
    dom.frame();
    r.destroy();
    expect(fallbackCanvas()).toBeUndefined();
  });

  it('swaps to the fallback, on the same handle, when a lost WebGL context is not back within 3 s', async () => {
    vi.useFakeTimers();
    const gl = mockWebGL();
    const r = createRenderer(container(), { reducedMotion: true }, { webgl: () => gl });
    r.update(snap);
    gl.domElement.dispatchEvent(new Event('webglcontextlost', { cancelable: true }));
    await vi.advanceTimersByTimeAsync(2_900);
    expect(fallbackCanvas()).toBeUndefined();
    await vi.advanceTimersByTimeAsync(100);
    expect(fallbackCanvas()).toBeDefined();
    expect(dom.container.children).not.toContain(gl.domElement);
    expect(attribution()?.textContent).toBe(ATTRIBUTION);
    // a late restore changes nothing: the fallback stays
    gl.domElement.dispatchEvent(new Event('webglcontextrestored'));
    r.update({ ...snap, version: 2 });
    dom.frame();
    expect(fallbackCanvas()).toBeDefined();
    r.destroy();
    expect(fallbackCanvas()).toBeUndefined();
  });

  it('keeps the 3D scene, rebuilt, when the lost context is restored within 3 s', async () => {
    vi.useFakeTimers();
    const gl = mockWebGL();
    const r = createRenderer(container(), { reducedMotion: true }, { webgl: () => gl });
    r.update(snap);
    await vi.advanceTimersByTimeAsync(100); // fake timers drive the animation frames
    expect(gl.renders).toBeGreaterThan(0);
    gl.domElement.dispatchEvent(new Event('webglcontextlost', { cancelable: true }));
    const renders = gl.renders;
    await vi.advanceTimersByTimeAsync(2_000);
    expect(gl.renders).toBe(renders); // nothing drawn while lost
    gl.domElement.dispatchEvent(new Event('webglcontextrestored'));
    expect(gl.shadowMap.needsUpdate).toBe(true); // rebuilt
    await vi.advanceTimersByTimeAsync(5_000);
    expect(fallbackCanvas()).toBeUndefined();
    expect(gl.renders).toBeGreaterThan(renders); // drawing again
    r.destroy();
  });

  it('cancels the context timer on destroy', async () => {
    vi.useFakeTimers();
    const gl = mockWebGL();
    const r = createRenderer(container(), { reducedMotion: true }, { webgl: () => gl });
    gl.domElement.dispatchEvent(new Event('webglcontextlost', { cancelable: true }));
    r.destroy();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('shows the home label in the language the caller gives (task 13 review, R22)', () => {
    const gl = mockWebGL();
    const r = createRenderer(container(), { reducedMotion: true, labels: { home: 'Je adres' } }, { webgl: () => gl });
    r.update(snap);
    const home = dom.container.children.find((c) => c.className.startsWith('scene-label home'));
    expect(home?.textContent).toBe('Je adres');
    r.destroy();
  });

  it('reports fps once per second', () => {
    let now = 0;
    const stats: number[] = [];
    const gl = mockWebGL();
    const r = createRenderer(
      container(),
      { reducedMotion: true, clock: () => now, onFrameStats: (s) => stats.push(s.fps) },
      { webgl: () => gl },
    );
    for (let i = 0; i < 125; i++) {
      now += 1000 / 50;
      dom.frame();
    }
    expect(stats).toHaveLength(2);
    expect(stats[0]).toBeCloseTo(50, 0);
    r.destroy();
  });
});
