import * as THREE from 'three';
import { describe, expect, it } from 'vitest';
import { EFFECTS, Effects } from './effects';

const run = (fx: Effects, seconds: number) => {
  for (let t = 0; t < seconds; t += 0.016) fx.frame(0.016);
};

describe('Effects', () => {
  it('turns every effect off under reduced motion, and back on', () => {
    const fx = new Effects(new THREE.Group());
    for (const e of EFFECTS) expect(fx.isEnabled(e)).toBe(true);
    fx.setReducedMotion(true);
    expect(fx.reducedMotion).toBe(true);
    for (const e of EFFECTS) expect(fx.isEnabled(e)).toBe(false);
    fx.setReducedMotion(false);
    for (const e of EFFECTS) expect(fx.isEnabled(e)).toBe(true);
  });

  it('holds still under reduced motion: no bounce, pulse, sway, steam or celebration', () => {
    const parent = new THREE.Group();
    const fx = new Effects(parent);
    fx.setReducedMotion(true);
    fx.setPreparing(new THREE.Vector3(0, 20, 0));
    fx.bounce('store');
    fx.bounce('courier');
    fx.celebrate(new THREE.Vector3(0, 30, 0));
    const before = parent.children.length;
    for (let i = 0; i < 20; i++) {
      fx.frame(0.05);
      expect(fx.squash('store')).toEqual({ xz: 1, y: 1 });
      expect(fx.hop()).toBe(0);
      expect(fx.pulse('home')).toBe(1);
      expect(fx.bob('store')).toBe(0);
    }
    expect(fx.swayTime).toBe(0);
    expect(fx.visibleSteam).toBe(0);
    expect(fx.activeParticles).toBe(0);
    expect(parent.children.length).toBe(before);
  });

  it('bounces a pin once, then rests', () => {
    const fx = new Effects(new THREE.Group());
    fx.bounce('home');
    run(fx, 0.1);
    const s = fx.squash('home');
    expect(s.xz !== 1 || s.y !== 1).toBe(true);
    run(fx, 2);
    expect(fx.squash('home')).toEqual({ xz: 1, y: 1 });
  });

  it('celebrates with a short burst of particles and disposes it afterwards', () => {
    const parent = new THREE.Group();
    const fx = new Effects(parent);
    const before = parent.children.length;
    fx.celebrate(new THREE.Vector3(0, 30, 0));
    expect(parent.children.length).toBe(before + 1);
    const burst = parent.children[parent.children.length - 1] as THREE.InstancedMesh;
    let disposed = false;
    burst.geometry.addEventListener('dispose', () => (disposed = true));
    run(fx, 0.3);
    expect(fx.activeParticles).toBeGreaterThan(0);
    expect(fx.activeParticles).toBeLessThanOrEqual(40);
    run(fx, 2.5);
    expect(fx.activeParticles).toBe(0);
    expect(parent.children.length).toBe(before);
    expect(disposed).toBe(true);
  });

  it('loops steam over the store while it prepares, and fades it out when told', () => {
    const fx = new Effects(new THREE.Group());
    fx.setPreparing(new THREE.Vector3(0, 20, 0));
    run(fx, 1);
    expect(fx.visibleSteam).toBeGreaterThan(0);
    fx.setPreparing(null);
    run(fx, 0.5); // the puffs fade out over a third of a second
    expect(fx.visibleSteam).toBe(0);
  });

  it('hides running steam at once when reduced motion turns on', () => {
    const fx = new Effects(new THREE.Group());
    fx.setPreparing(new THREE.Vector3(0, 20, 0));
    run(fx, 1);
    expect(fx.visibleSteam).toBeGreaterThan(0);
    fx.setReducedMotion(true);
    fx.frame(0.016);
    expect(fx.visibleSteam).toBe(0);
  });
});
