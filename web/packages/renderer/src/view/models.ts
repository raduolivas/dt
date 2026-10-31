import * as THREE from 'three';

import { BUILDING_TINTS, COLORS } from './colors';
import { LIT_CELLS, litWindowTexture, markShared } from './night';

export { COLORS, DAY, NIGHT, type Palette } from './colors';

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
/** A 2D canvas, or null where there is none (Node tests): textures then fall back to plain colors. */
function canvas2d(w: number, h: number): { c: HTMLCanvasElement; g: CanvasRenderingContext2D } | null {
  if (typeof document === 'undefined') return null;
  const c = document.createElement('canvas');
  c.width = w;
  c.height = h;
  const g = c.getContext('2d');
  return g ? { c, g } : null;
}

let windowBase: THREE.Texture | null = null;
/** The prototype's window cell: one window per UV unit, white frame around it. Shared, do not dispose. */
export function windowTexture(): THREE.Texture {
  if (windowBase) return windowBase;
  const cv = canvas2d(64, 64);
  if (!cv) {
    windowBase = new THREE.Texture();
    return windowBase;
  }
  const { c, g } = cv;
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
  windowBase = markShared(t);
  return t;
}

function sideMaterial(tint: number, repeatX: number, repeatY: number) {
  const t = windowTexture().clone();
  t.repeat.set(repeatX, repeatY);
  t.needsUpdate = true;
  return new THREE.MeshStandardMaterial({ color: tint, map: t, roughness: 0.75 });
}

const TINTS = BUILDING_TINTS;

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
  const cv = canvas2d(512, Math.round((512 * h) / w));
  if (!cv) return new THREE.Mesh(new THREE.PlaneGeometry(w, h), new THREE.MeshStandardMaterial({ color: bg }));
  const { c, g } = cv;
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
  // The sign lights up at night: its own picture as the emissive map (`applyTheme` raises the `sign` glow).
  const m = new THREE.MeshStandardMaterial({ map: t, emissive: 0xffffff, emissiveMap: t, emissiveIntensity: 0, roughness: 0.6 });
  m.userData.dtGlow = 'sign';
  return new THREE.Mesh(new THREE.PlaneGeometry(w, h), m);
}

// ---------------------------------------------------------------------------
// Store and house. Both are modelled with their FRONT facing +Z.
// ---------------------------------------------------------------------------
export const STORE_HEIGHT = 18;
export const HOUSE_HEIGHT = 24;

/** `accent` is the store body's own material, so the partner's color can recolor it. */
export function makeStore(label: string, accent?: THREE.MeshStandardMaterial): THREE.Group {
  const g = new THREE.Group();
  const body = box(44, STORE_HEIGHT, 32, COLORS.blue);
  if (accent) body.material = accent;
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
// Store and house on a real building footprint (spec §5.2). Coordinates are scene metres (x east, z south).
// ---------------------------------------------------------------------------
export interface FootprintShape {
  /** Exterior ring. */
  ring: { x: number; z: number }[];
  holes: { x: number; z: number }[][];
  /** Wall height in metres (after the toy cap). */
  height: number;
}

/** Extruded prism of a footprint from y0 to y1; material groups: 0 = top/bottom, 1 = sides. */
function prism(fp: FootprintShape, y0: number, y1: number): THREE.BufferGeometry {
  const shape = new THREE.Shape(fp.ring.map((p) => new THREE.Vector2(p.x, -p.z)));
  for (const h of fp.holes) shape.holes.push(new THREE.Path(h.map((p) => new THREE.Vector2(p.x, -p.z))));
  const g = new THREE.ExtrudeGeometry(shape, { depth: y1 - y0, bevelEnabled: false });
  g.rotateX(-Math.PI / 2);
  g.translate(0, y0, 0);
  return g;
}

function signedArea(r: { x: number; z: number }[]): number {
  let a = 0;
  for (let i = 0; i < r.length; i++) {
    const p = r[i]!;
    const q = r[(i + 1) % r.length]!;
    a += p.x * q.z - q.x * p.z;
  }
  return a / 2;
}

export function footprintCentroid(r: { x: number; z: number }[]): { x: number; z: number; area: number } {
  let a = 0;
  let cx = 0;
  let cz = 0;
  for (let i = 0; i < r.length; i++) {
    const p = r[i]!;
    const q = r[(i + 1) % r.length]!;
    const f = p.x * q.z - q.x * p.z;
    a += f;
    cx += (p.x + q.x) * f;
    cz += (p.z + q.z) * f;
  }
  if (Math.abs(a) < 1e-6) return { x: r[0]!.x, z: r[0]!.z, area: 0 };
  return { x: cx / (3 * a), z: cz / (3 * a), area: Math.abs(a / 2) };
}

/**
 * The facade that faces `toward` (the courier's stop on the street): its midpoint, length and outward direction.
 * A group placed with `facadeFrame` has local +X along the facade and local +Z pointing out of the building.
 */
export function frontFacade(fp: FootprintShape, toward: { x: number; z: number }) {
  const r = fp.ring;
  const sign = Math.sign(signedArea(r)) || 1;
  let best = { mid: { x: r[0]!.x, z: r[0]!.z }, length: 1, out: { x: 0, z: 1 } };
  let bestScore = Infinity;
  for (let i = 0; i < r.length; i++) {
    const a = r[i]!;
    const b = r[(i + 1) % r.length]!;
    const dx = b.x - a.x;
    const dz = b.z - a.z;
    const l = Math.hypot(dx, dz);
    if (l < 2) continue;
    const mid = { x: (a.x + b.x) / 2, z: (a.z + b.z) / 2 };
    const out = { x: (dz / l) * sign, z: (-dx / l) * sign };
    const tx = toward.x - mid.x;
    const tz = toward.z - mid.z;
    const facing = (tx * out.x + tz * out.z) / (Math.hypot(tx, tz) || 1); // 1 = looks straight at the stop
    const score = Math.hypot(tx, tz) * (2 - facing) - Math.min(l, 20) * 0.5;
    if (score < bestScore) {
      bestScore = score;
      best = { mid, length: l, out };
    }
  }
  return best;
}

function facadeFrame(f: ReturnType<typeof frontFacade>): THREE.Group {
  const g = new THREE.Group();
  g.position.set(f.mid.x, 0, f.mid.z);
  g.rotation.y = Math.atan2(f.out.x, f.out.z);
  return g;
}

/** Height of the store on a footprint: its own (toy) height, raised so it rises over the neighbours. */
export function storeHeightOn(fp: FootprintShape): number {
  return Math.max(fp.height, 12) + 4;
}

/** The prototype's store treatment on a real footprint: accent walls, white roof, striped awning, shop window, sign. */
export function makeStoreOnFootprint(
  fp: FootprintShape,
  label: string,
  toward: { x: number; z: number },
  accent: THREE.MeshStandardMaterial,
): THREE.Group {
  const g = new THREE.Group();
  // A little taller than its real self so the blue store shows over the neighbouring roofs (toy emphasis).
  const h = storeHeightOn(fp);
  const body = new THREE.Mesh(prism(fp, 0, h), [mat(COLORS.white), accent]);
  body.castShadow = body.receiveShadow = true;
  const cap = new THREE.Mesh(prism(fp, h, h + 1.4), [accent, mat(COLORS.white)]);
  cap.castShadow = cap.receiveShadow = true;
  g.add(body, cap);

  const f = frontFacade(fp, toward);
  const front = facadeFrame(f);
  const w = Math.max(6, f.length * 0.94);
  const k = Math.min(1, Math.max(0.45, h / 18)); // the prototype's store is 18 m high
  const awnY = 11.5 * k;
  const stripes = Math.max(4, Math.round(w / 5.5) * 2);
  for (let i = 0; i < stripes; i++) {
    const s = box(w / stripes, 0.8, 6 * k + 1, i % 2 ? COLORS.white : COLORS.coral);
    s.position.set(-w / 2 + (i + 0.5) * (w / stripes), awnY, (6 * k + 1) / 2 - 0.4);
    s.rotation.x = 0.38;
    front.add(s);
  }
  const win = box(w * 0.55, 7 * k, 0.6, COLORS.glass, { emissive: 0x6f9cff, emissiveIntensity: 0.25 });
  win.position.set(-w * 0.14, 5.5 * k, 0.3);
  const door = box(Math.min(6.5, w * 0.2), 10 * k, 0.7, COLORS.white);
  door.position.set(w * 0.32, 5 * k, 0.35);
  front.add(win, door);
  const sw = Math.min(30, Math.max(8, w * 0.9));
  const sh = (sw * 5.2) / 30;
  const sign = textSign(label.toUpperCase(), sw, sh, '#ffffff', '#2f5bea');
  sign.position.set(0, Math.min(h - sh / 2 - 0.3, awnY + 2 + sh / 2), 0.4);
  front.add(sign);

  g.add(front);

  // Rooftop sign board (a game touch: the store's name reads from above the city). It turns to face the camera:
  // the scene sets `userData.billboard.rotation.y` every frame.
  const c = footprintCentroid(fp.ring);
  const bw = Math.min(24, Math.max(12, Math.sqrt(c.area) * 1.3));
  const bh = bw * 0.24;
  const roofY = h + 1.4;
  const billboard = new THREE.Group();
  billboard.position.set(c.x, roofY, c.z);
  const board = textSign(label.toUpperCase(), bw, bh, '#ffffff', '#2f5bea');
  board.position.set(0, 2.4 + bh / 2, 0.3);
  const back = box(bw + 0.8, bh + 0.8, 0.5, COLORS.blue);
  back.material = accent;
  back.position.set(0, 2.4 + bh / 2, 0);
  billboard.add(back, board);
  for (const x of [-bw * 0.35, bw * 0.35]) {
    const post = box(0.6, 2.4, 0.6, COLORS.white);
    post.position.set(x, 1.2, 0);
    billboard.add(post);
  }
  g.add(billboard);
  g.userData.billboard = billboard;

  if (c.area > 400) {
    const tank = new THREE.Mesh(new THREE.CylinderGeometry(2.4, 2.4, 4, 12), mat(COLORS.blueLight));
    tank.position.set(c.x + bw * 0.4, h + 1.4 + 2, c.z + bw * 0.4);
    tank.castShadow = true;
    g.add(tank);
  }
  return g;
}

/** The prototype's house treatment on a real footprint: white walls with windows, coral hip roof, blue door, parcel. */
export function makeHouseOnFootprint(fp: FootprintShape, toward: { x: number; z: number }): HouseModel & { top: number } {
  const g = new THREE.Group();
  // A little taller than its real self, like the store, so the coral roof shows over the neighbours.
  const h = Math.max(fp.height, 10) + 3;
  const walls = windowTexture().clone();
  walls.repeat.set(1 / 6.5, 1 / 6.5);
  walls.needsUpdate = true;
  // Lit windows at night, like the city's buildings (the `windows` glow).
  const lit = litWindowTexture().clone();
  lit.repeat.set(1 / 6.5 / LIT_CELLS, 1 / 6.5 / LIT_CELLS);
  lit.needsUpdate = true;
  const wallMat = new THREE.MeshStandardMaterial({
    color: COLORS.white,
    map: walls,
    emissive: 0xffffff,
    emissiveMap: lit,
    emissiveIntensity: 0,
    roughness: 0.75,
  });
  wallMat.userData.dtGlow = 'windows';
  const body = new THREE.Mesh(prism(fp, 0, h), [mat(COLORS.white), wallMat]);
  body.castShadow = body.receiveShadow = true;
  g.add(body);

  // Hip roof: a pyramid from the outline up to the centroid.
  const c = footprintCentroid(fp.ring);
  const rh = Math.min(10, Math.max(6, Math.sqrt(c.area) * 0.45));
  const pos: number[] = [];
  const r = fp.ring;
  for (let i = 0; i < r.length; i++) {
    const a = r[i]!;
    const b = r[(i + 1) % r.length]!;
    pos.push(a.x, h, a.z, b.x, h, b.z, c.x, h + rh, c.z);
  }
  const roofGeo = new THREE.BufferGeometry();
  roofGeo.setAttribute('position', new THREE.Float32BufferAttribute(pos, 3));
  roofGeo.computeVertexNormals();
  const roof = new THREE.Mesh(roofGeo, mat(COLORS.coral, { flatShading: true, side: THREE.DoubleSide }));
  roof.castShadow = true;
  g.add(roof);

  const f = frontFacade(fp, toward);
  const front = facadeFrame(f);
  const w = Math.max(5, f.length);
  const k = Math.min(1, Math.max(0.5, h / 14)); // the prototype's house is 14 m high
  const doorX = -Math.min(w * 0.22, 5);
  const door = box(Math.min(5.5, w * 0.3), 9 * k, 0.7, COLORS.blue);
  door.position.set(doorX, 4.5 * k, 0.35);
  const step = box(Math.min(9, w * 0.5), 0.8, 5, 0xdfe6f6);
  step.position.set(doorX, 0.8 + 0.4, 2.5);
  front.add(door, step);

  const parcel = new THREE.Group();
  const pk = box(4.2, 3.4, 4.2, 0xd9a066);
  pk.position.y = 1.7;
  const tape = box(4.3, 3.5, 0.9, 0xf3d9ae);
  tape.position.y = 1.7;
  parcel.add(pk, tape);
  parcel.position.set(doorX, 0.8, 6);
  parcel.visible = false;
  front.add(parcel);
  g.add(front);
  return { group: g, parcel, top: h + rh };
}

/** A low-poly tree (trunk + faceted crown) as one geometry with vertex colors, for instancing. */
export function treeGeometry(): THREE.BufferGeometry {
  const trunk = new THREE.CylinderGeometry(0.5, 0.7, 4, 6).toNonIndexed();
  trunk.translate(0, 2, 0);
  const crown = new THREE.IcosahedronGeometry(3.4, 0);
  crown.translate(0, 6.6, 0);
  const paint = (geo: THREE.BufferGeometry, hex: number) => {
    const c = new THREE.Color(hex);
    const n = geo.getAttribute('position').count;
    const a = new Float32Array(n * 3);
    for (let i = 0; i < n; i++) a.set([c.r, c.g, c.b], i * 3);
    geo.setAttribute('color', new THREE.BufferAttribute(a, 3));
    geo.deleteAttribute('uv');
    return geo;
  };
  const parts = [paint(trunk, COLORS.trunk), paint(crown, COLORS.leaf)];
  const count = parts.reduce((s, p) => s + p.getAttribute('position').count, 0);
  const out = new THREE.BufferGeometry();
  for (const name of ['position', 'normal', 'color']) {
    const a = new Float32Array(count * 3);
    let o = 0;
    for (const p of parts) {
      const src = p.getAttribute(name).array as Float32Array;
      a.set(src, o);
      o += src.length;
    }
    out.setAttribute(name, new THREE.BufferAttribute(a, 3));
  }
  out.computeBoundingSphere();
  return out;
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
// Courier parts: geometries shared by every courier model (and every vehicle), materials from the `mat` cache.
// ---------------------------------------------------------------------------
const geoCache = new Map<string, THREE.BufferGeometry>();

/** One geometry per key for all courier models. Shared: never dispose it from a model. */
export function sharedGeo<T extends THREE.BufferGeometry>(key: string, make: () => T): T {
  let g = geoCache.get(key) as T | undefined;
  if (!g) {
    g = make();
    geoCache.set(key, g);
  }
  return g;
}

/** A mesh of a shared geometry in a cached material, casting a shadow. */
export function part(geo: THREE.BufferGeometry, color: number, extra: Partial<THREE.MeshStandardMaterialParameters> = {}) {
  const m = new THREE.Mesh(geo, mat(color, extra));
  m.castShadow = true;
  return m;
}

export function partBox(w: number, h: number, d: number, color: number, extra: Partial<THREE.MeshStandardMaterialParameters> = {}) {
  return part(sharedGeo(`box:${w}:${h}:${d}`, () => new THREE.BoxGeometry(w, h, d)), color, extra);
}

export function partSphere(r: number, color: number, extra: Partial<THREE.MeshStandardMaterialParameters> = {}) {
  return part(sharedGeo(`sphere:${r}`, () => new THREE.SphereGeometry(r, 14, 12)), color, extra);
}

/** A unit-length bar along +Y; `setBar` stretches it between two points. */
export function partBar(radius: number, color: number, top = radius) {
  return part(sharedGeo(`bar:${radius}:${top}`, () => new THREE.CylinderGeometry(top, radius, 1, 8)), color);
}

export const UP = new THREE.Vector3(0, 1, 0);
const _dir = new THREE.Vector3();
/** Puts a unit bar (or box) of length 1 along +Y between `a` and `b`. */
export function setBar(bar: THREE.Object3D, a: THREE.Vector3, b: THREE.Vector3) {
  _dir.subVectors(b, a);
  const len = _dir.length();
  bar.position.copy(a).addScaledVector(_dir, 0.5);
  bar.scale.set(1, Math.max(len, 0.001), 1);
  bar.quaternion.setFromUnitVectors(UP, _dir.normalize());
}

/** The blue courier box with white logos (the parcel), base at y = 0. */
export function makeCourierBox(size = 0.78): THREE.Group {
  const g = new THREE.Group();
  const cube = partBox(size, size * 0.95, size, COLORS.blue);
  cube.position.y = (size * 0.95) / 2;
  const lid = partBox(size + 0.04, 0.08, size + 0.04, COLORS.blueDark);
  lid.position.y = size;
  const logoGeo = sharedGeo(`circle:${size}`, () => new THREE.CircleGeometry(size * 0.28, 20));
  g.add(cube, lid);
  for (const side of [-1, 1]) {
    const logo = new THREE.Mesh(logoGeo, mat(0xffffff, { roughness: 0.6 }));
    logo.position.set(side * (size / 2 + 0.01), size * 0.5, 0);
    logo.rotation.y = (side * Math.PI) / 2;
    g.add(logo);
  }
  return g;
}

/** The rider's head: skin, with a helmet (half sphere) in `helmet` color. */
export function makeHead(helmet: number = COLORS.blue): THREE.Group {
  const g = new THREE.Group();
  const head = partSphere(0.19, COLORS.skin);
  const cap = part(
    sharedGeo('helmet', () => new THREE.SphereGeometry(0.225, 14, 10, 0, Math.PI * 2, 0, Math.PI / 2)),
    helmet,
  );
  cap.position.set(0, 0.04, -0.02);
  g.add(head, cap);
  return g;
}

// ---------------------------------------------------------------------------
// Bicycle courier (the prototype's) and the e-bike. Faces +Z. Units are "bike meters"; the scene scales it up.
// ---------------------------------------------------------------------------
const WHEEL_R = 0.55;
export const BIKE_WHEEL_RADIUS = WHEEL_R;

export interface BicycleStyle {
  /** The e-bike: chunkier frame, battery on the down tube, fat tyres, front light, hub motor, calmer pedalling. */
  electric?: boolean;
}

export class Bicycle {
  readonly group = new THREE.Group();
  private wheels: THREE.Group[] = [];
  private pedalAngle = 0;
  private legs: { thigh: THREE.Mesh; shin: THREE.Mesh; side: number }[] = [];
  private bottomBracket = new THREE.Vector3(0, 0.5, -0.05);
  private pedals: THREE.Mesh[] = [];
  /** Pedal turns per wheel turn (the e-bike's motor helps: calmer legs). */
  private readonly cadence: number;
  private readonly hip = new THREE.Vector3();
  private readonly knee = new THREE.Vector3();
  private readonly foot = new THREE.Vector3();
  readonly parcelBox: THREE.Group;

  constructor(style: BicycleStyle = {}) {
    const electric = style.electric ?? false;
    this.cadence = electric ? 0.35 : 0.55;
    const frameColor = electric ? COLORS.blueDark : COLORS.blue;
    const tube = electric ? 0.065 : 0.045;
    const tyre = electric ? 0.11 : 0.075;
    const v = (x: number, y: number, z: number) => new THREE.Vector3(x, y, z);

    // wheels
    const tireGeo = sharedGeo(`tire:${tyre}`, () => new THREE.TorusGeometry(WHEEL_R, tyre, 8, 28));
    const spokeGeo = sharedGeo('spoke', () => new THREE.BoxGeometry(0.03, WHEEL_R * 2, 0.03));
    for (const z of [-0.85, 0.85]) {
      const w = new THREE.Group();
      const tire = part(tireGeo, 0x20263a);
      tire.rotation.y = Math.PI / 2;
      w.add(tire);
      for (let k = 0; k < 3; k++) {
        const spoke = new THREE.Mesh(spokeGeo, mat(0xd7deef));
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
      const bar = partBar(tube, frameColor);
      setBar(bar, a, b);
      this.group.add(bar);
    }
    const stem = partBar(0.04, 0x20263a);
    setBar(stem, head, v(0, 1.4, 0.55));
    const handle = partBox(0.62, 0.06, 0.06, 0x20263a);
    handle.position.set(0, 1.42, 0.55);
    this.group.add(stem, handle);
    const saddle = partBox(0.2, 0.07, 0.34, 0x20263a);
    saddle.position.set(0, 1.24, -0.42);
    this.group.add(saddle);

    if (electric) {
      // battery along the down tube, with a green charge stripe; front light; hub motor on the rear wheel
      const battery = partBox(0.17, 1, 0.17, COLORS.white);
      setBar(battery, v(0, 0.66, 0.08), v(0, 0.98, 0.4));
      const charge = partBox(0.19, 1, 0.05, COLORS.green, { emissive: COLORS.green, emissiveIntensity: 0.4 });
      setBar(charge, v(0, 0.74, 0.16), v(0, 0.9, 0.32));
      const light = partBox(0.14, 0.1, 0.1, 0xfff6d8, { emissive: 0xfff1b8, emissiveIntensity: 0.9 });
      light.position.set(0, 1.18, 0.7);
      const motor = part(sharedGeo('hub', () => new THREE.CylinderGeometry(0.17, 0.17, 0.16, 14)), 0x3a4260);
      motor.rotation.z = Math.PI / 2;
      motor.position.copy(rear);
      this.group.add(battery, charge, light, motor);
    }

    // rider
    const hip = v(0, 1.28, -0.4);
    const shoulder = v(0, 1.85, 0.0);
    const torso = part(sharedGeo('torso', () => new THREE.CylinderGeometry(0.2, 0.17, 1, 10)), COLORS.shirt);
    setBar(torso, hip, shoulder);
    this.group.add(torso);
    const head3 = makeHead();
    head3.position.set(0, 2.12, 0.1);
    this.group.add(head3);
    for (const side of [-1, 1]) {
      const arm = partBar(0.06, COLORS.shirt);
      setBar(arm, v(side * 0.2, 1.78, 0.02), v(side * 0.28, 1.42, 0.55));
      this.group.add(arm);
      // legs
      const thigh = partBar(0.09, COLORS.pants);
      const shin = partBar(0.075, COLORS.pants);
      this.group.add(thigh, shin);
      this.legs.push({ thigh, shin, side });
      const pedal = partBox(0.12, 0.05, 0.22, 0x20263a);
      this.group.add(pedal);
      this.pedals.push(pedal);
    }

    // delivery box on the back
    this.parcelBox = makeCourierBox();
    this.parcelBox.position.set(0, 1.28, -1.02);
    this.group.add(this.parcelBox);

    this.updateLegs();
  }

  /** speedMs in world meters per second, scale = visual scale applied to the group. */
  animate(dt: number, speedMs: number, scale: number) {
    if (speedMs <= 0) return;
    const omega = speedMs / (WHEEL_R * scale);
    for (const w of this.wheels) w.rotation.x += omega * dt;
    this.pedalAngle += omega * this.cadence * dt;
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
      this.hip.set(x, hipY, hipZ);
      this.knee.set(x, kneeY, kneeZ);
      this.foot.set(x, footY, footZ);
      setBar(leg.thigh, this.hip, this.knee);
      setBar(leg.shin, this.knee, this.foot);
      this.pedals[leg.side > 0 ? 1 : 0]!.position.set(leg.side * 0.16, footY - 0.03, footZ);
    }
  }
}
