import type { OrderStatus, TrackingSnapshot } from './engine/types';

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

export function courierLabel(s: TrackingSnapshot): string {
  if (s.status === 'delivered') return 'Delivered ✓';
  if (s.status === 'requested') return 'Courier on standby';
  return `${STATUS[s.status].title} · ${formatEta(s.etaSeconds)}`;
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

  update(s: TrackingSnapshot, running: boolean): void {
    const copy = STATUS[s.status];
    setText(this.el.eta, formatEta(s.etaSeconds));
    setText(this.el.dist, formatDistance(s.distanceRemainingM));
    setText(this.el.speed, `${Math.round(s.courier.speedKmh)} km/h`);
    setText(this.el.orderId, s.orderId);
    setText(this.el.title, copy.title);
    setText(this.el.sub, copy.sub);

    this.el.timeline.forEach((li, i) => {
      li.classList.toggle('done', i < copy.timeline || s.status === 'delivered');
      li.classList.toggle('active', i === copy.timeline && s.status !== 'delivered');
    });
    this.el.steps.forEach((st, i) => {
      st.classList.toggle('done', i < copy.step || s.status === 'delivered');
      st.classList.toggle('active', i === copy.step && s.status !== 'delivered');
    });
    // bar spans from the first to the last dot (80% of the width)
    const fill = s.status === 'delivered' ? 1 : copy.step / 4;
    this.el.barFill.style.width = `${(fill * 100).toFixed(1)}%`;
    setText(this.el.progress, `${Math.round(s.progress * 100)}%`);

    this.el.live.classList.toggle('running', running);
    this.el.live.classList.toggle('done', s.status === 'delivered');
    setText(
      this.el.liveText,
      s.status === 'delivered' ? 'Delivered' : running ? 'Live' : s.status === 'requested' ? 'Ready' : 'Paused',
    );

    let runLabel = 'Pause';
    if (s.status === 'delivered') runLabel = 'New order';
    else if (!running) runLabel = s.status === 'requested' && s.elapsedSec === 0 ? 'Request order' : 'Resume';
    setText(this.el.run, runLabel);

    const now = performance.now();
    if (now - this.lastJson > 250) {
      this.lastJson = now;
      const compact = {
        ...s,
        courier: {
          position: { lat: +s.courier.position.lat.toFixed(6), lng: +s.courier.position.lng.toFixed(6) },
          heading: +s.courier.heading.toFixed(1),
          speedKmh: +s.courier.speedKmh.toFixed(1),
        },
        etaSeconds: s.etaSeconds === null ? null : Math.round(s.etaSeconds),
        distanceRemainingM: Math.round(s.distanceRemainingM),
        progress: +s.progress.toFixed(3),
        elapsedSec: Math.round(s.elapsedSec),
        route: `{ toStore: ${s.route.toStore.length} pts, toCustomer: ${s.route.toCustomer.length} pts }`,
        store: { lat: +s.store.lat.toFixed(6), lng: +s.store.lng.toFixed(6) },
        destination: { lat: +s.destination.lat.toFixed(6), lng: +s.destination.lng.toFixed(6) },
      };
      this.el.json.textContent = JSON.stringify(compact, null, 2).replace(/"(\{ toStore.*?\})"/, '$1');
    }
  }
}
