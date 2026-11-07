// Camera maths for the isometric orthographic view. Pure (plain tuples), so the
// fit and the reduced-motion rule are unit tested without WebGL.
export type Vec3 = [number, number, number];
export interface CameraPose { position: Vec3; target: Vec3; zoom: number }

/**
 * Isometric elevation (arctan(1/sqrt 2) = 35.26 degrees down). The azimuth is
 * 22.5 degrees rather than the textbook 45 so the five platforms run across the
 * screen and the cubes on a platform stack down it, which keeps every label readable.
 */
export const ISO_AZIMUTH = Math.PI / 8;
export const ISO_ELEVATION = Math.atan(1 / Math.SQRT2);
export const CAMERA_DISTANCE = 80;
export const TWEEN_MS = 600;
export const MIN_ZOOM = 6;
export const MAX_ZOOM = 160;

function isoDir(): Vec3 {
  const c = Math.cos(ISO_ELEVATION);
  return [c * Math.sin(ISO_AZIMUTH), Math.sin(ISO_ELEVATION), c * Math.cos(ISO_AZIMUTH)];
}

export function isoPosition(target: Vec3, distance: number = CAMERA_DISTANCE): Vec3 {
  const d = isoDir();
  return [target[0] + d[0] * distance, target[1] + d[1] * distance, target[2] + d[2] * distance];
}

const cross = (a: Vec3, b: Vec3): Vec3 => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const dot = (a: Vec3, b: Vec3) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const norm = (a: Vec3): Vec3 => {
  const l = Math.hypot(...a) || 1;
  return [a[0] / l, a[1] / l, a[2] / l];
};

/**
 * The orthographic zoom (pixels per world unit, as three's OrthographicCamera
 * in react-three-fiber uses it) at which the bounds fill `margin` of a
 * width x height viewport seen from the isometric direction.
 */
export function fitZoom(b: { min: Vec3; max: Vec3 }, width: number, height: number, margin = 0.88): number {
  const d = isoDir();
  const f: Vec3 = [-d[0], -d[1], -d[2]];
  const right = norm(cross(f, [0, 1, 0]));
  const up = cross(right, f);
  let rx0 = Infinity, rx1 = -Infinity, uy0 = Infinity, uy1 = -Infinity;
  for (const x of [b.min[0], b.max[0]]) {
    for (const y of [b.min[1], b.max[1]]) {
      for (const z of [b.min[2], b.max[2]]) {
        const p: Vec3 = [x, y, z];
        const r = dot(p, right);
        const u = dot(p, up);
        rx0 = Math.min(rx0, r); rx1 = Math.max(rx1, r);
        uy0 = Math.min(uy0, u); uy1 = Math.max(uy1, u);
      }
    }
  }
  const ex = Math.max(rx1 - rx0, 1e-6);
  const ey = Math.max(uy1 - uy0, 1e-6);
  if (width <= 0 || height <= 0) return MIN_ZOOM;
  return Math.min(width / ex, height / ey) * margin;
}

const ease = (u: number) => 1 - Math.pow(1 - u, 3);
const lerp3 = (a: Vec3, b: Vec3, u: number): Vec3 => [a[0] + (b[0] - a[0]) * u, a[1] + (b[1] - a[1]) * u, a[2] + (b[2] - a[2]) * u];

/** The pose `elapsedMs` into a move from `from` to `to`. Reduced motion: no tween, straight to `to`. */
export function tween(from: CameraPose, to: CameraPose, elapsedMs: number, reduced: boolean): { pose: CameraPose; done: boolean } {
  if (reduced || elapsedMs >= TWEEN_MS) return { pose: to, done: true };
  const u = ease(Math.max(0, elapsedMs) / TWEEN_MS);
  return {
    pose: { position: lerp3(from.position, to.position, u), target: lerp3(from.target, to.target, u), zoom: from.zoom + (to.zoom - from.zoom) * u },
    done: false,
  };
}
