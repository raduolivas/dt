import { Color, ShapeUtils, Vector2 } from 'three';
import { CITY_DAY, CITY_NIGHT, type CityColors } from '../../view/colors';
import { GeoProjection } from '../../engine/geo';
import type { TileXY } from '../corridor';

export type TileTheme = 'light' | 'dark';
/** Linear RGB (vertex colors are not color managed, so the sRGB palette is converted once here). */
export type RGB = [number, number, number];

export interface Palette {
  block: RGB;
  blockSide: RGB;
  road: RGB;
  roadPath: RGB;
  roadMark: RGB;
  water: RGB;
  park: RGB;
  walls: RGB[];
  roof: RGB;
  roofRim: RGB;
  roofAccent: RGB;
  roofAccentRim: RGB;
  roofUnit: RGB;
  landuse: Record<string, RGB>;
}

const linear = (hex: number): RGB => {
  const c = new Color(hex); // three converts sRGB hex to the linear working space
  return [c.r, c.g, c.b];
};

export function paletteFor(theme: TileTheme): Palette {
  const c: CityColors = theme === 'dark' ? CITY_NIGHT : CITY_DAY;
  return {
    block: linear(c.block),
    blockSide: linear(c.blockSide),
    road: linear(c.road),
    roadPath: linear(c.roadPath),
    roadMark: linear(c.roadMark),
    water: linear(c.water),
    park: linear(c.park),
    walls: c.walls.map(linear),
    roof: linear(c.roof),
    roofRim: linear(c.roofRim),
    roofAccent: linear(c.roofAccent),
    roofAccentRim: linear(c.roofAccentRim),
    roofUnit: linear(c.roofUnit),
    landuse: Object.fromEntries(Object.entries(c.landuse).map(([k, v]) => [k, linear(v)])),
  };
}

/** Top of the raised city blocks: every tile is a pale slab this high above the ground. */
export const GROUND_Y = 0.8;

/** Vertical order of the flat layers (metres) so they never z-fight. Roads stack by rank on top. */
export const LAYER_Y = {
  landuse: GROUND_Y + 0.02,
  park: GROUND_Y + 0.04,
  water: GROUND_Y + 0.06,
  waterRim: GROUND_Y + 0.07,
  road: GROUND_Y + 0.1,
  /** Height step per road rank (curb, surface and marks of one rank fit inside it). */
  roadRank: 0.04,
  bridge: 0.5,
} as const;

/** The highest a flat road layer gets (a top-rank bridge); route ribbons and the courier sit above it. */
export const ROAD_TOP_Y = LAYER_Y.road + 6 * LAYER_Y.roadRank + LAYER_Y.bridge;

/** A tile's rectangle in scene metres (local x depends only on lng and z only on lat, so it is axis aligned). */
export interface Bounds {
  minX: number;
  maxX: number;
  minZ: number;
  maxZ: number;
}

export function tileBounds(proj: Projector, extent: number): Bounds {
  const [x0, z0] = proj.project(0, 0);
  const [x1, z1] = proj.project(extent, extent);
  return { minX: Math.min(x0, x1), maxX: Math.max(x0, x1), minZ: Math.min(z0, z1), maxZ: Math.max(z0, z1) };
}

export function inBounds(b: Bounds, x: number, z: number): boolean {
  return x >= b.minX && x < b.maxX && z >= b.minZ && z < b.maxZ;
}

export interface MeshBuffers {
  positions: Float32Array;
  normals: Float32Array;
  indices: Uint32Array;
  colors: Float32Array;
}

/** Growable triangle mesh. Triangles are wound to face their vertex normal. */
export class MeshBuilder {
  private pos: number[] = [];
  private nor: number[] = [];
  private col: number[] = [];
  private idx: number[] = [];

  vertex(x: number, y: number, z: number, n: [number, number, number], c: RGB): number {
    this.pos.push(x, y, z);
    this.nor.push(n[0], n[1], n[2]);
    this.col.push(c[0], c[1], c[2]);
    return this.pos.length / 3 - 1;
  }

  tri(a: number, b: number, c: number): void {
    const p = this.pos;
    const ux = p[b * 3]! - p[a * 3]!;
    const uy = p[b * 3 + 1]! - p[a * 3 + 1]!;
    const uz = p[b * 3 + 2]! - p[a * 3 + 2]!;
    const vx = p[c * 3]! - p[a * 3]!;
    const vy = p[c * 3 + 1]! - p[a * 3 + 1]!;
    const vz = p[c * 3 + 2]! - p[a * 3 + 2]!;
    const nx = uy * vz - uz * vy;
    const ny = uz * vx - ux * vz;
    const nz = ux * vy - uy * vx;
    const dot = nx * this.nor[a * 3]! + ny * this.nor[a * 3 + 1]! + nz * this.nor[a * 3 + 2]!;
    if (dot === 0) return; // degenerate
    if (dot > 0) this.idx.push(a, b, c);
    else this.idx.push(a, c, b);
  }

  /** Vertices added so far. */
  get vertices(): number {
    return this.pos.length / 3;
  }

  get empty(): boolean {
    return this.idx.length === 0;
  }

  build(): MeshBuffers {
    return {
      positions: new Float32Array(this.pos),
      normals: new Float32Array(this.nor),
      indices: new Uint32Array(this.idx),
      colors: new Float32Array(this.col),
    };
  }
}

/** A decoded feature: MVT properties and rings in tile units (y down). */
export interface Feature {
  id: number | undefined;
  props: Record<string, string | number | boolean>;
  rings: { x: number; y: number }[][];
}

/** Converts tile-unit coordinates to scene metres (x east, z south) around the origin. */
export interface Projector {
  project(px: number, py: number): [number, number];
}

export function makeProjector(tile: TileXY, extent: number, geo: GeoProjection): Projector {
  const n = 2 ** tile.z;
  return {
    project(px, py) {
      const lng = ((tile.x + px / extent) / n) * 360 - 180;
      const lat = (Math.atan(Math.sinh(Math.PI * (1 - (2 * (tile.y + py / extent)) / n))) * 180) / Math.PI;
      const l = geo.toLocal({ lat, lng });
      return [l.x, l.z];
    },
  };
}

/** Splits MVT rings into polygons (exterior first, then holes), by winding. */
export function classifyRings(rings: { x: number; y: number }[][]): { x: number; y: number }[][][] {
  const polys: { x: number; y: number }[][][] = [];
  let cur: { x: number; y: number }[][] | undefined;
  let sign: boolean | undefined;
  for (const ring of rings) {
    let a = 0;
    for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) a += (ring[j]!.x - ring[i]!.x) * (ring[i]!.y + ring[j]!.y);
    if (a === 0) continue;
    if (sign === undefined) sign = a < 0;
    if (sign === a < 0) {
      cur = [ring];
      polys.push(cur);
    } else cur?.push(ring);
  }
  return polys;
}

/** Projects a polygon's rings; drops the closing duplicate point. */
export function projectPolygon(poly: { x: number; y: number }[][], proj: Projector): [number, number][][] {
  return poly.map((ring) => {
    const pts = ring.map((p) => proj.project(p.x, p.y));
    const f = pts[0]!;
    const l = pts[pts.length - 1]!;
    if (pts.length > 1 && f[0] === l[0] && f[1] === l[1]) pts.pop();
    return pts;
  });
}

/** Earcut via three. Returns the flat vertex list (exterior then holes) and triangle indices into it. */
export function triangulate(rings: [number, number][][]): { verts: [number, number][]; tris: number[] } {
  const contour = rings[0]!.map((p) => new Vector2(p[0], p[1]));
  const holes = rings.slice(1).map((r) => r.map((p) => new Vector2(p[0], p[1])));
  const faces = ShapeUtils.triangulateShape(contour, holes);
  const verts: [number, number][] = [];
  for (const v of [...contour, ...holes.flat()]) verts.push([v.x, v.y]);
  return { verts, tris: faces.flat() };
}

/** Small deterministic PRNG. */
export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Stable seed for a feature: its id, else a hash of its first vertex. */
export function featureSeed(f: Feature): number {
  if (f.id !== undefined) return Math.imul(f.id | 0, 2654435761) >>> 0;
  const p = f.rings[0]?.[0];
  return p ? (Math.imul(p.x | 0, 73856093) ^ Math.imul(p.y | 0, 19349663)) >>> 0 : 1;
}

export function flatArea(
  mesh: MeshBuilder,
  poly: { x: number; y: number }[][],
  proj: Projector,
  y: number,
  c: RGB,
): void {
  const rings = projectPolygon(poly, proj);
  if (rings[0]!.length < 3) return;
  const { verts, tris } = triangulate(rings);
  const ids = verts.map((v) => mesh.vertex(v[0], y, v[1], [0, 1, 0], c));
  for (let i = 0; i < tris.length; i += 3) mesh.tri(ids[tris[i]!]!, ids[tris[i + 1]!]!, ids[tris[i + 2]!]!);
}

/** A flat quad along segment a→b, `half` wide on each side, at height y (straight ends). */
export function segmentQuad(mesh: MeshBuilder, a: [number, number], b: [number, number], half: number, y: number, c: RGB): void {
  const dx = b[0] - a[0];
  const dz = b[1] - a[1];
  const l = Math.hypot(dx, dz);
  if (l < 1e-4) return;
  const nx = (-dz / l) * half;
  const nz = (dx / l) * half;
  const up: [number, number, number] = [0, 1, 0];
  const v0 = mesh.vertex(a[0] + nx, y, a[1] + nz, up, c);
  const v1 = mesh.vertex(a[0] - nx, y, a[1] - nz, up, c);
  const v2 = mesh.vertex(b[0] - nx, y, b[1] - nz, up, c);
  const v3 = mesh.vertex(b[0] + nx, y, b[1] + nz, up, c);
  mesh.tri(v0, v1, v2);
  mesh.tri(v0, v2, v3);
}

/** Even-odd point in polygon test on a projected ring. */
export function insideRing(ring: [number, number][], x: number, z: number): boolean {
  let c = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const [xi, zi] = ring[i]!;
    const [xj, zj] = ring[j]!;
    if (zi > z !== zj > z && x < ((xj - xi) * (z - zi)) / (zj - zi) + xi) c = !c;
  }
  return c;
}

/** Stable seed for one polygon of a (possibly merged) feature: a hash of its first vertex in tile units. */
export function polygonSeed(poly: { x: number; y: number }[][]): number {
  const p = poly[0]?.[0];
  if (!p) return 1;
  return (Math.imul(p.x | 0, 73856093) ^ Math.imul(p.y | 0, 19349663) ^ 0x9e3779b9) >>> 0;
}
