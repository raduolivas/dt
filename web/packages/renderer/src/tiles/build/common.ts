import { ShapeUtils, Vector2 } from 'three';
import { GeoProjection } from '../../engine/geo';
import type { TileXY } from '../corridor';

export type TileTheme = 'light' | 'dark';
export type RGB = [number, number, number];

export interface Palette {
  roadMajor: RGB;
  roadMid: RGB;
  roadMinor: RGB;
  roadPath: RGB;
  buildings: RGB[];
  water: RGB;
  park: RGB;
  landuse: Record<string, RGB>;
  landuseDefault: RGB;
}

const hex = (h: string): RGB => [
  parseInt(h.slice(1, 3), 16) / 255,
  parseInt(h.slice(3, 5), 16) / 255,
  parseInt(h.slice(5, 7), 16) / 255,
];

export function paletteFor(theme: TileTheme): Palette {
  if (theme === 'dark') {
    return {
      roadMajor: hex('#5a6478'),
      roadMid: hex('#4a5266'),
      roadMinor: hex('#3e455a'),
      roadPath: hex('#343a4c'),
      buildings: ['#6d6480', '#64708a', '#7a6a78', '#5e6c7c', '#72668a', '#6a7684'].map(hex),
      water: hex('#1f3f66'),
      park: hex('#2b5a3c'),
      landuse: { residential: hex('#2a3042'), industrial: hex('#323244'), commercial: hex('#2e3446') },
      landuseDefault: hex('#2a3042'),
    };
  }
  return {
    roadMajor: hex('#ffd9a0'),
    roadMid: hex('#fff2d6'),
    roadMinor: hex('#ffffff'),
    roadPath: hex('#e8dcc8'),
    buildings: ['#f2c6a0', '#e9d8c4', '#f4b9a8', '#d9c9b8', '#f0dca8', '#c9d6c0'].map(hex),
    water: hex('#8fd0f0'),
    park: hex('#a8dc9c'),
    landuse: { residential: hex('#efe6d2'), industrial: hex('#e2dccf'), commercial: hex('#f1e2cf') },
    landuseDefault: hex('#efe6d2'),
  };
}

/** Vertical order of flat layers (metres) so they never z-fight. */
export const LAYER_Y = { landuse: 0.01, park: 0.02, water: 0.03, road: 0.05 } as const;

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
