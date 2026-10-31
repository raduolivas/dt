import { describe, expect, it } from 'vitest';
import type { ViewerState } from '@dt/viewer';
import { buildCards, Cards, summaryLine } from './cards';
import type { Lang } from './i18n';
import { BRANDING, courier, live, NOW, snapshot } from './testing/fixtures';

const render = (state: ViewerState, lang: Lang = 'en') => buildCards({ state, lang, now: NOW });
const card = (root: HTMLElement, name: string) => root.querySelector<HTMLElement>(`[data-card="${name}"]`);
const text = (el: Element | null) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('ETA card', () => {
  it('shows "Calculating delivery time…" while the ETA is calculating', () => {
    const root = render(live(snapshot({ eta_state: 'calculating' })));
    expect(text(card(root, 'eta'))).toContain('Calculating delivery time…');
  });

  it('shows the window as two local clock times before pickup', () => {
    const s = snapshot({ eta_state: 'estimated', eta_seconds: 400, eta_window: { min_seconds: 120, max_seconds: 680 } });
    const eta = text(card(render(live(s)), 'eta'));
    // 12:38:20 + 2 min = 12:40:20 → 12:40; + 11 min 20 s = 12:49:40 → 12:50
    expect(eta).toContain('12:40–12:50');
    expect(eta.match(/\d{2}:\d{2}/g)).toHaveLength(2);
  });

  it('shows minutes after pickup', () => {
    const s = snapshot({ status: 'picked_up', courier: courier(), eta_state: 'live', eta_seconds: 690 });
    expect(text(card(render(live(s)), 'eta'))).toContain('12 min');
  });

  it('adds "estimated" when the quality is estimated, and only then', () => {
    const base = { status: 'picked_up' as const, courier: courier(), eta_state: 'live' as const, eta_seconds: 600 };
    expect(text(card(render(live(snapshot({ ...base, eta_quality: 'estimated' }))), 'eta'))).toMatch(/\bestimated\b/);
    expect(text(card(render(live(snapshot({ ...base, eta_quality: 'osrm' }))), 'eta'))).not.toMatch(/estimated/);
  });
});

describe('courier card', () => {
  const withSignal = (signal: 'pending' | 'live' | 'stale' | 'lost') =>
    render(live(snapshot({ status: 'courier_assigned', courier: courier({ signal, position: signal === 'pending' ? null : { lat: 52.372, lng: 4.891 } }) })));

  it('shows the courier name and the vehicle', () => {
    const c = card(withSignal('live'), 'courier');
    expect(text(c)).toContain('Lucas M.');
    expect(text(c)).toContain('By bike');
    expect(c?.querySelector('svg')).not.toBeNull();
  });

  it('shows "Updating courier location…" and nothing else for stale and lost, the same for both', () => {
    const stale = card(withSignal('stale'), 'courier')!;
    const lost = card(withSignal('lost'), 'courier')!;
    expect(stale.outerHTML).toBe(lost.outerHTML);
    expect(text(stale.querySelector('[data-signal]'))).toBe('Updating courier location…');
    expect(text(stale)).toBe('Lucas M. Updating courier location…');
    expect(text(stale)).not.toMatch(/lost|stale|signal|error/i);
  });

  it('shows "Locating courier…" while the signal is pending', () => {
    expect(text(card(withSignal('pending'), 'courier')?.querySelector('[data-signal]') ?? null)).toBe('Locating courier…');
  });

  it('never shows the courier photo', () => {
    const root = withSignal('live');
    for (const img of root.querySelectorAll('img')) expect(img.getAttribute('src')).not.toBe(courier().photo_url);
    expect(root.outerHTML).not.toContain('partner.example');
  });

  it('has no courier card while the courier is not visible', () => {
    expect(card(render(live(snapshot({ status: 'courier_assigned', courier: null }))), 'courier')).toBeNull();
  });
});

describe('route card', () => {
  it('shows "Your address" for the dropoff even when the label is set', () => {
    const s = snapshot({ dropoff: { location: { lat: 52.37, lng: 4.88 }, label: 'Jan Jansen, 3rd floor' } });
    const root = render(live(s));
    expect(text(card(root, 'route'))).toContain('Food Hub');
    expect(text(card(root, 'route'))).toContain('Your address');
    expect(root.outerHTML).not.toContain('Jan Jansen');
    expect(text(card(render(live(s), 'nl'), 'route'))).toContain('Je adres');
  });
});

describe('timeline', () => {
  it('lists the timeline entries with their local times, the last one current', () => {
    const s = snapshot({
      status: 'picked_up',
      courier: courier(),
      timeline: [
        { status: 'confirmed', at: '2026-10-29T11:20:00Z' },
        { status: 'courier_assigned', at: '2026-10-29T11:22:00Z' },
        { status: 'picked_up', at: '2026-10-29T11:35:00Z' },
      ],
    });
    const items = [...card(render(live(s)), 'timeline')!.querySelectorAll('li')];
    expect(items.map((li) => text(li))).toEqual(['Order confirmed 12:20', 'Courier assigned 12:22', 'Order picked up 12:35']);
    expect(items[2]!.getAttribute('aria-current')).toBe('step');
    expect(items[0]!.getAttribute('aria-current')).toBeNull();
  });
});

describe('end states and loading', () => {
  it('shows the final summary on delivered, without ETA or courier', () => {
    const s = snapshot({
      status: 'delivered',
      courier: null,
      timeline: [
        { status: 'confirmed', at: '2026-10-29T11:20:00Z' },
        { status: 'delivered', at: '2026-10-29T11:52:00Z' },
      ],
    });
    const root = render(live(s));
    expect(text(card(root, 'final'))).toContain('Delivered');
    expect(text(card(root, 'final'))).toContain('12:52');
    expect(card(root, 'eta')).toBeNull();
    expect(card(root, 'courier')).toBeNull();
  });

  it('shows a calm card for cancelled and failed deliveries', () => {
    for (const status of ['cancelled', 'failed'] as const) {
      const root = render(live(snapshot({ status })));
      expect(card(root, 'final')).not.toBeNull();
      expect(text(root)).not.toMatch(/error|fail/i);
    }
  });

  it('shows the inactive card, or the final summary when a final snapshot was seen', () => {
    expect(text(card(render({ kind: 'inactive', last: null }), 'inactive'))).toBe('This tracking link is no longer active');
    expect(card(render({ kind: 'inactive', last: snapshot({ status: 'picked_up' }) }), 'inactive')).not.toBeNull();
    const done = render({ kind: 'inactive', last: snapshot({ status: 'delivered' }) });
    expect(card(done, 'final')).not.toBeNull();
    expect(card(done, 'inactive')).toBeNull();
  });

  it('shows "Getting your delivery ready…" while warming', () => {
    expect(text(card(render({ kind: 'warming' }), 'warming'))).toBe('Getting your delivery ready…');
  });

  it('shows a busy placeholder while connecting', () => {
    expect(card(render({ kind: 'connecting' }), 'loading')?.getAttribute('aria-busy')).toBe('true');
  });
});

describe('branding', () => {
  it('shows the partner logo and name when branding is set', () => {
    const b = card(render(live(snapshot(), BRANDING)), 'brand')!;
    expect(text(b)).toContain('Pizza Piazza');
    expect(b.querySelector('img')?.getAttribute('src')).toBe(BRANDING.logo_url);
    expect(b.querySelector('img')?.getAttribute('alt')).toBe('');
  });

  it('has no brand card without branding, and no logo without a logo url', () => {
    expect(card(render(live(snapshot())), 'brand')).toBeNull();
    const b = card(render(live(snapshot(), { ...BRANDING, logo_url: null })), 'brand')!;
    expect(b.querySelector('img')).toBeNull();
  });
});

describe('Dutch', () => {
  it('renders the Dutch copy', () => {
    expect(text(card(render(live(snapshot()), 'nl'), 'eta'))).toContain('Bezorgtijd berekenen…');
  });
});

describe('Cards', () => {
  it('replaces only the cards that changed', () => {
    const c = new Cards();
    const s = snapshot({ status: 'courier_assigned', courier: courier() });
    c.update({ state: live(s), lang: 'en', now: NOW });
    const route = card(c.el, 'route');
    c.update({ state: live({ ...s, version: 2, courier: courier({ signal: 'stale' }) }), lang: 'en', now: NOW });
    expect(card(c.el, 'route')).toBe(route);
    expect(text(card(c.el, 'courier'))).toContain('Updating courier location…');
  });

  it('gives a one-line summary for the collapsed landscape sheet', () => {
    const s = snapshot({ status: 'picked_up', courier: courier(), eta_state: 'live', eta_seconds: 600 });
    expect(summaryLine({ state: live(s), lang: 'en', now: NOW })).toBe('On the way · 10 min');
    expect(summaryLine({ state: { kind: 'warming' }, lang: 'en', now: NOW })).toBe('Getting your delivery ready…');
  });
});
