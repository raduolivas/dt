// Fixed layout (spec 6.2: "same fixed layout for every order"): five raised
// platforms left to right in role order, cubes in a grid on each. Pure.
import type { Component } from '../api/types';
import type { Vec3 } from './camera';

export const ZONE_ORDER = ['outside', 'edge', 'services', 'messaging', 'data'] as const;
export type Zone = (typeof ZONE_ORDER)[number];
export const ZONE_LABEL: Record<Zone, string> = {
  outside: 'OUTSIDE', edge: 'EDGE', services: 'SERVICES', messaging: 'MESSAGING', data: 'DATA',
};

export const CUBE_W = 2;
export const CUBE_H = 1.5;
/** Depth between cubes on one platform. One column per platform: rows recede, which on screen keeps labels apart. */
export const ROW = 5.4;
const PLATFORM_W = 5.4;
const PAD = 1.8;
const GAP = 3.4;
export const PLATFORM_H = 0.45;

export interface PlatformLayout { zone: Zone; center: Vec3; size: [number, number] }
export interface Layout { platforms: PlatformLayout[]; cubes: Map<string, Vec3>; bounds: { min: Vec3; max: Vec3 } }

export function layoutScene(cubes: readonly Component[]): Layout {
  const byZone = new Map<Zone, Component[]>(ZONE_ORDER.map((z) => [z, []]));
  for (const c of cubes) byZone.get(c.zone as Zone)?.push(c);
  const platforms: PlatformLayout[] = [];
  const pos = new Map<string, Vec3>();
  let x = 0;
  let maxD = 0;
  for (const zone of ZONE_ORDER) {
    const cs = byZone.get(zone)!;
    const rows = Math.max(1, cs.length);
    const w = PLATFORM_W;
    const d = rows * ROW + PAD;
    const cx = x + w / 2;
    platforms.push({ zone, center: [cx, -PLATFORM_H / 2, 0], size: [w, d] });
    cs.forEach((c, i) => pos.set(c.id, [cx, CUBE_H / 2, (i - (rows - 1) / 2) * ROW]));
    maxD = Math.max(maxD, d);
    x += w + GAP;
  }
  // centre the row on the origin
  const shift = (x - GAP) / 2;
  for (const p of platforms) p.center = [p.center[0] - shift, p.center[1], p.center[2]];
  for (const [id, p] of pos) pos.set(id, [p[0] - shift, p[1], p[2]]);
  return {
    platforms,
    cubes: pos,
    bounds: { min: [-shift, -PLATFORM_H, -maxD / 2], max: [shift, CUBE_H + 2.5, maxD / 2] },
  };
}

/** Control points of a wire's glowing arc: from the top of cube a to the top of cube b, lifted by its span. */
export function wireArc(a: Vec3, b: Vec3): [Vec3, Vec3, Vec3] {
  const top = CUBE_H / 2;
  const s: Vec3 = [a[0], a[1] + top, a[2]];
  const e: Vec3 = [b[0], b[1] + top, b[2]];
  const span = Math.hypot(e[0] - s[0], e[2] - s[2]);
  const m: Vec3 = [(s[0] + e[0]) / 2, Math.max(s[1], e[1]) + 0.9 + span * 0.22, (s[2] + e[2]) / 2];
  return [s, m, e];
}
