import * as THREE from 'three';
import type { Vehicle } from '@dt/contract';
import { Bicycle, COLORS, makeCourierBox, makeHead, part, partBar, partBox, partSphere, setBar, sharedGeo } from './models';

/**
 * The courier models (spec §5.3): the prototype's bicycle, an e-bike, a scooter, a car and a walker, in the same
 * low-poly toy style and rider colors. Every model faces +Z, stands on y = 0, is about 2.4 "bike metres" tall and
 * long, and is drawn `COURIER_SCALE` times larger in the city, so framing and the x-ray silhouette work for all.
 */
export const COURIER_SCALE = 6;

export interface CourierModel {
  vehicle: Vehicle;
  root: THREE.Group;
  /** The parcel the courier carries (hidden once delivered). */
  parcel: THREE.Object3D;
  /** `speedMs`: the speed the courier is drawn moving at, world m/s. Zero speed keeps the model still. */
  animate(speedMs: number, dt: number): void;
}

const DARK = 0x20263a;
const LIGHT = { emissive: 0xfff1b8, emissiveIntensity: 0.9 };
const TAIL = { emissive: COLORS.coral, emissiveIntensity: 0.5 };

export function makeCourierModel(v: Vehicle): CourierModel {
  switch (v) {
    case 'e_bike':
      return bike('e_bike', new Bicycle({ electric: true }));
    case 'scooter':
      return scooter();
    case 'car':
      return car();
    case 'on_foot':
      return walker();
    case 'bicycle':
    default:
      return bike('bicycle', new Bicycle());
  }
}

/** A wheel group spinning about X: a tyre and a light bar across it, so the spin reads. */
function wheel(radius: number, width: number): THREE.Group {
  const w = new THREE.Group();
  const tyre = part(sharedGeo(`tyre:${radius}:${width}`, () => new THREE.CylinderGeometry(radius, radius, width, 14)), DARK);
  tyre.rotation.z = Math.PI / 2;
  const hub = partBox(width + 0.02, radius * 1.3, radius * 0.28, 0xd7deef);
  w.add(tyre, hub);
  return w;
}

function bike(vehicle: Vehicle, b: Bicycle): CourierModel {
  const root = new THREE.Group();
  root.add(b.group);
  let t = 0;
  return {
    vehicle,
    root,
    parcel: b.parcelBox,
    animate(speedMs, dt) {
      if (speedMs <= 0) {
        b.group.position.y = 0;
        return;
      }
      t += dt;
      b.animate(dt, speedMs, COURIER_SCALE);
      // the prototype's little bob while riding
      b.group.position.y = (Math.sin(t * 14) * 0.02 * Math.min(1, speedMs / 3)) / COURIER_SCALE;
    },
  };
}

/** A delivery moped: blue body, leg shield, seated rider, box on the rack. Wheels spin, the engine purrs. */
function scooter(): CourierModel {
  const root = new THREE.Group();
  const body = new THREE.Group(); // everything on the suspension
  root.add(body);
  const R = 0.36;
  const wheels = [wheel(R, 0.2), wheel(R, 0.2)];
  wheels[0]!.position.set(0, R, -0.72);
  wheels[1]!.position.set(0, R, 0.82);
  root.add(...wheels);
  const v = (x: number, y: number, z: number) => new THREE.Vector3(x, y, z);

  const floor = partBox(0.46, 0.1, 0.72, COLORS.blueDark);
  floor.position.set(0, 0.42, 0.05);
  const cowl = partBox(0.56, 0.46, 0.92, COLORS.blue);
  cowl.position.set(0, 0.7, -0.5);
  const seat = partBox(0.4, 0.12, 0.66, DARK);
  seat.position.set(0, 0.99, -0.42);
  const shield = partBox(0.52, 0.8, 0.12, COLORS.blue);
  shield.position.set(0, 0.84, 0.47);
  shield.rotation.x = -0.22;
  const stripe = partBox(0.54, 0.12, 0.13, COLORS.white);
  stripe.position.set(0, 0.92, 0.49);
  stripe.rotation.x = -0.22;
  const fork = partBar(0.05, DARK);
  setBar(fork, v(0, R, 0.82), v(0, 1.0, 0.6));
  const column = partBar(0.06, COLORS.blue);
  setBar(column, v(0, 0.95, 0.6), v(0, 1.36, 0.66));
  const bar = partBox(0.72, 0.07, 0.07, DARK);
  bar.position.set(0, 1.38, 0.64);
  const light = partBox(0.18, 0.12, 0.08, 0xfff6d8, LIGHT);
  light.position.set(0, 1.27, 0.74);
  const tail = partBox(0.3, 0.08, 0.06, COLORS.coral, TAIL);
  tail.position.set(0, 0.8, -0.97);
  const mudguard = partBox(0.24, 0.07, 0.5, COLORS.blue);
  mudguard.position.set(0, R * 2 + 0.06, 0.82);
  body.add(floor, cowl, seat, shield, stripe, fork, column, bar, light, tail, mudguard);

  // seated rider: upright, hands on the bar, feet on the floorboard
  const hip = v(0, 1.1, -0.36);
  const shoulder = v(0, 1.72, -0.12);
  const torso = part(sharedGeo('torso', () => new THREE.CylinderGeometry(0.2, 0.17, 1, 10)), COLORS.shirt);
  setBar(torso, hip, shoulder);
  const head = makeHead();
  head.position.set(0, 1.99, -0.04);
  body.add(torso, head);
  for (const side of [-1, 1]) {
    const arm = partBar(0.06, COLORS.shirt);
    setBar(arm, v(side * 0.2, 1.66, -0.1), v(side * 0.3, 1.38, 0.62));
    const thigh = partBar(0.09, COLORS.pants);
    setBar(thigh, v(side * 0.14, 1.1, -0.36), v(side * 0.16, 1.1, 0.12));
    const shin = partBar(0.075, COLORS.pants);
    setBar(shin, v(side * 0.16, 1.1, 0.12), v(side * 0.16, 0.5, 0.2));
    body.add(arm, thigh, shin);
  }

  const parcel = makeCourierBox(0.74);
  parcel.position.set(0, 0.93, -0.98);
  body.add(parcel);

  let t = 0;
  return {
    vehicle: 'scooter',
    root,
    parcel,
    animate(speedMs, dt) {
      if (speedMs <= 0) {
        body.position.y = 0;
        body.rotation.x = 0;
        return;
      }
      t += dt;
      const omega = speedMs / (R * COURIER_SCALE);
      for (const w of wheels) w.rotation.x += omega * dt;
      // the engine purrs: a quick small shake, and the nose lifts a touch with speed
      const k = Math.min(1, speedMs / 4);
      body.position.y = Math.sin(t * 46) * 0.012 * k;
      body.rotation.x = -0.025 * k;
    },
  };
}

/** A small blue delivery car with a white cabin, a glass band, lights and the parcel on the roof rack. */
function car(): CourierModel {
  const root = new THREE.Group();
  const body = new THREE.Group(); // on the suspension: dives when braking, squats when pulling away
  root.add(body);
  const R = 0.32;
  const wheels: THREE.Group[] = [];
  for (const x of [-0.66, 0.66]) {
    for (const z of [-0.88, 0.92]) {
      const w = wheel(R, 0.24);
      w.position.set(x, R, z);
      wheels.push(w);
    }
  }
  root.add(...wheels);

  const lower = partBox(1.46, 0.5, 2.62, COLORS.blue);
  lower.position.set(0, 0.6, 0.02);
  const cabin = partBox(1.32, 0.62, 1.46, COLORS.white);
  cabin.position.set(0, 1.16, -0.2);
  const glass = partBox(1.36, 0.36, 1.5, COLORS.glass, { emissive: 0x6f9cff, emissiveIntensity: 0.2 });
  glass.position.set(0, 1.18, -0.2);
  const hood = partBox(1.3, 0.1, 0.9, COLORS.blue);
  hood.position.set(0, 0.88, 0.86);
  const stripe = partBox(1.48, 0.1, 2.64, COLORS.white);
  stripe.position.set(0, 0.62, 0.02);
  const bumperF = partBox(1.5, 0.16, 0.12, DARK);
  bumperF.position.set(0, 0.42, 1.34);
  const bumperR = partBox(1.5, 0.16, 0.12, DARK);
  bumperR.position.set(0, 0.42, -1.3);
  body.add(lower, cabin, glass, hood, stripe, bumperF, bumperR);
  for (const x of [-0.5, 0.5]) {
    const head = partBox(0.26, 0.14, 0.06, 0xfff6d8, LIGHT);
    head.position.set(x, 0.72, 1.34);
    const tail = partBox(0.22, 0.14, 0.06, COLORS.coral, TAIL);
    tail.position.set(x, 0.72, -1.3);
    body.add(head, tail);
  }
  const rack = partBox(1.1, 0.06, 1.1, DARK);
  rack.position.set(0, 1.5, -0.22);
  body.add(rack);
  const parcel = makeCourierBox(0.72);
  parcel.position.set(0, 1.53, -0.22);
  body.add(parcel);

  let lastSpeed = 0;
  let pitch = 0;
  return {
    vehicle: 'car',
    root,
    parcel,
    animate(speedMs, dt) {
      if (dt <= 0) return;
      const accel = (speedMs - lastSpeed) / dt;
      lastSpeed = speedMs;
      if (speedMs > 0) {
        const omega = speedMs / (R * COURIER_SCALE);
        for (const w of wheels) w.rotation.x += omega * dt;
      }
      // pitch with the (world) acceleration, eased: the nose dips when braking and lifts when pulling away
      const want = Math.max(-0.06, Math.min(0.06, -accel * 0.012));
      if (want === 0 && Math.abs(pitch) < 1e-4) {
        pitch = 0;
      } else {
        pitch += (want - pitch) * (1 - Math.exp(-dt / 0.18));
      }
      body.rotation.x = pitch;
    },
  };
}

/** A courier on foot with the blue box as a backpack: legs, knees and arms swing in a walking cycle. */
function walker(): CourierModel {
  const root = new THREE.Group();
  const body = new THREE.Group(); // bobs a little with every step
  root.add(body);
  const HIP_Y = 1.02;
  const THIGH = 0.5;
  const SHIN = 0.5;

  const legs: { hip: THREE.Group; knee: THREE.Group; side: number }[] = [];
  for (const side of [-1, 1]) {
    const hip = new THREE.Group();
    hip.position.set(side * 0.13, HIP_Y, 0);
    const thigh = partBar(0.1, COLORS.pants, 0.11);
    thigh.position.y = -THIGH / 2;
    thigh.scale.y = THIGH;
    const knee = new THREE.Group();
    knee.position.y = -THIGH;
    const shin = partBar(0.08, COLORS.pants, 0.09);
    shin.position.y = -SHIN / 2;
    shin.scale.y = SHIN;
    const shoe = partBox(0.16, 0.1, 0.3, DARK);
    shoe.position.set(0, -SHIN + 0.03, 0.06);
    knee.add(shin, shoe);
    hip.add(thigh, knee);
    body.add(hip);
    legs.push({ hip, knee, side });
  }
  const torso = part(sharedGeo('walker-torso', () => new THREE.CylinderGeometry(0.25, 0.2, 0.78, 10)), COLORS.shirt);
  torso.position.y = HIP_Y + 0.37;
  const head = makeHead();
  head.position.set(0, 2.04, 0.02);
  head.scale.setScalar(1.25); // toy proportions: a bigger head reads from above
  body.add(torso, head);

  const arms: { shoulder: THREE.Group; side: number }[] = [];
  for (const side of [-1, 1]) {
    const shoulder = new THREE.Group();
    shoulder.position.set(side * 0.3, 1.7, 0);
    const arm = partBar(0.065, COLORS.shirt, 0.075);
    arm.position.y = -0.3;
    arm.scale.y = 0.6;
    const hand = partSphere(0.075, COLORS.skin);
    hand.position.y = -0.62;
    shoulder.add(arm, hand);
    shoulder.rotation.z = side * 0.12;
    body.add(shoulder);
    arms.push({ shoulder, side });
  }

  const parcel = makeCourierBox(0.62);
  parcel.position.set(0, 1.12, -0.5);
  const strap = partBox(0.5, 0.06, 0.08, DARK);
  strap.position.set(0, 1.62, -0.22);
  body.add(parcel, strap);

  let phase = 0;
  let amp = 0; // 0 standing .. 1 full stride
  const pose = () => {
    for (const leg of legs) {
      const p = phase + (leg.side > 0 ? 0 : Math.PI);
      leg.hip.rotation.x = -Math.sin(p) * 0.55 * amp;
      // the knee bends while the leg swings forward
      leg.knee.rotation.x = Math.max(0, Math.cos(p)) * 0.8 * amp;
    }
    for (const arm of arms) arm.shoulder.rotation.x = Math.sin(phase + (arm.side > 0 ? 0 : Math.PI)) * 0.5 * amp;
    body.position.y = Math.abs(Math.sin(phase)) * 0.06 * amp;
  };
  pose();
  return {
    vehicle: 'on_foot',
    root,
    parcel,
    animate(speedMs, dt) {
      const want = Math.min(1, speedMs / 0.8);
      if (want === 0 && amp === 0) return;
      amp += (want - amp) * (1 - Math.exp(-dt / 0.15));
      if (want === 0 && amp < 0.01) amp = 0;
      // about two steps a second at walking pace (toy cadence: the courier is drawn 6× larger than life)
      phase += Math.PI * 2 * Math.min(2.2, Math.max(0.6, speedMs / 1.6)) * dt * (want > 0 ? 1 : 0.4);
      pose();
    },
  };
}
