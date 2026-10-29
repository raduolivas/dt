import {
  GROUND_Y,
  LAYER_Y,
  MeshBuilder,
  classifyRings,
  flatArea,
  inBounds,
  insideRing,
  mulberry32,
  polygonSeed,
  projectPolygon,
  segmentQuad,
  type Bounds,
  type Feature,
  type MeshBuffers,
  type Palette,
  type Projector,
  type RGB,
} from './common';

export type AreaBuffers = MeshBuffers;
export type ParkBuffers = AreaBuffers & { trees: Float32Array };

const QUAY_HALF_M = 0.7;

/**
 * Water: a soft blue plane with a quay edge in the block side color (so canals read as sunk between the raised
 * blocks). Ring edges that only exist because the tile cut the polygon get no quay.
 */
export function buildWater(features: Feature[], proj: Projector, palette: Palette, bounds?: Bounds): AreaBuffers {
  const mesh = new MeshBuilder();
  for (const f of features) {
    for (const poly of classifyRings(f.rings)) {
      flatArea(mesh, poly, proj, LAYER_Y.water, palette.water);
      for (const ring of projectPolygon(poly, proj)) {
        for (let i = 0; i < ring.length; i++) {
          const a = ring[i]!;
          const b = ring[(i + 1) % ring.length]!;
          if (bounds && onTileCut(bounds, a, b)) continue;
          segmentQuad(mesh, a, b, QUAY_HALF_M, LAYER_Y.waterRim, palette.blockSide);
        }
      }
    }
  }
  return mesh.build();
}

/** True when the edge lies on (or beyond) one tile side: an artefact of clipping, not a real shore. */
function onTileCut(b: Bounds, p: [number, number], q: [number, number]): boolean {
  const e = 0.5;
  return (
    (p[0] <= b.minX + e && q[0] <= b.minX + e) ||
    (p[0] >= b.maxX - e && q[0] >= b.maxX - e) ||
    (p[1] <= b.minZ + e && q[1] <= b.minZ + e) ||
    (p[1] >= b.maxZ - e && q[1] >= b.maxZ - e)
  );
}

/**
 * The tile's raised pale slab (top at GROUND_Y, sides down to the ground, so the loaded city stands like a diorama
 * block) plus the faint land use tints.
 */
export function buildLanduse(features: Feature[], proj: Projector, palette: Palette, bounds?: Bounds): AreaBuffers {
  const mesh = new MeshBuilder();
  if (bounds) slab(mesh, bounds, palette.block, palette.blockSide);
  for (const f of features) {
    const c = palette.landuse[String(f.props.class ?? '')];
    if (!c) continue;
    for (const poly of classifyRings(f.rings)) flatArea(mesh, poly, proj, LAYER_Y.landuse, c);
  }
  return mesh.build();
}

function slab(mesh: MeshBuilder, b: Bounds, top: RGB, side: RGB): void {
  const up: [number, number, number] = [0, 1, 0];
  const t = [
    mesh.vertex(b.minX, GROUND_Y, b.minZ, up, top),
    mesh.vertex(b.maxX, GROUND_Y, b.minZ, up, top),
    mesh.vertex(b.maxX, GROUND_Y, b.maxZ, up, top),
    mesh.vertex(b.minX, GROUND_Y, b.maxZ, up, top),
  ];
  mesh.tri(t[0]!, t[1]!, t[2]!);
  mesh.tri(t[0]!, t[2]!, t[3]!);
  const corners: [number, number][] = [
    [b.minX, b.minZ],
    [b.maxX, b.minZ],
    [b.maxX, b.maxZ],
    [b.minX, b.maxZ],
  ];
  for (let i = 0; i < 4; i++) {
    const p = corners[i]!;
    const q = corners[(i + 1) % 4]!;
    const dx = q[0] - p[0];
    const dz = q[1] - p[1];
    const l = Math.hypot(dx, dz);
    const n: [number, number, number] = [dz / l, 0, -dx / l]; // the ring runs clockwise on screen: outward
    const v0 = mesh.vertex(p[0], 0, p[1], n, side);
    const v1 = mesh.vertex(q[0], 0, q[1], n, side);
    const v2 = mesh.vertex(q[0], GROUND_Y, q[1], n, side);
    const v3 = mesh.vertex(p[0], GROUND_Y, p[1], n, side);
    mesh.tri(v0, v1, v2);
    mesh.tri(v0, v2, v3);
  }
}

/** m² of green per tree by kind of green; 0 = lawn only. */
export const TREE_M2: Record<string, number> = {
  wood: 110,
  forest: 110,
  park: 260,
  garden: 380,
  cemetery: 300,
  recreation_ground: 600,
  village_green: 600,
  grass: 900,
  scrub: 500,
};
const MAX_TREES_PER_POLYGON = 160;

/** Green areas plus scattered tree positions `[x, z, scale]` (stride 3), seeded per polygon, kept inside the tile. */
export function buildParks(features: Feature[], proj: Projector, palette: Palette, bounds?: Bounds): ParkBuffers {
  const mesh = new MeshBuilder();
  const trees: number[] = [];
  for (const f of features) {
    const perTree = TREE_M2[String(f.props.class ?? '')] ?? 0;
    for (const poly of classifyRings(f.rings)) {
      flatArea(mesh, poly, proj, LAYER_Y.park, palette.park);
      if (perTree > 0) scatterTrees(poly, proj, perTree, mulberry32(polygonSeed(poly)), trees, bounds);
    }
  }
  return { ...mesh.build(), trees: new Float32Array(trees) };
}

function scatterTrees(
  poly: { x: number; y: number }[][],
  proj: Projector,
  perTree: number,
  rnd: () => number,
  out: number[],
  bounds?: Bounds,
): void {
  const rings = projectPolygon(poly, proj);
  const outer = rings[0]!;
  if (outer.length < 3) return;
  let minX = Infinity;
  let maxX = -Infinity;
  let minZ = Infinity;
  let maxZ = -Infinity;
  for (const [x, z] of outer) {
    minX = Math.min(minX, x);
    maxX = Math.max(maxX, x);
    minZ = Math.min(minZ, z);
    maxZ = Math.max(maxZ, z);
  }
  const boxArea = (maxX - minX) * (maxZ - minZ);
  const want = Math.min(MAX_TREES_PER_POLYGON, Math.floor(boxArea / perTree));
  for (let tries = 0; tries < want; tries++) {
    // Same random draws whatever is rejected, so a tree never moves between loads.
    const x = minX + rnd() * (maxX - minX);
    const z = minZ + rnd() * (maxZ - minZ);
    const scale = 0.8 + rnd() * 0.6;
    if (bounds && !inBounds(bounds, x, z)) continue;
    if (!insideRing(outer, x, z) || rings.slice(1).some((h) => insideRing(h, x, z))) continue;
    out.push(x, z, scale);
  }
}

const QUAY_TREE_SPACING_M = 11;
const QUAY_TREE_OFFSET_M = 2.8;

/**
 * Tree rows along the quays (Amsterdam's canals are lined with trees): seeded positions just inland of every real
 * shore edge, `[x, z, scale]` stride 3. `blocked` rejects spots taken by a building.
 */
export function quayTrees(
  features: Feature[],
  proj: Projector,
  bounds: Bounds,
  blocked: (x: number, z: number) => boolean,
): number[] {
  const out: number[] = [];
  for (const f of features) {
    for (const poly of classifyRings(f.rings)) {
      const rnd = mulberry32(polygonSeed(poly) ^ 0x51ed27);
      projectPolygon(poly, proj).forEach((ring, ri) => {
        let area = 0;
        for (let i = 0; i < ring.length; i++) {
          const a = ring[i]!;
          const b = ring[(i + 1) % ring.length]!;
          area += a[0] * b[1] - b[0] * a[1];
        }
        if (Math.abs(area / 2) < 300) return; // ponds and fountains
        const outward = (ri === 0 ? 1 : -1) * Math.sign(area);
        let carry = rnd() * QUAY_TREE_SPACING_M;
        for (let i = 0; i < ring.length; i++) {
          const a = ring[i]!;
          const b = ring[(i + 1) % ring.length]!;
          const dx = b[0] - a[0];
          const dz = b[1] - a[1];
          const l = Math.hypot(dx, dz);
          if (l < 1e-3) continue;
          if (onTileCut(bounds, a, b)) {
            carry = rnd() * QUAY_TREE_SPACING_M;
            continue;
          }
          const nx = (dz / l) * outward;
          const nz = (-dx / l) * outward;
          let t = carry;
          for (; t < l; t += QUAY_TREE_SPACING_M * (0.85 + rnd() * 0.3)) {
            const x = a[0] + (dx / l) * t + nx * QUAY_TREE_OFFSET_M;
            const z = a[1] + (dz / l) * t + nz * QUAY_TREE_OFFSET_M;
            const scale = 0.85 + rnd() * 0.45;
            if (!inBounds(bounds, x, z) || blocked(x, z)) continue;
            out.push(x, z, scale);
          }
          carry = t - l;
        }
      });
    }
  }
  return out;
}
