import * as THREE from 'three';
import { describe, expect, it } from 'vitest';
import type { Vehicle } from '@dt/contract';
import { AMS_STORE } from '../dev/amsterdam';
import { decodeTile } from '../tiles/build/decode';
import { fixtureTiles } from '../tiles/fixture';
import { LIT_CELLS, litWindowCells, makeCourierGlow } from './night';
import { applyTheme, createLights } from './theme';
import { makeCourierModel } from './vehicles';

const VEHICLES: Vehicle[] = ['bicycle', 'e_bike', 'scooter', 'car', 'on_foot'];

describe('lit windows', () => {
  it('lights a seeded share of the windows, the same on every load', () => {
    const a = litWindowCells();
    const b = litWindowCells();
    expect(a).toEqual(b);
    expect(a.length).toBe(LIT_CELLS * LIT_CELLS);
    const share = a.filter(Boolean).length / a.length;
    expect(share).toBeGreaterThan(0.2);
    expect(share).toBeLessThan(0.45);
  });
});

describe('streetlights', { timeout: 60_000 }, () => {
  it('stand along the streets of a tile, inside it, the same on every build', async () => {
    const tiles = await fixtureTiles();
    let total = 0;
    for (const [i, t] of tiles.entries()) {
      const req = { tile: { z: t.z, x: t.x, y: t.y }, data: t.data, origin: AMS_STORE.location };
      const r = decodeTile(req);
      const lights = r.layers.roads.lights;
      expect(lights.length % 3).toBe(0);
      if (i === 0) expect(Array.from(decodeTile(req).layers.roads.lights)).toEqual(Array.from(lights));
      // inside the tile's own block slab (its bounds are the landuse slab's extent)
      const slab = r.layers.landuse.positions;
      let minX = Infinity;
      let maxX = -Infinity;
      let minZ = Infinity;
      let maxZ = -Infinity;
      for (let i = 0; i < slab.length; i += 3) {
        minX = Math.min(minX, slab[i]!);
        maxX = Math.max(maxX, slab[i]!);
        minZ = Math.min(minZ, slab[i + 2]!);
        maxZ = Math.max(maxZ, slab[i + 2]!);
      }
      for (let i = 0; i < lights.length; i += 3) {
        expect(lights[i]!).toBeGreaterThanOrEqual(minX);
        expect(lights[i]!).toBeLessThan(maxX);
        expect(lights[i + 1]!).toBeGreaterThanOrEqual(minZ);
        expect(lights[i + 1]!).toBeLessThan(maxZ);
      }
      total += lights.length / 3;
    }
    expect(total / tiles.length).toBeGreaterThan(20);
  });
});

describe('courier glow', () => {
  it.each(VEHICLES)('lights the road ahead of a %s at night only', (v) => {
    const scene = new THREE.Scene();
    const { hemi, sun } = createLights();
    scene.add(hemi, sun);
    const root = new THREE.Group();
    const glow = makeCourierGlow();
    root.add(makeCourierModel(v).root, glow);
    scene.add(root);
    applyTheme(scene, 'light', { fadeMs: 0 });
    expect(glow.visible).toBe(false); // by day: not drawn at all (no extra draw call)
    applyTheme(scene, 'dark', { fadeMs: 0 });
    expect(glow.visible).toBe(true);
    expect((glow.material as THREE.MeshBasicMaterial).opacity).toBeGreaterThan(0.3);
    expect((glow.material as THREE.MeshBasicMaterial).blending).toBe(THREE.AdditiveBlending);
  });
});
