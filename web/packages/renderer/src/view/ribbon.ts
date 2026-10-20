import * as THREE from 'three';
import type { Vec2 } from '../engine/geo';

/** Flat ribbon (road-following line) lying at height `y`. */
export function buildRibbon(points: Vec2[], width: number, y: number): THREE.BufferGeometry {
  const pos: number[] = [];
  const idx: number[] = [];
  const hw = width / 2;
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1];
    const b = points[i];
    const dx = b.x - a.x;
    const dz = b.z - a.z;
    const len = Math.hypot(dx, dz);
    if (len < 0.01) continue;
    const ux = dx / len;
    const uz = dz / len;
    const nx = -uz;
    const nz = ux;
    // extend by half a width on both ends so corners join without gaps
    const ax = a.x - ux * hw;
    const az = a.z - uz * hw;
    const bx = b.x + ux * hw;
    const bz = b.z + uz * hw;
    const base = pos.length / 3;
    pos.push(
      ax + nx * hw, y, az + nz * hw,
      ax - nx * hw, y, az - nz * hw,
      bx - nx * hw, y, bz - nz * hw,
      bx + nx * hw, y, bz + nz * hw,
    );
    idx.push(base, base + 1, base + 2, base, base + 2, base + 3);
  }
  const g = new THREE.BufferGeometry();
  g.setAttribute('position', new THREE.Float32BufferAttribute(pos, 3));
  g.setIndex(idx);
  return g;
}

/** The part of `points` that is still ahead of position `p`. */
export function trimRouteFrom(points: Vec2[], p: Vec2): Vec2[] {
  if (points.length < 2) return points;
  let best = 0;
  let bestD = Infinity;
  let bestPt: Vec2 = points[0];
  for (let i = 0; i < points.length - 1; i++) {
    const a = points[i];
    const b = points[i + 1];
    const abx = b.x - a.x;
    const abz = b.z - a.z;
    const l2 = abx * abx + abz * abz || 1;
    const t = Math.max(0, Math.min(1, ((p.x - a.x) * abx + (p.z - a.z) * abz) / l2));
    const q = { x: a.x + abx * t, z: a.z + abz * t };
    const d = Math.hypot(p.x - q.x, p.z - q.z);
    if (d < bestD) {
      bestD = d;
      best = i;
      bestPt = q;
    }
  }
  return [bestPt, ...points.slice(best + 1)];
}
