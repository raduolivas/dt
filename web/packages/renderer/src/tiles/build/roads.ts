import {
  LAYER_Y,
  MeshBuilder,
  inBounds,
  segmentQuad,
  type Bounds,
  type Feature,
  type MeshBuffers,
  type Palette,
  type Projector,
  type RGB,
} from './common';

export type RoadBuffers = MeshBuffers;

interface RoadStyle {
  /** Drawn width in metres: toy sized (wider than real) so the oversized courier fits the street. */
  width: number;
  rank: number;
  /** Paths are lighter and have no curb. */
  path?: boolean;
  /** Dashed centre line, as on the prototype's streets. */
  marks?: boolean;
}

const STYLES: Record<string, RoadStyle> = {
  motorway: { width: 16, rank: 5, marks: true },
  trunk: { width: 15, rank: 5, marks: true },
  primary: { width: 13, rank: 4, marks: true },
  secondary: { width: 12, rank: 3, marks: true },
  tertiary: { width: 11, rank: 3, marks: true },
  minor: { width: 8.5, rank: 2, marks: true },
  busway: { width: 7, rank: 2 },
  service: { width: 5, rank: 1 },
  raceway: { width: 6, rank: 1 },
  track: { width: 3, rank: 0, path: true },
  path: { width: 2.6, rank: 0, path: true },
  pier: { width: 4, rank: 0, path: true },
};
// Paths by OSM subclass (OpenMapTiles puts footway, cycleway, pedestrian... under `path`).
const PATHS: Record<string, RoadStyle> = {
  cycleway: { width: 3.2, rank: 0, path: true },
  pedestrian: { width: 5, rank: 0, path: true },
  footway: { width: 2.4, rank: 0, path: true },
  steps: { width: 2.4, rank: 0, path: true },
};

const CURB_M = 0.8; // per side
const DASH_M = 5;
const DASH_PERIOD_M = 9;
const DASH_HALF_W = 0.35;
const MITER_LIMIT = 2.5;

function styleOf(f: Feature): RoadStyle | undefined {
  const cls = String(f.props.class ?? '');
  if (cls === 'path') return PATHS[String(f.props.subclass ?? '')] ?? STYLES.path;
  return STYLES[cls];
}

/**
 * Flat road ribbons in the prototype's road color, width by class, on a curb in the block side color (so the pale
 * blocks read as raised), with the light dashed centre line on streets. Footways and paths are thinner and lighter.
 * Bridges sit slightly raised. Tunnels, rail, ferries and cable cars are not drawn.
 */
export function buildRoads(features: Feature[], proj: Projector, palette: Palette, bounds?: Bounds): RoadBuffers {
  const mesh = new MeshBuilder();
  for (const f of features) {
    const style = styleOf(f);
    if (!style || f.props.brunnel === 'tunnel') continue;
    const bridge = f.props.brunnel === 'bridge' ? LAYER_Y.bridge : 0;
    const base = LAYER_Y.road + style.rank * LAYER_Y.roadRank + bridge;
    const color: RGB = style.path ? palette.roadPath : palette.road;
    for (const line of f.rings) {
      const pts: [number, number][] = [];
      for (const p of line) {
        const q = proj.project(p.x, p.y);
        const last = pts[pts.length - 1];
        if (!last || Math.hypot(q[0] - last[0], q[1] - last[1]) > 0.01) pts.push(q);
      }
      if (pts.length < 2) continue;
      if (!style.path) ribbon(mesh, pts, style.width / 2 + CURB_M, base, palette.blockSide);
      ribbon(mesh, pts, style.width / 2, base + 0.015, color);
      if (style.marks) dashes(mesh, pts, base + 0.03, palette.roadMark, bounds);
    }
  }
  return mesh.build();
}

/** Dashes along the centre line; a dash is kept only when its middle is inside the tile (tiles overlap a little). */
function dashes(mesh: MeshBuilder, pts: [number, number][], y: number, c: RGB, bounds?: Bounds): void {
  let phase = 2; // metres into the current period
  for (let i = 1; i < pts.length; i++) {
    const a = pts[i - 1]!;
    const b = pts[i]!;
    const len = Math.hypot(b[0] - a[0], b[1] - a[1]);
    const ux = (b[0] - a[0]) / len;
    const uz = (b[1] - a[1]) / len;
    let t = 0;
    while (t < len) {
      const inDash = phase < DASH_M;
      const step = Math.min(len - t, inDash ? DASH_M - phase : DASH_PERIOD_M - phase);
      if (inDash && step > 0.4) {
        const s: [number, number] = [a[0] + ux * t, a[1] + uz * t];
        const e: [number, number] = [a[0] + ux * (t + step), a[1] + uz * (t + step)];
        if (!bounds || inBounds(bounds, (s[0] + e[0]) / 2, (s[1] + e[1]) / 2)) segmentQuad(mesh, s, e, DASH_HALF_W, y, c);
      }
      t += step;
      phase = (phase + step) % DASH_PERIOD_M;
    }
  }
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
    let p = pts[i]!;
    // Round-ish caps: extend the free ends by half a width so streets meet without notches.
    if (i === 0) p = [p[0] - d1[0] * half * 0.8, p[1] - d1[1] * half * 0.8];
    if (i === pts.length - 1) p = [p[0] + d0[0] * half * 0.8, p[1] + d0[1] * half * 0.8];
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
