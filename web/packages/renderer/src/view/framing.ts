import * as THREE from 'three';

/** The prototype's isometric camera: orthographic, looking down from this offset (spec §5.1). */
export const CAM_OFFSET = new THREE.Vector3(300, 280, 300);
const BASE_HALF_H = 240;
const MIN_HALF_W = 260;
/** Zoom range of the orbit controls (prototype); framing a long route may go further out. */
export const MIN_ZOOM = 0.6;
export const MAX_ZOOM = 5;
export const FOLLOW_ZOOM = 1.7;
/** Screen pixels per metre the follow view keeps at least, so the toy courier and city read on a phone too. */
const FOLLOW_PX_PER_M = 2.2;

/** The prototype's follow zoom on wide screens, closer on narrow ones (the frustum never gets narrower than 520 m). */
export function followZoom(cam: THREE.OrthographicCamera, widthPx: number): number {
  return Math.min(MAX_ZOOM, Math.max(FOLLOW_ZOOM, (FOLLOW_PX_PER_M * (cam.right - cam.left)) / Math.max(1, widthPx)));
}

/** The prototype's frustum: 480 m high at zoom 1, never narrower than 520 m. */
export function setFrustum(cam: THREE.OrthographicCamera, width: number, height: number): void {
  const aspect = Math.max(1, width) / Math.max(1, height);
  let halfH = BASE_HALF_H;
  let halfW = halfH * aspect;
  if (halfW < MIN_HALF_W) {
    halfW = MIN_HALF_W;
    halfH = halfW / aspect;
  }
  cam.left = -halfW;
  cam.right = halfW;
  cam.top = halfH;
  cam.bottom = -halfH;
}

/** Screen space covered by cards over the map, in CSS px. */
export interface Insets {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

/** A label pinned over the scene (bottom centre at `at`), its box in CSS px: framing keeps the whole box in view. */
export interface LabelBox {
  at: THREE.Vector3;
  width: number;
  height: number;
}

export interface Frame {
  target: THREE.Vector3;
  zoom: number;
  /** Camera distance from the target along `back` (orthographic: only the depth range depends on it). */
  distance: number;
  far: number;
  /** Unit vector from the target towards the camera (the orbit direction the frame was built for). */
  back: THREE.Vector3;
}

/**
 * Where to put the camera so every point (store, home, courier) is in view with a margin, keeping the prototype's
 * viewing angle. `lift` keeps the pins above the points in view too. Does not move the camera.
 */
export function computeFrame(
  cam: THREE.OrthographicCamera,
  points: THREE.Vector3[],
  opts: {
    margin?: number;
    maxZoom?: number;
    lift?: number;
    back?: THREE.Vector3;
    insets?: Insets;
    viewport?: [number, number];
    labels?: readonly LabelBox[];
  } = {},
): Frame {
  const margin = opts.margin ?? 0.2;
  const maxZoom = opts.maxZoom ?? 2.4;
  const lift = opts.lift ?? 34;
  // The current orbit direction when given (the user may have rotated), else the prototype's isometric angle.
  const back = (opts.back ?? CAM_OFFSET).clone().normalize();
  const forward = back.clone().negate();
  const right = new THREE.Vector3().crossVectors(forward, new THREE.Vector3(0, 1, 0)).normalize();
  const up = new THREE.Vector3().crossVectors(right, forward).normalize();

  let minR = Infinity;
  let maxR = -Infinity;
  let minU = Infinity;
  let maxU = -Infinity;
  const tmp = new THREE.Vector3();
  for (const p of points) {
    for (const dy of [0, lift]) {
      tmp.set(p.x, p.y + dy, p.z);
      const r = tmp.dot(right);
      const u = tmp.dot(up);
      minR = Math.min(minR, r);
      maxR = Math.max(maxR, r);
      minU = Math.min(minU, u);
      maxU = Math.max(maxU, u);
    }
  }
  // A label's anchor counts as a point; its box (screen px, fixed at any zoom) is kept clear by padding the insets.
  let halfW = 0;
  let labelH = 0;
  for (const l of opts.labels ?? []) {
    const r = l.at.dot(right);
    const u = l.at.dot(up);
    minR = Math.min(minR, r);
    maxR = Math.max(maxR, r);
    minU = Math.min(minU, u);
    maxU = Math.max(maxU, u);
    halfW = Math.max(halfW, l.width / 2);
    labelH = Math.max(labelH, l.height);
  }
  if (!Number.isFinite(minR)) {
    minR = maxR = minU = maxU = 0;
  }
  // Only the part of the viewport not covered by the cards (insets, CSS px) frames the points.
  const [vw, vh] = opts.viewport ?? [1, 1];
  const given = opts.insets ?? { top: 0, right: 0, bottom: 0, left: 0 };
  const ins = { top: given.top + labelH, right: given.right + halfW, bottom: given.bottom, left: given.left + halfW };
  const fx = Math.max(0.2, (vw - ins.left - ins.right) / vw);
  const fy = Math.max(0.2, (vh - ins.top - ins.bottom) / vh);
  const spanR = Math.max(1, maxR - minR) * (1 + 2 * margin);
  const spanU = Math.max(1, maxU - minU) * (1 + 2 * margin);
  const zoom = Math.min(maxZoom, (fx * (cam.right - cam.left)) / spanR, (fy * (cam.top - cam.bottom)) / spanU);
  // Shift the view so the middle of the points lands in the middle of the free area.
  const ndcX = (ins.left - ins.right) / vw;
  const ndcY = (ins.bottom - ins.top) / vh;
  const cr = (minR + maxR) / 2 - (ndcX * (cam.right - cam.left)) / 2 / zoom;
  const cu = (minU + maxU) / 2 - (ndcY * (cam.top - cam.bottom)) / 2 / zoom;
  // The point on the ground (y = 0) that projects to the centre of the extents.
  const t = -(up.y * cu + right.y * cr) / forward.y;
  const target = right.clone().multiplyScalar(cr).addScaledVector(up, cu).addScaledVector(forward, t);

  let radius = 0;
  for (const p of points) radius = Math.max(radius, p.distanceTo(target) + lift);
  const distance = Math.max(CAM_OFFSET.length(), radius + 200);
  return { target, zoom, distance, far: Math.max(4000, distance + radius + 1000), back };
}

/** Puts `cam` on a frame: position, zoom, depth range. */
export function applyFrame(cam: THREE.OrthographicCamera, f: Frame): void {
  cam.position.copy(f.back).multiplyScalar(f.distance).add(f.target);
  cam.zoom = f.zoom;
  cam.near = 1;
  cam.far = f.far;
  cam.lookAt(f.target);
  cam.updateProjectionMatrix();
}

/** Sets the frustum for the viewport, then frames the points (store, home and courier) and their labels. */
export function frameCamera(
  cam: THREE.OrthographicCamera,
  points: THREE.Vector3[],
  width: number,
  height: number,
  back?: THREE.Vector3,
  insets?: Insets,
  labels?: readonly LabelBox[],
): Frame {
  setFrustum(cam, width, height);
  const f = computeFrame(cam, points, { back, insets, viewport: [width, height], labels });
  applyFrame(cam, f);
  return f;
}
