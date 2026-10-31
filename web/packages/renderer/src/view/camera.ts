import * as THREE from 'three';
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js';
import { CAM_OFFSET, FOLLOW_ZOOM, MAX_ZOOM, MIN_ZOOM, applyFrame, type Frame } from './framing';
import { UP } from './models';

export type CameraMode = '3d' | '2d';

/** The camera's intro: it turns in by this angle (rad) from further out, at this rate (1/s). */
const INTRO_YAW = 0.55;
const INTRO_ZOOM = 0.6;
const INTRO_RATE = 1.8;
const GLIDE_RATE = 5;
/** The prototype's orbit limits (3D). */
const MIN_POLAR = 0.35;
const MAX_POLAR = 1.25;
/** The isometric view's angle from straight up (the prototype's `CAM_OFFSET`). */
export const ISO_POLAR = new THREE.Spherical().setFromVector3(CAM_OFFSET).phi;
/** 2D: straight down (a hair off vertical, so the view keeps its compass heading). */
const TOP_POLAR = 1e-3;

/** Which camera moves are on (both off under reduced motion: the camera snaps). */
export interface CameraMotion {
  /** Reframes, follow starts and the 2D/3D tilt glide. */
  glide(): boolean;
  /** The gentle move in when the city is first on screen. */
  intro(): boolean;
}

/**
 * The prototype's camera (spec §5.1): orthographic, isometric, orbit with damping, limited polar angle, zoom 0.6–5,
 * follow mode with smooth goals, the intro move; and the top-down 2D view of the same scene (rotation off, zoom kept).
 * Every move snaps when `motion` says so (reduced motion).
 */
export class CameraRig {
  readonly camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 1, 4000);
  readonly controls: OrbitControls;
  private goal: { target: THREE.Vector3; zoom: number; rate: number } | null = null;
  /** The polar angle the 2D/3D tilt is gliding to; null when it rests. */
  private tilt: number | null = null;
  /** The intro is still to come (once per page). */
  private introDue = true;
  /** Yaw (rad) the intro still has to turn; 0 when done. */
  private introLeft = 0;
  private follow = false;
  /** The user moved the camera (orbit, pan or zoom) since it was last framed. */
  private moved = false;
  private view: CameraMode;
  private readonly tmp = new THREE.Vector3();
  private readonly sph = new THREE.Spherical();

  constructor(
    domElement: HTMLElement | null,
    private readonly motion: CameraMotion,
    mode: CameraMode = '3d',
  ) {
    this.camera.position.copy(CAM_OFFSET);
    this.camera.zoom = FOLLOW_ZOOM;
    this.camera.lookAt(0, 0, 0);
    const c = new OrbitControls(this.camera, domElement);
    this.controls = c;
    c.enableDamping = true;
    c.dampingFactor = 0.08;
    c.minZoom = MIN_ZOOM;
    c.maxZoom = MAX_ZOOM;
    c.rotateSpeed = 0.6;
    c.enablePan = true; // the prototype: no pan while following
    c.addEventListener('start', () => {
      // the user takes the camera: glides and the intro stop where they are
      this.goal = null;
      this.introLeft = 0;
      this.moved = true;
    });
    this.view = mode;
    this.limit(mode);
    if (mode === '2d') this.setPolar(TOP_POLAR);
  }

  get mode(): CameraMode {
    return this.view;
  }

  get following(): boolean {
    return this.follow;
  }

  /** The user took the camera since the last frame or follow start: their view is theirs until the next one. */
  get userMoved(): boolean {
    return this.moved;
  }

  /** The intro has not been played (or skipped) yet. */
  get introPending(): boolean {
    return this.introDue;
  }

  /** 3D: the isometric orbit view; 2D: straight down on the same scene. Glides, or snaps under reduced motion. */
  setMode(m: CameraMode): void {
    if (m === this.view) return;
    this.view = m;
    this.tilt = m === '2d' ? TOP_POLAR : ISO_POLAR;
    // while it tilts, the orbit limits must not hold it back; rotation stays off until the 3D view is back
    this.controls.enableRotate = false;
    this.controls.minPolarAngle = 0;
    this.controls.maxPolarAngle = Math.PI / 2;
    if (!this.motion.glide()) this.finishTilt();
  }

  /** Follow `at` (moved by `frame`); starting glides there at `zoom`. Off leaves the camera where it is. */
  setFollow(on: boolean, at: THREE.Vector3, zoom: number): void {
    this.follow = on;
    this.controls.enablePan = !on;
    if (!on) return;
    this.moved = false;
    this.goal = { target: at.clone(), zoom, rate: GLIDE_RATE };
    if (!this.motion.glide()) this.finishMove();
  }

  /**
   * Unit vector from the target towards the camera that a frame should be built for: the current orbit direction,
   * as it will be once the intro has turned and the 2D/3D tilt has settled.
   */
  frameBack(): THREE.Vector3 {
    this.sph.setFromVector3(this.tmp.copy(this.camera.position).sub(this.controls.target));
    if (this.tilt !== null) this.sph.phi = this.tilt;
    this.sph.radius = 1;
    return new THREE.Vector3().setFromSpherical(this.sph).applyAxisAngle(UP, this.introLeft);
  }

  /** Jumps onto a frame (built with `frameBack()`). */
  snapToFrame(f: Frame): void {
    this.moved = false;
    applyFrame(this.camera, f); // the tested framing path
    this.controls.target.copy(f.target);
    this.goal = null;
    this.introLeft = 0;
    if (this.tilt !== null) this.finishTilt();
  }

  /** Glides onto a frame: same frame, reached by moving target and camera together along the orbit direction. */
  glideToFrame(f: Frame): void {
    this.moved = false;
    const now = this.tmp.copy(this.camera.position).sub(this.controls.target).normalize();
    this.camera.position.copy(now).multiplyScalar(f.distance).add(this.controls.target);
    this.camera.far = f.far;
    this.camera.updateProjectionMatrix();
    this.goal = { target: f.target, zoom: f.zoom, rate: this.introLeft > 0 ? INTRO_RATE : GLIDE_RATE };
    if (!this.motion.glide()) this.finishMove();
  }

  /** Moves target and camera together so the camera looks at `p`. */
  snapTo(p: THREE.Vector3): void {
    const delta = this.tmp.copy(p).sub(this.controls.target);
    this.controls.target.add(delta);
    this.camera.position.add(delta);
  }

  /**
   * The camera's gentle intro: from a little further out and turned aside, it settles on the frame. Once per page;
   * skipped (but used up) under reduced motion or while following. Returns whether it plays.
   */
  startIntro(): boolean {
    if (!this.introDue) return false;
    this.introDue = false;
    if (!this.motion.intro() || this.follow) return false;
    const zoom = this.camera.zoom;
    this.orbitBy(-INTRO_YAW);
    this.introLeft = INTRO_YAW;
    this.camera.zoom = zoom * INTRO_ZOOM;
    this.camera.updateProjectionMatrix();
    this.goal = { target: this.controls.target.clone(), zoom, rate: INTRO_RATE };
    return true;
  }

  /** Ends a glide, the intro and the tilt at once (reduced motion: the camera snaps). */
  finishMove(): void {
    if (this.introLeft > 0) this.orbitBy(this.introLeft);
    this.introLeft = 0;
    if (this.tilt !== null) this.finishTilt();
    const g = this.goal;
    if (!g) return;
    this.snapTo(g.target);
    this.camera.zoom = g.zoom;
    this.camera.updateProjectionMatrix();
    this.goal = null;
  }

  /** Advances the intro, glides, tilt and follow (towards `followAt`), then the orbit controls. */
  frame(dt: number, followAt: THREE.Vector3): void {
    if (this.introLeft > 0) {
      const step = this.introLeft < 0.002 ? this.introLeft : this.introLeft * (1 - Math.exp(-INTRO_RATE * dt));
      this.orbitBy(step);
      this.introLeft -= step;
    }
    if (this.goal) {
      if (this.follow) this.goal.target.copy(followAt); // following: the goal moves with the courier
      const kk = 1 - Math.exp(-this.goal.rate * dt);
      const delta = this.tmp.copy(this.goal.target).sub(this.controls.target).multiplyScalar(kk);
      this.controls.target.add(delta);
      this.camera.position.add(delta);
      this.camera.zoom += (this.goal.zoom - this.camera.zoom) * kk;
      this.camera.updateProjectionMatrix();
      if (delta.length() < 0.05 && Math.abs(this.goal.zoom - this.camera.zoom) < 0.01) {
        this.goal = null;
      }
    }
    if (this.follow) {
      const k = this.motion.glide() ? 1 - Math.exp(-6 * dt) : 1;
      const delta = this.tmp.copy(followAt).sub(this.controls.target).multiplyScalar(k);
      this.controls.target.add(delta);
      this.camera.position.add(delta);
    }
    if (this.tilt !== null) {
      const phi = this.polar();
      const next = phi + (this.tilt - phi) * (1 - Math.exp(-GLIDE_RATE * dt));
      if (Math.abs(this.tilt - next) < 1e-3) this.finishTilt();
      else this.setPolar(next);
    }
    this.controls.update();
  }

  dispose(): void {
    this.controls.dispose();
  }

  /** Turns the camera about the vertical axis through what it looks at. */
  private orbitBy(rad: number) {
    const t = this.controls.target;
    const off = this.tmp.copy(this.camera.position).sub(t).applyAxisAngle(UP, rad);
    this.camera.position.copy(t).add(off);
    this.camera.lookAt(t);
  }

  private polar(): number {
    return this.sph.setFromVector3(this.tmp.copy(this.camera.position).sub(this.controls.target)).phi;
  }

  /** Puts the camera at polar angle `phi` around the target, keeping its distance and compass heading. */
  private setPolar(phi: number) {
    const t = this.controls.target;
    this.sph.setFromVector3(this.tmp.copy(this.camera.position).sub(t));
    this.sph.phi = phi;
    this.camera.position.setFromSpherical(this.sph).add(t);
    this.camera.lookAt(t);
  }

  private finishTilt() {
    if (this.tilt === null) return;
    this.setPolar(this.tilt);
    this.tilt = null;
    this.limit(this.view);
  }

  /** The orbit limits of a view: 3D the prototype's; 2D held straight down, no rotation (zoom stays). */
  private limit(m: CameraMode) {
    const c = this.controls;
    c.enableRotate = m === '3d';
    c.minPolarAngle = m === '3d' ? MIN_POLAR : TOP_POLAR;
    c.maxPolarAngle = m === '3d' ? MAX_POLAR : TOP_POLAR;
  }
}
