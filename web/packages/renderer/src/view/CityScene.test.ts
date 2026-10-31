import * as THREE from 'three';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TrackingSnapshot } from '@dt/contract';
import { installFakeDom, mockWebGL } from '../testing/fakeDom';
import { CityScene } from './CityScene';
import { nightMix } from './theme';

const snap: TrackingSnapshot = {
  delivery_id: 'del_ctx',
  delivery_model: 'end_to_end',
  status: 'picked_up',
  courier: {
    display_name: 'Lucas M.',
    vehicle: 'bicycle',
    photo_url: null,
    position: { lat: 52.3725, lng: 4.892 },
    heading_deg: 90,
    speed_kmh: 15,
    position_at: null,
    signal: 'live',
  },
  pickup: { location: { lat: 52.3731, lng: 4.8926 }, name: 'Food Hub' },
  dropoff: { location: { lat: 52.3705, lng: 4.8895 }, label: null },
  route: { to_pickup: null, to_dropoff: [[52.3731, 4.8926], [52.3705, 4.8895]] },
  eta_seconds: 240,
  eta_window: null,
  eta_state: 'live',
  eta_quality: 'estimated',
  distance_remaining_m: 400,
  progress: 0.4,
  timeline: [],
  updated_at: '2026-10-29T10:00:00Z',
  version: 1,
} as TrackingSnapshot;

/** Versions of everything the GPU holds a copy of: materials, textures, vertex buffers. */
function gpuVersions(scene: THREE.Scene) {
  const mats = new Map<THREE.Material, number>();
  const tex = new Map<THREE.Texture, number>();
  const attrs = new Map<THREE.BufferAttribute, number>();
  scene.traverse((o) => {
    const mesh = o as THREE.Mesh;
    if (!mesh.isMesh) return;
    for (const m of Array.isArray(mesh.material) ? mesh.material : [mesh.material]) {
      mats.set(m, m.version);
      for (const v of Object.values(m)) if (v instanceof THREE.Texture && v.image) tex.set(v, v.version);
    }
    for (const a of Object.values(mesh.geometry.attributes)) if (a instanceof THREE.BufferAttribute) attrs.set(a, a.version);
  });
  return { mats, tex, attrs };
}

afterEach(() => vi.unstubAllGlobals());

describe('CityScene', () => {
  it('TestContextLossRebuildsScene', () => {
    const dom = installFakeDom();
    const gl = mockWebGL();
    const city = new CityScene(dom.container as unknown as HTMLElement, { webgl: () => gl, theme: 'dark', reducedMotion: true });
    city.onSnapshot(snap);
    city.frame(1 / 60);
    expect(gl.renders).toBe(1);
    const scene = gl.lastScene!;
    let meshes = 0;
    scene.traverse((o) => void ((o as THREE.Mesh).isMesh && meshes++));
    const before = gpuVersions(scene);
    expect(before.mats.size).toBeGreaterThan(10);
    expect(before.attrs.size).toBeGreaterThan(10);

    // iOS Safari drops the context (tab in the background, low memory): the scene waits instead of drawing
    const lost = new Event('webglcontextlost', { cancelable: true });
    gl.domElement.dispatchEvent(lost);
    expect(lost.defaultPrevented).toBe(true); // tells the browser we will restore
    city.frame(1 / 60);
    expect(gl.renders).toBe(1);

    // restored: every material, texture and vertex buffer is uploaded again, the shadow map redrawn
    gl.domElement.dispatchEvent(new Event('webglcontextrestored'));
    const after = gpuVersions(scene);
    for (const [m, v] of before.mats) expect(m.version, m.type).toBeGreaterThan(v);
    for (const [t, v] of before.tex) expect(t.version).toBeGreaterThan(v);
    for (const [a, v] of before.attrs) expect(a.version).toBeGreaterThan(v);
    expect(after.mats.size).toBe(before.mats.size);
    let meshesAfter = 0;
    scene.traverse((o) => void ((o as THREE.Mesh).isMesh && meshesAfter++));
    expect(meshesAfter).toBe(meshes);
    expect(gl.shadowMap.needsUpdate).toBe(true);
    // the theme is where it was (night), and drawing goes on
    expect(city.currentTheme).toBe('dark');
    expect(nightMix(scene)).toBe(1);
    city.frame(1 / 60);
    expect(gl.renders).toBe(2);

    city.dispose();
    const late = new Event('webglcontextlost', { cancelable: true });
    gl.domElement.dispatchEvent(late);
    expect(late.defaultPrevented).toBe(false); // listeners removed
  });

  it('lowers pixel ratio and shadow map size on request', () => {
    const dom = installFakeDom();
    const gl = mockWebGL();
    const city = new CityScene(dom.container as unknown as HTMLElement, { webgl: () => gl, reducedMotion: true });
    expect(gl.pixelRatio).toBe(2);
    city.setQuality({ maxPixelRatio: 1, shadowMapSize: 512 });
    expect(gl.pixelRatio).toBe(1);
    city.onSnapshot(snap);
    city.frame(1 / 60);
    let size = 0;
    gl.lastScene!.traverse((o) => {
      if (o instanceof THREE.DirectionalLight) size = o.shadow.mapSize.x;
    });
    expect(size).toBe(512);
    city.dispose();
  });
});

// ---- insets (task 13 review, R21) and scene labels (R22) ------------------------------------------------------------

describe('CityScene insets', () => {
  const W = 390;
  const H = 844;

  /** Where the scene put a label's anchor (bottom centre) on screen, from its CSS transform. */
  function labelAt(dom: ReturnType<typeof installFakeDom>, cls: 'store' | 'home') {
    const el = dom.container.children.find((c) => c.className === `scene-label ${cls}` || c.className.startsWith(`scene-label ${cls} `))!;
    const m = /translate\(([-\d.]+)px, ([-\d.]+)px\)/.exec(el.style.transform ?? '')!;
    return { x: Number(m[1]), y: Number(m[2]) };
  }

  function setup(s: TrackingSnapshot = snap) {
    const dom = installFakeDom();
    const gl = mockWebGL();
    const city = new CityScene(dom.container as unknown as HTMLElement, { webgl: () => gl, reducedMotion: true });
    city.onSnapshot(s);
    city.frame(1 / 60);
    const reframes = vi.spyOn(city as unknown as { frameAll: () => void }, 'frameAll');
    /** The customer takes the camera (a drag or a pinch starts). */
    const grab = () => (city as unknown as { controls: THREE.EventDispatcher<{ start: object }> }).controls.dispatchEvent({ type: 'start' });
    return { dom, city, reframes, grab };
  }

  it('frames store and home again in the map the cards leave free', () => {
    const { dom, city } = setup();
    city.setInsets({ top: 0, right: 0, bottom: 560, left: 0 });
    city.frame(1 / 60);
    for (const c of ['store', 'home'] as const) {
      const p = labelAt(dom, c);
      expect(p.y, c).toBeLessThan(H - 560);
      expect(p.x, c).toBeGreaterThan(0);
      expect(p.x, c).toBeLessThan(W);
    }
    city.dispose();
  });

  it('keeps following the courier', () => {
    const { city, reframes } = setup();
    city.setFollow(true);
    city.setInsets({ top: 0, right: 0, bottom: 400, left: 0 });
    city.frame(1 / 60);
    expect(city.isFollowing).toBe(true);
    expect(reframes).not.toHaveBeenCalled();
    city.dispose();
  });

  it('keeps the home-only framing after delivery', () => {
    const delivered = { ...snap, status: 'delivered', courier: null, version: 2 } as TrackingSnapshot;
    const { dom, city, reframes } = setup();
    city.onSnapshot(delivered); // frames the home alone
    city.frame(1 / 60);
    reframes.mockClear();
    city.setInsets({ top: 0, right: 0, bottom: 500, left: 0 });
    city.frame(1 / 60);
    expect(reframes).toHaveBeenCalledWith(false, true);
    const home = labelAt(dom, 'home');
    expect(home.y).toBeLessThan(H - 500);
    expect(Math.abs(home.x - W / 2)).toBeLessThan(20); // the home alone, centred; framing the store too would move it
    city.dispose();
  });

  it('ignores changes of a few pixels', () => {
    const { city, reframes } = setup();
    city.setInsets({ top: 0, right: 0, bottom: 200, left: 0 });
    reframes.mockClear();
    city.setInsets({ top: 0, right: 0, bottom: 203.6, left: 0.4 });
    city.setInsets({ top: 0, right: 0, bottom: 196.2, left: 0 });
    expect(reframes).not.toHaveBeenCalled();
    city.dispose();
  });

  it("keeps the customer's own view unless the cards would hide store, courier or home", () => {
    const { dom, city, reframes, grab } = setup();
    city.setInsets({ top: 0, right: 0, bottom: 200, left: 0 });
    city.frame(1 / 60);
    grab();
    const before = labelAt(dom, 'home');
    city.setInsets({ top: 0, right: 0, bottom: 240, left: 0 }); // the points stay clear
    city.frame(1 / 60);
    expect(reframes).toHaveBeenCalledTimes(1); // only the first, before the customer took the camera
    expect(labelAt(dom, 'home')).toEqual(before);
    city.setInsets({ top: 0, right: 0, bottom: 700, left: 0 }); // now the sheet would cover them
    city.frame(1 / 60);
    expect(reframes).toHaveBeenCalledTimes(2);
    expect(labelAt(dom, 'home').y).toBeLessThan(H - 700);
    city.dispose();
  });

  it('shows the home label the caller gives', () => {
    const dom = installFakeDom();
    const city = new CityScene(dom.container as unknown as HTMLElement, { webgl: () => mockWebGL(), reducedMotion: true, labels: { home: 'Je adres' } });
    city.onSnapshot(snap);
    expect(dom.container.children.find((c) => c.className.startsWith('scene-label home'))!.textContent).toBe('Je adres');
    city.dispose();
  });
});
