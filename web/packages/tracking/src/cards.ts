import type { Branding, DeliveryStatus, TrackingCourier, TrackingSnapshot, Vehicle } from '@dt/contract';
import type { ViewerState } from '@dt/viewer';
import { t, timelineKey, vehicleKey, type CopyKey, type Lang } from './i18n';

// The customer cards, as real DOM text. They draw what the snapshot holds and nothing more: never the courier photo,
// never the dropoff label (always "Your address"), and the same calm copy for a stale and a lost courier signal.

export interface CardsView {
  state: ViewerState;
  lang: Lang;
  /** When the snapshot arrived (ms): the ETA window counts from here. */
  now: number;
  /** The last branding seen, for the summary after the link went inactive (that state carries none). */
  branding?: Branding | null;
}

const FINAL: ReadonlySet<DeliveryStatus> = new Set(['delivered', 'cancelled', 'failed']);
export const isFinal = (s: DeliveryStatus): boolean => FINAL.has(s);

const MINUTE = 60_000;
const pad = (n: number) => String(n).padStart(2, '0');

/** Local wall-clock time, 24 h: "12:40". */
export function formatClock(ms: number): string {
  const d = new Date(ms);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

interface Eta {
  label: string;
  value: string;
  /** The value is a time (not "Calculating…"). */
  timed: boolean;
}

/** The ETA as shown: a window of clock times before pickup, minutes after, "Calculating…" without one. */
function eta(s: TrackingSnapshot, lang: Lang, now: number): Eta {
  if (s.eta_state === 'estimated' && s.eta_window) {
    const from = Math.floor((now + s.eta_window.min_seconds * 1000) / MINUTE) * MINUTE;
    const to = Math.ceil((now + s.eta_window.max_seconds * 1000) / MINUTE) * MINUTE;
    return { label: t('arrivesBetween', lang), value: `${formatClock(from)}–${formatClock(to)}`, timed: true };
  }
  if (s.eta_state !== 'calculating' && s.eta_seconds !== null) {
    const n = Math.max(1, Math.round(s.eta_seconds / 60));
    return { label: t('arrivesIn', lang), value: t('minutes', lang, { n }), timed: true };
  }
  return { label: '', value: t('calculating', lang), timed: false };
}

// ---- DOM helpers ---------------------------------------------------------------------------------------------------

type Child = Node | string | null | false;

function h(tag: string, attrs: Record<string, string> = {}, ...children: Child[]): HTMLElement {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  for (const c of children) if (c !== null && c !== false) el.append(c);
  return el;
}

/** A card section; `name` is its key for tests and for keeping unchanged cards in place. */
const card = (name: string, attrs: Record<string, string>, ...children: Child[]) =>
  h('section', { class: `dt-card dt-card-${name}`, 'data-card': name, ...attrs }, ...children);

/** Static, trusted SVG markup (icons below). */
function icon(svg: string, cls = 'dt-icon'): HTMLElement {
  const s = h('span', { class: cls, 'aria-hidden': 'true' });
  s.innerHTML = svg;
  return s;
}

const svg = (body: string) =>
  `<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;

const VEHICLE_ICONS: Record<Vehicle, string> = {
  bicycle: svg('<circle cx="5.5" cy="17" r="3.5"/><circle cx="18.5" cy="17" r="3.5"/><path d="M5.5 17l4-8h5.5l3.5 8M9.5 9L8 6H6M12.5 13H9"/>'),
  e_bike: svg('<circle cx="5.5" cy="17" r="3.5"/><circle cx="18.5" cy="17" r="3.5"/><path d="M5.5 17l4-8h5.5l3.5 8M9.5 9L8 6H6"/><path d="M13 10.5l-2 3h3l-2 3"/>'),
  scooter: svg('<circle cx="6" cy="18" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.5 18h7M15 5h2.5l2 10.5M4 14h8l2 4M4 14l1-3h5"/>'),
  car: svg('<path d="M3 16v-4l2-5h14l2 5v4z"/><circle cx="7.5" cy="16.5" r="2"/><circle cx="16.5" cy="16.5" r="2"/><path d="M3 12h18"/>'),
  on_foot: svg('<circle cx="13" cy="4" r="2"/><path d="M10 21l2-6-3-3 1-4 4 2 2 3M9 8l-3 4M12 15l3 6"/>'),
};
const STORE_ICON = svg('<path d="M4 10v10h16V10M3 10l2-6h14l2 6zM9 20v-5h6v5"/>');
const HOME_ICON = svg('<path d="M4 11l8-7 8 7v9H4zM10 20v-6h4v6"/>');
const CHECK_ICON = svg('<path d="M5 12.5l4.5 4.5L19 7.5"/>');
const INFO_ICON = svg('<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5v.5"/>');
const LINK_ICON = svg('<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>');

// ---- cards -------------------------------------------------------------------------------------------------------

function brandCard(b: Branding | null): HTMLElement | null {
  if (!b || (!b.display_name && !b.logo_url)) return null;
  // the name is next to the logo, so the image is decorative; no referrer goes to the logo host
  const logo = b.logo_url
    ? h('img', { class: 'dt-logo', src: b.logo_url, alt: '', width: '32', height: '32', decoding: 'async', referrerpolicy: 'no-referrer' })
    : null;
  if (logo) (logo as HTMLImageElement).onerror = () => logo.remove();
  return h('header', { class: 'dt-card dt-card-brand', 'data-card': 'brand' }, logo, b.display_name ? h('span', { class: 'dt-brand-name' }, b.display_name) : null);
}

function etaCard(s: TrackingSnapshot, lang: Lang, now: number): HTMLElement {
  const e = eta(s, lang, now);
  return card(
    'eta',
    {},
    h('h2', { class: 'dt-head' }, t(`head_${s.status}` as CopyKey, lang)),
    h('p', { class: 'dt-sub' }, t(`sub_${s.status}` as CopyKey, lang, { store: s.pickup.name })),
    h(
      'p',
      { class: e.timed ? 'dt-eta' : 'dt-eta dt-eta-calc' },
      e.label ? h('span', { class: 'dt-eta-label' }, e.label) : null,
      e.label ? ' ' : null,
      h('span', { class: 'dt-eta-value' }, e.value),
      e.timed && s.eta_quality === 'estimated' ? ' ' : null,
      e.timed && s.eta_quality === 'estimated' ? h('span', { class: 'dt-badge' }, t('estimated', lang)) : null,
    ),
  );
}

function courierCard(c: TrackingCourier, lang: Lang): HTMLElement {
  // stale and lost read the same, and without alarm wording (M3 §3.3)
  const signal = c.signal === 'pending' ? t('locatingCourier', lang) : c.signal === 'live' ? null : t('updatingLocation', lang);
  return card(
    'courier',
    { 'aria-label': t('courier', lang) },
    icon(VEHICLE_ICONS[c.vehicle] ?? VEHICLE_ICONS.bicycle, 'dt-avatar'),
    h(
      'p',
      { class: 'dt-courier' },
      h('b', { class: 'dt-name' }, c.display_name),
      ' ',
      signal
        ? h('span', { class: 'dt-meta dt-signal', 'data-signal': '' }, signal)
        : h('span', { class: 'dt-meta' }, t(vehicleKey(c.vehicle), lang)),
    ),
  );
}

function routeCard(s: TrackingSnapshot, lang: Lang): HTMLElement {
  const stop = (cls: string, svgIcon: string, label: string, name: string) =>
    h('li', { class: cls }, icon(svgIcon, 'dt-stop-icon'), h('span', { class: 'dt-stop-label' }, label), ' ', h('b', {}, name));
  return card(
    'route',
    {},
    h(
      'ol',
      { class: 'dt-route' },
      stop('dt-stop dt-stop-store', STORE_ICON, t('pickup', lang), s.pickup.name),
      // never `dropoff.label`: it is free text the partner may fill with personal details
      stop('dt-stop dt-stop-home', HOME_ICON, t('dropoff', lang), t('yourAddress', lang)),
    ),
  );
}

function timelineCard(s: TrackingSnapshot, lang: Lang): HTMLElement | null {
  if (s.timeline.length === 0) return null;
  const last = s.timeline.length - 1;
  const done = isFinal(s.status);
  const items = s.timeline.map((e, i) => {
    const current = i === last && !done;
    return h(
      'li',
      { class: current ? 'dt-step dt-step-current' : 'dt-step dt-step-done', ...(current ? { 'aria-current': 'step' } : {}) },
      h('span', {}, t(timelineKey(e.status), lang)),
      ' ',
      h('time', { datetime: e.at }, formatClock(Date.parse(e.at))),
    );
  });
  return card('timeline', { 'aria-label': t('progress', lang) }, h('ol', { class: 'dt-timeline' }, ...items));
}

function finalCard(s: TrackingSnapshot, lang: Lang): HTMLElement {
  if (s.status === 'delivered') {
    const at = [...s.timeline].reverse().find((e) => e.status === 'delivered')?.at;
    return card(
      'final',
      { role: 'status', class: 'dt-card dt-card-final dt-final-delivered' },
      icon(CHECK_ICON, 'dt-final-icon'),
      h('h2', { class: 'dt-head' }, t('final_delivered', lang)),
      h('p', { class: 'dt-sub' }, at ? t('final_delivered_at', lang, { time: formatClock(Date.parse(at)) }) : t('final_delivered_sub', lang)),
    );
  }
  return card(
    'final',
    { role: 'status' },
    icon(INFO_ICON, 'dt-final-icon'),
    h('h2', { class: 'dt-head' }, t(s.status === 'cancelled' ? 'final_cancelled' : 'final_not_completed', lang)),
    h('p', { class: 'dt-sub' }, t('final_contact', lang, { store: s.pickup.name })),
  );
}

function snapshotCards(s: TrackingSnapshot, branding: Branding | null, lang: Lang, now: number): (HTMLElement | null)[] {
  const top = brandCard(branding);
  if (isFinal(s.status)) return [top, finalCard(s, lang), routeCard(s, lang), timelineCard(s, lang)];
  return [top, etaCard(s, lang, now), s.courier ? courierCard(s.courier, lang) : null, routeCard(s, lang), timelineCard(s, lang)];
}

function cardsFor({ state, lang, now, branding = null }: CardsView): HTMLElement[] {
  let list: (HTMLElement | null)[];
  switch (state.kind) {
    case 'connecting':
      list = [
        card('loading', { 'aria-busy': 'true' }, h('span', { class: 'dt-sr' }, t('loading', lang)), h('i', { class: 'dt-skel' }), h('i', { class: 'dt-skel dt-skel-short' })),
      ];
      break;
    case 'warming':
      list = [card('warming', { role: 'status' }, h('i', { class: 'dt-spinner', 'aria-hidden': 'true' }), h('p', { class: 'dt-head' }, t('warming', lang)))];
      break;
    case 'inactive':
      // after a final state the customer keeps the summary; otherwise a friendly card, never an error
      list =
        state.last && isFinal(state.last.status)
          ? snapshotCards(state.last, branding, lang, now)
          : [card('inactive', { role: 'status' }, icon(LINK_ICON, 'dt-final-icon'), h('p', { class: 'dt-head' }, t('inactive', lang)))];
      break;
    case 'live':
      list = snapshotCards(state.snapshot, state.branding, lang, now);
      break;
  }
  return list.filter((c): c is HTMLElement => c !== null);
}

/** The cards for a state, in a fresh `.dt-cards` container. */
export function buildCards(v: CardsView): HTMLElement {
  return h('div', { class: 'dt-cards' }, ...cardsFor(v));
}

/** One line for the collapsed sheet on a short landscape screen (and for screen readers): "On the way · 10 min". */
export function summaryLine({ state, lang, now }: CardsView): string {
  if (state.kind === 'connecting') return t('loading', lang);
  if (state.kind === 'warming') return t('warming', lang);
  const s = state.kind === 'live' ? state.snapshot : state.last;
  if (!s || (state.kind === 'inactive' && !isFinal(s.status))) return t('inactive', lang);
  if (s.status === 'delivered') return t('final_delivered', lang);
  if (s.status === 'cancelled') return t('final_cancelled', lang);
  if (s.status === 'failed') return t('final_not_completed', lang);
  const e = eta(s, lang, now);
  return e.timed ? `${t(`head_${s.status}` as CopyKey, lang)} · ${e.value}` : t(`head_${s.status}` as CopyKey, lang);
}

/** The cards in place: on each update only the cards whose content changed are replaced (focus and images stay). */
export class Cards {
  readonly el: HTMLElement = h('div', { class: 'dt-cards' });

  update(v: CardsView): void {
    const old = new Map<string, Element>();
    for (const c of this.el.children) old.set((c as HTMLElement).dataset.card ?? '', c);
    this.el.replaceChildren(
      ...cardsFor(v).map((next) => {
        const prev = old.get(next.dataset.card ?? '');
        return prev && prev.isEqualNode(next) ? prev : next;
      }),
    );
  }
}
