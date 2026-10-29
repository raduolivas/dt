import {
  LAYER_Y,
  MeshBuilder,
  classifyRings,
  featureSeed,
  flatArea,
  mulberry32,
  projectPolygon,
  type Feature,
  type MeshBuffers,
  type Palette,
  type Projector,
} from './common';

export type AreaBuffers = MeshBuffers;
export type ParkBuffers = AreaBuffers & { trees: Float32Array };

/** Water polygons. */
export function buildWater(features: Feature[], proj: Projector, palette: Palette): AreaBuffers {
  const mesh = new MeshBuilder();
  for (const f of features) for (const poly of classifyRings(f.rings)) flatArea(mesh, poly, proj, LAYER_Y.water, palette.water);
  return mesh.build();
}

/** Residential, industrial and similar land use tints. */
export function buildLanduse(features: Feature[], proj: Projector, palette: Palette): AreaBuffers {
  const mesh = new MeshBuilder();
  for (const f of features) {
    const c = palette.landuse[String(f.props.class ?? '')] ?? palette.landuseDefault;
    for (const poly of classifyRings(f.rings)) flatArea(mesh, poly, proj, LAYER_Y.landuse, c);
  }
  return mesh.build();
}

const TREE_SPACING_M2 = 450;
const MAX_TREES_PER_FEATURE = 120;

/** Green areas plus scattered tree positions `[x, z, scale]` (stride 3), seeded per feature. */
export function buildParks(features: Feature[], proj: Projector, palette: Palette): ParkBuffers {
  const mesh = new MeshBuilder();
  const trees: number[] = [];
  for (const f of features) {
    for (const poly of classifyRings(f.rings)) {
      flatArea(mesh, poly, proj, LAYER_Y.park, palette.park);
      scatterTrees(poly, proj, mulberry32(featureSeed(f)), trees);
    }
  }
  return { ...mesh.build(), trees: new Float32Array(trees) };
}

function scatterTrees(poly: { x: number; y: number }[][], proj: Projector, rnd: () => number, out: number[]): void {
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
  const want = Math.min(MAX_TREES_PER_FEATURE, Math.floor(boxArea / TREE_SPACING_M2));
  let placed = 0;
  for (let tries = 0; placed < want && tries < want * 6; tries++) {
    const x = minX + rnd() * (maxX - minX);
    const z = minZ + rnd() * (maxZ - minZ);
    const scale = 0.8 + rnd() * 0.5;
    if (!inside(outer, x, z) || rings.slice(1).some((h) => inside(h, x, z))) continue;
    out.push(x, z, scale);
    placed++;
  }
}

function inside(ring: [number, number][], x: number, z: number): boolean {
  let c = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const [xi, zi] = ring[i]!;
    const [xj, zj] = ring[j]!;
    if (zi > z !== zj > z && x < ((xj - xi) * (z - zi)) / (zj - zi) + xi) c = !c;
  }
  return c;
}
