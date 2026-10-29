import {
  MeshBuilder,
  classifyRings,
  featureSeed,
  mulberry32,
  projectPolygon,
  triangulate,
  type Feature,
  type MeshBuffers,
  type Palette,
  type Projector,
  type RGB,
} from './common';

export type BuildingBuffers = MeshBuffers;

export const DEFAULT_BUILDING_HEIGHT = 9;

/** Stable per-building tint: a palette colour and a brightness jitter, both seeded from the feature. */
export function buildingTint(f: Feature, palette: Palette): RGB {
  const rnd = mulberry32(featureSeed(f));
  const base = palette.buildings[Math.floor(rnd() * palette.buildings.length)]!;
  const k = 0.92 + rnd() * 0.16;
  return [Math.min(1, base[0] * k), Math.min(1, base[1] * k), Math.min(1, base[2] * k)];
}

/** Extrudes footprints from `render_min_height` to `render_height` (default 9 m). */
export function buildBuildings(features: Feature[], proj: Projector, palette: Palette): BuildingBuffers {
  const mesh = new MeshBuilder();
  for (const f of features) {
    if (f.props.hide_3d === true || f.props.hide_3d === 1) continue; // outline of a building with parts
    const h = Number(f.props.render_height ?? DEFAULT_BUILDING_HEIGHT) || DEFAULT_BUILDING_HEIGHT;
    const minH = Number(f.props.render_min_height ?? 0) || 0;
    if (h <= minH) continue;
    const tint = buildingTint(f, palette);
    const wall: RGB = [tint[0] * 0.9, tint[1] * 0.9, tint[2] * 0.9];
    for (const poly of classifyRings(f.rings)) {
      const rings = projectPolygon(poly, proj);
      if (rings[0]!.length < 3) continue;
      // Walls: every edge of every ring, facing away from the polygon interior.
      rings.forEach((ring, ri) => {
        let area = 0;
        for (let i = 0; i < ring.length; i++) {
          const a = ring[i]!;
          const b = ring[(i + 1) % ring.length]!;
          area += a[0] * b[1] - b[0] * a[1];
        }
        // Exterior rings point outward, holes inward (towards the hole's centre, away from the solid).
        const outward = (ri === 0 ? 1 : -1) * Math.sign(area);
        for (let i = 0; i < ring.length; i++) {
          const a = ring[i]!;
          const b = ring[(i + 1) % ring.length]!;
          const dx = b[0] - a[0];
          const dz = b[1] - a[1];
          const l = Math.hypot(dx, dz);
          if (l < 1e-4) continue;
          const n: [number, number, number] = [(dz / l) * outward, 0, (-dx / l) * outward];
          const v0 = mesh.vertex(a[0], minH, a[1], n, wall);
          const v1 = mesh.vertex(b[0], minH, b[1], n, wall);
          const v2 = mesh.vertex(b[0], h, b[1], n, wall);
          const v3 = mesh.vertex(a[0], h, a[1], n, wall);
          mesh.tri(v0, v1, v2);
          mesh.tri(v0, v2, v3);
        }
      });
      const { verts, tris } = triangulate(rings);
      const ids = verts.map((v) => mesh.vertex(v[0], h, v[1], [0, 1, 0], tint));
      for (let i = 0; i < tris.length; i += 3) mesh.tri(ids[tris[i]!]!, ids[tris[i + 1]!]!, ids[tris[i + 2]!]!);
    }
  }
  return mesh.build();
}
