import * as THREE from 'three';
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js';
import type { TrackingSnapshot } from '@dt/contract';
import { GeoProjection, type Vec2 } from '../engine/geo';
import { BLOCK, GRID, ROAD_W, placeNear, projection as gridProjection, type Place } from './grid';
import { activeLeg, activeLegKind, isFinal, toLatLng } from '../snapshot';
import { GROUND_Y, ROAD_TOP_Y } from '../tiles/build/common';
import type { TileTheme } from '../tiles/build/decode';
import { openTileSource, type TileSource, type TileSourceSpec } from '../tiles/source';
import { createWorkerBuilder, type TileBuilder } from '../tiles/workerPool';
import './scene.css';
import { TileCity, placeSites } from './city';
import { CITY_DAY, CITY_NIGHT } from './colors';
import { CAM_OFFSET, FOLLOW_ZOOM, MAX_ZOOM, MIN_ZOOM, computeFrame, followZoom, setFrustum } from './framing';
import {
  Bicycle,
  COLORS,
  HOUSE_HEIGHT,
  STORE_HEIGHT,
  makeBuilding,
  makeHouse,
  makePin,
  makeStore,
  makeTree,
  mat,
  mulberry32,
  type HouseModel,
} from './models';
import { buildRibbon, trimRouteFrom } from './ribbon';

/** The dropoff is private to the customer's own view; the label is never the address from the snapshot. */
const HOME_LABEL = 'Your address';
const ROUTE_ACTIVE = 0x2f6bff;

const BIKE_SCALE = 6; // visual scale of the courier (toy city)
const GRID_ROAD_TOP = 0.4;
const GRID_BLOCK_TOP = 0.8;
/** Tile city: the courier rides on the road surface, ribbons float above every flat layer. */
const TILE_COURIER_Y = GROUND_Y + 0.35;
const LAZY_EVERY_S = 0.5;
const LAZY_MAX_RADIUS_M = 1200;
/** Objects drawn again as a see-through silhouette where buildings hide them (the courier, the active route). */
const XRAY_LAYER = 1;

function angleDiff(target: number, current: number) {
  return ((((target - current + Math.PI) % (Math.PI * 2)) + Math.PI * 2) % (Math.PI * 2)) - Math.PI;
}

/** Store or home as the scene sees it, in either city. */
interface SiteInfo {
  /** Where the pin and the label stand. */
  center: Vec2;
  /** Top of the model under the pin. */
  top: number;
  /** The courier's stop on the street (curb ring). */
  stop: Vec2;
}

export interface CitySceneOptions {
  accent?: string;
  /** Absent = the procedural grid city (dev only). */
  tiles?: TileSourceSpec;
  theme?: TileTheme;
  /** Injected tile builder (tests); default: the inline worker pool. */
  builder?: TileBuilder;
  onTiles?: (s: { bytes: number; ms: number; tiles: number }) => void;
}

export class CityScene {
  private readonly renderer: THREE.WebGLRenderer;
  private readonly scene = new THREE.Scene();
  private readonly camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 1, 4000);
  private readonly controls: OrbitControls;
  private readonly resizeObserver: ResizeObserver;
  private readonly sun: THREE.DirectionalLight;
  private readonly hemi: THREE.HemisphereLight;
  private readonly ground: THREE.Mesh;

  private cityGroup = new THREE.Group();
  private readonly bicycle = new Bicycle();
  private readonly courierRoot = new THREE.Group();
  private readonly halo: THREE.Mesh;
  private readonly routeActive: THREE.Mesh;
  private readonly routeNext: THREE.Mesh;

  private readonly storeAccent = new THREE.MeshStandardMaterial({ color: COLORS.blue, roughness: 0.8, metalness: 0.02 });
  private storePinMat: THREE.MeshStandardMaterial | null = null;
  private accent: number | null = null;
  private storePin: THREE.Group | null = null;
  private homePin: THREE.Group | null = null;
  private homePinMat: THREE.MeshStandardMaterial | null = null;
  private rings: THREE.Mesh[] = [];
  private house: HouseModel | null = null;
  /** The store's rooftop sign, turned towards the camera every frame. */
  private billboard: THREE.Object3D | null = null;
  private store: SiteInfo | null = null;
  private home: SiteInfo | null = null;
  private deliveryKey = '';
  private routeKey = '';

  // real map
  private readonly tileSpec: TileSourceSpec | null;
  private tileSource: Promise<TileSource> | null = null;
  private builder: TileBuilder | null;
  private readonly ownsBuilder: boolean;
  private tileCity: TileCity | null = null;
  private generation = 0;
  private lazyClock = 0;
  private theme: TileTheme;
  private readonly onTiles?: CitySceneOptions['onTiles'];

  private projection: GeoProjection = gridProjection;
  private roadTop = GRID_ROAD_TOP;
  private ribbonY = GRID_ROAD_TOP + 0.15;

  private readonly labels: { store: HTMLElement; home: HTMLElement; courier: HTMLElement };

  private snap: TrackingSnapshot | null = null;
  private hasPos = false;
  private readonly curPos = new THREE.Vector3();
  private readonly targetPos = new THREE.Vector3();
  private curYaw = 0;
  private targetYaw = 0;
  private speedMs = 0;
  /** The first view frames store, home and courier; follow starts when asked for. */
  private follow = false;
  private goal: { target: THREE.Vector3; zoom: number } | null = null;
  private parcelDrop = 0; // seconds since delivery, -1 when idle
  private time = 0;
  private width = 1;
  private height = 1;
  private readonly tmp = new THREE.Vector3();
  private readonly xray = new THREE.MeshBasicMaterial({
    color: COLORS.blue,
    transparent: true,
    opacity: 0.45,
    depthWrite: false,
    depthFunc: THREE.GreaterDepth,
  });

  constructor(private readonly container: HTMLElement, opts: CitySceneOptions = {}) {
    if (opts.accent) this.accent = new THREE.Color(opts.accent).getHex();
    this.tileSpec = opts.tiles ?? null;
    this.theme = opts.theme ?? 'light';
    this.ownsBuilder = !opts.builder;
    this.builder = opts.builder ?? null;
    this.onTiles = opts.onTiles;
    if (this.tileSpec) {
      this.tileSource = openTileSource(this.tileSpec);
      this.tileSource.catch((err) => console.warn('tiles:', err));
      this.roadTop = TILE_COURIER_Y;
      this.ribbonY = ROAD_TOP_Y + 0.15;
    }

    this.renderer = new THREE.WebGLRenderer({ antialias: true, alpha: false });
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    this.renderer.shadowMap.enabled = true;
    this.renderer.shadowMap.type = THREE.PCFSoftShadowMap;
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;
    this.renderer.domElement.className = 'scene-canvas';
    container.appendChild(this.renderer.domElement);

    this.scene.background = new THREE.Color(COLORS.ground);

    // lights
    this.hemi = new THREE.HemisphereLight(0xffffff, 0xb9c6e6, 1.5);
    this.scene.add(this.hemi);
    const sun = new THREE.DirectionalLight(0xffffff, 2.4);
    this.sun = sun;
    sun.position.set(-260, 420, 180);
    sun.castShadow = true;
    sun.shadow.mapSize.set(2048, 2048);
    const sc = sun.shadow.camera;
    sc.left = -520;
    sc.right = 520;
    sc.top = 520;
    sc.bottom = -520;
    sc.near = 10;
    sc.far = 1400;
    sun.shadow.bias = -0.0006;
    sun.shadow.normalBias = 0.6;
    this.scene.add(sun, sun.target);

    // the ground the city stands on (the tile city's blocks are raised slabs on it)
    this.ground = new THREE.Mesh(new THREE.PlaneGeometry(60000, 60000), mat(COLORS.ground, { roughness: 1 }));
    this.ground.rotation.x = -Math.PI / 2;
    this.ground.receiveShadow = true;
    if (this.tileSpec) this.scene.add(this.ground);

    // camera + controls
    this.camera.position.copy(CAM_OFFSET);
    this.camera.zoom = FOLLOW_ZOOM;
    this.controls = new OrbitControls(this.camera, this.renderer.domElement);
    this.controls.enableDamping = true;
    this.controls.dampingFactor = 0.08;
    this.controls.minPolarAngle = 0.35;
    this.controls.maxPolarAngle = 1.25;
    this.controls.minZoom = MIN_ZOOM;
    this.controls.maxZoom = MAX_ZOOM;
    this.controls.rotateSpeed = 0.6;
    this.controls.enablePan = true;
    this.controls.addEventListener('start', () => (this.goal = null));

    // courier
    this.courierRoot.visible = false; // until a snapshot says there is a courier
    this.courierRoot.scale.setScalar(BIKE_SCALE);
    this.courierRoot.add(this.bicycle.group);
    this.halo = new THREE.Mesh(
      new THREE.RingGeometry(1.5, 1.95, 40),
      new THREE.MeshBasicMaterial({ color: COLORS.blue, transparent: true, opacity: 0.55, side: THREE.DoubleSide }),
    );
    this.halo.rotation.x = -Math.PI / 2;
    this.halo.position.y = 0.1;
    this.courierRoot.add(this.halo);
    this.scene.add(this.courierRoot);

    // route ribbons
    const ribbonMat = (color: number, opacity: number) =>
      new THREE.MeshBasicMaterial({
        color,
        transparent: true,
        opacity,
        side: THREE.DoubleSide,
        polygonOffset: true,
        polygonOffsetFactor: -2,
        polygonOffsetUnits: -2,
      });
    this.routeNext = new THREE.Mesh(new THREE.BufferGeometry(), ribbonMat(COLORS.blueLight, 0.75));
    this.routeActive = new THREE.Mesh(new THREE.BufferGeometry(), ribbonMat(this.accent ?? ROUTE_ACTIVE, 0.95));
    this.routeNext.renderOrder = 1;
    this.routeActive.renderOrder = 2;
    this.scene.add(this.routeNext, this.routeActive);
    // Game trick: where a building hides the courier or its route, a soft silhouette shows through.
    this.routeActive.layers.enable(XRAY_LAYER);
    this.courierRoot.traverse((o) => o.layers.enable(XRAY_LAYER));
    this.renderer.info.autoReset = false;

    // HTML labels
    const mkLabel = (cls: string) => {
      const el = document.createElement('div');
      el.className = `scene-label ${cls}`;
      container.appendChild(el);
      return el;
    };
    this.labels = { store: mkLabel('store'), home: mkLabel('home'), courier: mkLabel('courier') };
    this.labels.home.textContent = HOME_LABEL;
    // the city is built from the first snapshot: the places come from it
    this.setLabelsVisible(false);

    this.resizeObserver = new ResizeObserver(() => this.resize());
    this.resizeObserver.observe(container);
    this.resize();
  }

  // ---------------------------------------------------------------------------
  // public API
  // ---------------------------------------------------------------------------

  /** `null` goes back to the default blue. */
  setAccent(color: string | null): void {
    this.accent = color ? new THREE.Color(color).getHex() : null;
    this.applyAccent();
  }

  setFollow(on: boolean): void {
    this.follow = on;
    if (on) this.goal = { target: this.curPos.clone(), zoom: followZoom(this.camera, this.width) };
  }

  get isFollowing(): boolean {
    return this.follow;
  }

  /** Frames store, home and courier. */
  overview(): void {
    this.follow = false;
    this.frameAll(false);
  }

  setTheme(t: TileTheme): void {
    if (t === this.theme) return;
    this.theme = t;
    const c = t === 'dark' ? CITY_NIGHT : CITY_DAY;
    (this.scene.background as THREE.Color).setHex(c.ground);
    (this.ground.material as THREE.MeshStandardMaterial).color.setHex(c.ground);
    this.tileCity?.setTheme(t);
  }

  onSnapshot(s: TrackingSnapshot): void {
    const prev = this.snap;
    this.snap = s;

    const dk = `${s.delivery_id}|${s.pickup.location.lat},${s.pickup.location.lng}|${s.dropoff.location.lat},${s.dropoff.location.lng}`;
    const newDelivery = this.tileSpec ? dk !== this.deliveryKey : this.gridPlacesChanged(s);
    if (newDelivery) {
      if (this.tileSpec) {
        this.deliveryKey = dk;
        this.startTileDelivery(s);
      }
      this.hasPos = false; // snap courier on this snapshot
    } else if (this.tileCity) {
      const rk = routeKey(s);
      if (rk !== this.routeKey) {
        this.routeKey = rk;
        this.tileCity.addRoute(s);
      }
    }
    this.labels.store.textContent = s.pickup.name;

    // The server decides who is visible: no courier (or no position yet) means nothing is drawn.
    const c = s.courier;
    const pos = !isFinal(s.status) && c?.position ? this.projection.toLocal(c.position) : null;
    this.courierRoot.visible = pos !== null;
    this.labels.courier.style.display = pos !== null ? '' : 'none';
    // No courier drawn: the camera's follow point rests where the order is (store before pickup, home after).
    const atHome = s.status === 'picked_up' || s.status === 'arriving' || s.status === 'delivered';
    const rest = atHome
      ? (this.home?.stop ?? this.projection.toLocal(s.dropoff.location))
      : (this.store?.stop ?? this.projection.toLocal(s.pickup.location));
    const at = pos ?? rest;
    this.targetPos.set(at.x, 0, at.z);
    if (c?.position && c.heading_deg !== null) this.targetYaw = Math.PI - (c.heading_deg * Math.PI) / 180;
    this.speedMs = (c?.speed_kmh ?? 0) / 3.6;

    if (!this.hasPos || this.curPos.distanceTo(this.targetPos) > 60) {
      this.curPos.copy(this.targetPos);
      this.curYaw = this.targetYaw;
      if (!this.hasPos) this.frameAll(true);
      this.hasPos = true;
      if (this.follow) this.snapCameraTo(this.curPos);
    }

    // parcel on the bike / at the door
    this.bicycle.parcelBox.visible = s.status !== 'delivered';
    if (s.status === 'delivered' && prev?.status !== 'delivered') this.parcelDrop = 0;
    if (s.status !== 'delivered') {
      this.parcelDrop = -1;
      if (this.house) this.house.parcel.visible = false;
    }
    this.applyHomePin();
    this.updateRoutes(s);
  }

  frame(dt: number): void {
    dt = Math.min(dt, 0.1);
    this.time += dt;

    // courier smoothing (also makes sparse real-world GPS fixes look fluid)
    const k = 1 - Math.exp(-14 * dt);
    this.curPos.lerp(this.targetPos, k);
    this.curYaw += angleDiff(this.targetYaw, this.curYaw) * (1 - Math.exp(-9 * dt));
    const bob = Math.sin(this.time * 14) * 0.02 * Math.min(1, this.speedMs / 3);
    this.courierRoot.position.set(this.curPos.x, this.roadTop + bob, this.curPos.z);
    this.courierRoot.rotation.y = this.curYaw;
    this.bicycle.animate(dt, this.speedMs, BIKE_SCALE);
    const pulse = 1 + Math.sin(this.time * 4) * 0.12;
    this.halo.scale.setScalar(pulse);
    (this.halo.material as THREE.MeshBasicMaterial).opacity = 0.4 + Math.sin(this.time * 4) * 0.15;

    // pins + rings
    const bobY = Math.sin(this.time * 2.4) * 1.2;
    if (this.storePin && this.store) this.storePin.position.y = this.store.top + 8 + bobY;
    if (this.homePin && this.home) this.homePin.position.y = this.home.top + 8 + bobY;
    this.rings.forEach((r, i) => {
      const t = (this.time * 0.9 + i * 0.5) % 1;
      r.scale.setScalar(1 + t * 0.9);
      (r.material as THREE.MeshBasicMaterial).opacity = 0.65 * (1 - t);
    });

    // parcel drop animation at the door
    if (this.house && this.parcelDrop >= 0) {
      this.parcelDrop += dt;
      const t = Math.min(this.parcelDrop / 0.7, 1);
      const y = t < 1 ? 10 * (1 - t) * (1 - t) + Math.abs(Math.sin(t * Math.PI * 2.5)) * (1 - t) * 1.6 : 0;
      this.house.parcel.visible = true;
      this.house.parcel.position.y = 0.8 + y;
    }

    // camera
    if (this.goal) {
      const kk = 1 - Math.exp(-5 * dt);
      const delta = this.tmp.copy(this.goal.target).sub(this.controls.target).multiplyScalar(kk);
      this.controls.target.add(delta);
      this.camera.position.add(delta);
      this.camera.zoom += (this.goal.zoom - this.camera.zoom) * kk;
      this.camera.updateProjectionMatrix();
      if (!this.follow && delta.length() < 0.05 && Math.abs(this.goal.zoom - this.camera.zoom) < 0.01) {
        this.goal = null;
      }
    }
    if (this.follow) {
      const delta = this.tmp.copy(this.curPos).sub(this.controls.target).multiplyScalar(1 - Math.exp(-6 * dt));
      this.controls.target.add(delta);
      this.camera.position.add(delta);
    }
    this.controls.update();

    if (this.billboard) {
      const d = this.tmp.copy(this.camera.position).sub(this.controls.target);
      this.billboard.rotation.y = Math.atan2(d.x, d.z);
    }

    // the sun's shadow box follows what the camera looks at
    const tgt = this.controls.target;
    this.sun.position.set(tgt.x - 260, 420, tgt.z + 180);
    this.sun.target.position.copy(tgt);

    if (this.tileCity) {
      this.tileCity.frame(this.time);
      const toCamera = this.tmp.copy(this.camera.position).sub(this.controls.target);
      this.tileCity.setCutaway(this.courierRoot.visible ? this.courierRoot.position : null, toCamera);
      this.lazyClock += dt;
      if (this.lazyClock >= LAZY_EVERY_S) {
        this.lazyClock = 0;
        const visible = Math.hypot(this.camera.right - this.camera.left, this.camera.top - this.camera.bottom) / this.camera.zoom;
        // Capped: a 12 km route framed from far away must not pull in its whole box at once.
        this.tileCity.loadMore(this.controls.target, Math.min(LAZY_MAX_RADIUS_M, visible / 2 + 300));
        if (this.courierRoot.visible) this.tileCity.loadMore(this.curPos, 600);
      }
    }

    this.updateLabels();
    this.render();
  }

  private render() {
    const r = this.renderer;
    r.info.reset();
    r.render(this.scene, this.camera);
    if (!this.tileSpec) return; // the grid city's streets are wide open
    // Second pass: only the x-ray layer, only where it is behind something already drawn.
    const bg = this.scene.background;
    this.scene.background = null;
    this.scene.overrideMaterial = this.xray;
    this.camera.layers.set(XRAY_LAYER);
    r.autoClear = false;
    r.shadowMap.autoUpdate = false;
    r.render(this.scene, this.camera);
    r.shadowMap.autoUpdate = true;
    r.autoClear = true;
    this.camera.layers.set(0);
    this.scene.overrideMaterial = null;
    this.scene.background = bg;
  }

  get drawCalls(): number {
    return this.renderer.info.render.calls;
  }

  dispose(): void {
    this.generation++;
    this.resizeObserver.disconnect();
    this.controls.dispose();
    this.disposeCity();
    if (this.ownsBuilder) this.builder?.dispose?.();
    this.scene.traverse((o) => {
      if (o instanceof THREE.Mesh) o.geometry.dispose();
    });
    this.xray.dispose();
    this.storeAccent.dispose();
    this.renderer.dispose();
    this.renderer.domElement.remove();
    Object.values(this.labels).forEach((l) => l.remove());
  }

  // ---------------------------------------------------------------------------
  // internals
  // ---------------------------------------------------------------------------

  private snapCameraTo(p: THREE.Vector3) {
    const delta = this.tmp.copy(p).sub(this.controls.target);
    this.controls.target.add(delta);
    this.camera.position.add(delta);
  }

  /** Frames store, home and the courier (when shown) with a margin; `snap` jumps there, else glides. */
  private frameAll(snap: boolean) {
    const s = this.snap;
    if (!s) return;
    const pts = [this.store?.center ?? this.projection.toLocal(s.pickup.location), this.home?.center ?? this.projection.toLocal(s.dropoff.location)];
    if (this.courierRoot.visible) pts.push({ x: this.targetPos.x, z: this.targetPos.z });
    const f = computeFrame(
      this.camera,
      pts.map((p) => new THREE.Vector3(p.x, 0, p.z)),
    );
    this.controls.minZoom = Math.min(MIN_ZOOM, f.zoom * 0.8);
    this.camera.far = f.far;
    if (snap) {
      const off = this.camera.position.clone().sub(this.controls.target).normalize().multiplyScalar(f.distance);
      this.controls.target.copy(f.target);
      this.camera.position.copy(f.target).add(off);
      this.camera.zoom = f.zoom;
      this.camera.updateProjectionMatrix();
      this.goal = null;
    } else {
      this.camera.updateProjectionMatrix();
      this.goal = { target: f.target, zoom: f.zoom };
    }
  }

  private resize() {
    const w = Math.max(1, this.container.clientWidth);
    const h = Math.max(1, this.container.clientHeight);
    this.width = w;
    this.height = h;
    this.renderer.setSize(w, h, false);
    setFrustum(this.camera, w, h);
    this.camera.updateProjectionMatrix();
  }

  private updateRoutes(s: TrackingSnapshot) {
    const local = (p: { lat: number; lng: number }) => this.projection.toLocal(p);
    const toDropoff = s.route.to_dropoff.map((p) => local(toLatLng(p)));
    const leg = activeLeg(s).map(local);
    const onPickupLeg = activeLegKind(s) === 'to_pickup';
    const here: Vec2 = { x: this.targetPos.x, z: this.targetPos.z };
    let active: Vec2[] = [];
    let next: Vec2[] = [];
    if (!isFinal(s.status)) {
      // The leg the courier is on (from the courier when shown), then the delivery leg when that one is next.
      active = this.courierRoot.visible ? trimRouteFrom(leg, here) : leg;
      next = onPickupLeg ? toDropoff : [];
    }
    this.routeActive.geometry.dispose();
    this.routeActive.geometry = buildRibbon(active, 4.6, this.ribbonY + 0.05);
    this.routeNext.geometry.dispose();
    this.routeNext.geometry = buildRibbon(next, 3.2, this.ribbonY);
  }

  private setLabelsVisible(on: boolean) {
    this.labels.store.style.visibility = on ? '' : 'hidden';
    this.labels.home.style.visibility = on ? '' : 'hidden';
  }

  private applyAccent() {
    (this.routeActive.material as THREE.MeshBasicMaterial).color.setHex(this.accent ?? ROUTE_ACTIVE);
    this.xray.color.setHex(this.accent ?? COLORS.blue);
    this.storeAccent.color.setHex(this.accent ?? COLORS.blue);
    this.storePinMat?.color.setHex(this.accent ?? COLORS.blue);
    this.storePinMat?.emissive.setHex(this.accent ?? COLORS.blue);
  }

  private applyHomePin() {
    if (!this.homePinMat || !this.snap) return;
    const c = this.snap.status === 'delivered' ? COLORS.green : COLORS.coral;
    this.homePinMat.color.setHex(c);
    this.homePinMat.emissive.setHex(c);
  }

  private updateLabels() {
    const place = (el: HTMLElement, world: THREE.Vector3) => {
      const v = this.tmp.copy(world).project(this.camera);
      const x = (v.x * 0.5 + 0.5) * this.width;
      const y = (-v.y * 0.5 + 0.5) * this.height;
      el.style.transform = `translate(${x.toFixed(1)}px, ${y.toFixed(1)}px) translate(-50%, -100%)`;
      el.style.opacity = Math.abs(v.x) < 1.1 && Math.abs(v.y) < 1.1 ? '1' : '0';
    };
    if (this.store) place(this.labels.store, new THREE.Vector3(this.store.center.x, this.store.top + 24, this.store.center.z));
    if (this.home) place(this.labels.home, new THREE.Vector3(this.home.center.x, this.home.top + 24, this.home.center.z));
    place(this.labels.courier, new THREE.Vector3(this.curPos.x, 16, this.curPos.z));
  }

  private disposeCity() {
    this.tileCity?.dispose();
    this.tileCity = null;
    this.scene.remove(this.cityGroup);
    this.cityGroup.traverse((o) => {
      if (o instanceof THREE.Mesh) {
        o.geometry.dispose();
        const ms = Array.isArray(o.material) ? o.material : [o.material];
        for (const m of ms) if (m instanceof THREE.MeshStandardMaterial && m.map && m.map.image) m.map.dispose();
      }
    });
    this.cityGroup = new THREE.Group();
    this.scene.add(this.cityGroup);
    this.rings = [];
    this.store = null;
    this.home = null;
    this.house = null;
    this.billboard = null;
    this.storePin = null;
    this.homePin = null;
    this.setLabelsVisible(false);
  }

  /** Pins above store and home, curb rings at their stops. */
  private addMarkers() {
    const store = this.store!;
    const home = this.home!;
    this.storePin = makePin(COLORS.blue);
    this.storePin.position.set(store.center.x, store.top + 8, store.center.z);
    this.homePin = makePin(COLORS.coral);
    this.homePin.position.set(home.center.x, home.top + 8, home.center.z);
    // the pins share cached materials; give them their own so accent and delivered can recolor them
    this.homePinMat = ((this.homePin.children[1] as THREE.Mesh).material as THREE.MeshStandardMaterial).clone();
    (this.homePin.children[0] as THREE.Mesh).material = this.homePinMat;
    (this.homePin.children[1] as THREE.Mesh).material = this.homePinMat;
    this.storePinMat = ((this.storePin.children[1] as THREE.Mesh).material as THREE.MeshStandardMaterial).clone();
    (this.storePin.children[0] as THREE.Mesh).material = this.storePinMat;
    (this.storePin.children[1] as THREE.Mesh).material = this.storePinMat;
    this.cityGroup.add(this.storePin, this.homePin);

    for (const [p, color] of [
      [store.stop, COLORS.blue],
      [home.stop, COLORS.coral],
    ] as [Vec2, number][]) {
      const ring = new THREE.Mesh(
        new THREE.RingGeometry(5, 6.4, 40),
        new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 0.6, side: THREE.DoubleSide, depthWrite: false }),
      );
      ring.rotation.x = -Math.PI / 2;
      ring.position.set(p.x, this.ribbonY + 0.1, p.z);
      this.cityGroup.add(ring);
      this.rings.push(ring);
    }
    this.setLabelsVisible(true);
    this.applyAccent();
    this.applyHomePin();
  }

  // ---------------------------------------------------------------------------
  // real-map city
  // ---------------------------------------------------------------------------

  private startTileDelivery(s: TrackingSnapshot) {
    const gen = ++this.generation;
    this.disposeCity();
    this.projection = new GeoProjection(s.pickup.location); // the pickup is the scene origin
    this.routeKey = routeKey(s);
    const group = this.cityGroup;
    const started = performance.now();
    void this.tileSource!.then(async (source) => {
      if (gen !== this.generation) return;
      this.builder ??= createWorkerBuilder();
      const city = new TileCity(group, source, this.projection, this.theme, { builder: this.builder });
      this.tileCity = city;
      const { bytes } = await city.loadFirst(s);
      if (gen !== this.generation) return;
      this.onTiles?.({ bytes, ms: performance.now() - started, tiles: city.loadedTiles });
      const sites = placeSites(city, this.snap ?? s, group, this.storeAccent);
      this.store = sites.store;
      this.home = sites.home;
      this.house = sites.home.house;
      this.billboard = (sites.store.object.userData.billboard as THREE.Object3D | undefined) ?? null;
      this.addMarkers();
      if (!this.follow) this.frameAll(false);
      if (this.snap) this.updateRoutes(this.snap);
    }).catch((err) => console.warn('tiles:', err));
  }

  // ---------------------------------------------------------------------------
  // procedural grid city (dev only)
  // ---------------------------------------------------------------------------

  private gridPlace: { store: Place; home: Place } | null = null;

  private gridPlacesChanged(s: TrackingSnapshot): boolean {
    const store = placeNear('store', s.pickup.name, s.pickup.location);
    const home = placeNear('home', HOME_LABEL, s.dropoff.location);
    const key = `${store.cell.i},${store.cell.j}|${home.cell.i},${home.cell.j}`;
    if (key === this.deliveryKey && this.gridPlace) {
      this.gridPlace.store.label = s.pickup.name;
      return false;
    }
    this.deliveryKey = key;
    this.gridPlace = { store, home };
    this.buildGridCity();
    return true;
  }

  private buildGridCity() {
    this.disposeCity();
    const city = this.cityGroup;
    const STORE = this.gridPlace!.store;
    const dest = this.gridPlace!.home;
    const reserved = new Set<string>([
      `${STORE.cell.i},${STORE.cell.j}`,
      `${dest.cell.i},${dest.cell.j}`,
    ]);
    const ROAD_TOP = GRID_ROAD_TOP;
    const BLOCK_TOP = GRID_BLOCK_TOP;

    // ground
    const ground = new THREE.Mesh(new THREE.PlaneGeometry(4000, 4000), mat(COLORS.ground, { roughness: 1 }));
    ground.rotation.x = -Math.PI / 2;
    ground.receiveShadow = true;
    city.add(ground);

    // roads
    const span = GRID * BLOCK * 2 + ROAD_W;
    const roadMat = mat(COLORS.road, { roughness: 0.95 });
    for (let i = -GRID; i <= GRID; i++) {
      const v = new THREE.Mesh(new THREE.BoxGeometry(ROAD_W, ROAD_TOP, span), roadMat);
      v.position.set(i * BLOCK, ROAD_TOP / 2, 0);
      v.receiveShadow = true;
      const h = new THREE.Mesh(new THREE.BoxGeometry(span, ROAD_TOP, ROAD_W), roadMat);
      h.position.set(0, ROAD_TOP / 2, i * BLOCK);
      h.receiveShadow = true;
      city.add(v, h);
    }

    // dashed lane markings (instanced, skipped inside intersections)
    const dashes: { p: Vec2; horizontal: boolean }[] = [];
    for (let i = -GRID; i <= GRID; i++) {
      for (let k = -GRID; k < GRID; k++) {
        const from = k * BLOCK + ROAD_W / 2 + 6;
        const to = (k + 1) * BLOCK - ROAD_W / 2 - 6;
        for (let t = from; t < to; t += 9) {
          dashes.push({ p: { x: t + 2.5, z: i * BLOCK }, horizontal: true });
          dashes.push({ p: { x: i * BLOCK, z: t + 2.5 }, horizontal: false });
        }
      }
    }
    const dashMesh = new THREE.InstancedMesh(new THREE.BoxGeometry(1, 0.05, 1), mat(COLORS.roadMark), dashes.length);
    const dummy = new THREE.Object3D();
    dashes.forEach(({ p, horizontal }, n) => {
      dummy.position.set(p.x, ROAD_TOP + 0.03, p.z);
      dummy.scale.set(horizontal ? 5 : 0.7, 1, horizontal ? 0.7 : 5);
      dummy.updateMatrix();
      dashMesh.setMatrixAt(n, dummy.matrix);
    });
    dashMesh.instanceMatrix.needsUpdate = true;
    city.add(dashMesh);

    // blocks
    const blockMat = mat(COLORS.block, { roughness: 0.9 });
    const inner = BLOCK - ROAD_W;
    for (let i = -GRID; i < GRID; i++) {
      for (let j = -GRID; j < GRID; j++) {
        const cx = i * BLOCK + BLOCK / 2;
        const cz = j * BLOCK + BLOCK / 2;
        const plate = new THREE.Mesh(new THREE.BoxGeometry(inner, BLOCK_TOP, inner), blockMat);
        plate.position.set(cx, BLOCK_TOP / 2, cz);
        plate.receiveShadow = true;
        city.add(plate);

        const rand = mulberry32((i + 17) * 9973 + (j + 31) * 7919);
        const isReserved = reserved.has(`${i},${j}`);
        const lot = inner / 4 + 0.5; // lot center offset
        if (!isReserved) {
          for (const sx of [-1, 1]) {
            for (const sz of [-1, 1]) {
              const px = cx + sx * lot + (rand() - 0.5) * 4;
              const pz = cz + sz * lot + (rand() - 0.5) * 4;
              if (rand() < 0.68) {
                const tall = rand() < 0.18 ? 10 : 0;
                const b = makeBuilding(rand, 20 + rand() * 12, 20 + rand() * 12, 7 + rand() * 9 + tall);
                b.position.set(px, BLOCK_TOP, pz);
                city.add(b);
              } else {
                for (let t = 0; t < 3; t++) {
                  const tree = makeTree(rand);
                  tree.position.set(px + (rand() - 0.5) * 22, BLOCK_TOP, pz + (rand() - 0.5) * 22);
                  city.add(tree);
                }
              }
            }
          }
        }
        // street trees along the horizontal edges (not in front of the store / house)
        const edges: [number, number][] = isReserved
          ? [
              [-1, -1],
              [1, -1],
              [-1, 1],
              [1, 1],
            ]
          : [
              [rand() * 2 - 1, -1],
              [rand() * 2 - 1, 1],
            ];
        for (const [ex, ez] of edges) {
          if (isReserved && Math.abs(ez) === 1) {
            // only keep trees at the back of the plot
            const place = i === STORE.cell.i && j === STORE.cell.j ? STORE : dest;
            if (ez === -place.side) continue;
          }
          const tree = makeTree(rand);
          tree.position.set(cx + ex * (inner / 2 - 4), BLOCK_TOP, cz + ez * (inner / 2 - 4));
          city.add(tree);
        }
      }
    }

    // store
    const store = makeStore(STORE.label, this.storeAccent);
    store.position.set(STORE.building.x, BLOCK_TOP, STORE.building.z);
    store.rotation.y = STORE.side === 1 ? Math.PI : 0;
    city.add(store);

    // house
    const house = makeHouse();
    this.house = house;
    house.group.position.set(dest.building.x, BLOCK_TOP, dest.building.z - dest.side * 6);
    house.group.rotation.y = dest.side === 1 ? Math.PI : 0;
    city.add(house.group);

    this.store = { center: STORE.building, top: STORE_HEIGHT, stop: STORE.stop };
    this.home = { center: dest.building, top: HOUSE_HEIGHT, stop: dest.stop };
    this.addMarkers();
    for (const r of this.rings) r.position.y = ROAD_TOP + 0.2;
  }
}

function routeKey(s: TrackingSnapshot): string {
  const end = (l: number[][] | null) => (l && l.length ? `${l.length}:${l[0]}:${l[l.length - 1]}` : '-');
  return `${end(s.route.to_pickup)}|${end(s.route.to_dropoff)}`;
}
