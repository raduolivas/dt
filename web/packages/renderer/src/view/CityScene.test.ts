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
