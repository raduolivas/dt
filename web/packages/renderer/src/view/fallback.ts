import type { TrackingSnapshot } from '@dt/contract';
import { GeoProjection, type Vec2 } from '../engine/geo';
import { COLORS, DAY, NIGHT } from './colors';
import type { Insets } from './framing';
import { CourierMotion } from './motion';
import { routeLegs } from './ribbon';

/** The 3D scene's active route blue (`ROUTE_ACTIVE`), unless the partner has an accent. */
const ROUTE_ACTIVE = '#2f6bff';
const hex = (c: number) => `#${c.toString(16).padStart(6, '0')}`;
/** Margin around store, home, route and courier (CSS px), and the closest it gets for a very short route. */
const PAD_PX = 36;
const MAX_PX_PER_M = 3;
const FADED_ALPHA = 0.4;

export interface FallbackOptions {
  theme?: 'light' | 'dark';
  accent?: string | null;
  /** Milliseconds; the courier's glide and dead reckoning run on it. */
  clock?: () => number;
}

/**
 * The renderer without WebGL (unavailable, or the map tiles did not come): a calm flat map on a 2D canvas, in the
 * prototype's palette. It draws the route legs, the pickup, the dropoff and the courier dot, from the same snapshot
 * and by the same rules as the 3D scene (the courier only when the snapshot shows it, gliding and dead-reckoned,
 * faded while stale or lost). Status text is the page's (SDK's) job, not the canvas's.
 */
export class FallbackView {
  private readonly canvas: HTMLCanvasElement;
  private readonly g: CanvasRenderingContext2D | null;
  private readonly resizeObserver: ResizeObserver | null;
  private readonly clock: () => number;
  private readonly motion: CourierMotion;
  private projection = new GeoProjection({ lat: 0, lng: 0 });
  private deliveryKey = '';
  private snap: TrackingSnapshot | null = null;
  private legs: { active: Vec2[]; next: Vec2[] } = { active: [], next: [] };
  private store: Vec2 | null = null;
  private home: Vec2 | null = null;
  private theme: 'light' | 'dark';
  private accent: string | null;
  private insets: Insets = { top: 0, right: 0, bottom: 0, left: 0 };
  private width = 1;
  private height = 1;
  private dpr = 1;
  /** World (local metres) to screen (CSS px): `x * scale + ox`, `z * scale + oy` (north up). */
  private scale = 1;
  private ox = 0;
  private oy = 0;
  private dirty = true;
  /** What the last drawing showed of the courier. */
  private drawnX = NaN;
  private drawnZ = NaN;
  private drawnFaded = false;

  constructor(
    private readonly container: HTMLElement,
    o: FallbackOptions = {},
  ) {
    this.theme = o.theme ?? 'light';
    this.accent = o.accent ?? null;
    this.clock = o.clock ?? (() => performance.now());
    this.motion = new CourierMotion((p) => this.projection.toLocal(p));
    this.canvas = document.createElement('canvas');
    this.canvas.className = 'scene-canvas dt-fallback';
    container.appendChild(this.canvas);
    this.g = this.canvas.getContext('2d');
    this.resizeObserver = typeof ResizeObserver === 'function' ? new ResizeObserver(() => this.resize()) : null;
    this.resizeObserver?.observe(container);
    this.resize();
  }

  onSnapshot(s: TrackingSnapshot): void {
    const key = `${s.delivery_id}|${s.pickup.location.lat},${s.pickup.location.lng}`;
    if (key !== this.deliveryKey) {
      this.deliveryKey = key;
      this.projection = new GeoProjection(s.pickup.location); // the pickup is the origin, as in the 3D scene
      this.motion.reset();
    }
    this.snap = s;
    const toLocal = (p: Parameters<GeoProjection['toLocal']>[0]) => this.projection.toLocal(p);
    this.legs = routeLegs(s, toLocal, null);
    this.store = toLocal(s.pickup.location);
    this.home = toLocal(s.dropoff.location);
    this.motion.onSnapshot(s, this.clock());
    this.fit();
  }

  /** Redraws when something changed or the courier moved. */
  frame(dt: number): void {
    const m = this.motion.frame(Math.min(dt, 0.1) * 1000, this.clock());
    const moved = m.visible ? m.p.x !== this.drawnX || m.p.z !== this.drawnZ : !Number.isNaN(this.drawnX);
    if (!this.dirty && !moved && m.faded === this.drawnFaded) return;
    this.dirty = false;
    this.drawnX = m.visible ? m.p.x : NaN;
    this.drawnZ = m.visible ? m.p.z : NaN;
    this.drawnFaded = m.faded;
    this.draw(m.visible ? m.p : null, m.faded);
  }

  setTheme(t: 'light' | 'dark'): void {
    this.theme = t;
    this.dirty = true;
  }

  setAccent(c: string | null): void {
    this.accent = c;
    this.dirty = true;
  }

  setInsets(i: Insets): void {
    this.insets = { ...i };
    this.fit();
  }

  get drawCalls(): number {
    return 0;
  }

  dispose(): void {
    this.resizeObserver?.disconnect();
    this.canvas.remove();
  }

  private resize() {
    this.width = Math.max(1, this.container.clientWidth);
    this.height = Math.max(1, this.container.clientHeight);
    this.dpr = Math.min(typeof devicePixelRatio === 'number' ? devicePixelRatio : 1, 2);
    this.canvas.width = Math.round(this.width * this.dpr);
    this.canvas.height = Math.round(this.height * this.dpr);
    this.fit();
  }

  /** Fits store, home, the route and the courier's fix into the part of the canvas the cards leave free. */
  private fit() {
    this.dirty = true;
    if (!this.store || !this.home) return;
    let minX = Infinity;
    let maxX = -Infinity;
    let minZ = Infinity;
    let maxZ = -Infinity;
    const add = (p: Vec2) => {
      minX = Math.min(minX, p.x);
      maxX = Math.max(maxX, p.x);
      minZ = Math.min(minZ, p.z);
      maxZ = Math.max(maxZ, p.z);
    };
    add(this.store);
    add(this.home);
    this.legs.active.forEach(add);
    this.legs.next.forEach(add);
    const fix = this.motion.fix;
    if (fix) add(fix);
    const i = this.insets;
    const freeW = Math.max(40, this.width - i.left - i.right - 2 * PAD_PX);
    const freeH = Math.max(40, this.height - i.top - i.bottom - 2 * PAD_PX);
    this.scale = Math.min(MAX_PX_PER_M, freeW / Math.max(1, maxX - minX), freeH / Math.max(1, maxZ - minZ));
    this.ox = i.left + PAD_PX + freeW / 2 - ((minX + maxX) / 2) * this.scale;
    this.oy = i.top + PAD_PX + freeH / 2 - ((minZ + maxZ) / 2) * this.scale;
  }

  private draw(courier: Vec2 | null, faded: boolean) {
    const g = this.g;
    if (!g) return;
    const night = this.theme === 'dark';
    g.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    g.globalAlpha = 1;
    g.fillStyle = hex(night ? NIGHT.sky : DAY.sky);
    g.fillRect(0, 0, this.width, this.height);
    const s = this.snap;
    if (!s || !this.store || !this.home) return;
    const outline = night ? hex(NIGHT.ground) : '#ffffff';
    g.lineCap = 'round';
    g.lineJoin = 'round';
    // the next leg light, the active one in the accent over a soft casing
    this.line(this.legs.next, hex(COLORS.blue), 5, 0.45);
    this.line(this.legs.active, outline, 10, 0.9);
    this.line(this.legs.active, this.accent ?? ROUTE_ACTIVE, 6, 1);
    // store and home (green once delivered)
    this.dot(this.store, 9, this.accent ?? hex(COLORS.blue), outline, 1);
    this.dot(this.home, 9, hex(s.status === 'delivered' ? COLORS.green : COLORS.coral), outline, 1);
    if (courier) {
      const a = faded ? FADED_ALPHA : 1;
      if (!faded) this.dot(courier, 16, hex(COLORS.blue), null, 0.18); // the halo
      this.dot(courier, 7, hex(COLORS.blue), outline, a);
    }
    g.globalAlpha = 1;
  }

  private line(pts: Vec2[], color: string, width: number, alpha: number) {
    const g = this.g!;
    if (pts.length < 2) return;
    g.globalAlpha = alpha;
    g.strokeStyle = color;
    g.lineWidth = width;
    g.beginPath();
    pts.forEach((p, i) => {
      const x = p.x * this.scale + this.ox;
      const y = p.z * this.scale + this.oy;
      if (i === 0) g.moveTo(x, y);
      else g.lineTo(x, y);
    });
    g.stroke();
  }

  private dot(p: Vec2, r: number, fill: string, outline: string | null, alpha: number) {
    const g = this.g!;
    g.globalAlpha = alpha;
    g.beginPath();
    g.arc(p.x * this.scale + this.ox, p.z * this.scale + this.oy, r, 0, Math.PI * 2);
    g.fillStyle = fill;
    g.fill();
    if (outline) {
      g.lineWidth = 3;
      g.strokeStyle = outline;
      g.stroke();
    }
  }
}
