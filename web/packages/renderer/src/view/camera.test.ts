import * as THREE from 'three';
import { describe, expect, it } from 'vitest';
import { CameraRig, ISO_POLAR } from './camera';
import { CAM_OFFSET, computeFrame, setFrustum } from './framing';

const motion = (on: boolean) => ({ glide: () => on, intro: () => on });

function rig(on: boolean, mode: '3d' | '2d' = '3d') {
  const r = new CameraRig(null, motion(on), mode);
  setFrustum(r.camera, 390, 844);
  r.camera.updateProjectionMatrix();
  r.controls.update();
  return r;
}

const down = (r: CameraRig) => r.camera.getWorldDirection(new THREE.Vector3());
const polar = (r: CameraRig) => new THREE.Spherical().setFromVector3(r.camera.position.clone().sub(r.controls.target)).phi;
const run = (r: CameraRig, s: number, at = new THREE.Vector3()) => {
  for (let t = 0; t < s; t += 1 / 60) r.frame(1 / 60, at);
};

describe('CameraRig', () => {
  it('starts at the prototype isometric angle', () => {
    const r = rig(true);
    expect(r.camera.position.clone().normalize().distanceTo(CAM_OFFSET.clone().normalize())).toBeLessThan(1e-6);
    expect(polar(r)).toBeCloseTo(ISO_POLAR, 6);
  });

  it("setMode('2d') points the camera straight down, keeps the zoom and turns orbit rotation off", () => {
    const r = rig(false);
    r.camera.zoom = 2.3;
    r.setMode('2d');
    expect(down(r).y).toBeLessThan(-0.9999);
    expect(r.camera.zoom).toBe(2.3);
    expect(r.controls.enableRotate).toBe(false);
    expect(r.controls.enableZoom).toBe(true);
    run(r, 1); // the orbit controls keep it there
    expect(down(r).y).toBeLessThan(-0.9999);
  });

  it('tilts to 2D smoothly unless reduced motion, and back to 3D', () => {
    const r = rig(true);
    r.setMode('2d');
    expect(polar(r)).toBeCloseTo(ISO_POLAR, 6); // not yet: it glides
    r.frame(1 / 60, new THREE.Vector3());
    const p1 = polar(r);
    expect(p1).toBeLessThan(ISO_POLAR);
    expect(p1).toBeGreaterThan(ISO_POLAR * 0.8); // a small step, no jump
    run(r, 3);
    expect(down(r).y).toBeLessThan(-0.9999);
    r.setMode('3d');
    run(r, 3);
    expect(polar(r)).toBeCloseTo(ISO_POLAR, 3);
    expect(r.controls.enableRotate).toBe(true);
  });

  it('starts in 2D when asked', () => {
    const r = rig(true, '2d');
    expect(down(r).y).toBeLessThan(-0.9999);
    expect(r.mode).toBe('2d');
  });

  it('frames from straight above in 2D', () => {
    const r = rig(false);
    r.setMode('2d');
    const pts = [new THREE.Vector3(-200, 0, 0), new THREE.Vector3(200, 0, 50)];
    r.snapToFrame(computeFrame(r.camera, pts, { back: r.frameBack() }));
    expect(down(r).y).toBeLessThan(-0.9999);
    for (const p of pts) {
      const v = p.clone().project(r.camera);
      expect(Math.abs(v.x)).toBeLessThan(1);
      expect(Math.abs(v.y)).toBeLessThan(1);
    }
  });

  it('glides to a frame, or snaps there under reduced motion', () => {
    const target = new THREE.Vector3(120, 0, -80);
    const f = (r: CameraRig) => computeFrame(r.camera, [target], { back: r.frameBack() });
    const glide = rig(true);
    const fg = f(glide);
    glide.glideToFrame(fg);
    expect(glide.controls.target.distanceTo(fg.target)).toBeGreaterThan(50);
    run(glide, 3);
    expect(glide.controls.target.distanceTo(fg.target)).toBeLessThan(1); // a glide ends within a step of its goal
    expect(glide.camera.zoom).toBeCloseTo(fg.zoom, 1);

    const snap = rig(false);
    const fs = f(snap);
    snap.glideToFrame(fs);
    expect(snap.controls.target.distanceTo(fs.target)).toBeLessThan(1e-6);
    expect(snap.camera.zoom).toBe(fs.zoom);
  });

  it('plays the intro once, and skips it under reduced motion', () => {
    const r = rig(true);
    const before = r.camera.position.clone();
    expect(r.startIntro()).toBe(true);
    expect(r.camera.position.distanceTo(before)).toBeGreaterThan(1); // turned aside
    run(r, 6);
    expect(r.camera.position.distanceTo(before)).toBeLessThan(0.5); // settled where it was framed
    expect(r.startIntro()).toBe(false); // once per page

    const still = rig(false);
    const at = still.camera.position.clone();
    const zoom = still.camera.zoom;
    expect(still.startIntro()).toBe(false);
    expect(still.camera.position.distanceTo(at)).toBe(0);
    expect(still.camera.zoom).toBe(zoom);
  });

  it('finishes a running intro at once when reduced motion turns on', () => {
    const r = rig(true);
    const before = r.camera.position.clone();
    const zoom = r.camera.zoom;
    r.startIntro();
    r.frame(1 / 60, new THREE.Vector3());
    r.finishMove();
    expect(r.camera.position.distanceTo(before)).toBeLessThan(1e-6);
    expect(r.camera.zoom).toBeCloseTo(zoom, 9);
  });

  it('follows the point it is given', () => {
    const r = rig(false);
    const at = new THREE.Vector3(40, 0, 25);
    r.setFollow(true, at, 2);
    run(r, 0.2, at);
    expect(r.controls.target.distanceTo(at)).toBeLessThan(1e-6);
    expect(r.controls.enablePan).toBe(false);
    r.setFollow(false, at, 2);
    expect(r.controls.enablePan).toBe(true);
  });
});
