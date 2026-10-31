import { CourierError } from '@dt/courier';
import type { Courier, CourierDelivery, CourierEventType, CourierState } from '@dt/courier';
import { FAIL_REASONS, t } from './i18n';
import type { CopyKey, Lang } from './i18n';
import type { WakeLock } from './wakelock';

// The courier page: consent, deliveries and end of shift, in plain DOM. All text goes in with textContent. The status
// bar is updated in place (it changes with every GPS fix) so a tap is never lost to a re-render; the deliveries list
// is rebuilt only when the deliveries or the page's own state change.

export interface PageDeps {
  /** API base including /v1. */
  api: string;
  /** `location.pathname` */
  path: string;
  lang: Lang;
  makeCourier(o: { api: string; linkCode?: string }): Courier;
  /** `history.replaceState` to this path (drops the link code from the address bar). */
  replaceUrl(path: string): void;
  wakeLock: WakeLock;
}

type EndWhy = 'ended' | 'expired' | 'used' | 'invalid' | 'no_session' | 'done';
type Screen =
  | { kind: 'consent'; code: string }
  | { kind: 'starting' }
  | { kind: 'retry' }
  | { kind: 'deliveries' }
  | { kind: 'over'; why: EndWhy };

interface Note {
  text: string;
  /** Shown on the card of this delivery while it exists, else as a banner. */
  deliveryId: string;
}

const WAITING_STATUSES = new Set(['confirmed', 'courier_assigned', 'at_pickup']);
const OTHER_MAX = 200;

type Child = Node | string | null | false | undefined;
function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, string | boolean | undefined> = {},
  ...kids: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === false) continue;
    if (k === 'class') el.className = String(v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids) if (k) el.append(k);
  return el;
}

const sessionOnly = (e: unknown) => e instanceof CourierError && e.code === 'session_ended';

export function mountCourierPage(root: HTMLElement, deps: PageDeps): void {
  const { lang } = deps;
  const tr = (key: CopyKey, vars?: Record<string, string | number>) => t(key, lang, vars);

  let screen: Screen = { kind: 'starting' };
  let courier: Courier | undefined;
  let unsubscribe: (() => void)[] = [];
  let confirmingEnd = false;
  let ending = false;
  const pending = new Set<string>(); // `${deliveryId}:${type}` taps whose report() has not been answered
  const failOpen = new Map<string, { other: boolean; text: string }>(); // delivery id -> open "can't deliver" panel
  const notes: Note[] = [];
  let statusEl: HTMLElement | undefined;

  const ACTIONS = new Set(['at_pickup', 'pickup_confirmed', 'delivered', 'arriving', 'failed']);
  const STATUSES = new Set(['confirmed', 'courier_assigned', 'at_pickup', 'picked_up', 'arriving', 'delivered', 'cancelled', 'failed']);
  const actionName = (type: string): string => (ACTIONS.has(type) ? tr(`act_${type}` as CopyKey) : type);
  const statusName = (s: string): string => (STATUSES.has(s) ? tr(`st_${s}` as CopyKey) : s);

  // ---- screens -----------------------------------------------------------------------------------------

  function show(next: Screen) {
    screen = next;
    render();
  }

  function render() {
    const keep = focusKey();
    root.replaceChildren();
    statusEl = undefined;
    const main = h('main', { class: 'page' });
    switch (screen.kind) {
      case 'consent':
        main.append(consentView(screen.code));
        break;
      case 'starting':
        main.append(messageView('starting', undefined, true));
        break;
      case 'retry':
        {
          const again = h('button', { class: 'btn primary big', type: 'button', 'data-act': 'retry' }, tr('retry'));
          again.addEventListener('click', () => void begin());
          main.append(messageView('retryTitle', 'retryBody'), again);
        }
        break;
      case 'over':
        main.append(overView(screen.why));
        break;
      case 'deliveries':
        main.append(...deliveriesView());
        break;
    }
    root.append(main);
    restoreFocus(keep);
  }

  /** The text field being typed in survives a rebuild. */
  function focusKey(): { key: string; start: number | null } | undefined {
    const a = document.activeElement as HTMLInputElement | null;
    const key = a?.getAttribute?.('data-keep');
    return a && key ? { key, start: a.selectionStart } : undefined;
  }
  function restoreFocus(k: { key: string; start: number | null } | undefined) {
    if (!k) return;
    const el = root.querySelector<HTMLInputElement>(`[data-keep="${CSS.escape(k.key)}"]`);
    if (!el) return;
    el.focus();
    if (k.start !== null) el.setSelectionRange(k.start, k.start);
  }

  function messageView(title: CopyKey, body?: CopyKey, spinner = false): HTMLElement {
    return h(
      'section',
      { class: 'card message', role: 'status' },
      h('h1', {}, tr(title)),
      body && h('p', {}, tr(body)),
      spinner && h('div', { class: 'spinner', 'aria-hidden': 'true' }),
    );
  }

  function consentView(code: string): HTMLElement {
    const start = h('button', { class: 'btn primary big', type: 'button', 'data-act': 'start' }, tr('startShift'));
    start.addEventListener('click', () => void begin(code));
    const row = (k: CopyKey, b: CopyKey) => h('div', { class: 'row' }, h('h2', {}, tr(k)), h('p', {}, tr(b)));
    return h(
      'section',
      { class: 'consent' },
      h('h1', {}, tr('consentTitle')),
      h('p', { class: 'lead' }, tr('consentIntro')),
      row('consentWhat', 'consentWhatBody'),
      row('consentWhy', 'consentWhyBody'),
      row('consentWho', 'consentWhoBody'),
      start,
    );
  }

  const OVER: Record<EndWhy, [CopyKey, CopyKey]> = {
    ended: ['endedTitle', 'endedBody'],
    expired: ['expiredTitle', 'expiredBody'],
    used: ['usedTitle', 'usedBody'],
    invalid: ['invalidTitle', 'invalidBody'],
    no_session: ['noSessionTitle', 'noSessionBody'],
    done: ['doneTitle', 'doneBody'],
  };
  function overView(why: EndWhy): HTMLElement {
    const [title, body] = OVER[why];
    return messageView(title, body);
  }

  // ---- deliveries ---------------------------------------------------------------------------------------

  function deliveriesView(): Node[] {
    const c = courier;
    if (!c) return [];
    const out: Node[] = [];
    out.push(h('h1', { class: 'sr-only' }, tr('yourDeliveries')));
    statusEl = h('div', { class: 'statusbar', 'aria-live': 'polite', 'data-role': 'status' });
    out.push(statusEl);
    updateStatus(c.state);
    if (!deps.wakeLock.supported) out.push(h('p', { class: 'notice' }, tr('keepScreenOn')));
    if (c.state.gps === 'denied') out.push(deniedHelp());
    else if (c.state.gps === 'asking') out.push(h('p', { class: 'notice asking' }, tr('gpsAsking')));

    const ids = new Set(c.deliveries.map((d) => d.id));
    for (const n of notes.filter((n) => !ids.has(n.deliveryId))) out.push(noteView(n, true));

    if (c.deliveries.length === 0) out.push(h('p', { class: 'empty' }, tr('noDeliveries')));
    for (const d of c.deliveries) out.push(cardView(d));

    out.push(endView());
    return out;
  }

  function deniedHelp(): HTMLElement {
    return h(
      'section',
      { class: 'card warn', 'data-role': 'denied' },
      h('h2', {}, tr('deniedTitle')),
      h('p', {}, tr('deniedBody')),
      h('ol', {}, h('li', {}, tr('deniedStep1')), h('li', {}, tr('deniedStep2')), h('li', {}, tr('deniedStep3'))),
    );
  }

  function noteView(n: Note, banner: boolean): HTMLElement {
    const ok = h('button', { class: 'btn small', type: 'button' }, tr('dismiss'));
    ok.addEventListener('click', () => {
      notes.splice(notes.indexOf(n), 1);
      render();
    });
    return h('div', { class: banner ? 'note banner' : 'note', role: 'alert', 'data-role': 'note' }, h('p', {}, n.text), ok);
  }

  function cardView(d: CourierDelivery): HTMLElement {
    const main = d.next_actions.find((a) => a !== 'failed');
    const canFail = d.next_actions.includes('failed');
    const card = h('article', { class: 'card delivery', 'data-delivery': d.id });

    card.append(
      h('div', { class: 'ref' }, d.reference),
      h('p', { class: 'status' }, statusName(d.status)),
      h('div', { class: 'place' }, h('span', { class: 'lbl' }, tr('from')), h('strong', {}, d.pickup.label)),
      h(
        'div',
        { class: 'place' },
        h('span', { class: 'lbl' }, tr('to')),
        h('strong', {}, d.dropoff.label ?? ''),
        d.dropoff.notes && h('span', { class: 'sub' }, `${tr('notes')}: ${d.dropoff.notes}`),
      ),
    );
    if (d.items_summary) {
      card.append(h('div', { class: 'place' }, h('span', { class: 'lbl' }, tr('items')), h('span', {}, d.items_summary)));
    }

    for (const n of notes.filter((n) => n.deliveryId === d.id)) card.append(noteView(n, false));

    const sending = [...pending].some((k) => k.startsWith(`${d.id}:`));
    if (main) {
      const btn = h(
        'button',
        { class: 'btn primary main', type: 'button', 'data-act': main, disabled: pending.has(`${d.id}:${main}`) },
        tr(`act_${main}` as CopyKey),
      );
      btn.addEventListener('click', () => void tap(d, main));
      card.append(btn);
    } else if (WAITING_STATUSES.has(d.status)) {
      const courierConfirmed = d.pickup_confirmations.some((p) => p.by === 'courier');
      card.append(h('p', { class: 'waiting', 'data-role': 'waiting' }, tr(courierConfirmed ? 'waitBoth' : 'waitStore')));
    }
    if (sending) card.append(h('p', { class: 'unsent', 'data-role': 'unsent' }, tr('notSentYet')));

    if (canFail) card.append(failView(d));
    return card;
  }

  function failView(d: CourierDelivery): HTMLElement {
    const open = failOpen.get(d.id);
    if (!open) {
      const link = h('button', { class: 'btn link', type: 'button', 'data-act': 'failed' }, tr('act_failed'));
      link.addEventListener('click', () => {
        failOpen.set(d.id, { other: false, text: '' });
        render();
      });
      return h('div', { class: 'fail' }, link);
    }
    const panel = h('div', { class: 'fail open', 'data-role': 'reasons' }, h('h2', {}, tr('whyTitle')));
    for (const r of FAIL_REASONS) {
      const b = h('button', { class: 'btn reason', type: 'button', 'data-reason': r }, tr(`reason_${r}` as CopyKey));
      b.addEventListener('click', () => void tap(d, 'failed', r));
      panel.append(b);
    }
    if (!open.other) {
      const o = h('button', { class: 'btn reason', type: 'button', 'data-reason': 'other' }, tr('reason_other'));
      o.addEventListener('click', () => {
        open.other = true;
        render();
        root.querySelector<HTMLInputElement>(`[data-keep="other-${CSS.escape(d.id)}"]`)?.focus();
      });
      panel.append(o);
    } else {
      const input = h('input', {
        class: 'field',
        type: 'text',
        maxlength: String(OTHER_MAX - 'other: '.length),
        'aria-label': tr('otherLabel'),
        placeholder: tr('otherLabel'),
        'data-keep': `other-${d.id}`,
      });
      input.value = open.text;
      input.addEventListener('input', () => (open.text = input.value));
      const send = h('button', { class: 'btn primary', type: 'button', 'data-act': 'send-other' }, tr('send'));
      send.addEventListener('click', () => {
        const text = open.text.trim();
        void tap(d, 'failed', text ? `other: ${text}`.slice(0, OTHER_MAX) : 'other');
      });
      panel.append(input, send);
    }
    const back = h('button', { class: 'btn link', type: 'button' }, tr('cancel'));
    back.addEventListener('click', () => {
      failOpen.delete(d.id);
      render();
    });
    panel.append(back);
    return panel;
  }

  function endView(): HTMLElement {
    if (!confirmingEnd) {
      const b = h('button', { class: 'btn outline end', type: 'button', 'data-act': 'end' }, tr('endShift'));
      b.addEventListener('click', () => {
        confirmingEnd = true;
        render();
      });
      return h('div', { class: 'endrow' }, b);
    }
    const unsent = courier?.state.pendingTaps ?? 0;
    const yes = h('button', { class: 'btn danger', type: 'button', 'data-act': 'end-yes' }, tr('endConfirmYes'));
    yes.addEventListener('click', () => void endShift());
    const no = h('button', { class: 'btn primary', type: 'button', 'data-act': 'end-no' }, tr('endConfirmNo'));
    no.addEventListener('click', () => {
      confirmingEnd = false;
      render();
    });
    return h(
      'section',
      { class: 'card confirm', role: 'alertdialog', 'aria-label': tr('endConfirmTitle'), 'data-role': 'end-confirm' },
      h('h2', {}, tr('endConfirmTitle')),
      h('p', {}, tr('endConfirmBody')),
      unsent > 0 && h('p', { class: 'unsent' }, tr('endConfirmUnsent', { n: unsent })),
      no,
      yes,
    );
  }

  function updateStatus(s: CourierState) {
    if (!statusEl) return;
    const gps: Record<CourierState['gps'], CopyKey> = {
      on: 'gpsOn',
      off: 'gpsOff',
      asking: 'gpsAskingShort',
      denied: 'gpsDenied',
    };
    const chip = (text: string, bad: boolean) => h('span', { class: bad ? 'chip bad' : 'chip' }, text);
    statusEl.replaceChildren(
      chip(tr(gps[s.gps]), s.gps === 'denied' || s.gps === 'off'),
      chip(tr(s.network === 'online' ? 'online' : 'offline'), s.network === 'offline'),
      ...(s.buffered > 0 ? [chip(tr('updatesWaiting', { n: s.buffered }), false)] : []),
      ...(s.pendingTaps > 0 ? [chip(tr('tapsUnsent', { n: s.pendingTaps }), true)] : []),
    );
  }

  // ---- actions -------------------------------------------------------------------------------------------

  function addNote(deliveryId: string, text: string) {
    notes.push({ deliveryId, text });
    if (screen.kind === 'deliveries') render();
  }

  async function tap(d: CourierDelivery, type: CourierEventType, reason?: string) {
    const c = courier;
    const key = `${d.id}:${type}`;
    if (!c || pending.has(key)) return;
    for (let i = notes.length - 1; i >= 0; i--) if (notes[i].deliveryId === d.id) notes.splice(i, 1);
    pending.add(key);
    failOpen.delete(d.id);
    render();
    try {
      const r = await c.report(d.id, type, reason ? { reason } : undefined);
      if (r.outcome === 'not_yours') addNote(d.id, tr('noLongerYours'));
      else if (r.outcome === 'conflict' || r.outcome === 'stale') {
        addNote(d.id, tr('alreadyUpdated', { status: statusName(r.delivery?.status ?? d.status) }));
      }
    } catch (e) {
      if (!sessionOnly(e)) addNote(d.id, tr('notSent', { action: actionName(type) }));
    } finally {
      pending.delete(key);
      if (screen.kind === 'deliveries') render();
    }
  }

  async function endShift() {
    const c = courier;
    if (!c) return;
    ending = true;
    show({ kind: 'starting' });
    try {
      await c.end();
    } finally {
      deps.wakeLock.release();
      stopListening();
      show({ kind: 'over', why: 'done' });
    }
  }

  function stopListening() {
    for (const off of unsubscribe) off();
    unsubscribe = [];
  }

  function listen(c: Courier) {
    unsubscribe = [
      c.on('deliveries', () => {
        if (screen.kind === 'deliveries') render();
      }),
      c.on('state', (s) => {
        if (screen.kind === 'deliveries' && (s.session === 'ended' || s.session === 'expired')) {
          if (!ending) over(s.session);
        } else if (screen.kind === 'deliveries') {
          // GPS permission changes swap the help panel; everything else only touches the status bar
          const wasDenied = !!root.querySelector('[data-role=denied]');
          const asking = !!root.querySelector('.notice.asking');
          if (wasDenied !== (s.gps === 'denied') || asking !== (s.gps === 'asking')) render();
          else updateStatus(s);
        }
      }),
      c.on('notice', (n) => {
        if (n.kind === 'ended') {
          if (!ending) over('ended');
        } else if (n.kind === 'reassigned') {
          addNote(n.deliveryId ?? '', tr('noLongerYours'));
        } else if (n.kind === 'rejected') {
          addNote(n.deliveryId ?? '', tr('notSent', { action: n.type ? actionName(n.type) : '' }));
        }
      }),
    ];
  }

  function over(why: EndWhy) {
    deps.wakeLock.release();
    stopListening();
    show({ kind: 'over', why });
  }

  /** Exchange the code (a new shift) or resume the stored session (no code), then show the deliveries. */
  async function begin(code?: string) {
    if (!courier) {
      courier = deps.makeCourier({ api: deps.api, linkCode: code });
      listen(courier);
    }
    const c = courier;
    show({ kind: 'starting' });
    try {
      await c.start();
    } catch (e) {
      const errCode = e instanceof CourierError ? e.code : '';
      if (errCode === 'link_code_used' || errCode === 'link_code_invalid' || errCode === 'no_session') {
        if (code) deps.replaceUrl('/');
        stopListening();
        courier = undefined;
        over(errCode === 'link_code_used' ? 'used' : errCode === 'link_code_invalid' ? 'invalid' : 'no_session');
      } else {
        // network, rate limit, server trouble: the same instance can try again
        show({ kind: 'retry' });
      }
      return;
    }
    // The code has been exchanged: it must not stay in the address bar, history or a shared screenshot.
    if (code) deps.replaceUrl('/');
    deps.wakeLock.hold();
    show({ kind: 'deliveries' });
  }

  // ---- entry ---------------------------------------------------------------------------------------------

  const m = /^\/s\/([A-Za-z0-9_-]+)\/?$/.exec(deps.path);
  if (m) {
    show({ kind: 'consent', code: m[1] });
  } else if (/^\/s(\/|$)/.test(deps.path)) {
    show({ kind: 'over', why: 'invalid' });
  } else {
    void begin();
  }
}
