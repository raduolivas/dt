import * as THREE from 'three';

export const COLORS = {
  ground: 0xe4eaf7,
  road: 0xaab5cf,
  roadMark: 0xf4f7ff,
  block: 0xf7f9fe,
  blue: 0x2f5bea,
  blueDark: 0x1e3fb0,
  blueLight: 0x9db4ee,
  white: 0xffffff,
  coral: 0xff6b4a,
  green: 0x37c887,
  leaf: 0x7fcf9f,
  leafDark: 0x58b985,
  trunk: 0x9b7b63,
  skin: 0xf2c3a0,
  shirt: 0xff8a3d,
  pants: 0x2b3350,
  glass: 0xbfd6ff,
} as const;

const materialCache = new Map<string, THREE.MeshStandardMaterial>();

export function mat(color: number, extra: Partial<THREE.MeshStandardMaterialParameters> = {}) {
  const key = `${color}|${JSON.stringify(extra)}`;
  let m = materialCache.get(key);
  if (!m) {
    m = new THREE.MeshStandardMaterial({ color, roughness: 0.8, metalness: 0.02, ...extra });
    materialCache.set(key, m);
  }
  return m;
}

function box(w: number, h: number, d: number, color: number, extra = {}) {
  const m = new THREE.Mesh(new THREE.BoxGeometry(w, h, d), mat(color, extra));
  m.castShadow = true;
  m.receiveShadow = true;
  return m;
}

export function mulberry32(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// ---------------------------------------------------------------------------
// Windows texture shared by all generic buildings
// ---------------------------------------------------------------------------
let windowBase: THREE.CanvasTexture | null = null;
function windowTexture(): THREE.CanvasTexture {
  if (windowBase) return windowBase;
  const c = document.createElement('canvas');
  c.width = c.height = 64;
  const g = c.getContext('2d')!;
  g.fillStyle = '#ffffff';
  g.fillRect(0, 0, 64, 64);
  const grad = g.createLinearGradient(0, 12, 0, 52);
  grad.addColorStop(0, '#c9dcff');
  grad.addColorStop(1, '#8fb0f0');
  g.fillStyle = grad;
  g.fillRect(12, 12, 40, 40);
  g.strokeStyle = '#ffffff';
  g.lineWidth = 3;
  g.beginPath();
  g.moveTo(32, 12);
  g.lineTo(32, 52);
  g.stroke();
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  t.wrapS = t.wrapT = THREE.RepeatWrapping;
  t.anisotropy = 4;
  windowBase = t;
  return t;
}

function sideMaterial(tint: number, repeatX: number, repeatY: number) {
  const t = windowTexture().clone();
  t.repeat.set(repeatX, repeatY);
  t.needsUpdate = true;
  return new THREE.MeshStandardMaterial({ color: tint, map: t, roughness: 0.75 });
}

const TINTS = [0xffffff, 0xf1f5ff, 0xe6edff, 0xdde7fb, 0xf6f0ff];

export function makeBuilding(rand: () => number, w: number, d: number, h: number): THREE.Group {
  const g = new THREE.Group();
  const tint = TINTS[Math.floor(rand() * TINTS.length)];
  const accent = rand() < 0.22;
  const roofColor = accent ? 0x6f8ff0 : 0xeef2fc;
  const sx = sideMaterial(tint, d / 7, h / 7);
  const sz = sideMaterial(tint, w / 7, h / 7);
  const roof = mat(roofColor);
  const body = new THREE.Mesh(new THREE.BoxGeometry(w, h, d), [sx, sx, roof, roof, sz, sz]);
  body.position.y = h / 2;
  body.castShadow = body.receiveShadow = true;
  g.add(body);
  const cap = box(w + 1.2, 1.2, d + 1.2, accent ? COLORS.blue : 0xdfe6f6);
  cap.position.y = h + 0.6;
  g.add(cap);
  if (rand() < 0.6) {
    const unit = box(w * 0.3, 2.6, d * 0.3, 0xcdd6ea);
    unit.position.set((rand() - 0.5) * w * 0.4, h + 2.5, (rand() - 0.5) * d * 0.4);
    g.add(unit);
  }
  return g;
}

export function makeTree(rand: () => number): THREE.Group {
  const g = new THREE.Group();
  const s = 0.8 + rand() * 0.6;
  const trunk = new THREE.Mesh(new THREE.CylinderGeometry(0.5, 0.7, 4, 6), mat(COLORS.trunk));
  trunk.position.y = 2;
  trunk.castShadow = true;
  g.add(trunk);
  const crown = new THREE.Mesh(
    new THREE.IcosahedronGeometry(3.4, 0),
    mat(rand() < 0.5 ? COLORS.leaf : COLORS.leafDark, { flatShading: true }),
  );
  crown.position.y = 6.6;
  crown.castShadow = true;
  g.add(crown);
  g.scale.setScalar(s);
  return g;
}

// ---------------------------------------------------------------------------
// Signs
// ---------------------------------------------------------------------------
function textSign(text: string, w: number, h: number, bg: string, fg: string): THREE.Mesh {
  const c = document.createElement('canvas');
  c.width = 512;
  c.height = Math.round((512 * h) / w);
  const g = c.getContext('2d')!;
  g.fillStyle = bg;
  g.fillRect(0, 0, c.width, c.height);
  g.fillStyle = fg;
  g.font = `800 ${Math.round(c.height * 0.58)}px system-ui, -apple-system, "Segoe UI", sans-serif`;
  g.textAlign = 'center';
  g.textBaseline = 'middle';
  g.fillText(text, c.width / 2, c.height / 2 + 4);
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  t.anisotropy = 4;
  return new THREE.Mesh(
    new THREE.PlaneGeometry(w, h),
    new THREE.MeshStandardMaterial({ map: t, roughness: 0.6 }),
  );
}

// ---------------------------------------------------------------------------
// Store and house. Both are modelled with their FRONT facing +Z.
// ---------------------------------------------------------------------------
export const STORE_HEIGHT = 18;
export const HOUSE_HEIGHT = 24;

export function makeStore(label: string): THREE.Group {
  const g = new THREE.Group();
  const body = box(44, STORE_HEIGHT, 32, COLORS.blue);
  body.position.y = STORE_HEIGHT / 2;
  g.add(body);
  const roof = box(47, 1.6, 35, COLORS.white);
  roof.position.y = STORE_HEIGHT + 0.8;
  g.add(roof);

  // striped awning
  const stripes = 8;
  for (let i = 0; i < stripes; i++) {
    const s = box(44 / stripes, 1, 9, i % 2 ? COLORS.white : COLORS.coral);
    s.position.set(-22 + (i + 0.5) * (44 / stripes), 11.5, 20.5);
    s.rotation.x = 0.38;
    g.add(s);
  }
  // shop window + door
  const win = box(26, 7, 0.6, COLORS.glass, { emissive: 0x6f9cff, emissiveIntensity: 0.25 });
  win.position.set(-6, 5.5, 16.2);
  g.add(win);
  const door = box(6.5, 10, 0.7, COLORS.white);
  door.position.set(13, 5, 16.25);
  g.add(door);
  // sign
  const sign = textSign(label.toUpperCase(), 30, 5.2, '#ffffff', '#2f5bea');
  sign.position.set(0, 15.2, 16.15);
  g.add(sign);
  // rooftop details
  const ac = box(8, 3, 6, 0xcdd6ea);
  ac.position.set(-12, STORE_HEIGHT + 3, -6);
  g.add(ac);
  const tank = new THREE.Mesh(new THREE.CylinderGeometry(3, 3, 5, 12), mat(COLORS.blueLight));
  tank.position.set(12, STORE_HEIGHT + 4, -7);
  tank.castShadow = true;
  g.add(tank);
  return g;
}

export interface HouseModel {
  group: THREE.Group;
  /** Parcel that is dropped at the door when the order is delivered. */
  parcel: THREE.Group;
}

export function makeHouse(): HouseModel {
  const g = new THREE.Group();
  const body = box(30, 14, 24, COLORS.white);
  body.position.y = 7;
  g.add(body);
  const roof = new THREE.Mesh(new THREE.ConeGeometry(1, 1, 4), mat(COLORS.coral));
  roof.geometry.rotateY(Math.PI / 4);
  roof.scale.set(25.5, 10, 20);
  roof.position.y = 14 + 5;
  roof.castShadow = true;
  g.add(roof);
  const chimney = box(3, 8, 3, 0xd6dcec);
  chimney.position.set(8, 20, -3);
  g.add(chimney);
  const door = box(5.5, 9, 0.7, COLORS.blue);
  door.position.set(-5, 4.5, 12.2);
  g.add(door);
  for (const x of [5, 11]) {
    const w = box(5, 5, 0.6, COLORS.glass, { emissive: 0x6f9cff, emissiveIntensity: 0.2 });
    w.position.set(x, 8, 12.2);
    g.add(w);
  }
  // little step + mailbox
  const step = box(9, 0.8, 5, 0xdfe6f6);
  step.position.set(-5, 0.4, 14.4);
  g.add(step);

  const parcel = new THREE.Group();
  const pk = box(4.2, 3.4, 4.2, 0xd9a066);
  pk.position.y = 1.7;
  const tape = box(4.3, 3.5, 0.9, 0xf3d9ae);
  tape.position.y = 1.7;
  parcel.add(pk, tape);
  parcel.position.set(-5, 0.8, 15.2);
  parcel.visible = false;
  g.add(parcel);
  return { group: g, parcel };
}

// ---------------------------------------------------------------------------
// Pin used to highlight the store and the customer's home
// ---------------------------------------------------------------------------
export function makePin(color: number): THREE.Group {
  const g = new THREE.Group();
  const cone = new THREE.Mesh(
    new THREE.ConeGeometry(3.4, 8, 20),
    mat(color, { emissive: color, emissiveIntensity: 0.25 }),
  );
  cone.rotation.x = Math.PI;
  cone.position.y = 4;
  cone.castShadow = true;
  const head = new THREE.Mesh(
    new THREE.SphereGeometry(3.9, 20, 16),
    mat(color, { emissive: color, emissiveIntensity: 0.25 }),
  );
  head.position.y = 10;
  head.castShadow = true;
  const dot = new THREE.Mesh(new THREE.SphereGeometry(1.5, 12, 10), mat(0xffffff));
  dot.position.set(0, 10.2, 2.9);
  g.add(cone, head, dot);
  return g;
}

// ---------------------------------------------------------------------------
// Bicycle courier. Faces +Z. Units are "bike meters"; the scene scales it up.
// ---------------------------------------------------------------------------
const WHEEL_R = 0.55;
export const BIKE_WHEEL_RADIUS = WHEEL_R;

function makeBar(radius: number, color: number): THREE.Mesh {
  const m = new THREE.Mesh(new THREE.CylinderGeometry(radius, radius, 1, 8), mat(color));
  m.castShadow = true;
  return m;
}

const UP = new THREE.Vector3(0, 1, 0);
const _dir = new THREE.Vector3();
function setBar(bar: THREE.Mesh, a: THREE.Vector3, b: THREE.Vector3) {
  _dir.subVectors(b, a);
  const len = _dir.length();
  bar.position.copy(a).addScaledVector(_dir, 0.5);
  bar.scale.set(1, Math.max(len, 0.001), 1);
  bar.quaternion.setFromUnitVectors(UP, _dir.normalize());
}

export class Bicycle {
  readonly group = new THREE.Group();
  private wheels: THREE.Group[] = [];
  private pedalAngle = 0;
  private legs: { thigh: THREE.Mesh; shin: THREE.Mesh; side: number }[] = [];
  private bottomBracket = new THREE.Vector3(0, 0.5, -0.05);
  private pedals: THREE.Mesh[] = [];
  readonly parcelBox: THREE.Group;

  constructor() {
    const frameColor = COLORS.blue;
    const v = (x: number, y: number, z: number) => new THREE.Vector3(x, y, z);

    // wheels
    for (const z of [-0.85, 0.85]) {
      const w = new THREE.Group();
      const tire = new THREE.Mesh(
        new THREE.TorusGeometry(WHEEL_R, 0.075, 8, 28),
        mat(0x20263a),
      );
      tire.rotation.y = Math.PI / 2;
      tire.castShadow = true;
      w.add(tire);
      for (let k = 0; k < 3; k++) {
        const spoke = new THREE.Mesh(
          new THREE.BoxGeometry(0.03, WHEEL_R * 2, 0.03),
          mat(0xd7deef),
        );
        spoke.rotation.x = (k * Math.PI) / 3;
        w.add(spoke);
      }
      w.position.set(0, WHEEL_R, z);
      this.group.add(w);
      this.wheels.push(w);
    }

    // frame
    const rear = v(0, WHEEL_R, -0.85);
    const front = v(0, WHEEL_R, 0.85);
    const bb = this.bottomBracket;
    const seat = v(0, 1.18, -0.4);
    const head = v(0, 1.2, 0.58);
    const frame: [THREE.Vector3, THREE.Vector3][] = [
      [rear, bb],
      [bb, seat],
      [seat, rear],
      [seat, head],
      [bb, head],
      [head, front],
    ];
    for (const [a, b] of frame) {
      const bar = makeBar(0.045, frameColor);
      setBar(bar, a, b);
      this.group.add(bar);
    }
    const stem = makeBar(0.04, 0x20263a);
    setBar(stem, head, v(0, 1.4, 0.55));
    const handle = box(0.62, 0.06, 0.06, 0x20263a);
    handle.position.set(0, 1.42, 0.55);
    this.group.add(stem, handle);
    const saddle = box(0.2, 0.07, 0.34, 0x20263a);
    saddle.position.set(0, 1.24, -0.42);
    this.group.add(saddle);

    // rider
    const hip = v(0, 1.28, -0.4);
    const shoulder = v(0, 1.85, 0.0);
    const torso = new THREE.Mesh(new THREE.CylinderGeometry(0.2, 0.17, 1, 10), mat(COLORS.shirt));
    torso.castShadow = true;
    setBar(torso, hip, shoulder);
    this.group.add(torso);
    const head3 = new THREE.Mesh(new THREE.SphereGeometry(0.19, 14, 12), mat(COLORS.skin));
    head3.position.set(0, 2.12, 0.1);
    head3.castShadow = true;
    const helmet = new THREE.Mesh(
      new THREE.SphereGeometry(0.225, 14, 10, 0, Math.PI * 2, 0, Math.PI / 2),
      mat(COLORS.blue),
    );
    helmet.position.set(0, 2.16, 0.08);
    helmet.castShadow = true;
    this.group.add(head3, helmet);
    for (const side of [-1, 1]) {
      const arm = makeBar(0.06, COLORS.shirt);
      setBar(arm, v(side * 0.2, 1.78, 0.02), v(side * 0.28, 1.42, 0.55));
      this.group.add(arm);
      // legs
      const thigh = makeBar(0.09, COLORS.pants);
      const shin = makeBar(0.075, COLORS.pants);
      this.group.add(thigh, shin);
      this.legs.push({ thigh, shin, side });
      const pedal = box(0.12, 0.05, 0.22, 0x20263a);
      this.group.add(pedal);
      this.pedals.push(pedal);
    }

    // delivery box on the back
    this.parcelBox = new THREE.Group();
    const cube = box(0.78, 0.74, 0.78, COLORS.blue);
    cube.position.set(0, 0.37, 0);
    const lid = box(0.82, 0.08, 0.82, COLORS.blueDark);
    lid.position.set(0, 0.78, 0);
    const logo = new THREE.Mesh(
      new THREE.CircleGeometry(0.22, 20),
      new THREE.MeshStandardMaterial({ color: 0xffffff, roughness: 0.6 }),
    );
    logo.position.set(0.4, 0.4, 0);
    logo.rotation.y = Math.PI / 2;
    const logo2 = logo.clone();
    logo2.position.x = -0.4;
    logo2.rotation.y = -Math.PI / 2;
    this.parcelBox.add(cube, lid, logo, logo2);
    this.parcelBox.position.set(0, 1.28, -1.02);
    this.group.add(this.parcelBox);

    this.updateLegs();
  }

  /** speedMs in world meters per second, scale = visual scale applied to the group. */
  animate(dt: number, speedMs: number, scale: number) {
    const omega = speedMs / (WHEEL_R * scale);
    for (const w of this.wheels) w.rotation.x += omega * dt;
    this.pedalAngle += omega * 0.55 * dt;
    this.updateLegs();
  }

  private updateLegs() {
    const L1 = 0.62;
    const L2 = 0.62;
    const hipY = 1.28;
    const hipZ = -0.4;
    for (const leg of this.legs) {
      const phase = this.pedalAngle + (leg.side > 0 ? 0 : Math.PI);
      const footY = this.bottomBracket.y + Math.sin(phase) * 0.2;
      const footZ = this.bottomBracket.z + Math.cos(phase) * 0.2;
      // 2-bone IK in the YZ plane, knee points forward (+Z)
      let dy = footY - hipY;
      let dz = footZ - hipZ;
      let d = Math.hypot(dy, dz);
      const maxD = L1 + L2 - 0.01;
      if (d > maxD) {
        dy *= maxD / d;
        dz *= maxD / d;
        d = maxD;
      }
      const a = (L1 * L1 - L2 * L2 + d * d) / (2 * d);
      const h = Math.sqrt(Math.max(0, L1 * L1 - a * a));
      const ny = dy / d;
      const nz = dz / d;
      const kneeY = hipY + ny * a + nz * h;
      const kneeZ = hipZ + nz * a - ny * h;
      const x = leg.side * 0.13;
      const hip = new THREE.Vector3(x, hipY, hipZ);
      const knee = new THREE.Vector3(x, kneeY, kneeZ);
      const foot = new THREE.Vector3(x, footY, footZ);
      setBar(leg.thigh, hip, knee);
      setBar(leg.shin, knee, foot);
      this.pedals[leg.side > 0 ? 1 : 0].position.set(leg.side * 0.16, footY - 0.03, footZ);
    }
  }
}
