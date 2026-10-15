import * as THREE from 'three';
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js';
import type { Vec2 } from '../engine/geo';
import type { DestinationPreset, TrackingSnapshot } from '../engine/types';
import {
  BLOCK,
  DESTINATIONS,
  GRID,
  ROAD_W,
  STORE,
  projection,
  type Place,
} from '../engine/world';
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

const BIKE_SCALE = 6; // visual scale of the courier (toy city)
const ROAD_TOP = 0.4;
const BLOCK_TOP = 0.8;
const BASE_HALF_H = 240;
const MIN_HALF_W = 260;
const FOLLOW_ZOOM = 1.7;
const CAM_OFFSET = new THREE.Vector3(300, 280, 300);

function angleDiff(target: number, current: number) {
  return ((((target - current + Math.PI) % (Math.PI * 2)) + Math.PI * 2) % (Math.PI * 2)) - Math.PI;
}

export class CityScene {
  private readonly renderer: THREE.WebGLRenderer;
  private readonly scene = new THREE.Scene();
  private readonly camera = new THREE.OrthographicCamera(-1, 1, 1, -1, 1, 4000);
  private readonly controls: OrbitControls;
  private readonly resizeObserver: ResizeObserver;

  private cityGroup = new THREE.Group();
  private readonly bicycle = new Bicycle();
  private readonly courierRoot = new THREE.Group();
  private readonly halo: THREE.Mesh;
  private readonly routeActive: THREE.Mesh;
  private readonly routeNext: THREE.Mesh;

  private storePin: THREE.Group | null = null;
  private homePin: THREE.Group | null = null;
  private homePinMat: THREE.MeshStandardMaterial | null = null;
  private rings: THREE.Mesh[] = [];
  private house: HouseModel | null = null;
  private destination: Place = DESTINATIONS.medium;

  private readonly labels: { store: HTMLElement; home: HTMLElement; courier: HTMLElement };

  private snap: TrackingSnapshot | null = null;
  private hasPos = false;
  private readonly curPos = new THREE.Vector3();
  private readonly targetPos = new THREE.Vector3();
  private curYaw = 0;
  private targetYaw = 0;
  private speedMs = 0;
  private follow = true;
  private goal: { target: THREE.Vector3; zoom: number } | null = null;
  private parcelDrop = 0; // seconds since delivery, -1 when idle
  private time = 0;
  private width = 1;
  private height = 1;
  private readonly tmp = new THREE.Vector3();

  constructor(private readonly container: HTMLElement) {
    this.renderer = new THREE.WebGLRenderer({ antialias: true, alpha: false });
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    this.renderer.shadowMap.enabled = true;
    this.renderer.shadowMap.type = THREE.PCFSoftShadowMap;
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;
    this.renderer.domElement.className = 'scene-canvas';
    container.appendChild(this.renderer.domElement);

    this.scene.background = new THREE.Color(COLORS.ground);

    // lights
    this.scene.add(new THREE.HemisphereLight(0xffffff, 0xb9c6e6, 1.5));
    const sun = new THREE.DirectionalLight(0xffffff, 2.4);
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
    this.scene.add(sun);

    // camera + controls
    this.camera.position.copy(CAM_OFFSET);
    this.camera.zoom = FOLLOW_ZOOM;
    this.controls = new OrbitControls(this.camera, this.renderer.domElement);
    this.controls.enableDamping = true;
    this.controls.dampingFactor = 0.08;
    this.controls.minPolarAngle = 0.35;
    this.controls.maxPolarAngle = 1.25;
    this.controls.minZoom = 0.6;
    this.controls.maxZoom = 5;
    this.controls.rotateSpeed = 0.6;
    this.controls.enablePan = false;
    this.controls.addEventListener('start', () => (this.goal = null));

    // courier
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
    this.routeActive = new THREE.Mesh(new THREE.BufferGeometry(), ribbonMat(0x2f6bff, 0.95));
    this.routeNext.renderOrder = 1;
    this.routeActive.renderOrder = 2;
    this.scene.add(this.routeNext, this.routeActive);

    // HTML labels
    const mkLabel = (cls: string) => {
      const el = document.createElement('div');
      el.className = `scene-label ${cls}`;
      container.appendChild(el);
      return el;
    };
    this.labels = { store: mkLabel('store'), home: mkLabel('home'), courier: mkLabel('courier') };
    this.labels.store.textContent = STORE.label;
    this.labels.home.textContent = this.destination.label;

    this.buildCity();

    this.resizeObserver = new ResizeObserver(() => this.resize());
    this.resizeObserver.observe(container);
    this.resize();
  }

  // ---------------------------------------------------------------------------
  // public API
  // ---------------------------------------------------------------------------

  setDestination(preset: DestinationPreset): void {
    this.destination = DESTINATIONS[preset];
    this.labels.home.textContent = this.destination.label;
    this.buildCity();
    this.hasPos = false; // snap courier on the next snapshot
  }

  setFollow(on: boolean): void {
    this.follow = on;
    this.controls.enablePan = !on;
    if (on) this.goal = { target: this.curPos.clone(), zoom: FOLLOW_ZOOM };
  }

  get isFollowing(): boolean {
    return this.follow;
  }

  overview(): void {
    this.follow = false;
    this.controls.enablePan = true;
    this.goal = { target: new THREE.Vector3(0, 0, 0), zoom: 0.85 };
  }

  setCourierLabel(text: string): void {
    this.labels.courier.textContent = text;
  }

  onSnapshot(s: TrackingSnapshot): void {
    const prev = this.snap;
    this.snap = s;

    const p = projection.toLocal(s.courier.position);
    this.targetPos.set(p.x, 0, p.z);
    this.targetYaw = Math.PI - (s.courier.heading * Math.PI) / 180;
    this.speedMs = s.courier.speedKmh / 3.6;

    if (!this.hasPos || this.curPos.distanceTo(this.targetPos) > 60) {
      this.curPos.copy(this.targetPos);
      this.curYaw = this.targetYaw;
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
    if (this.homePinMat) {
      const c = s.status === 'delivered' ? COLORS.green : COLORS.coral;
      this.homePinMat.color.setHex(c);
      this.homePinMat.emissive.setHex(c);
    }

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
    this.courierRoot.position.set(this.curPos.x, ROAD_TOP + bob, this.curPos.z);
    this.courierRoot.rotation.y = this.curYaw;
    this.bicycle.animate(dt, this.speedMs, BIKE_SCALE);
    const pulse = 1 + Math.sin(this.time * 4) * 0.12;
    this.halo.scale.setScalar(pulse);
    (this.halo.material as THREE.MeshBasicMaterial).opacity = 0.4 + Math.sin(this.time * 4) * 0.15;

    // pins + rings
    const bobY = Math.sin(this.time * 2.4) * 1.2;
    if (this.storePin) this.storePin.position.y = STORE_HEIGHT + 8 + bobY;
    if (this.homePin) this.homePin.position.y = HOUSE_HEIGHT + 8 + bobY;
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

    this.updateLabels();
    this.renderer.render(this.scene, this.camera);
  }

  dispose(): void {
    this.resizeObserver.disconnect();
    this.controls.dispose();
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

  private resize() {
    const w = Math.max(1, this.container.clientWidth);
    const h = Math.max(1, this.container.clientHeight);
    this.width = w;
    this.height = h;
    this.renderer.setSize(w, h, false);
    const aspect = w / h;
    let halfH = BASE_HALF_H;
    let halfW = halfH * aspect;
    if (halfW < MIN_HALF_W) {
      halfW = MIN_HALF_W;
      halfH = halfW / aspect;
    }
    this.camera.left = -halfW;
    this.camera.right = halfW;
    this.camera.top = halfH;
    this.camera.bottom = -halfH;
    this.camera.updateProjectionMatrix();
  }

  private updateRoutes(s: TrackingSnapshot) {
    const toStore = s.route.toStore.map((p) => projection.toLocal(p));
    const toCust = s.route.toCustomer.map((p) => projection.toLocal(p));
    const here: Vec2 = { x: this.targetPos.x, z: this.targetPos.z };
    let active: Vec2[] = [];
    let next: Vec2[] = [];
    switch (s.status) {
      case 'requested':
      case 'accepted':
      case 'to_store':
        active = trimRouteFrom(toStore, here);
        next = toCust;
        break;
      case 'at_store':
      case 'picked_up':
        active = toCust;
        break;
      case 'on_the_way':
      case 'arriving':
        active = trimRouteFrom(toCust, here);
        break;
      case 'delivered':
        break;
    }
    this.routeActive.geometry.dispose();
    this.routeActive.geometry = buildRibbon(active, 4.6, ROAD_TOP + 0.15);
    this.routeNext.geometry.dispose();
    this.routeNext.geometry = buildRibbon(next, 3.2, ROAD_TOP + 0.1);
  }

  private updateLabels() {
    const place = (el: HTMLElement, world: THREE.Vector3) => {
      const v = this.tmp.copy(world).project(this.camera);
      const x = (v.x * 0.5 + 0.5) * this.width;
      const y = (-v.y * 0.5 + 0.5) * this.height;
      el.style.transform = `translate(${x.toFixed(1)}px, ${y.toFixed(1)}px) translate(-50%, -100%)`;
      el.style.opacity = Math.abs(v.x) < 1.1 && Math.abs(v.y) < 1.1 ? '1' : '0';
    };
    const sb = STORE.building;
    const hb = this.destination.building;
    place(this.labels.store, new THREE.Vector3(sb.x, STORE_HEIGHT + 24, sb.z));
    place(this.labels.home, new THREE.Vector3(hb.x, HOUSE_HEIGHT + 24, hb.z));
    place(this.labels.courier, new THREE.Vector3(this.curPos.x, 16, this.curPos.z));
  }

  // ---------------------------------------------------------------------------
  // city
  // ---------------------------------------------------------------------------

  private buildCity() {
    this.scene.remove(this.cityGroup);
    this.cityGroup.traverse((o) => {
      if (o instanceof THREE.Mesh || o instanceof THREE.InstancedMesh) o.geometry.dispose();
    });
    const city = new THREE.Group();
    this.cityGroup = city;
    this.rings = [];

    const reserved = new Set<string>([
      `${STORE.cell.i},${STORE.cell.j}`,
      `${this.destination.cell.i},${this.destination.cell.j}`,
    ]);

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
    const dashMesh = new THREE.InstancedMesh(
      new THREE.BoxGeometry(1, 0.05, 1),
      mat(COLORS.roadMark),
      dashes.length,
    );
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
            const place = i === STORE.cell.i && j === STORE.cell.j ? STORE : this.destination;
            if (ez === -place.side) continue;
          }
          const tree = makeTree(rand);
          tree.position.set(cx + ex * (inner / 2 - 4), BLOCK_TOP, cz + ez * (inner / 2 - 4));
          city.add(tree);
        }
      }
    }

    // store
    const store = makeStore(STORE.label);
    store.position.set(STORE.building.x, BLOCK_TOP, STORE.building.z);
    store.rotation.y = STORE.side === 1 ? Math.PI : 0;
    city.add(store);

    // house
    const house = makeHouse();
    this.house = house;
    const hp = this.destination;
    house.group.position.set(hp.building.x, BLOCK_TOP, hp.building.z - hp.side * 6);
    house.group.rotation.y = hp.side === 1 ? Math.PI : 0;
    city.add(house.group);

    // pins + curb rings
    this.storePin = makePin(COLORS.blue);
    this.storePin.position.set(STORE.building.x, STORE_HEIGHT + 8, STORE.building.z);
    this.homePin = makePin(COLORS.coral);
    this.homePin.position.set(hp.building.x, HOUSE_HEIGHT + 8, hp.building.z);
    this.homePinMat = (this.homePin.children[1] as THREE.Mesh).material as THREE.MeshStandardMaterial;
    // the coral pin shares a cached material; give it its own so the delivered state can recolor it
    this.homePinMat = this.homePinMat.clone();
    (this.homePin.children[0] as THREE.Mesh).material = this.homePinMat;
    (this.homePin.children[1] as THREE.Mesh).material = this.homePinMat;
    city.add(this.storePin, this.homePin);

    for (const [p, color] of [
      [STORE.stop, COLORS.blue],
      [hp.stop, COLORS.coral],
    ] as [Vec2, number][]) {
      const ring = new THREE.Mesh(
        new THREE.RingGeometry(5, 6.4, 40),
        new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 0.6, side: THREE.DoubleSide, depthWrite: false }),
      );
      ring.rotation.x = -Math.PI / 2;
      ring.position.set(p.x, ROAD_TOP + 0.2, p.z);
      city.add(ring);
      this.rings.push(ring);
    }

    this.scene.add(city);
    if (this.snap) this.onSnapshot(this.snap);
  }
}
