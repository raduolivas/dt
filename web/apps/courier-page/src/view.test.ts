import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Mock } from 'vitest';
import { CourierError } from '@dt/courier';
import type { Courier, CourierDelivery, CourierNotice, CourierState, ReportResult } from '@dt/courier';
import { COPY, pickLang, t } from './i18n';
import { mountCourierPage } from './view';
import type { PageDeps } from './view';
import { createWakeLock } from './wakelock';

const flush = () => new Promise((r) => setTimeout(r, 0));

function delivery(over: Partial<CourierDelivery> = {}): CourierDelivery {
  return {
    id: 'd1',
    reference: 'ORD-1',
    model: 'direct',
    status: 'courier_assigned',
    ready_at: null,
    pickup: { lat: 52, lng: 4, label: 'Bakery Jansen' },
    dropoff: { lat: 52.1, lng: 4.1, label: 'Keizersgracht 1', notes: 'Ring twice' },
    items_summary: null,
    item_count: null,
    pickup_confirmations: [],
    next_actions: ['at_pickup', 'failed'],
    ...over,
  } as CourierDelivery;
}

interface Fake extends Courier {
  set(d: CourierDelivery[]): void;
  patch(s: Partial<CourierState>): void;
  notice(n: CourierNotice): void;
  start: Mock<Courier['start']>;
  end: Mock<Courier['end']>;
  report: Mock<Courier['report']>;
}

function fakeCourier(list: CourierDelivery[] = []): Fake {
  let deliveries = list;
  let state: CourierState = { gps: 'on', network: 'online', buffered: 0, pendingTaps: 0, session: 'active' };
  const ls = { deliveries: new Set<(d: readonly CourierDelivery[]) => void>(), state: new Set<(s: CourierState) => void>(), notice: new Set<(n: CourierNotice) => void>() };
  return {
    start: vi.fn(async () => undefined),
    end: vi.fn(async () => undefined),
    report: vi.fn(async (): Promise<ReportResult> => ({ outcome: 'applied' })),
    get deliveries() {
      return deliveries;
    },
    get state() {
      return state;
    },
    on(ev: 'deliveries' | 'state' | 'notice', fn: never) {
      (ls[ev] as Set<unknown>).add(fn);
      return () => void (ls[ev] as Set<unknown>).delete(fn);
    },
    set(d) {
      deliveries = d;
      ls.deliveries.forEach((f) => f(d));
    },
    patch(s) {
      state = { ...state, ...s };
      ls.state.forEach((f) => f(state));
    },
    notice(n) {
      ls.notice.forEach((f) => f(n));
    },
  } as Fake;
}

let root: HTMLElement;
let fake: Fake;
let replaceUrl: Mock<(path: string) => void>;
let wake: { supported: boolean; hold: Mock<() => void>; release: Mock<() => void> };

function mount(path: string, over: Partial<PageDeps> = {}) {
  mountCourierPage(root, {
    api: 'http://api.test/v1',
    path,
    lang: 'en',
    makeCourier: () => fake,
    replaceUrl,
    wakeLock: wake,
    ...over,
  });
}
const q = (sel: string) => root.querySelector<HTMLElement>(sel);
const text = () => root.textContent ?? '';

beforeEach(() => {
  document.body.innerHTML = '<div id="root"></div>';
  root = document.getElementById('root')!;
  fake = fakeCourier([delivery()]);
  replaceUrl = vi.fn<(path: string) => void>();
  wake = { supported: true, hold: vi.fn<() => void>(), release: vi.fn<() => void>() };
});

describe('courier page', () => {
  it('consent shows before any start()', () => {
    mount('/s/abc123');
    expect(q('[data-act=start]')).not.toBeNull();
    expect(text()).toContain(COPY.en.consentWhatBody);
    expect(fake.start).not.toHaveBeenCalled();
  });

  it('code removed from URL after exchange', async () => {
    mount('/s/abc123');
    q('[data-act=start]')!.click();
    expect(replaceUrl).not.toHaveBeenCalled(); // not before the exchange has happened
    await flush();
    expect(fake.start).toHaveBeenCalledTimes(1);
    expect(replaceUrl).toHaveBeenCalledWith('/');
    expect(q('[data-delivery=d1]')).not.toBeNull();
    expect(wake.hold).toHaveBeenCalled();
  });

  it('passes the link code to the SDK', () => {
    const makeCourier = vi.fn(() => fake);
    mount('/s/abc123', { makeCourier });
    q('[data-act=start]')!.click();
    expect(makeCourier).toHaveBeenCalledWith({ api: 'http://api.test/v1', linkCode: 'abc123' });
  });

  it('big button is first non-failed next action, failed is a small link', async () => {
    fake = fakeCourier([delivery({ status: 'at_pickup', next_actions: ['failed', 'pickup_confirmed', 'delivered'] })]);
    mount('/');
    await flush();
    const main = q('.btn.main')!;
    expect(main.getAttribute('data-act')).toBe('pickup_confirmed');
    expect(main.textContent).toBe('Picked up');
    expect(root.querySelectorAll('.btn.main')).toHaveLength(1);
    expect(q('[data-act=failed]')!.classList.contains('link')).toBe(true);
    main.click();
    expect(fake.report).toHaveBeenCalledWith('d1', 'pickup_confirmed', undefined);
  });

  it('skips an unknown next action instead of failing to render', async () => {
    fake = fakeCourier([
      delivery({ status: 'at_pickup', next_actions: ['teleport', 'pickup_confirmed', 'failed'] as unknown as CourierDelivery['next_actions'] }),
      delivery({ id: 'd2', next_actions: ['teleport'] as unknown as CourierDelivery['next_actions'] }),
    ]);
    mount('/');
    await flush();
    expect(q('[data-delivery=d1] .btn.main')!.getAttribute('data-act')).toBe('pickup_confirmed');
    expect(q('[data-delivery=d2]')).not.toBeNull();
    expect(q('[data-delivery=d2] .btn.main')).toBeNull();
    expect(q('[data-act=teleport]')).toBeNull();
  });

  it('store policy shows waiting text', async () => {
    fake = fakeCourier([delivery({ status: 'at_pickup', next_actions: ['failed'] })]);
    mount('/');
    await flush();
    expect(q('.btn.main')).toBeNull();
    expect(q('[data-role=waiting]')!.textContent).toBe('The store confirms pickup');
    fake.set([delivery({ status: 'at_pickup', next_actions: ['failed'], pickup_confirmations: [{ by: 'courier', at: '2026-10-31T10:00:00Z' }] })]);
    expect(q('[data-role=waiting]')!.textContent).toBe('Waiting for the store to confirm');
  });

  it('ended notice shows ask-for-new-link screen', async () => {
    mount('/');
    await flush();
    fake.notice({ kind: 'ended' });
    expect(text()).toContain('Ask your dispatcher for a new link');
    expect(q('[data-delivery]')).toBeNull();
    expect(wake.release).toHaveBeenCalled();
  });

  it('shows the used-link and no-session screens', async () => {
    fake.start.mockRejectedValueOnce(new CourierError('link_code_used'));
    mount('/s/abc');
    q('[data-act=start]')!.click();
    await flush();
    expect(text()).toContain(COPY.en.usedTitle);
    expect(replaceUrl).toHaveBeenCalledWith('/');

    document.body.innerHTML = '<div id="root"></div>';
    root = document.getElementById('root')!;
    fake = fakeCourier();
    fake.start.mockRejectedValueOnce(new CourierError('no_session'));
    mount('/');
    await flush();
    expect(text()).toContain(COPY.en.noSessionTitle);
  });

  it('a network failure on start offers a retry on the same courier', async () => {
    fake.start.mockRejectedValueOnce(new CourierError('network'));
    mount('/s/abc');
    q('[data-act=start]')!.click();
    await flush();
    expect(text()).toContain(COPY.en.retryTitle);
    expect(replaceUrl).not.toHaveBeenCalled();
    q('[data-act=retry]')!.click();
    await flush();
    expect(fake.start).toHaveBeenCalledTimes(2);
    expect(q('[data-delivery=d1]')).not.toBeNull();
  });

  it('shows a rejected tap on its card and a reassigned delivery as a banner', async () => {
    mount('/');
    await flush();
    fake.notice({ kind: 'rejected', deliveryId: 'd1', type: 'at_pickup' });
    expect(q('[data-delivery=d1] [data-role=note]')!.textContent).toContain("Not sent: I’m at the store. Check with your dispatcher.");
    fake.notice({ kind: 'reassigned', deliveryId: 'gone' });
    fake.set([]);
    expect(root.querySelectorAll('.banner')).toHaveLength(2); // the card is gone, so both notes become banners
    expect(text()).toContain('This delivery is no longer yours');
  });

  it('shows a tap as not sent yet until it is answered', async () => {
    let answer: (r: ReportResult) => void = () => undefined;
    fake.report.mockReturnValueOnce(new Promise<ReportResult>((r) => (answer = r)));
    mount('/');
    await flush();
    q('.btn.main')!.click();
    expect(q('[data-role=unsent]')).not.toBeNull();
    expect((q('.btn.main') as HTMLButtonElement).disabled).toBe(true);
    answer({ outcome: 'applied' });
    await flush();
    expect(q('[data-role=unsent]')).toBeNull();
  });

  it('shows the real status after a conflict', async () => {
    fake.report.mockResolvedValueOnce({ outcome: 'conflict', delivery: delivery({ status: 'picked_up' }) });
    mount('/');
    await flush();
    q('.btn.main')!.click();
    await flush();
    expect(q('[data-role=note]')!.textContent).toContain('Picked up, on your way');
  });

  it('sends a can’t-deliver reason, with free text for other', async () => {
    mount('/');
    await flush();
    q('[data-act=failed]')!.click();
    q('[data-reason=refused]')!.click();
    expect(fake.report).toHaveBeenLastCalledWith('d1', 'failed', { reason: 'refused' });
    await flush();

    q('[data-act=failed]')!.click();
    q('[data-reason=other]')!.click();
    const input = q('input.field') as HTMLInputElement;
    expect(input.maxLength).toBeLessThanOrEqual(200);
    input.value = 'Dog loose';
    input.dispatchEvent(new Event('input'));
    q('[data-act=send-other]')!.click();
    expect(fake.report).toHaveBeenLastCalledWith('d1', 'failed', { reason: 'other: Dog loose' });
  });

  it('ending the shift needs a confirm', async () => {
    mount('/');
    await flush();
    q('[data-act=end]')!.click();
    expect(fake.end).not.toHaveBeenCalled();
    q('[data-act=end-no]')!.click();
    expect(q('[data-act=end]')).not.toBeNull();
    q('[data-act=end]')!.click();
    q('[data-act=end-yes]')!.click();
    await flush();
    expect(fake.end).toHaveBeenCalledTimes(1);
    expect(text()).toContain(COPY.en.doneTitle);
  });

  it('status bar follows the state without rebuilding the cards', async () => {
    mount('/');
    await flush();
    const card = q('[data-delivery=d1]');
    fake.patch({ network: 'offline', buffered: 3, pendingTaps: 2 });
    const bar = q('[data-role=status]')!.textContent!;
    expect(bar).toContain('Offline');
    expect(bar).toContain('3 updates waiting');
    expect(bar).toContain('2 not sent yet');
    expect(q('[data-delivery=d1]')).toBe(card);
  });

  it('shows the location help when permission is denied', async () => {
    mount('/');
    await flush();
    fake.patch({ gps: 'denied' });
    expect(q('[data-role=denied]')).not.toBeNull();
  });

  it('nl copy for nl-NL', async () => {
    expect(pickLang(['nl-NL'])).toBe('nl');
    expect(pickLang(['en-US', 'de'])).toBe('en');
    mount('/s/abc', { lang: pickLang(['nl-NL']) });
    expect(q('[data-act=start]')!.textContent).toBe(t('startShift', 'nl'));
    expect(q('[data-act=start]')!.textContent).toBe('Start dienst');
  });

  it('en and nl have the same keys', () => {
    expect(Object.keys(COPY.nl).sort()).toEqual(Object.keys(COPY.en).sort());
  });
});

describe('wake lock', () => {
  function setup(supported = true) {
    const release = vi.fn(async () => undefined);
    const request = vi.fn(async () => ({ release, addEventListener: () => undefined }));
    let visibility: DocumentVisibilityState = 'visible';
    const doc = new EventTarget() as unknown as Document;
    Object.defineProperty(doc, 'visibilityState', { get: () => visibility });
    const lock = createWakeLock(supported ? { wakeLock: { request } } : {}, doc);
    return {
      lock,
      request,
      release,
      hide: () => {
        visibility = 'hidden';
        doc.dispatchEvent(new Event('visibilitychange'));
      },
      show: () => {
        visibility = 'visible';
        doc.dispatchEvent(new Event('visibilitychange'));
      },
    };
  }

  it('wake lock reacquired on visibilitychange', async () => {
    const w = setup();
    w.lock.hold();
    await flush();
    expect(w.request).toHaveBeenCalledTimes(1);
    // the browser drops the lock when the page is hidden: the sentinel's release event fires
    w.hide();
    w.show();
    await flush();
    expect(w.request).toHaveBeenCalledTimes(1); // still held until the browser says otherwise
  });

  it('takes the lock again after the browser released it', async () => {
    let onRelease: () => void = () => undefined;
    const request = vi.fn(async () => ({
      release: async () => undefined,
      addEventListener: (_: 'release', fn: () => void) => (onRelease = fn),
    }));
    let visibility: DocumentVisibilityState = 'visible';
    const doc = new EventTarget() as unknown as Document;
    Object.defineProperty(doc, 'visibilityState', { get: () => visibility });
    const lock = createWakeLock({ wakeLock: { request } }, doc);
    lock.hold();
    await flush();
    visibility = 'hidden';
    onRelease();
    doc.dispatchEvent(new Event('visibilitychange'));
    await flush();
    expect(request).toHaveBeenCalledTimes(1); // hidden: not asked
    visibility = 'visible';
    doc.dispatchEvent(new Event('visibilitychange'));
    await flush();
    expect(request).toHaveBeenCalledTimes(2);
  });

  it('is unsupported without the API, and the page says to keep the screen on', async () => {
    const w = setup(false);
    expect(w.lock.supported).toBe(false);
    w.lock.hold(); // no-op, no throw
    wake = { ...wake, supported: false };
    mount('/');
    await flush();
    expect(text()).toContain(COPY.en.keepScreenOn);
  });

  it('release lets go and stops reacquiring', async () => {
    const w = setup();
    w.lock.hold();
    await flush();
    w.lock.release();
    expect(w.release).toHaveBeenCalled();
    w.show();
    await flush();
    expect(w.request).toHaveBeenCalledTimes(1);
  });
});
