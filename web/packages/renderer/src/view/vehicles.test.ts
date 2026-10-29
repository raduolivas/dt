import * as THREE from 'three';
import { describe, expect, it } from 'vitest';
import type { Vehicle } from '@dt/contract';
import { makeCourierModel } from './vehicles';

const VEHICLES: Vehicle[] = ['bicycle', 'e_bike', 'scooter', 'car', 'on_foot'];

function meshes(root: THREE.Object3D): THREE.Mesh[] {
  const out: THREE.Mesh[] = [];
  root.traverse((o) => {
    if (o instanceof THREE.Mesh) out.push(o);
  });
  return out;
}

/** Every mesh's world matrix, to tell whether `animate` moved anything. */
function pose(root: THREE.Object3D): number[] {
  root.updateMatrixWorld(true);
  return meshes(root).flatMap((m) => m.matrixWorld.elements.map((e) => Math.round(e * 1e5) / 1e5));
}

describe('makeCourierModel', () => {
  for (const v of VEHICLES) {
    it(`${v}: a model with a non-empty mesh, about the bicycle's size`, () => {
      const m = makeCourierModel(v);
      const ms = meshes(m.root);
      expect(ms.length).toBeGreaterThan(0);
      expect(ms.some((x) => (x.geometry.getAttribute('position')?.count ?? 0) > 0)).toBe(true);
      const size = new THREE.Box3().setFromObject(m.root).getSize(new THREE.Vector3());
      // the bicycle with its rider is about 2.4 tall and 2.4 long (bike metres): framing and the x-ray stay valid
      expect(size.y).toBeGreaterThan(1.6);
      expect(size.y).toBeLessThan(3);
      expect(Math.max(size.x, size.z)).toBeLessThan(3.6);
      expect(m.parcel.parent).not.toBeNull();
    });

    it(`${v}: stands still at zero speed and moves in its own way when going`, () => {
      const m = makeCourierModel(v);
      m.animate(0, 0.016);
      const rest = pose(m.root);
      for (let i = 0; i < 30; i++) m.animate(0, 0.016);
      expect(pose(m.root)).toEqual(rest);
      for (let i = 0; i < 30; i++) m.animate(5, 0.016);
      expect(pose(m.root)).not.toEqual(rest);
    });
  }

  it('shares geometries and materials between models of the same kind', () => {
    for (const v of VEHICLES) {
      const a = meshes(makeCourierModel(v).root);
      const b = meshes(makeCourierModel(v).root);
      a.forEach((m, i) => {
        expect(b[i]!.material).toBe(m.material);
        expect(b[i]!.geometry).toBe(m.geometry);
      });
    }
  });
});
