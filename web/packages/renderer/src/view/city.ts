import * as THREE from 'three';
import type { LatLng, TrackingSnapshot } from '@dt/contract';
import type { GeoProjection, Vec2 } from '../engine/geo';
import { toLatLng } from '../snapshot';
import { GROUND_Y } from '../tiles/build/common';
import type { BuildLayers, FootprintBuffers, TileTheme } from '../tiles/build/decode';
import { boxTiles, corridorTiles, distanceToTileM, type TileXY } from '../tiles/corridor';
import type { TileSource } from '../tiles/source';
import { createWorkerBuilder, type TileBuilder } from '../tiles/workerPool';
import { COLORS } from './colors';
import {
  HOUSE_HEIGHT,
  STORE_HEIGHT,
  footprintCentroid,
  makeHouse,
  makeHouseOnFootprint,
  makeStore,
  makeStoreOnFootprint,
  storeHeightOn,
  mat,
  treeGeometry,
  windowTexture,
  type FootprintShape,
  type HouseModel,
} from './models';

export type { TileBuilder } from '../tiles/workerPool';

/** Scene layers of one tile; each is at most one draw call (one merged mesh, trees one instanced mesh). */
export const TILE_LAYERS = ['landuse', 'parks', 'water', 'roads', 'buildings', 'trees'] as const;
export type TileLayer = (typeof TILE_LAYERS)[number];

/** Plan P9: the first load is a corridor along the legs plus the store and home surroundings. */
const FIRST_CORRIDOR_M = 150;
const FIRST_AROUND_STOPS_M = 200;
/** The rest of the route box loads lazily around the camera or the courier. */
const LAZY_BOX_M = 300;
const LAZY_RADIUS_M = 700;
const LAZY_CONCURRENCY = 2;
const RETRY_MS = 5000;

/** A building that contains a point, in scene metres. */
export interface Footprint extends FootprintShape {
  minHeight: number;
  center: Vec2;
  area: number;
  /** The point it was found for (local metres). */
  at: Vec2;
}

interface LoadedTile {
  xy: TileXY;
  group: THREE.Group;
  /** Raw tile, kept to rebuild on a theme change. */
  data: ArrayBuffer | null;
  footprints: FootprintBuffers | null;
  buildings: THREE.Mesh | null;
  /** Lookup index of the footprints, computed on the first lookup. */
  index: FootprintIndex | null;
}

const key = (t: TileXY) => `${t.z}/${t.x}/${t.y}`;

/**
 * The real-map city: loads OpenMapTiles tiles around a delivery, builds their geometry in the worker and adds one
 * merged mesh per layer and tile under `root`, in the prototype's toy style.
 */
export class TileCity {
  private readonly tiles = new Map<string, LoadedTile>();
  private readonly pending = new Map<string, TileXY>();
  private readonly loading = new Set<string>();
  private readonly inflight = new Set<Promise<void>>();
  private readonly abort = new AbortController();
  private readonly builder: TileBuilder;
  private readonly ownsBuilder: boolean;
  private readonly materials: Record<Exclude<TileLayer, 'trees'> | 'trees', THREE.Material>;
  private readonly treeGeo = treeGeometry();
  private readonly shimmer = { value: 0 };
  private readonly cut: Cutaway = {
    uDtFocus: { value: new THREE.Vector3() },
    uDtToCamera: { value: new THREE.Vector3(0, 1, 0) },
    uDtCut: { value: 0 },
  };
  private hidden: Footprint[] = [];
  private disposed = false;
  /** Lazy loading waits for the first load, so `loadFirst`'s byte count covers every tile fetched until then. */
  private firstDone = false;
  /** When a tile last failed: it is retried after RETRY_MS, not in a tight loop. */
  private readonly failedAt = new Map<string, number>();
  private lazyActive = 0;
  private lastFocus: THREE.Vector3 | null = null;
  private lastRadius = LAZY_RADIUS_M;

  constructor(
    private readonly root: THREE.Group,
    private readonly source: TileSource,
    readonly projection: GeoProjection,
    private theme: TileTheme,
    opts: { builder?: TileBuilder } = {},
  ) {
    this.ownsBuilder = !opts.builder;
    this.builder = opts.builder ?? createWorkerBuilder();
    const flat = new THREE.MeshStandardMaterial({ vertexColors: true, roughness: 0.95, metalness: 0 });
    this.materials = {
      landuse: flat,
      parks: flat,
      roads: flat,
      water: waterMaterial(this.shimmer),
      buildings: withCutaway(
        new THREE.MeshStandardMaterial({ vertexColors: true, map: windowTexture(), roughness: 0.75, metalness: 0.02 }),
        this.cut,
        'buildings',
      ),
      trees: withCutaway(
        new THREE.MeshStandardMaterial({ vertexColors: true, flatShading: true, roughness: 0.8, metalness: 0.02 }),
        this.cut,
        'trees',
        this.shimmer,
      ),
    };
  }

  get loadedTiles(): number {
    return this.tiles.size;
  }

  get pendingTiles(): number {
    return this.pending.size;
  }

  /** Loads the first tiles of a delivery and resolves when they are built. `bytes`: tile bytes fetched. */
  async loadFirst(s: TrackingSnapshot): Promise<{ bytes: number }> {
    const z = this.source.zoom;
    const legs = legsOf(s);
    const stops = [s.pickup.location, s.dropoff.location];
    const first = new Map<string, TileXY>();
    for (const t of corridorTiles(legs, stops, z, FIRST_CORRIDOR_M)) first.set(key(t), t);
    for (const p of stops) for (const t of boxTiles([], [p], z, FIRST_AROUND_STOPS_M)) first.set(key(t), t);
    this.addRoute(s);
    for (const k of first.keys()) this.pending.delete(k);

    // Nearest to the store, home and courier first, so what the camera frames appears first.
    const focus = [...stops, ...(s.courier?.position ? [s.courier.position] : [])];
    const order = [...first.values()].sort(
      (a, b) => Math.min(...focus.map((p) => distanceToTileM(p, a))) - Math.min(...focus.map((p) => distanceToTileM(p, b))),
    );
    const sizes = await Promise.all(order.map((t) => this.load(t)));
    this.firstDone = true;
    if (this.lastFocus) {
      // A focus asked for during the first load runs after it resolved (next task), so its bytes are not counted.
      const kick = new Promise<void>((resolve) =>
        setTimeout(() => {
          if (this.lastFocus) this.loadMore(this.lastFocus, this.lastRadius);
          resolve();
        }, 0),
      );
      this.inflight.add(kick);
      void kick.finally(() => this.inflight.delete(kick));
    }
    return { bytes: sizes.reduce((a, b) => a + b, 0) };
  }

  /** Queues the route's bounding box (plus 300 m) for lazy loading; call again when the route changes. */
  addRoute(s: TrackingSnapshot): void {
    const points = [s.pickup.location, s.dropoff.location, ...(s.courier?.position ? [s.courier.position] : [])];
    for (const t of boxTiles(legsOf(s), points, this.source.zoom, LAZY_BOX_M)) {
      const k = key(t);
      if (!this.tiles.has(k) && !this.loading.has(k)) this.pending.set(k, t);
    }
  }

  /** Loads the queued tiles within `radiusM` of `focus` (scene metres; camera target or courier), a couple at a time. */
  loadMore(focus: THREE.Vector3, radiusM = LAZY_RADIUS_M): void {
    if (this.disposed) return;
    this.lastFocus = focus.clone();
    this.lastRadius = radiusM;
    if (!this.firstDone) return; // remembered; runs when the first load is done
    const at = this.projection.toLatLng({ x: focus.x, z: focus.z });
    const near = [...this.pending.values()]
      .map((t) => ({ t, d: distanceToTileM(at, t) }))
      .filter((e) => e.d <= radiusM && Date.now() - (this.failedAt.get(key(e.t)) ?? -Infinity) >= RETRY_MS)
      .sort((a, b) => a.d - b.d);
    for (const { t } of near) {
      if (this.lazyActive >= LAZY_CONCURRENCY) break;
      this.pending.delete(key(t));
      this.lazyActive++;
      const job: Promise<void> = this.load(t).then(() => {
        this.lazyActive--;
        if (this.lastFocus) this.loadMore(this.lastFocus, this.lastRadius);
      });
      this.inflight.add(job);
      void job.finally(() => this.inflight.delete(job));
    }
  }

  /** Resolves when nothing is loading (tests). */
  async idle(): Promise<void> {
    while (this.inflight.size > 0) await Promise.allSettled([...this.inflight]);
  }

  /** Rebuilds the loaded tiles with the other palette. */
  setTheme(t: TileTheme): void {
    if (t === this.theme) return;
    this.theme = t;
    for (const tile of this.tiles.values()) void this.build(tile);
  }

  /** Per-frame: the water shimmer and the trees' sway (`time` in seconds; pass a constant to stop both). */
  frame(time: number): void {
    this.shimmer.value = time;
  }

  /**
   * Game cutaway: buildings and trees between the camera and `focus` (the courier) become see-through within
   * `radius` metres of the line of sight, so narrow streets never hide the courier. `null` turns it off.
   */
  setCutaway(focus: THREE.Vector3 | null, toCamera: THREE.Vector3, radius = 16): void {
    this.cut.uDtCut.value = focus ? radius : 0;
    if (focus) this.cut.uDtFocus.value.copy(focus);
    this.cut.uDtToCamera.value.copy(toCamera).normalize();
  }

  /** The building containing `p` (the largest when parts overlap), or null. */
  findFootprint(p: LatLng): Footprint | null {
    const l = this.projection.toLocal(p);
    let best: Footprint | null = null;
    for (const tile of this.tiles.values()) {
      for (const b of this.containing(tile, l.x, l.z)) {
        const fp = footprintOf(tile.footprints!, tile.index!, b, l);
        if (!best || fp.area > best.area) best = fp;
      }
    }
    return best;
  }

  /** Hides the generic building(s) where a store or house treatment replaces them (all tiles, all parts). */
  hideFootprint(fp: Footprint): void {
    this.hidden.push(fp);
    for (const tile of this.tiles.values()) this.applyHidden(tile, fp);
  }

  dispose(): void {
    this.disposed = true;
    this.abort.abort();
    for (const tile of this.tiles.values()) disposeGroup(tile.group);
    this.tiles.clear();
    this.pending.clear();
    for (const m of new Set(Object.values(this.materials))) m.dispose();
    this.treeGeo.dispose();
    if (this.ownsBuilder) this.builder.dispose?.();
  }

  // ---------------------------------------------------------------------------

  private load(t: TileXY): Promise<number> {
    const k = key(t);
    if (this.tiles.has(k) || this.loading.has(k)) return Promise.resolve(0);
    this.loading.add(k);
    let bytes = 0;
    const p = (async () => {
      try {
        const data = await this.source.get(t.z, t.x, t.y, this.abort.signal);
        bytes = data?.byteLength ?? 0;
        if (this.disposed) return;
        const tile: LoadedTile = { xy: t, group: new THREE.Group(), data, footprints: null, buildings: null, index: null };
        tile.group.name = `tile:${k}`;
        tile.group.userData.tile = k;
        this.tiles.set(k, tile);
        this.root.add(tile.group);
        await this.build(tile);
      } catch (err) {
        if (!this.disposed && !(err instanceof DOMException && err.name === 'AbortError')) {
          console.warn(`tile ${k}:`, err);
        }
        // Forget the tile so a later loadMore can try again.
        const failed = this.tiles.get(k);
        if (failed) {
          this.tiles.delete(k);
          disposeGroup(failed.group);
          this.root.remove(failed.group);
        }
        if (!this.disposed) {
          this.pending.set(k, t);
          this.failedAt.set(k, Date.now());
        }
      } finally {
        this.loading.delete(k);
      }
    })();
    this.inflight.add(p);
    void p.finally(() => this.inflight.delete(p));
    return p.then(() => bytes);
  }

  private async build(tile: LoadedTile): Promise<void> {
    if (!tile.data) return;
    const theme = this.theme;
    const layers = await this.builder.build(
      { tile: tile.xy, data: tile.data, origin: this.projection.origin, theme },
      this.abort.signal,
    );
    if (this.disposed || theme !== this.theme || this.tiles.get(key(tile.xy)) !== tile) return;
    disposeGroup(tile.group);
    tile.group.clear();
    this.addMeshes(tile, layers);
    for (const fp of this.hidden) this.applyHidden(tile, fp);
  }

  private addMeshes(tile: LoadedTile, l: BuildLayers): void {
    const add = (layer: TileLayer, obj: THREE.Mesh | null) => {
      if (!obj) return;
      obj.userData.layer = layer;
      obj.matrixAutoUpdate = false;
      obj.updateMatrix();
      tile.group.add(obj);
      return obj;
    };
    add('landuse', mesh(l.landuse, this.materials.landuse, false));
    add('parks', mesh(l.parks, this.materials.parks, false));
    add('water', mesh(l.water, this.materials.water, false));
    add('roads', mesh(l.roads, this.materials.roads, false));
    tile.buildings = add('buildings', mesh(l.buildings, this.materials.buildings, true)) ?? null;
    tile.footprints = l.footprints;
    tile.index = null;
    const n = l.parks.trees.length / 3;
    if (n > 0) {
      const trees = new THREE.InstancedMesh(this.treeGeo, this.materials.trees, n);
      const m = new THREE.Matrix4();
      const q = new THREE.Quaternion();
      const e = new THREE.Euler();
      const v = new THREE.Vector3();
      const sc = new THREE.Vector3();
      const dark = new THREE.Color(COLORS.leafDark);
      const leaf = new THREE.Color(COLORS.leaf);
      const tint = new THREE.Color(dark.r / leaf.r, dark.g / leaf.g, dark.b / leaf.b);
      const white = new THREE.Color(1, 1, 1);
      for (let i = 0; i < n; i++) {
        const x = l.parks.trees[i * 3]!;
        const z = l.parks.trees[i * 3 + 1]!;
        const s = l.parks.trees[i * 3 + 2]!;
        const r = fract(Math.sin(x * 12.9898 + z * 78.233) * 43758.5453); // stable per position
        q.setFromEuler(e.set(0, r * Math.PI * 2, 0));
        m.compose(v.set(x, GROUND_Y, z), q, sc.setScalar(s));
        trees.setMatrixAt(i, m);
        trees.setColorAt(i, r < 0.5 ? white : tint);
      }
      trees.instanceMatrix.needsUpdate = true;
      trees.computeBoundingSphere();
      trees.castShadow = true;
      trees.receiveShadow = false;
      add('trees', trees);
    }
  }

  /** Indices of the building polygons of `tile` that contain (x, z). */
  private containing(tile: LoadedTile, x: number, z: number): number[] {
    const fp = tile.footprints;
    if (!fp || fp.info.length === 0) return [];
    const idx = this.indexOf(tile)!;
    const out: number[] = [];
    const bb = idx.bbox;
    const nb = fp.info.length / 4;
    for (let b = 0; b < nb; b++) {
      if (x < bb[b * 4]! || z < bb[b * 4 + 1]! || x > bb[b * 4 + 2]! || z > bb[b * 4 + 3]!) continue;
      if (polygonContains(fp, idx, b, x, z)) out.push(b);
    }
    return out;
  }

  private indexOf(tile: LoadedTile): FootprintIndex | null {
    if (!tile.footprints) return null;
    tile.index ??= indexFootprints(tile.footprints);
    return tile.index;
  }

  private applyHidden(tile: LoadedTile, fp: Footprint): void {
    const f = tile.footprints;
    const m = tile.buildings;
    if (!f || !m) return;
    const hide = new Set(this.containing(tile, fp.at.x, fp.at.z));
    const idx = this.indexOf(tile);
    if (!idx) return;
    // Smaller parts whose centre lies inside the replaced building go too.
    let minX = Infinity;
    let minZ = Infinity;
    let maxX = -Infinity;
    let maxZ = -Infinity;
    for (const p of fp.ring) {
      minX = Math.min(minX, p.x);
      minZ = Math.min(minZ, p.z);
      maxX = Math.max(maxX, p.x);
      maxZ = Math.max(maxZ, p.z);
    }
    const nb = f.info.length / 4;
    const bb = idx.bbox;
    for (let b = 0; b < nb; b++) {
      if (hide.has(b)) continue;
      if (bb[b * 4]! > maxX || bb[b * 4 + 1]! > maxZ || bb[b * 4 + 2]! < minX || bb[b * 4 + 3]! < minZ) continue;
      const ring = exteriorOf(f, idx, b);
      const c = footprintCentroid(ring);
      if (c.area > 0 && c.area < fp.area && pointInRing(fp.ring, c.x, c.z)) hide.add(b);
    }
    if (hide.size === 0) return;
    const pos = m.geometry.getAttribute('position') as THREE.BufferAttribute;
    for (const b of hide) {
      const first = f.info[b * 4 + 2]!;
      const count = f.info[b * 4 + 3]!;
      for (let v = first + 1; v < first + count; v++) pos.setXYZ(v, pos.getX(first), pos.getY(first), pos.getZ(first));
    }
    pos.needsUpdate = true;
  }
}

// -----------------------------------------------------------------------------
// Store and home
// -----------------------------------------------------------------------------

export interface Site {
  kind: 'footprint' | 'standalone';
  object: THREE.Object3D;
  /** Where the pin stands (scene metres) and the top of the model under it. */
  center: Vec2;
  top: number;
  /** The courier's stop on the street (for the curb ring and the facade the model faces). */
  stop: Vec2;
}

export interface Sites {
  store: Site;
  home: Site & { house: HouseModel };
}

const STANDALONE_BASE_H = 0.6;

/**
 * Store and home: the footprint containing the point gets the prototype's store/house treatment (and the generic
 * building is hidden); without one, the prototype's standalone model stands on a small base at the point.
 */
export function placeSites(
  city: TileCity,
  s: TrackingSnapshot,
  group: THREE.Group,
  accent: THREE.MeshStandardMaterial = mat(COLORS.blue).clone(),
): Sites {
  const projection = city.projection;
  const local = (p: LatLng) => projection.toLocal(p);
  const pickup = local(s.pickup.location);
  const dropoff = local(s.dropoff.location);
  const toPickup = s.route.to_pickup;
  const toDropoff = s.route.to_dropoff;
  const storeStop = toPickup?.length
    ? local(toLatLng(toPickup[toPickup.length - 1]!))
    : toDropoff.length
      ? local(toLatLng(toDropoff[0]!))
      : pickup;
  const homeStop = toDropoff.length ? local(toLatLng(toDropoff[toDropoff.length - 1]!)) : dropoff;

  let store: Site;
  const sfp = city.findFootprint(s.pickup.location);
  if (sfp) {
    city.hideFootprint(sfp);
    const obj = makeStoreOnFootprint(sfp, s.pickup.name, storeStop, accent);
    store = { kind: 'footprint', object: obj, center: sfp.center, top: storeHeightOn(sfp) + 12, stop: storeStop };
  } else {
    const obj = standalone(makeStore(s.pickup.name, accent), pickup, storeStop, 52, 40);
    store = { kind: 'standalone', object: obj, center: pickup, top: STORE_HEIGHT + GROUND_Y + STANDALONE_BASE_H, stop: storeStop };
  }
  group.add(store.object);

  let home: Sites['home'];
  const hfp = city.findFootprint(s.dropoff.location);
  const sameBuilding = hfp && sfp && hfp.center.x === sfp.center.x && hfp.center.z === sfp.center.z;
  if (hfp && !sameBuilding) {
    city.hideFootprint(hfp);
    const h = makeHouseOnFootprint(hfp, homeStop);
    home = { kind: 'footprint', object: h.group, center: hfp.center, top: h.top, stop: homeStop, house: h };
  } else {
    const h = makeHouse();
    h.group.position.z = -6; // the prototype's house sits back on its lot
    const obj = standalone(h.group, dropoff, homeStop, 40, 36);
    home = { kind: 'standalone', object: obj, center: dropoff, top: HOUSE_HEIGHT + GROUND_Y + STANDALONE_BASE_H, stop: homeStop, house: h };
  }
  group.add(home.object);
  return { store, home };
}

/** A prototype model on a small raised base at `at`, its front (+Z) turned towards `toward`. */
function standalone(model: THREE.Object3D, at: Vec2, toward: Vec2, w: number, d: number): THREE.Group {
  const g = new THREE.Group();
  const base = new THREE.Mesh(new THREE.BoxGeometry(w, STANDALONE_BASE_H + GROUND_Y, d), mat(COLORS.block, { roughness: 0.9 }));
  base.position.y = (STANDALONE_BASE_H + GROUND_Y) / 2;
  base.receiveShadow = true;
  base.castShadow = true;
  model.position.y += STANDALONE_BASE_H + GROUND_Y;
  g.add(base, model);
  g.position.set(at.x, 0, at.z);
  const dx = toward.x - at.x;
  const dz = toward.z - at.z;
  if (Math.hypot(dx, dz) > 1) g.rotation.y = Math.atan2(dx, dz);
  return g;
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

function legsOf(s: TrackingSnapshot): LatLng[][] {
  const legs: LatLng[][] = [];
  if (s.route.to_pickup?.length) legs.push(s.route.to_pickup.map(toLatLng));
  if (s.route.to_dropoff.length) legs.push(s.route.to_dropoff.map(toLatLng));
  return legs;
}

function mesh(
  b: { positions: Float32Array; normals: Float32Array; colors: Float32Array; indices: Uint32Array; uvs?: Float32Array },
  material: THREE.Material,
  shadows: boolean,
): THREE.Mesh | null {
  if (b.indices.length === 0) return null;
  const g = new THREE.BufferGeometry();
  g.setAttribute('position', new THREE.BufferAttribute(b.positions, 3));
  g.setAttribute('normal', new THREE.BufferAttribute(b.normals, 3));
  g.setAttribute('color', new THREE.BufferAttribute(b.colors, 3));
  if (b.uvs) g.setAttribute('uv', new THREE.BufferAttribute(b.uvs, 2));
  g.setIndex(new THREE.BufferAttribute(b.indices, 1));
  g.computeBoundingSphere();
  const m = new THREE.Mesh(g, material);
  m.receiveShadow = true;
  m.castShadow = shadows;
  return m;
}

function disposeGroup(g: THREE.Group): void {
  g.traverse((o) => {
    // Tree instances share one geometry (owned by the city); tile meshes own theirs.
    if (o instanceof THREE.Mesh && !(o instanceof THREE.InstancedMesh)) o.geometry.dispose();
    if (o instanceof THREE.InstancedMesh) o.dispose();
  });
}

function fract(v: number): number {
  return v - Math.floor(v);
}

interface FootprintIndex {
  /** Per building `[minX, minZ, maxX, maxZ]` of its exterior ring. */
  bbox: Float32Array;
  /** Per building: index (in rings of 4) of its first ring, and how many rings it has. */
  firstRing: Uint32Array;
  ringCount: Uint32Array;
}

function indexFootprints(f: FootprintBuffers): FootprintIndex {
  const nb = f.info.length / 4;
  const bb = new Float32Array(nb * 4);
  const firstRing = new Uint32Array(nb);
  const ringCount = new Uint32Array(nb);
  for (let b = 0; b < nb; b++) bb.set([Infinity, Infinity, -Infinity, -Infinity], b * 4);
  for (let r = 0; r < f.rings.length / 4; r++) {
    const b = f.rings[r * 4 + 2]!;
    if (ringCount[b] === 0) firstRing[b] = r;
    ringCount[b]!++;
    if (f.rings[r * 4 + 3]) continue;
    const first = f.rings[r * 4]!;
    const n = f.rings[r * 4 + 1]!;
    for (let i = first; i < first + n; i++) {
      const x = f.coords[i * 2]!;
      const z = f.coords[i * 2 + 1]!;
      bb[b * 4] = Math.min(bb[b * 4]!, x);
      bb[b * 4 + 1] = Math.min(bb[b * 4 + 1]!, z);
      bb[b * 4 + 2] = Math.max(bb[b * 4 + 2]!, x);
      bb[b * 4 + 3] = Math.max(bb[b * 4 + 3]!, z);
    }
  }
  return { bbox: bb, firstRing, ringCount };
}

function ringsOf(f: FootprintBuffers, idx: FootprintIndex, b: number): { first: number; n: number; hole: boolean }[] {
  const out: { first: number; n: number; hole: boolean }[] = [];
  const r0 = idx.firstRing[b]!;
  for (let r = r0; r < r0 + idx.ringCount[b]!; r++) {
    out.push({ first: f.rings[r * 4]!, n: f.rings[r * 4 + 1]!, hole: f.rings[r * 4 + 3] === 1 });
  }
  return out;
}

function ringPoints(f: FootprintBuffers, first: number, n: number): Vec2[] {
  const pts: Vec2[] = [];
  for (let i = first; i < first + n; i++) pts.push({ x: f.coords[i * 2]!, z: f.coords[i * 2 + 1]! });
  return pts;
}

function exteriorOf(f: FootprintBuffers, idx: FootprintIndex, b: number): Vec2[] {
  const r = ringsOf(f, idx, b).find((x) => !x.hole);
  return r ? ringPoints(f, r.first, r.n) : [];
}

function pointInRing(ring: Vec2[], x: number, z: number): boolean {
  let c = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const a = ring[i]!;
    const b = ring[j]!;
    if (a.z > z !== b.z > z && x < ((b.x - a.x) * (z - a.z)) / (b.z - a.z) + a.x) c = !c;
  }
  return c;
}

function polygonContains(f: FootprintBuffers, idx: FootprintIndex, b: number, x: number, z: number): boolean {
  let inside = false;
  for (const r of ringsOf(f, idx, b)) {
    const hit = pointInRing(ringPoints(f, r.first, r.n), x, z);
    if (!r.hole && hit) inside = true;
    if (r.hole && hit) return false;
  }
  return inside;
}

function footprintOf(f: FootprintBuffers, idx: FootprintIndex, b: number, at: Vec2): Footprint {
  const rings = ringsOf(f, idx, b);
  const outer = rings.find((r) => !r.hole)!;
  const ring = ringPoints(f, outer.first, outer.n);
  const c = footprintCentroid(ring);
  const center = pointInRing(ring, c.x, c.z) ? { x: c.x, z: c.z } : { ...at };
  return {
    ring,
    holes: rings.filter((r) => r.hole).map((r) => ringPoints(f, r.first, r.n)),
    height: f.info[b * 4]!,
    minHeight: f.info[b * 4 + 1]!,
    center,
    area: c.area,
    at,
  };
}

interface Cutaway {
  uDtFocus: { value: THREE.Vector3 };
  uDtToCamera: { value: THREE.Vector3 };
  uDtCut: { value: number };
}

/** Screen-door (dithered) see-through around the line from the focus to the camera; no sorting, no extra pass. */
function withCutaway(
  m: THREE.MeshStandardMaterial,
  cut: Cutaway,
  name: string,
  sway?: { value: number },
): THREE.MeshStandardMaterial {
  m.onBeforeCompile = (shader) => {
    Object.assign(shader.uniforms, cut);
    if (sway) shader.uniforms.uDtSway = sway;
    // Trees sway in the wind: the crown moves, the trunk's foot stays put; each tree on its own phase.
    const swayGlsl = sway
      ? `{
          float dtPh = 0.0;
          #ifdef USE_INSTANCING
            dtPh = instanceMatrix[3].x * 0.071 + instanceMatrix[3].z * 0.053;
          #endif
          float dtBend = max(0.0, transformed.y - 2.5) / 7.0;
          dtBend *= dtBend;
          transformed.x += sin(uDtSway * 1.7 + dtPh) * 0.32 * dtBend;
          transformed.z += sin(uDtSway * 1.3 + dtPh * 1.7) * 0.2 * dtBend;
        }`
      : '';
    shader.vertexShader = shader.vertexShader
      .replace('#include <common>', `#include <common>\nvarying vec3 vDtWorld;${sway ? '\nuniform float uDtSway;' : ''}`)
      .replace(
        '#include <begin_vertex>',
        `#include <begin_vertex>
        ${swayGlsl}
        {
          vec4 dtW = vec4(transformed, 1.0);
          #ifdef USE_INSTANCING
            dtW = instanceMatrix * dtW;
          #endif
          vDtWorld = (modelMatrix * dtW).xyz;
        }`,
      );
    shader.fragmentShader = shader.fragmentShader
      .replace(
        '#include <common>',
        '#include <common>\nvarying vec3 vDtWorld;\nuniform vec3 uDtFocus;\nuniform vec3 uDtToCamera;\nuniform float uDtCut;',
      )
      .replace(
        '#include <clipping_planes_fragment>',
        `#include <clipping_planes_fragment>
        if (uDtCut > 0.0) {
          vec3 v = vDtWorld - uDtFocus;
          float along = dot(v, uDtToCamera);
          float perp = length(v - uDtToCamera * along);
          float k = smoothstep(uDtCut, uDtCut * 0.55, perp) * smoothstep(2.0, 8.0, along);
          float n = fract(52.9829189 * fract(dot(gl_FragCoord.xy, vec2(0.06711056, 0.00583715))));
          if (n < k * 0.82) discard;
        }`,
      );
  };
  m.customProgramCacheKey = () => `dt-cutaway-${name}`;
  return m;
}

/** The water: vertex colored, slightly glossy, with a gentle moving shimmer (shader tweak, no extra pass). */
function waterMaterial(time: { value: number }): THREE.MeshStandardMaterial {
  const m = new THREE.MeshStandardMaterial({ vertexColors: true, roughness: 0.4, metalness: 0.05 });
  m.onBeforeCompile = (shader) => {
    shader.uniforms.uDtTime = time;
    shader.vertexShader = shader.vertexShader
      .replace('#include <common>', '#include <common>\nvarying vec2 vDtWater;')
      .replace('#include <begin_vertex>', '#include <begin_vertex>\nvDtWater = (modelMatrix * vec4(transformed, 1.0)).xz;');
    shader.fragmentShader = shader.fragmentShader
      .replace('#include <common>', '#include <common>\nvarying vec2 vDtWater;\nuniform float uDtTime;')
      .replace(
        '#include <color_fragment>',
        `#include <color_fragment>
        {
          // Only the water itself shimmers (it is much bluer than the quay edge).
          float isWater = step(1.8, diffuseColor.b / max(diffuseColor.r, 0.01));
          float s = sin(vDtWater.x * 0.09 + uDtTime * 0.8) * sin(vDtWater.y * 0.075 - uDtTime * 0.6);
          float glint = smoothstep(0.82, 1.0, s);
          diffuseColor.rgb += isWater * (0.025 * s + 0.08 * glint);
        }`,
      );
  };
  m.customProgramCacheKey = () => 'dt-water';
  return m;
}
