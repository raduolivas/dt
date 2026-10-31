import * as THREE from 'three';
import type { LatLng, TrackingSnapshot } from '@dt/contract';
import type { Vec2 } from '../engine/geo';
import { snapToLeg } from './motion';
import { activeLeg, activeLegKind, isFinal, toLatLng } from '../snapshot';

/**
 * Writes a flat ribbon (road-following line) lying at height `y` into `g`, reusing its buffers: they only grow (by
 * doubling) when the route needs more room, so a ribbon refreshed every frame allocates nothing. Draw range is the
 * part in use. The bounding sphere is not kept up to date: meshes using this geometry turn frustum culling off.
 */
export function fillRibbon(g: THREE.BufferGeometry, points: Vec2[], width: number, y: number): THREE.BufferGeometry {
  const segs = Math.max(0, points.length - 1);
  let posAttr = g.getAttribute('position') as THREE.BufferAttribute | undefined;
  let index = g.getIndex();
  if (!posAttr || posAttr.count < segs * 4 || !index) {
    const cap = Math.max(16, segs * 4, posAttr ? posAttr.count * 2 : 0);
    posAttr = new THREE.BufferAttribute(new Float32Array(cap * 3), 3);
    posAttr.setUsage(THREE.DynamicDrawUsage);
    g.setAttribute('position', posAttr);
    index = new THREE.BufferAttribute(new Uint32Array((cap / 4) * 6), 1);
    index.setUsage(THREE.DynamicDrawUsage);
    g.setIndex(index);
  }
  const pos = posAttr.array as Float32Array;
  const idx = index.array as Uint32Array;
  const hw = width / 2;
  let v = 0; // vertices written
  let n = 0; // indices written
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1]!;
    const b = points[i]!;
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
    let o = v * 3;
    pos[o++] = ax + nx * hw; pos[o++] = y; pos[o++] = az + nz * hw;
    pos[o++] = ax - nx * hw; pos[o++] = y; pos[o++] = az - nz * hw;
    pos[o++] = bx - nx * hw; pos[o++] = y; pos[o++] = bz - nz * hw;
    pos[o++] = bx + nx * hw; pos[o++] = y; pos[o++] = bz + nz * hw;
    idx[n++] = v; idx[n++] = v + 1; idx[n++] = v + 2;
    idx[n++] = v; idx[n++] = v + 2; idx[n++] = v + 3;
    v += 4;
  }
  posAttr.needsUpdate = true;
  index.needsUpdate = true;
  g.setDrawRange(0, n);
  return g;
}

/** A new flat ribbon geometry; see `fillRibbon`. */
export function buildRibbon(points: Vec2[], width: number, y: number): THREE.BufferGeometry {
  return fillRibbon(new THREE.BufferGeometry(), points, width, y);
}

/**
 * The part of `leg` still ahead of the courier: from its position snapped onto the leg (within 25 m) to the end.
 * A courier drawn raw (further from the leg) leaves the leg whole.
 */
export function trimLegFrom(leg: Vec2[], courier: Vec2): Vec2[] {
  const snap = snapToLeg(courier, leg);
  return snap ? [snap.p, ...leg.slice(snap.index + 1)] : leg;
}

/**
 * The two route ribbons for a snapshot: `active` is the leg the courier is on (`activeLeg()`), from the courier when
 * it is drawn; `next` is the delivery leg while the courier is still on the way to the pickup. Final: none.
 */
export function routeLegs(
  s: TrackingSnapshot,
  toLocal: (p: LatLng) => Vec2,
  courier: Vec2 | null,
): { active: Vec2[]; next: Vec2[] } {
  if (isFinal(s.status)) return { active: [], next: [] };
  const leg = activeLeg(s).map(toLocal);
  return {
    active: courier ? trimLegFrom(leg, courier) : leg,
    next: activeLegKind(s) === 'to_pickup' ? s.route.to_dropoff.map((p) => toLocal(toLatLng(p))) : [],
  };
}

/** Ribbon width in metres: `minM` at close zoom, never thinner than `minPx` screen pixels when zoomed out. */
export function ribbonWidth(minM: number, minPx: number, metresPerPx: number): number {
  return Math.max(minM, minPx * metresPerPx);
}
