import { LAYER_Y, MeshBuilder, type Feature, type MeshBuffers, type Palette, type Projector, type RGB } from './common';

export type RoadBuffers = MeshBuffers;

interface RoadStyle {
  width: number;
  rank: number;
  color: (p: Palette) => RGB;
}

const STYLES: Record<string, RoadStyle> = {
  motorway: { width: 12, rank: 5, color: (p) => p.roadMajor },
  trunk: { width: 11, rank: 5, color: (p) => p.roadMajor },
  primary: { width: 10, rank: 4, color: (p) => p.roadMajor },
  secondary: { width: 8, rank: 3, color: (p) => p.roadMid },
  tertiary: { width: 7, rank: 3, color: (p) => p.roadMid },
  minor: { width: 5, rank: 2, color: (p) => p.roadMinor },
  service: { width: 3.5, rank: 1, color: (p) => p.roadMinor },
  busway: { width: 4, rank: 1, color: (p) => p.roadMid },
  raceway: { width: 6, rank: 1, color: (p) => p.roadMinor },
  track: { width: 2.5, rank: 0, color: (p) => p.roadPath },
  path: { width: 2, rank: 0, color: (p) => p.roadPath },
  pedestrian: { width: 3, rank: 0, color: (p) => p.roadPath },
};

const MITER_LIMIT = 2.5;

/** Flat ribbons for the `transportation` lines. Tunnels, rail, ferries and cable cars are not drawn. */
export function buildRoads(features: Feature[], proj: Projector, palette: Palette): RoadBuffers {
  const mesh = new MeshBuilder();
  // Draw big roads last so they sit on top (via y), smaller first.
  for (const f of features) {
    const cls = String(f.props.class ?? '');
    const style = STYLES[cls];
    if (!style || f.props.brunnel === 'tunnel') continue;
    const color = style.color(palette);
    const y = LAYER_Y.road + style.rank * 0.01;
    for (const line of f.rings) {
      const pts: [number, number][] = [];
      for (const p of line) {
        const q = proj.project(p.x, p.y);
        const last = pts[pts.length - 1];
        if (!last || Math.hypot(q[0] - last[0], q[1] - last[1]) > 0.01) pts.push(q);
      }
      ribbon(mesh, pts, style.width / 2, y, color);
    }
  }
  return mesh.build();
}

function ribbon(mesh: MeshBuilder, pts: [number, number][], half: number, y: number, c: RGB): void {
  if (pts.length < 2) return;
  const dirs: [number, number][] = [];
  for (let i = 1; i < pts.length; i++) {
    const dx = pts[i]![0] - pts[i - 1]![0];
    const dz = pts[i]![1] - pts[i - 1]![1];
    const l = Math.hypot(dx, dz);
    dirs.push([dx / l, dz / l]);
  }
  let prevL = -1;
  let prevR = -1;
  for (let i = 0; i < pts.length; i++) {
    const d0 = dirs[Math.max(0, i - 1)]!;
    const d1 = dirs[Math.min(dirs.length - 1, i)]!;
    // Left normals of the two adjacent segments, averaged into a miter.
    let nx = -(d0[1] + d1[1]);
    let nz = d0[0] + d1[0];
    const nl = Math.hypot(nx, nz);
    if (nl < 1e-6) {
      nx = -d1[1];
      nz = d1[0];
    } else {
      nx /= nl;
      nz /= nl;
    }
    const cos = Math.max(1 / MITER_LIMIT, nx * -d1[1] + nz * d1[0]);
    const off = half / cos;
    const p = pts[i]!;
    const l = mesh.vertex(p[0] + nx * off, y, p[1] + nz * off, [0, 1, 0], c);
    const r = mesh.vertex(p[0] - nx * off, y, p[1] - nz * off, [0, 1, 0], c);
    if (prevL >= 0) {
      mesh.tri(prevL, prevR, l);
      mesh.tri(prevR, r, l);
    }
    prevL = l;
    prevR = r;
  }
}
