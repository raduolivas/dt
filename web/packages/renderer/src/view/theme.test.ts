import * as THREE from 'three';
import { describe, expect, it } from 'vitest';
import type { LatLng, TrackingSnapshot } from '@dt/contract';
import { GeoProjection } from '../engine/geo';
import { decodeTile } from '../tiles/build/decode';
import { FIXTURE_PATH, FileSource } from '../tiles/fixture';
import { openPmtiles } from '../tiles/pmtiles';
import { TileCity, placeSites, type TileBuilder } from './city';
import { COLORS, DAY, NIGHT, mat, makePin } from './models';
import { makeCourierGlow } from './night';
import { applyTheme, createLights, nightMix } from './theme';
import { makeCourierModel } from './vehicles';

const builder: TileBuilder = { build: async (req) => decodeTile(req).layers };
const STORE: LatLng = { lat: 52.3731, lng: 4.8926 };
const HOME: LatLng = { lat: 52.3705, lng: 4.8885 };

/** Every key path of a nested palette (arrays count as leaves). */
function keyPaths(o: object, prefix = ''): string[] {
  return Object.entries(o).flatMap(([k, v]) =>
    v !== null && typeof v === 'object' && !Array.isArray(v) ? keyPaths(v as object, `${prefix}${k}.`) : [`${prefix}${k}`],
  );
}

function themedScene() {
  const scene = new THREE.Scene();
  scene.background = new THREE.Color(DAY.sky);
  const { hemi, sun } = createLights();
  scene.add(hemi, sun, sun.target);
  return { scene, hemi, sun };
}

const countLights = (o: THREE.Object3D) => {
  let n = 0;
  o.traverse((c) => {
    if (c instanceof THREE.Light) n++;
  });
  return n;
};

describe('palettes', () => {
  it('DAY is the prototype palette', () => {
    for (const [k, v] of Object.entries(COLORS)) expect(DAY[k as keyof typeof COLORS], k).toBe(v);
  });

  it('NIGHT has every key of DAY', () => {
    expect(keyPaths(NIGHT).sort()).toEqual(keyPaths(DAY).sort());
    expect(NIGHT.city.walls.length).toBe(DAY.city.walls.length);
  });
});

describe('applyTheme', () => {
  it('sets the final colors in one call under reduced motion (no fade)', () => {
    const { scene, hemi, sun } = themedScene();
    const ground = new THREE.Mesh(new THREE.PlaneGeometry(1, 1), mat(COLORS.ground, { roughness: 1 }).clone());
    const glass = new THREE.Mesh(
      new THREE.BoxGeometry(1, 1, 1),
      mat(COLORS.glass, { emissive: 0x6f9cff, emissiveIntensity: 0.25 }).clone(),
    );
    let hook = -1;
    const prop = new THREE.Object3D();
    prop.userData.dtNight = (k: number) => (hook = k);
    scene.add(ground, glass, prop);

    applyTheme(scene, 'dark', { fadeMs: 0 });

    expect((scene.background as THREE.Color).getHex()).toBe(NIGHT.sky);
    expect(hemi.color.getHex()).toBe(NIGHT.hemiSky);
    expect(hemi.groundColor.getHex()).toBe(NIGHT.hemiGround);
    expect(hemi.intensity).toBeCloseTo(NIGHT.hemiIntensity);
    expect(sun.color.getHex()).toBe(NIGHT.sunColor);
    expect(sun.intensity).toBeCloseTo(NIGHT.sunIntensity);
    expect((ground.material as THREE.MeshStandardMaterial).color.getHex()).toBe(NIGHT.ground);
    const g = glass.material as THREE.MeshStandardMaterial;
    expect(g.emissive.getHex()).toBe(NIGHT.glow.glass.color);
    expect(g.emissiveIntensity).toBeCloseTo(NIGHT.glow.glass.intensity);
    expect(hook).toBe(1);
    expect(nightMix(scene)).toBe(1);

    // and back to day, again at once
    applyTheme(scene, 'light', { fadeMs: 0 });
    expect((scene.background as THREE.Color).getHex()).toBe(DAY.sky);
    expect((ground.material as THREE.MeshStandardMaterial).color.getHex()).toBe(DAY.ground);
    expect(g.emissive.getHex()).toBe(0x6f9cff);
    expect(g.emissiveIntensity).toBeCloseTo(0.25);
    expect(hook).toBe(0);
  });

  it('fades over fadeMs otherwise', () => {
    const { scene, sun } = themedScene();
    const fade = applyTheme(scene, 'dark', { fadeMs: 1000 });
    expect(fade.done).toBe(false);
    expect((scene.background as THREE.Color).getHex()).toBe(DAY.sky);
    fade.step(500);
    const half = nightMix(scene);
    expect(half).toBeGreaterThan(0.2);
    expect(half).toBeLessThan(0.8);
    fade.step(600);
    expect(fade.done).toBe(true);
    expect((scene.background as THREE.Color).getHex()).toBe(NIGHT.sky);
    expect(sun.intensity).toBeCloseTo(NIGHT.sunIntensity);
  });

  it('leaves partner-colored materials alone', () => {
    const { scene } = themedScene();
    const pin = makePin(COLORS.blue);
    const m = (pin.children[1] as THREE.Mesh).material as THREE.MeshStandardMaterial;
    const own = m.clone();
    own.userData.dtColor = false;
    own.userData.dtGlow = 'pin';
    own.color.setHex(0x123456);
    own.emissive.setHex(0x123456);
    (pin.children[1] as THREE.Mesh).material = own;
    scene.add(pin);
    applyTheme(scene, 'dark', { fadeMs: 0 });
    expect(own.color.getHex()).toBe(0x123456);
    expect(own.emissive.getHex()).toBe(0x123456); // the pin keeps its color and only glows brighter
    expect(own.emissiveIntensity).toBeCloseTo(NIGHT.glow.pin.intensity);
  });

  it('adds no real-time light at night: the THREE.Light count is the same by day and by night', { timeout: 30_000 }, async () => {
    const { scene } = themedScene();
    const s = {
      delivery_id: 'del_night',
      delivery_model: 'end_to_end',
      status: 'courier_assigned',
      courier: null,
      pickup: { location: STORE, name: 'Food Hub' },
      dropoff: { location: HOME, label: null },
      route: { to_pickup: null, to_dropoff: [[STORE.lat, STORE.lng], [HOME.lat, HOME.lng]] },
      eta_seconds: null,
      eta_window: null,
      eta_state: 'calculating',
      eta_quality: 'estimated',
      distance_remaining_m: null,
      progress: null,
      timeline: [],
      updated_at: '2026-10-29T22:00:00Z',
      version: 1,
    } as TrackingSnapshot;
    const group = new THREE.Group();
    scene.add(group);
    const source = await openPmtiles('fixture', { source: new FileSource(FIXTURE_PATH) });
    const city = new TileCity(group, source, new GeoProjection(STORE), { builder });
    await city.loadFirst(s);
    placeSites(city, s, group);
    const courier = new THREE.Group();
    courier.add(makeCourierModel('bicycle').root, makeCourierGlow());
    scene.add(courier);

    const day = countLights(scene);
    expect(day).toBe(2); // the hemisphere and the sun
    applyTheme(scene, 'dark', { fadeMs: 0 });
    expect(city.streetlightCount).toBeGreaterThan(0); // the night city has its streetlights...
    expect(countLights(scene)).toBe(day); // ...and none of them is a light
    city.dispose();
  });
});
