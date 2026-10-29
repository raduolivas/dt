import type { TrackingSnapshot } from '@dt/contract';
import type { OrderStatus } from './simulator';

/** What the dev page knows beyond the snapshot: the simulator's finer phase and run state. */
export interface DevState {
  running: boolean;
  phase: OrderStatus;
  elapsedSec: number;
}

export interface StatusCopy {
  title: string;
  sub: string;
  /** index in the detailed timeline (0..6) */
  timeline: number;
  /** index in the 5-step bottom stepper (0..4) */
  step: number;
}

export const STATUS: Record<OrderStatus, StatusCopy> = {
  requested: { title: 'Order requested', sub: 'Looking for a courier…', timeline: 0, step: 0 },
  accepted: { title: 'Courier assigned', sub: 'Lucas is getting ready to ride', timeline: 1, step: 0 },
  to_store: { title: 'Heading to the store', sub: 'Lucas is riding to Food Hub', timeline: 2, step: 1 },
  at_store: { title: 'Waiting for your order', sub: 'Food Hub is preparing it', timeline: 3, step: 1 },
  picked_up: { title: 'Order picked up', sub: 'Lucas is setting off', timeline: 4, step: 2 },
  on_the_way: { title: 'On the way', sub: 'Lucas is riding to your place', timeline: 5, step: 3 },
  arriving: { title: 'Arriving now', sub: 'Lucas is almost at your door', timeline: 5, step: 3 },
  delivered: { title: 'Delivered', sub: 'Enjoy your meal!', timeline: 6, step: 4 },
};

export function formatEta(seconds: number | null): string {
  if (seconds === null) return '--:--';
  const s = Math.max(0, Math.round(seconds));
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, '0')}`;
}

export function formatDistance(m: number): string {
  return m >= 1000 ? `${(m / 1000).toFixed(1)} km` : `${Math.round(m)} m`;
}

export function courierLabel(s: TrackingSnapshot, phase: OrderStatus): string {
  if (phase === 'delivered') return 'Delivered ✓';
  if (phase === 'requested') return 'Courier on standby';
  return `${STATUS[phase].title} · ${formatEta(s.eta_seconds)}`;
}

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

function setText(el: HTMLElement, text: string) {
  if (el.textContent !== text) el.textContent = text;
}

export class Hud {
  private readonly el = {
    eta: $('stat-eta'),
    dist: $('stat-dist'),
    speed: $('stat-speed'),
    orderId: $('order-id'),
    title: $('order-title'),
    sub: $('order-sub'),
    timeline: Array.from($('timeline').children) as HTMLElement[],
    steps: Array.from(document.querySelectorAll<HTMLElement>('#steps .step')),
    barFill: $('bar-fill'),
    progress: $('progress-text'),
    live: $('live'),
    liveText: $('live-text'),
    run: $('btn-run') as HTMLButtonElement,
    json: $('json'),
  };
  private lastJson = 0;

  update(s: TrackingSnapshot, dev: DevState): void {
    const { running, phase, elapsedSec } = dev;
    const delivered = phase === 'delivered';
    const copy = STATUS[phase];
    setText(this.el.eta, formatEta(s.eta_seconds));
    setText(this.el.dist, formatDistance(s.distance_remaining_m ?? 0));
    setText(this.el.speed, `${Math.round(s.courier?.speed_kmh ?? 0)} km/h`);
    setText(this.el.orderId, s.delivery_id);
    setText(this.el.title, copy.title);
    setText(this.el.sub, copy.sub);

    this.el.timeline.forEach((li, i) => {
      li.classList.toggle('done', i < copy.timeline || delivered);
      li.classList.toggle('active', i === copy.timeline && !delivered);
    });
    this.el.steps.forEach((st, i) => {
      st.classList.toggle('done', i < copy.step || delivered);
      st.classList.toggle('active', i === copy.step && !delivered);
    });
    // bar spans from the first to the last dot (80% of the width)
    const fill = delivered ? 1 : copy.step / 4;
    this.el.barFill.style.width = `${(fill * 100).toFixed(1)}%`;
    setText(this.el.progress, `${Math.round((s.progress ?? 0) * 100)}%`);

    this.el.live.classList.toggle('running', running);
    this.el.live.classList.toggle('done', delivered);
    setText(
      this.el.liveText,
      delivered ? 'Delivered' : running ? 'Live' : phase === 'requested' ? 'Ready' : 'Paused',
    );

    let runLabel = 'Pause';
    if (delivered) runLabel = 'New order';
    else if (!running) runLabel = phase === 'requested' && elapsedSec === 0 ? 'Request order' : 'Resume';
    setText(this.el.run, runLabel);

    const now = performance.now();
    if (now - this.lastJson > 250) {
      this.lastJson = now;
      const ll = (p: { lat: number; lng: number }) => ({ lat: +p.lat.toFixed(6), lng: +p.lng.toFixed(6) });
      const c = s.courier;
      const compact = {
        ...s,
        courier: c && {
          ...c,
          position: c.position && ll(c.position),
          heading_deg: c.heading_deg === null ? null : +c.heading_deg.toFixed(1),
          speed_kmh: c.speed_kmh === null ? null : +c.speed_kmh.toFixed(1),
        },
        pickup: { ...s.pickup, location: ll(s.pickup.location) },
        dropoff: { ...s.dropoff, location: ll(s.dropoff.location) },
        eta_seconds: s.eta_seconds === null ? null : Math.round(s.eta_seconds),
        distance_remaining_m: s.distance_remaining_m === null ? null : Math.round(s.distance_remaining_m),
        progress: s.progress === null ? null : +s.progress.toFixed(3),
        route: `{ to_pickup: ${s.route.to_pickup ? `${s.route.to_pickup.length} pts` : 'null'}, to_dropoff: ${s.route.to_dropoff.length} pts }`,
      };
      this.el.json.textContent = JSON.stringify(compact, null, 2).replace(/"(\{ to_pickup.*?\})"/, '$1');
    }
  }
}
