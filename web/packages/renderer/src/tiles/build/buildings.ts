import {
  MeshBuilder,
  classifyRings,
  insideRing,
  mulberry32,
  polygonSeed,
  projectPolygon,
  segmentQuad,
  triangulate,
  type Feature,
  type MeshBuffers,
  type Palette,
  type Projector,
  type RGB,
} from './common';

/**
 * Footprints, so the main thread can find the building that contains the store or the home and give it the
 * prototype's store/house treatment (and hide the generic one). One entry per polygon (OpenMapTiles merges many
 * buildings into one multipolygon feature; each polygon is one building).
 * - `coords`: x, z pairs of every ring.
 * - `rings`: stride 4 `[firstPoint, pointCount, building, isHole]`.
 * - `info`: stride 4 `[height, minHeight, firstVertex, vertexCount]` (heights after the toy cap).
 */
export interface FootprintBuffers {
  coords: Float32Array;
  rings: Uint32Array;
  info: Float32Array;
}

export type BuildingBuffers = MeshBuffers & { uvs: Float32Array };

export const DEFAULT_BUILDING_HEIGHT = 9;

/**
 * Toy height (Claude's call, spec §5.2): up to 9 m (the default) unchanged, then a soft cap easing towards 22 m.
 * Amsterdam's streets are narrow; real heights would hide the street, the route and the courier at the prototype's
 * camera angle, and the oversized courier needs a toy-sized city around it.
 */
export function toyHeight(h: number): number {
  if (h <= 9) return h;
  return 9 + 13 * (1 - Math.exp(-(h - 9) / 18));
}

const WINDOW_M = 6.5; // one window cell of the texture, like the prototype's 7 m repeat
const RIM_HALF_M = 0.45; // the lighter roof edge, half inside and half overhanging
const UNIT_MIN_AREA = 120; // m², buildings big enough for a rooftop unit
const ACCENT_SHARE = 0.14; // soft periwinkle roofs for variety (strong blue is kept for the route and the store)
// A UV inside the texture's white frame: roofs and the rim stay plain.
const PLAIN_U = 0.04;
const PLAIN_V = 0.04;

/** Stable per-building tint, seeded from the building polygon. */
export function buildingTint(seed: number, palette: Palette): RGB {
  const rnd = mulberry32(seed);
  const base = palette.walls[Math.floor(rnd() * palette.walls.length)]!;
  const k = 0.96 + rnd() * 0.06;
  return [Math.min(1, base[0] * k), Math.min(1, base[1] * k), Math.min(1, base[2] * k)];
}

class UvMesh extends MeshBuilder {
  readonly uv: number[] = [];
  v(x: number, y: number, z: number, n: [number, number, number], c: RGB, u: number, w: number): number {
    this.uv.push(u, w);
    return this.vertex(x, y, z, n, c);
  }
  /** Pads UVs for vertices added through the plain `vertex()` (they get the plain UV). */
  padUvs(): void {
    while (this.uv.length / 2 < this.vertices) this.uv.push(PLAIN_U, PLAIN_V);
  }
}

/**
 * Extrudes footprints from `render_min_height` to `render_height` (default 9 m, soft capped). Walls carry UVs for
 * the prototype's window texture (whole windows per wall), flat roofs get a small lighter rim, some roofs are the
 * soft periwinkle and bigger roofs get a seeded rooftop unit.
 */
export function buildBuildings(
  features: Feature[],
  proj: Projector,
  palette: Palette,
): BuildingBuffers & { footprints: FootprintBuffers } {
  const mesh = new UvMesh();
  const coords: number[] = [];
  const rings: number[] = [];
  const info: number[] = [];
  for (const f of features) {
    if (f.props.hide_3d === true || f.props.hide_3d === 1) continue; // outline of a building with parts
    const raw = Number(f.props.render_height ?? DEFAULT_BUILDING_HEIGHT) || DEFAULT_BUILDING_HEIGHT;
    const rawMin = Number(f.props.render_min_height ?? 0) || 0;
    if (raw <= rawMin) continue;
    const h = Math.max(3, toyHeight(raw));
    const minH = rawMin * (h / raw);
    for (const poly of classifyRings(f.rings)) {
      const projected = projectPolygon(poly, proj);
      if (projected[0]!.length < 3) continue;
      const first = mesh.vertices;
      addBuilding(mesh, projected, h, minH, mulberry32(polygonSeed(poly)), palette);
      const b = info.length / 4;
      projected.forEach((ring, ri) => {
        rings.push(coords.length / 2, ring.length, b, ri === 0 ? 0 : 1);
        for (const [x, z] of ring) coords.push(x, z);
      });
      info.push(h, minH, first, mesh.vertices - first);
    }
  }
  return {
    ...mesh.build(),
    uvs: new Float32Array(mesh.uv),
    footprints: { coords: new Float32Array(coords), rings: new Uint32Array(rings), info: new Float32Array(info) },
  };
}

function ringArea(ring: [number, number][]): number {
  let a = 0;
  for (let i = 0; i < ring.length; i++) {
    const p = ring[i]!;
    const q = ring[(i + 1) % ring.length]!;
    a += p[0] * q[1] - q[0] * p[1];
  }
  return a / 2;
}

function addBuilding(mesh: UvMesh, rings: [number, number][][], h: number, minH: number, rnd: () => number, palette: Palette): void {
  const tint = buildingTint(Math.floor(rnd() * 2 ** 32), palette);
  const accent = rnd() < ACCENT_SHARE;
  const roof = accent ? palette.roofAccent : palette.roof;
  const rim = accent ? palette.roofAccentRim : palette.roofRim;
  const rows = h - minH >= 4 ? Math.max(1, Math.round((h - minH) / WINDOW_M)) : 0;

  // Walls: every edge of every ring, facing away from the polygon interior, with whole windows.
  rings.forEach((ring, ri) => {
    const outward = (ri === 0 ? 1 : -1) * Math.sign(ringArea(ring));
    for (let i = 0; i < ring.length; i++) {
      const a = ring[i]!;
      const b = ring[(i + 1) % ring.length]!;
      const dx = b[0] - a[0];
      const dz = b[1] - a[1];
      const l = Math.hypot(dx, dz);
      if (l < 1e-4) continue;
      const n: [number, number, number] = [(dz / l) * outward, 0, (-dx / l) * outward];
      const cols = l >= 3 && rows > 0 ? Math.max(1, Math.round(l / WINDOW_M)) : 0;
      const u1 = cols > 0 ? cols : PLAIN_U;
      const v1 = rows > 0 ? rows : PLAIN_V;
      const u0 = cols > 0 ? 0 : PLAIN_U;
      const v0 = rows > 0 ? 0 : PLAIN_V;
      const w0 = mesh.v(a[0], minH, a[1], n, tint, u0, v0);
      const w1 = mesh.v(b[0], minH, b[1], n, tint, u1, v0);
      const w2 = mesh.v(b[0], h, b[1], n, tint, u1, v1);
      const w3 = mesh.v(a[0], h, a[1], n, tint, u0, v1);
      mesh.tri(w0, w1, w2);
      mesh.tri(w0, w2, w3);
    }
  });

  // Flat roof.
  const { verts, tris } = triangulate(rings);
  const ids = verts.map((v) => mesh.v(v[0], h, v[1], [0, 1, 0], roof, PLAIN_U, PLAIN_V));
  for (let i = 0; i < tris.length; i += 3) mesh.tri(ids[tris[i]!]!, ids[tris[i + 1]!]!, ids[tris[i + 2]!]!);

  // Lighter rim along the outline, a hair above the roof.
  const outer = rings[0]!;
  for (let i = 0; i < outer.length; i++) segmentQuad(mesh, outer[i]!, outer[(i + 1) % outer.length]!, RIM_HALF_M, h + 0.04, rim);
  mesh.padUvs();

  // Rooftop unit (the prototype's grey box), at the centroid when it lies inside.
  const area = Math.abs(ringArea(outer));
  if (area >= UNIT_MIN_AREA && rnd() < 0.6) {
    let cx = 0;
    let cz = 0;
    for (const [x, z] of outer) {
      cx += x;
      cz += z;
    }
    cx /= outer.length;
    cz /= outer.length;
    if (insideRing(outer, cx, cz)) {
      const s = Math.min(7, Math.max(2, Math.sqrt(area) * 0.25)) / 2;
      roofUnit(mesh, cx + (rnd() - 0.5) * s, cz + (rnd() - 0.5) * s, s, h, 2.2, palette.roofUnit);
    }
  }
}

function roofUnit(mesh: UvMesh, cx: number, cz: number, s: number, y: number, uh: number, c: RGB): void {
  const top = y + uh;
  const q = (pts: [number, number, number][], n: [number, number, number]) => {
    const ids = pts.map((p) => mesh.v(p[0], p[1], p[2], n, c, PLAIN_U, PLAIN_V));
    mesh.tri(ids[0]!, ids[1]!, ids[2]!);
    mesh.tri(ids[0]!, ids[2]!, ids[3]!);
  };
  const x0 = cx - s;
  const x1 = cx + s;
  const z0 = cz - s;
  const z1 = cz + s;
  q([[x0, top, z0], [x1, top, z0], [x1, top, z1], [x0, top, z1]], [0, 1, 0]);
  q([[x0, y, z1], [x1, y, z1], [x1, top, z1], [x0, top, z1]], [0, 0, 1]);
  q([[x1, y, z0], [x0, y, z0], [x0, top, z0], [x1, top, z0]], [0, 0, -1]);
  q([[x1, y, z1], [x1, y, z0], [x1, top, z0], [x1, top, z1]], [1, 0, 0]);
  q([[x0, y, z0], [x0, y, z1], [x0, top, z1], [x0, top, z0]], [-1, 0, 0]);
}
