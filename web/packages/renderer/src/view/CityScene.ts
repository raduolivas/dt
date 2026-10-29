import * as THREE from 'three';
import type { TrackingSnapshot } from '@dt/contract';
import { GeoProjection, type Vec2 } from '../engine/geo';
import { BLOCK, GRID, ROAD_W, placeNear, projection as gridProjection, type Place } from './grid';
import { GROUND_Y, ROAD_TOP_Y } from '../tiles/build/common';
import type { TileTheme } from '../tiles/build/decode';
import { openTileSource, type TileSource, type TileSourceSpec } from '../tiles/source';
import { createWorkerBuilder, type TileBuilder } from '../tiles/workerPool';
import './scene.css';
import { TileCity, placeSites } from './city';
import { DAY } from './colors';
import { isSharedTexture, makeCourierGlow } from './night';
import { THEME_FADE_MS, applyTheme, createLights, nightMix, refreshTheme, type ThemeFade } from './theme';
import { MIN_ZOOM, computeFrame, followZoom, setFrustum, type Insets } from './framing';
import { CameraRig, type CameraMode } from './camera';
import type { Quality } from './quality';
import {
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
import { CourierMotion, SpeedMeter } from './motion';
import { Effects } from './effects';
import { COURIER_SCALE, makeCourierModel, type CourierModel } from './vehicles';
import { fillRibbon, ribbonWidth, routeLegs, trimLegFrom } from './ribbon';

/** The dropoff is private to the customer's own view; the label is never the address from the snapshot. */
const HOME_LABEL = 'Your address';
const ROUTE_ACTIVE = 0x2f6bff;

const GRID_ROAD_TOP = 0.4;
const GRID_BLOCK_TOP = 0.8;
/** Tile city: the courier rides on the road surface, ribbons float above every flat layer. */
const TILE_COURIER_Y = GROUND_Y + 0.35;
const LAZY_EVERY_S = 0.5;
const LAZY_MAX_RADIUS_M = 1200;
/** Route ribbons: the prototype's widths up close, a minimum on-screen width when zoomed out (overview). */
const ACTIVE_RIBBON_M = 4.6;
const ACTIVE_RIBBON_MIN_PX = 7;
const NEXT_RIBBON_M = 3.2;
const NEXT_RIBBON_MIN_PX = 5;
/** Objects drawn again as a see-through silhouette where buildings hide them (the courier, the active route). */
const XRAY_LAYER = 1;
/** The route's see-through copies (own colors, so the next leg stays light and the active one accent). */
const XRAY_ROUTE_LAYER = 2;

/** A stale or lost courier is drawn faded and still; the customer sees no alarm. */
const FADED_OPACITY = 0.4;
const XRAY_OPACITY = 0.45;
/** Statuses during which the store is preparing the order (steam over the store). */
const PREPARING: ReadonlySet<string> = new Set(['confirmed', 'courier_assigned', 'at_pickup']);
/** The active ribbon is rebuilt when the courier has moved this far (m) since it was last cut. */
const TRIM_STEP_M = 0.5;

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
  /** Milliseconds; the courier's glide and dead reckoning run on it. */
  clock?: () => number;
  /** No camera moves, bounces, sway, shimmer or pulses (the courier still moves). */
  reducedMotion?: boolean;
  /** The first view: the isometric 3D view (default) or straight down (2D). */
  mode?: CameraMode;
  /** Makes the WebGL renderer (tests pass a mock); default a `THREE.WebGLRenderer`. */
  webgl?: () => THREE.WebGLRenderer;
  /** The WebGL context was lost (`true`) or restored and rebuilt (`false`). */
  onContext?: (lost: boolean) => void;
}

export class CityScene {
  private readonly renderer: THREE.WebGLRenderer;
  private readonly scene = new THREE.Scene();
  /** Camera, orbit controls, framing moves, the intro and the 2D/3D view. */
  private readonly rig: CameraRig;
  private readonly camera: THREE.OrthographicCamera;
  private readonly controls: CameraRig['controls'];
  private readonly resizeObserver: ResizeObserver;
  private readonly sun: THREE.DirectionalLight;
  private readonly ground: THREE.Mesh;

  private cityGroup = new THREE.Group();
  private courier: CourierModel = makeCourierModel('bicycle');
  private readonly courierRoot = new THREE.Group();
  private readonly effects: Effects;
  private readonly speed = new SpeedMeter();
  private readonly halo: THREE.Mesh;
  /** The courier's night glow (halo on the street, headlight beam); hidden with the halo when the courier fades. */
  private readonly glowHolder = new THREE.Group();
  private readonly courierGlow = makeCourierGlow();
  /** Set at the end of the constructor (the first theme, at once). */
  private themeFade: ThemeFade | null = null;
  private readonly routeActive: THREE.Mesh;
  private readonly routeNext: THREE.Mesh;
  private readonly routeActiveXray: THREE.Mesh;
  private readonly xrayRibbonMats: THREE.Material[] = [];

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
  /** The speed the courier is drawn moving at (m/s): it drives wheels, pedals and steps. */
  private shownSpeed = 0;
  private readonly motion: CourierMotion;
  private readonly clock: () => number;
  private courierMats: THREE.Material[] = [];
  private faded = false;
  private trimAt: Vec2 | null = null;
  /** The WebGL context is gone (iOS Safari drops it in the background or low on memory): nothing is drawn. */
  private contextLost = false;
  private readonly onContextLost = (e: Event) => {
    e.preventDefault(); // we restore it: the browser may hand it back
    this.contextLost = true;
    this.onContext?.(true);
  };
  private readonly onContextRestored = () => {
    this.contextLost = false;
    this.rebuildGpu();
    this.onContext?.(false);
  };
  private readonly onContext?: (lost: boolean) => void;
  private time = 0;
  private width = 1;
  private height = 1;
  private readonly tmp = new THREE.Vector3();
  private legs: { active: Vec2[]; next: Vec2[] } = { active: [], next: [] };
  private ribbonWidthM = -1;
  private insets: Insets = { top: 0, right: 0, bottom: 0, left: 0 };
  private readonly xray = new THREE.MeshBasicMaterial({
    color: COLORS.blue,
    transparent: true,
    opacity: XRAY_OPACITY,
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
    this.onContext = opts.onContext;
    if (this.tileSpec) {
      this.tileSource = openTileSource(this.tileSpec);
      this.tileSource.catch((err) => console.warn('tiles:', err));
      this.roadTop = TILE_COURIER_Y;
      this.ribbonY = ROAD_TOP_Y + 0.15;
    }

    this.renderer = opts.webgl ? opts.webgl() : new THREE.WebGLRenderer({ antialias: true, alpha: false });
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    this.renderer.shadowMap.enabled = true;
    this.renderer.shadowMap.type = THREE.PCFSoftShadowMap;
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;
    this.renderer.domElement.className = 'scene-canvas';
    container.appendChild(this.renderer.domElement);
    // three.js re-creates its GL state on restore; the scene marks its buffers and textures for upload again
    this.renderer.domElement.addEventListener('webglcontextlost', this.onContextLost);
    this.renderer.domElement.addEventListener('webglcontextrestored', this.onContextRestored);

    this.scene.background = new THREE.Color(DAY.sky);

    // lights: the hemisphere and the sun (the moon at night); the only real-time lights, by day and by night
    const { hemi, sun } = createLights();
    this.sun = sun;
    this.scene.add(hemi, sun, sun.target);

    // the ground the city stands on (the tile city's blocks are raised slabs on it)
    this.ground = new THREE.Mesh(new THREE.PlaneGeometry(60000, 60000), mat(COLORS.ground, { roughness: 1 }));
    this.ground.rotation.x = -Math.PI / 2;
    this.ground.receiveShadow = true;
    if (this.tileSpec) this.scene.add(this.ground);

    // camera + controls (glides and the intro are off under reduced motion: the camera snaps)
    this.rig = new CameraRig(
      this.renderer.domElement,
      { glide: () => this.effects.isEnabled('reframeGlide'), intro: () => this.effects.isEnabled('cameraIntro') },
      opts.mode,
    );
    this.camera = this.rig.camera;
    this.controls = this.rig.controls;
    this.effects = new Effects(this.scene);
    const reduced =
      opts.reducedMotion ?? (typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches);
    this.effects.setReducedMotion(reduced);

    // courier
    this.courierRoot.visible = false; // until a snapshot says there is a courier
    this.courierRoot.scale.setScalar(COURIER_SCALE);
    this.halo = new THREE.Mesh(
      new THREE.RingGeometry(1.5, 1.95, 40),
      new THREE.MeshBasicMaterial({ color: COLORS.blue, transparent: true, opacity: 0.55, side: THREE.DoubleSide }),
    );
    this.halo.rotation.x = -Math.PI / 2;
    this.halo.position.y = 0.1;
    (this.halo.material as THREE.Material).userData.dtColor = false;
    this.courierRoot.add(this.halo);
    this.glowHolder.add(this.courierGlow);
    this.courierRoot.add(this.glowHolder);
    this.scene.add(this.courierRoot);
    this.halo.layers.enable(XRAY_LAYER);
    this.mountCourier(this.courier);
    this.motion = new CourierMotion((p) => this.projection.toLocal(p));
    this.clock = opts.clock ?? (() => performance.now());

    // route ribbons
    const ribbonMat = (color: number, opacity: number) => {
      const m = new THREE.MeshBasicMaterial({
        color,
        transparent: true,
        opacity,
        side: THREE.DoubleSide,
        polygonOffset: true,
        polygonOffsetFactor: -2,
        polygonOffsetUnits: -2,
      });
      m.userData.dtColor = false; // the route is the partner's, by day and by night
      return m;
    };
    // The next leg: the route blue, half transparent (blueLight alone vanished against the real canals).
    this.routeNext = new THREE.Mesh(new THREE.BufferGeometry(), ribbonMat(COLORS.blue, 0.5));
    this.routeActive = new THREE.Mesh(new THREE.BufferGeometry(), ribbonMat(this.accent ?? ROUTE_ACTIVE, 0.95));
    this.routeNext.renderOrder = 1;
    this.routeActive.renderOrder = 2;
    this.scene.add(this.routeNext, this.routeActive);
    // the ribbons' buffers are refilled in place (the bounding sphere would go stale)
    this.routeNext.frustumCulled = false;
    this.routeActive.frustumCulled = false;
    // The route reads in every view: where buildings hide it, a see-through copy of each ribbon shows through.
    const xrayRibbon = (src: THREE.Mesh, opacity: number) => {
      const m = (src.material as THREE.MeshBasicMaterial).clone(); // keeps `dtColor = false`
      m.opacity = opacity;
      m.depthWrite = false;
      m.depthFunc = THREE.GreaterDepth;
      const copy = new THREE.Mesh(src.geometry, m);
      copy.layers.set(XRAY_ROUTE_LAYER);
      copy.frustumCulled = false;
      copy.renderOrder = src.renderOrder;
      this.scene.add(copy);
      this.xrayRibbonMats.push(m);
      return copy;
    };
    xrayRibbon(this.routeNext, 0.35);
    this.routeActiveXray = xrayRibbon(this.routeActive, 0.7);
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

    this.storeAccent.userData.dtColor = false;
    // the first theme is there at once
    this.themeFade = applyTheme(this.scene, this.theme, { fadeMs: 0 });
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
    this.rig.setFollow(on, this.curPos, followZoom(this.camera, this.width));
  }

  get isFollowing(): boolean {
    return this.rig.following;
  }

  /** Frames store, home and courier. */
  overview(): void {
    this.rig.setFollow(false, this.curPos, 0);
    this.frameAll(false);
  }

  /** 3D (isometric orbit) or 2D (straight down, no rotation, zoom kept): a smooth tilt, or at once under reduced motion. */
  setMode(m: CameraMode): void {
    this.rig.setMode(m);
  }

  /** Pixel ratio cap and shadow map size (adaptive quality on slow phones). */
  setQuality(q: Quality): void {
    this.renderer.setPixelRatio(Math.min(typeof devicePixelRatio === 'number' ? devicePixelRatio : 1, q.maxPixelRatio));
    this.renderer.setSize(this.width, this.height, false);
    const shadow = this.sun.shadow;
    if (shadow.mapSize.x === q.shadowMapSize) return;
    shadow.mapSize.set(q.shadowMapSize, q.shadowMapSize);
    shadow.map?.dispose(); // made again at the new size on the next frame
    shadow.map = null;
  }

  /** No camera moves, bounces, sway, shimmer or pulses; reframing snaps. The courier still moves. */
  setReducedMotion(on: boolean): void {
    this.effects.setReducedMotion(on);
    if (on) this.rig.finishMove();
    if (on) this.themeFade?.finish(); // no day/night fade under reduced motion
  }

  get reducedMotion(): boolean {
    return this.effects.reducedMotion;
  }

  /** Screen space the page's cards cover (CSS px): framing keeps store, home and courier out from under them. */
  setInsets(i: Insets): void {
    this.insets = { ...i };
  }

  /** Day (`light`) or night (`dark`): fades, or switches at once under reduced motion. */
  setTheme(t: TileTheme): void {
    if (t === this.theme) return;
    this.theme = t;
    this.themeFade = applyTheme(this.scene, t, { fadeMs: this.effects.reducedMotion ? 0 : THEME_FADE_MS });
  }

  get currentTheme(): TileTheme {
    return this.theme;
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
      this.motion.reset();
    } else if (this.tileCity) {
      const rk = routeKey(s);
      if (rk !== this.routeKey) {
        this.routeKey = rk;
        this.tileCity.addRoute(s);
      }
    }
    this.labels.store.textContent = s.pickup.name;

    // The server decides who is visible: no courier (or no position yet) means nothing is drawn.
    const wasShown = this.courierRoot.visible;
    const v = s.courier?.vehicle;
    if (v && v !== this.courier.vehicle) this.mountCourier(makeCourierModel(v));
    this.motion.onSnapshot(s, this.clock());
    const pos = this.motion.fix;
    this.courierRoot.visible = pos !== null;
    this.labels.courier.style.display = pos !== null ? '' : 'none';
    if (pos === null) this.setFaded(false);
    // No courier drawn: the camera's follow point rests where the order is (store before pickup, home after).
    const atHome = s.status === 'picked_up' || s.status === 'arriving' || s.status === 'delivered';
    const rest = atHome
      ? (this.home?.stop ?? this.projection.toLocal(s.dropoff.location))
      : (this.store?.stop ?? this.projection.toLocal(s.pickup.location));
    const at = pos ?? rest;
    this.targetPos.set(at.x, 0, at.z);

    const changed = prev !== null && !newDelivery && prev.status !== s.status;
    if (!this.hasPos) {
      this.curPos.copy(this.targetPos);
      this.speed.reset();
      this.frameAll(true);
      this.hasPos = true;
      if (this.rig.following) this.rig.snapTo(this.curPos);
      if (!this.tileSpec) this.rig.startIntro(); // the grid city is there at once; the tile city starts it when loaded
    } else if (pos === null) {
      // the courier left the scene: the resting point (store or home) takes over
      this.curPos.copy(this.targetPos);
      this.speed.reset();
      if (changed && !this.rig.following) this.frameAll(false, s.status === 'delivered');
    } else if (!wasShown) {
      // the courier appears (preparing -> live): start at its fix and show it with the store and home
      this.curPos.copy(this.targetPos);
      this.speed.reset();
      if (!this.rig.following) this.frameAll(false);
    } else if (changed && !this.rig.following) {
      this.frameAll(false); // a new state: store, courier and home in view again
    }

    // moments: the courier arrives at the store, the parcel is at the door
    if (changed && s.status === 'at_pickup') {
      this.effects.bounce('store');
      if (pos !== null) this.effects.bounce('courier');
    }
    this.courier.parcel.visible = s.status !== 'delivered';
    if (s.status === 'delivered') {
      if (this.house) this.house.parcel.visible = true;
      if (changed) this.celebrate();
    } else if (this.house) {
      this.house.parcel.visible = false;
    }
    this.applyHomePin();
    this.updateRoutes(s);
  }

  frame(dt: number): void {
    dt = Math.min(dt, 0.1);
    this.time += dt;
    const fx = this.effects;
    fx.frame(dt);
    this.themeFade?.step(dt * 1000);

    // courier smoothing (also makes sparse real-world GPS fixes look fluid)
    const m = this.motion.frame(dt * 1000, this.clock());
    if (m.visible) {
      this.curPos.set(m.p.x, 0, m.p.z);
      this.curYaw = m.yaw;
      this.setFaded(m.faded);
    } else {
      this.curPos.lerp(this.targetPos, 1 - Math.exp(-14 * dt)); // the resting point, for the camera to follow
    }
    // Wheels, pedals and steps follow the courier as drawn: still when dead reckoning has stopped or the glide settled.
    this.shownSpeed = m.visible && !m.faded ? this.speed.update(m.p, dt) : 0;
    this.courierRoot.position.set(this.curPos.x, this.roadTop + fx.hop(), this.curPos.z);
    this.courierRoot.rotation.y = this.curYaw;
    if (this.courierRoot.visible) this.courier.animate(this.shownSpeed, dt);
    this.halo.scale.setScalar(fx.pulse('courier'));
    (this.halo.material as THREE.MeshBasicMaterial).opacity = fx.haloOpacity();

    // pins + rings
    if (this.storePin && this.store) {
      this.storePin.position.y = this.store.top + 8 + fx.bob('store');
      const q = fx.squash('store');
      this.storePin.scale.set(q.xz, q.y, q.xz);
    }
    if (this.homePin && this.home) {
      this.homePin.position.y = this.home.top + 8 + fx.bob('home');
      const q = fx.squash('home');
      const k = fx.pulse('home');
      this.homePin.scale.set(q.xz * k, q.y * k, q.xz * k);
    }
    this.rings.forEach((r, i) => {
      const t = fx.ringPhase(i);
      r.scale.setScalar(t < 0 ? 1.25 : 1 + t * 0.9);
      (r.material as THREE.MeshBasicMaterial).opacity = t < 0 ? 0.5 : 0.65 * (1 - t);
    });

    // the store prepares the order: steam over its roof (beside the pin, on the camera's left)
    if (this.store && this.snap && PREPARING.has(this.snap.status)) {
      fx.setPreparing(this.tmp.set(this.store.center.x - 7, this.store.top, this.store.center.z + 7));
    } else {
      fx.setPreparing(null);
    }

    // the parcel drops onto the doorstep
    if (this.house) this.house.parcel.position.y = 0.8 + fx.parcelLift();

    // camera
    this.rig.frame(dt, this.curPos);
    this.rebuildRibbons(false);

    if (this.billboard) {
      const d = this.tmp.copy(this.camera.position).sub(this.controls.target);
      this.billboard.rotation.y = Math.atan2(d.x, d.z);
    }

    // the sun's shadow box follows what the camera looks at
    const tgt = this.controls.target;
    const off = this.sun.userData.offset as THREE.Vector3; // the sun's, or at night the moon's, place in the sky
    this.sun.position.set(tgt.x + off.x, off.y, tgt.z + off.z);
    this.sun.target.position.copy(tgt);

    if (this.tileCity) {
      this.tileCity.frame(fx.swayTime); // water shimmer and tree sway (still under reduced motion)
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
    if (!this.contextLost) this.render();
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
    // Third pass: the route's own see-through copies (no override, they keep their colors).
    this.scene.overrideMaterial = null;
    this.camera.layers.set(XRAY_ROUTE_LAYER);
    r.render(this.scene, this.camera);
    r.shadowMap.autoUpdate = true;
    r.autoClear = true;
    this.camera.layers.set(0);
    this.scene.background = bg;
  }

  /**
   * After a lost WebGL context: three.js starts over with a fresh GL state, so every program, texture and vertex
   * buffer the scene uses is marked for upload again (the scene graph and its data are all still here) and the shadow
   * map is drawn again. The theme stays where it was (day, night or mid-fade); the size is set again.
   */
  private rebuildGpu() {
    const seen = new Set<object>();
    const bump = (a: THREE.BufferAttribute | THREE.InterleavedBufferAttribute | null | undefined) => {
      if (!a) return;
      if (a instanceof THREE.InterleavedBufferAttribute) a.data.needsUpdate = true;
      else a.needsUpdate = true;
    };
    const material = (m: THREE.Material) => {
      if (seen.has(m)) return;
      seen.add(m);
      m.needsUpdate = true;
      for (const v of Object.values(m)) {
        if (v instanceof THREE.Texture && v.image && !seen.has(v)) {
          seen.add(v);
          v.needsUpdate = true;
        }
      }
    };
    this.scene.traverse((o) => {
      const geo = (o as THREE.Mesh).geometry as THREE.BufferGeometry | undefined;
      if (geo?.isBufferGeometry && !seen.has(geo)) {
        seen.add(geo);
        Object.values(geo.attributes).forEach(bump);
        bump(geo.index);
      }
      if (o instanceof THREE.InstancedMesh) {
        bump(o.instanceMatrix);
        bump(o.instanceColor);
      }
      const mats = (o as THREE.Mesh).material;
      if (mats) (Array.isArray(mats) ? mats : [mats]).forEach(material);
    });
    material(this.xray); // the x-ray pass's override material is in no mesh
    this.sun.shadow.map?.dispose();
    this.sun.shadow.map = null;
    this.renderer.shadowMap.needsUpdate = true;
    refreshTheme(this.scene);
    this.resize();
  }

  get drawCalls(): number {
    return this.renderer.info.render.calls;
  }

  dispose(): void {
    this.generation++;
    this.resizeObserver.disconnect();
    this.renderer.domElement.removeEventListener('webglcontextlost', this.onContextLost);
    this.renderer.domElement.removeEventListener('webglcontextrestored', this.onContextRestored);
    this.rig.dispose();
    this.disposeCity();
    if (this.ownsBuilder) this.builder?.dispose?.();
    this.scene.traverse((o) => {
      if (o instanceof THREE.Mesh) o.geometry.dispose();
    });
    this.effects.dispose();
    this.xray.dispose();
    this.xrayRibbonMats.forEach((m) => m.dispose());
    this.courierMats.forEach((m) => m.dispose());
    (this.courierGlow.material as THREE.Material).dispose();
    this.storeAccent.dispose();
    this.renderer.dispose();
    this.renderer.domElement.remove();
    Object.values(this.labels).forEach((l) => l.remove());
  }

  // ---------------------------------------------------------------------------
  // internals
  // ---------------------------------------------------------------------------

  /**
   * Frames store, home and the courier (when shown) with a margin, or only the home (`homeOnly`, on delivered).
   * `snap` jumps there, else glides (under reduced motion it always snaps).
   */
  private frameAll(snap: boolean, homeOnly = false) {
    const s = this.snap;
    if (!s) return;
    const home = this.home?.center ?? this.projection.toLocal(s.dropoff.location);
    const pts = homeOnly ? [home] : [this.store?.center ?? this.projection.toLocal(s.pickup.location), home];
    if (!homeOnly && this.courierRoot.visible) pts.push({ x: this.targetPos.x, z: this.targetPos.z });
    // Built for the orbit direction the camera has (or will have once the intro has turned and the 2D/3D tilt has
    // settled), so it still frames after the user rotated the view.
    const back = this.rig.frameBack();
    const f = computeFrame(
      this.camera,
      pts.map((p) => new THREE.Vector3(p.x, 0, p.z)),
      { back, insets: this.insets, viewport: [this.width, this.height] },
    );
    this.controls.minZoom = Math.min(MIN_ZOOM, f.zoom * 0.8);
    if (snap) this.rig.snapToFrame(f);
    else this.rig.glideToFrame(f);
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
    this.legs = routeLegs(s, (p) => this.projection.toLocal(p), null); // whole legs; the active one is cut per frame
    this.rebuildRibbons(true);
  }

  /** Metres per screen pixel at the current zoom. */
  private metresPerPx(): number {
    return (this.camera.right - this.camera.left) / this.camera.zoom / Math.max(1, this.width);
  }

  /**
   * Refills the ribbons in place. The active one starts at the courier (snapped onto the leg), so it is cut again
   * when the courier moved `TRIM_STEP_M`; both are refilled when the route changed or the zoom moved their on-screen
   * minimum width by > 10 %.
   */
  private rebuildRibbons(force: boolean) {
    const mpp = this.metresPerPx();
    const w = ribbonWidth(ACTIVE_RIBBON_M, ACTIVE_RIBBON_MIN_PX, mpp);
    const widthChanged = force || this.ribbonWidthM <= 0 || Math.abs(w - this.ribbonWidthM) / this.ribbonWidthM >= 0.1;
    const at: Vec2 | null = this.courierRoot.visible ? { x: this.curPos.x, z: this.curPos.z } : null;
    const moved =
      (at === null) !== (this.trimAt === null) ||
      (at !== null && this.trimAt !== null && Math.hypot(at.x - this.trimAt.x, at.z - this.trimAt.z) >= TRIM_STEP_M);
    if (!widthChanged && !moved) return;
    if (widthChanged) this.ribbonWidthM = w;
    const activeW = this.ribbonWidthM;
    this.trimAt = at;
    fillRibbon(this.routeActive.geometry, at ? trimLegFrom(this.legs.active, at) : this.legs.active, activeW, this.ribbonY + 0.05);
    if (widthChanged) {
      fillRibbon(this.routeNext.geometry, this.legs.next, ribbonWidth(NEXT_RIBBON_M, NEXT_RIBBON_MIN_PX, mpp), this.ribbonY);
    }
  }

  private setFaded(on: boolean) {
    if (on === this.faded) return;
    this.faded = on;
    this.applyFade();
  }

  private applyFade() {
    const on = this.faded;
    for (const m of this.courierMats) {
      m.transparent = on;
      m.opacity = on ? FADED_OPACITY : 1;
      m.needsUpdate = true;
    }
    // the see-through silhouette fades with it, and the halo goes (also from the x-ray pass)
    this.xray.opacity = on ? XRAY_OPACITY * FADED_OPACITY : XRAY_OPACITY;
    this.halo.visible = !on;
    this.glowHolder.visible = !on;
  }

  /**
   * Puts a courier model in the scene. Its materials are shared with every other model of the kind: it gets its own
   * copies (one per shared material) so it can fade; the geometries stay shared.
   */
  private mountCourier(model: CourierModel) {
    if (this.courier !== model) this.courierRoot.remove(this.courier.root);
    this.courierMats.forEach((m) => m.dispose());
    this.courierMats = [];
    const copies = new Map<THREE.Material, THREE.Material>();
    model.root.traverse((o) => {
      // Game trick: where a building hides the courier, a soft silhouette shows through.
      o.layers.enable(XRAY_LAYER);
      if (o instanceof THREE.Mesh && !Array.isArray(o.material)) {
        let c = copies.get(o.material);
        if (!c) {
          c = (o.material as THREE.Material).clone();
          copies.set(o.material, c);
          this.courierMats.push(c);
        }
        o.material = c;
      }
    });
    this.courier = model;
    this.courierRoot.add(model.root);
    if (this.snap) model.parcel.visible = this.snap.status !== 'delivered';
    this.applyFade();
    // lamps and tail lights glow at night, on every model (a theme set before the first mount applies in the constructor)
    if (this.themeFade) refreshTheme(this.scene, model.root);
  }

  /** Delivered: the pin bounces, the parcel drops at the door and a little confetti goes up over the home. */
  private celebrate() {
    this.effects.bounce('home');
    this.effects.dropParcel();
    if (this.home) this.effects.celebrate(new THREE.Vector3(this.home.center.x, this.home.top + 20, this.home.center.z));
  }

  private setLabelsVisible(on: boolean) {
    this.labels.store.style.visibility = on ? '' : 'hidden';
    this.labels.home.style.visibility = on ? '' : 'hidden';
  }

  private applyAccent() {
    (this.routeActive.material as THREE.MeshBasicMaterial).color.setHex(this.accent ?? ROUTE_ACTIVE);
    (this.routeActiveXray.material as THREE.MeshBasicMaterial).color.setHex(this.accent ?? ROUTE_ACTIVE);
    this.xray.color.setHex(this.accent ?? COLORS.blue);
    this.storeAccent.color.setHex(this.accent ?? COLORS.blue);
    this.storePinMat?.color.setHex(this.accent ?? COLORS.blue);
    this.storePinMat?.emissive.setHex(this.accent ?? COLORS.blue);
  }

  /** Delivered reads as success: the home pin, its curb ring and its label turn green (the prototype's pin color). */
  private applyHomePin() {
    if (!this.snap) return;
    const done = this.snap.status === 'delivered';
    this.labels.home.classList.toggle('delivered', done);
    if (!this.homePinMat) return;
    const c = done ? COLORS.green : COLORS.coral;
    this.homePinMat.color.setHex(c);
    this.homePinMat.emissive.setHex(c);
    (this.rings[1]?.material as THREE.MeshBasicMaterial | undefined)?.color.setHex(c);
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
        for (const m of ms) {
          if (!(m instanceof THREE.MeshStandardMaterial)) continue;
          // the shared window textures stay (every city and model uses them); clones are the model's own
          if (m.map && m.map.image && !isSharedTexture(m.map)) m.map.dispose();
          if (m.emissiveMap && m.emissiveMap !== m.map && m.emissiveMap.image && !isSharedTexture(m.emissiveMap)) {
            m.emissiveMap.dispose();
          }
        }
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
    // the pins share cached materials; give them their own so accent and delivered can recolor them (their color is
    // the partner's or the home's: the theme only makes them glow brighter at night)
    const own = (m: THREE.MeshStandardMaterial) => {
      m.userData = { dtColor: false, dtGlow: 'pin' };
      return m;
    };
    this.homePinMat = own(((this.homePin.children[1] as THREE.Mesh).material as THREE.MeshStandardMaterial).clone());
    (this.homePin.children[0] as THREE.Mesh).material = this.homePinMat;
    (this.homePin.children[1] as THREE.Mesh).material = this.homePinMat;
    this.storePinMat = own(((this.storePin.children[1] as THREE.Mesh).material as THREE.MeshStandardMaterial).clone());
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
      (ring.material as THREE.Material).userData.dtColor = false; // recolored on delivered
      ring.rotation.x = -Math.PI / 2;
      ring.position.set(p.x, this.ribbonY + 0.1, p.z);
      this.cityGroup.add(ring);
      this.rings.push(ring);
    }
    this.setLabelsVisible(true);
    this.applyAccent();
    this.applyHomePin();
    refreshTheme(this.scene, this.cityGroup); // the store, home and pins built now take the current theme
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
      const city = new TileCity(group, source, this.projection, { builder: this.builder });
      this.tileCity = city;
      city.setNight(nightMix(this.scene));
      const { bytes } = await city.loadFirst(s);
      if (gen !== this.generation) return;
      this.onTiles?.({ bytes, ms: performance.now() - started, tiles: city.loadedTiles });
      const sites = placeSites(city, this.snap ?? s, group, this.storeAccent);
      this.store = sites.store;
      this.home = sites.home;
      this.house = sites.home.house;
      this.billboard = (sites.store.object.userData.billboard as THREE.Object3D | undefined) ?? null;
      this.addMarkers();
      if (this.snap?.status === 'delivered') this.house.parcel.visible = true;
      if (this.rig.introPending && !this.rig.following) {
        // the city is on screen for the first time: frame it and play the intro
        this.frameAll(true);
        this.rig.startIntro();
      } else if (!this.rig.following) {
        this.frameAll(false, this.snap?.status === 'delivered');
      }
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
