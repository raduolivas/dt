import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, type MountOptions } from './mount';
import {
  BRANDING,
  courier,
  fakeColorScheme,
  gpu,
  lastRenderer,
  lastViewer,
  live,
  resetFakes,
  snapshot,
  trackListeners,
  viewers,
} from './testing/fixtures';

vi.mock('@dt/renderer', async () => {
  const f = await import('./testing/fixtures');
  return { createRenderer: f.createRenderer };
});
vi.mock('@dt/viewer', async () => {
  const f = await import('./testing/fixtures');
  return { createViewer: f.createViewer };
});

let el: HTMLElement;
beforeEach(() => {
  resetFakes();
  el = document.createElement('div');
  document.body.appendChild(el);
});
afterEach(() => {
  el.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const shadow = () => el.firstElementChild!.shadowRoot!;
const q = <T extends Element = HTMLElement>(sel: string) => shadow().querySelector<T>(sel);
const opts = (o: Partial<MountOptions> = {}): MountOptions => ({ token: 'lt_secret_token', lang: 'en', ...o });

describe('mount', () => {
  it('renders inside a Shadow DOM with the map, the cards and the mode toggle', () => {
    const t = mount(el, opts());
    expect(el.children).toHaveLength(1);
    expect(shadow().querySelector('style')?.textContent).toContain('.dt-panel');
    expect(q('.dt-map')).toBe(lastRenderer().container);
    expect(q('[data-card="loading"]')).not.toBeNull();
    // the renderer owns the map credit: exactly one
    expect(shadow().querySelectorAll('.dt-attribution')).toHaveLength(1);
    t.destroy();
  });

  it('passes the token and the API to the viewer client, and starts it', () => {
    const t = mount(el, opts({ api: 'https://api.example.com/v1' }));
    expect(lastViewer().options).toMatchObject({ token: 'lt_secret_token', api: 'https://api.example.com/v1' });
    expect(lastViewer().running).toBe(true);
    t.destroy();
  });

  it('gives the renderer its labels in the page language', () => {
    const nl = mount(el, opts({ lang: 'nl' }));
    expect(lastRenderer().options.labels).toEqual({ home: 'Je adres' });
    nl.destroy();
    const en = mount(el, opts({ lang: 'en' }));
    expect(lastRenderer().options.labels).toEqual({ home: 'Your address' });
    en.destroy();
  });

  it('passes mode and tiles to the renderer', () => {
    const tiles = { kind: 'pmtiles' as const, url: 'https://cdn.example/tiles/nl.pmtiles' };
    const t = mount(el, opts({ mode: '2d', tiles }));
    expect(lastRenderer().options).toMatchObject({ mode: '2d', tiles });
    t.destroy();
  });

  it('passes a test clock to the renderer, and none by default', () => {
    const clock = () => 1234;
    const frozen = mount(el, opts({ clock }));
    expect(lastRenderer().options.clock).toBe(clock);
    frozen.destroy();
    const real = mount(el, opts());
    expect(lastRenderer().options.clock).toBeUndefined();
    real.destroy();
  });

  it('draws every live snapshot and gives the renderer the partner color', () => {
    const t = mount(el, opts());
    const s = snapshot();
    lastViewer().push(live(s, BRANDING));
    expect(lastRenderer().calls.update).toEqual([s]);
    expect(lastRenderer().calls.setAccent.at(-1)).toBe('#ffeb3b');
    t.destroy();
  });
});

describe('events', () => {
  it('fires each event once per change, and delivered once', () => {
    const on = {
      onReady: vi.fn(),
      onStatusChange: vi.fn(),
      onEtaChange: vi.fn(),
      onDelivered: vi.fn(),
      onError: vi.fn(),
    };
    const t = mount(el, opts(on));
    const v = lastViewer();
    const window = { min_seconds: 600, max_seconds: 1200 };
    v.push(live(snapshot({ version: 1, eta_state: 'estimated', eta_seconds: 900, eta_window: window })));
    v.push(live(snapshot({ version: 2, eta_state: 'estimated', eta_seconds: 900, eta_window: window })));
    v.push(live(snapshot({ version: 3, status: 'picked_up', courier: courier(), eta_state: 'live', eta_seconds: 600 })));
    v.push(live(snapshot({ version: 4, status: 'picked_up', courier: courier(), eta_state: 'live', eta_seconds: 600 })));
    v.push(live(snapshot({ version: 5, status: 'delivered' })));
    v.push(live(snapshot({ version: 6, status: 'delivered' })));
    v.push({ kind: 'inactive', last: snapshot({ version: 6, status: 'delivered' }) });

    expect(on.onReady).toHaveBeenCalledTimes(1);
    expect(on.onStatusChange.mock.calls.map((c) => c[0])).toEqual(['confirmed', 'picked_up', 'delivered']);
    expect(on.onEtaChange.mock.calls.map((c) => c[0])).toEqual([
      { seconds: 900, window },
      { seconds: 600, window: null },
      { seconds: null, window: null },
    ]);
    expect(on.onDelivered).toHaveBeenCalledTimes(1);
    expect(on.onError).not.toHaveBeenCalled();
    t.destroy();
  });

  it('fires statuschange and delivered when the first final snapshot arrives only as inactive.last', () => {
    const on = { onStatusChange: vi.fn(), onDelivered: vi.fn(), onEtaChange: vi.fn() };
    const t = mount(el, opts(on));
    const v = lastViewer();
    v.push(live(snapshot({ version: 3, status: 'picked_up', courier: courier() })));
    v.push({ kind: 'inactive', last: snapshot({ version: 4, status: 'delivered' }) });
    v.push({ kind: 'inactive', last: snapshot({ version: 4, status: 'delivered' }) });
    expect(on.onStatusChange.mock.calls.map((c) => c[0])).toEqual(['picked_up', 'delivered']);
    expect(on.onDelivered).toHaveBeenCalledTimes(1);
    t.destroy();
  });

  it('fires statuschange for a final inactive.last seen first, and no delivered for a cancelled delivery', () => {
    const on = { onStatusChange: vi.fn(), onDelivered: vi.fn() };
    const t = mount(el, opts(on));
    lastViewer().push({ kind: 'inactive', last: snapshot({ version: 2, status: 'cancelled' }) });
    expect(on.onStatusChange.mock.calls.map((c) => c[0])).toEqual(['cancelled']);
    expect(on.onDelivered).not.toHaveBeenCalled();
    t.destroy();
  });

  it('does not fire statuschange for a non-final inactive.last', () => {
    const onStatusChange = vi.fn();
    const t = mount(el, opts({ onStatusChange }));
    const v = lastViewer();
    v.push(live(snapshot({ version: 3, status: 'picked_up', courier: courier() })));
    v.push({ kind: 'inactive', last: snapshot({ version: 3, status: 'picked_up', courier: courier() }) });
    expect(onStatusChange.mock.calls.map((c) => c[0])).toEqual(['picked_up']);
    t.destroy();
  });

  it('fires ready for a link that is inactive from the start', () => {
    const onReady = vi.fn();
    const t = mount(el, opts({ onReady }));
    lastViewer().push({ kind: 'inactive', last: null });
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(q('[data-card="inactive"]')).not.toBeNull();
    t.destroy();
  });

  it('passes viewer errors on with their code only, never the token', () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const onError = vi.fn();
    const t = mount(el, opts({ onError }));
    lastViewer().fail({ code: 'session_failed', detail: 'status 500' });
    expect(onError).toHaveBeenCalledWith({ code: 'session_failed' });
    expect(JSON.stringify(onError.mock.calls)).not.toContain('lt_secret_token');
    expect(JSON.stringify(vi.mocked(console.warn).mock.calls)).not.toContain('lt_secret_token');
    t.destroy();
  });

  it('shows the inactive card and reports missing_token without a token', () => {
    const onError = vi.fn();
    const t = mount(el, opts({ token: '', onError }));
    expect(onError).toHaveBeenCalledWith({ code: 'missing_token' });
    expect(viewers).toHaveLength(0);
    expect(q('[data-card="inactive"]')).not.toBeNull();
    t.destroy();
  });
});

describe('map after the link goes inactive', () => {
  it('hides the courier when the last snapshot is not final', () => {
    const t = mount(el, opts());
    const v = lastViewer();
    v.push(live(snapshot({ version: 3, status: 'picked_up', courier: courier() })));
    v.push({ kind: 'inactive', last: snapshot({ version: 3, status: 'picked_up', courier: courier() }) });
    const shown = lastRenderer().calls.update.at(-1)!;
    expect(lastRenderer().calls.update).toHaveLength(2);
    expect(shown.courier).toBeNull();
    expect(shown.status).toBe('picked_up');
    expect(shown.version).toBeGreaterThan(3); // past the renderer's version gate
    expect(q('[data-card="inactive"]')).not.toBeNull();
    t.destroy();
  });

  it('draws a final last snapshot that the map has not seen', () => {
    const t = mount(el, opts());
    const v = lastViewer();
    v.push(live(snapshot({ version: 3, status: 'picked_up', courier: courier() })));
    v.push({ kind: 'inactive', last: snapshot({ version: 4, status: 'delivered' }) });
    expect(lastRenderer().calls.update.map((s) => [s.version, s.status])).toEqual([
      [3, 'picked_up'],
      [4, 'delivered'],
    ]);
    t.destroy();
  });

  it('leaves the map alone when there is no last snapshot', () => {
    const t = mount(el, opts());
    lastViewer().push({ kind: 'inactive', last: null });
    expect(lastRenderer().calls.update).toHaveLength(0);
    t.destroy();
  });
});

describe('mode toggle', () => {
  it('is a real, focusable button that switches between 3D and 2D', () => {
    const onMode = vi.fn();
    const t = mount(el, opts());
    const b = q<HTMLButtonElement>('button.dt-mode')!;
    expect(b.tagName).toBe('BUTTON');
    expect(b.getAttribute('type')).toBe('button');
    expect(b.tabIndex).toBe(0);
    expect(b.textContent).toBe('2D');
    expect(b.getAttribute('aria-label')).toBe('Show the map in 2D');
    b.addEventListener('click', onMode);
    b.click();
    expect(lastRenderer().calls.setMode).toEqual(['2d']);
    expect(b.textContent).toBe('3D');
    t.setMode('3d');
    expect(lastRenderer().calls.setMode).toEqual(['2d', '3d']);
    expect(b.textContent).toBe('2D');
    t.destroy();
  });
});

describe('theme', () => {
  it("follows prefers-color-scheme live with 'auto'", () => {
    const scheme = fakeColorScheme(false);
    vi.stubGlobal('matchMedia', scheme.matchMedia);
    const t = mount(el, opts({ theme: 'auto' }));
    expect(lastRenderer().options.theme).toBe('light');
    expect(q('.dt-root')!.dataset.theme).toBe('light');
    scheme.set(true);
    expect(lastRenderer().calls.setTheme.at(-1)).toBe('dark');
    expect(q('.dt-root')!.dataset.theme).toBe('dark');
    t.setTheme('light');
    scheme.set(true);
    expect(q('.dt-root')!.dataset.theme).toBe('light');
    t.destroy();
  });

  it('uses the branding theme unless the option sets one', () => {
    vi.stubGlobal('matchMedia', fakeColorScheme(false).matchMedia);
    const t = mount(el, opts());
    lastViewer().push(live(snapshot(), { ...BRANDING, theme: 'dark' }));
    expect(q('.dt-root')!.dataset.theme).toBe('dark');
    t.destroy();
    const forced = mount(el, opts({ theme: 'light' }));
    lastViewer().push(live(snapshot(), { ...BRANDING, theme: 'dark' }));
    expect(el.lastElementChild!.shadowRoot!.querySelector<HTMLElement>('.dt-root')!.dataset.theme).toBe('light');
    forced.destroy();
  });

  it('keeps AA contrast for the partner color on the cards', () => {
    vi.stubGlobal('matchMedia', fakeColorScheme(false).matchMedia);
    const t = mount(el, opts({ theme: 'light' }));
    lastViewer().push(live(snapshot(), BRANDING));
    const accent = q('.dt-root')!.style.getPropertyValue('--accent');
    expect(accent).toMatch(/^#[0-9a-f]{6}$/);
    expect(accent).not.toBe('#ffeb3b');
    t.destroy();
  });
});

describe('layout', () => {
  it('gives the renderer the insets of the cards', () => {
    const t = mount(el, opts());
    expect(lastRenderer().calls.setInsets.length).toBeGreaterThan(0);
    t.destroy();
  });

  it('sets the insets for the cards of a snapshot before drawing it', () => {
    const t = mount(el, opts());
    const r = lastRenderer();
    r.log.length = 0;
    const box = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect');
    // the panel grows once the snapshot's cards are in
    box.mockImplementation(function (this: HTMLElement) {
      const top = this.classList.contains('dt-panel') ? 400 : 0;
      return { left: 0, top, width: 375, height: this.classList.contains('dt-panel') ? 267 : 667, right: 375, bottom: 667, x: 0, y: top, toJSON() {} } as DOMRect;
    });
    lastViewer().push(live(snapshot()));
    expect(r.log.indexOf('setInsets')).toBeGreaterThanOrEqual(0);
    expect(r.log.indexOf('setInsets')).toBeLessThan(r.log.indexOf('update'));
    t.destroy();
  });

  it('leaves reframing to the renderer when the cards change height (no overview)', () => {
    const t = mount(el, opts());
    const r = lastRenderer();
    let top = 470;
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const panel = this.classList.contains('dt-panel');
      const y = panel ? top : 0;
      return { left: 0, top: y, width: 375, height: panel ? 667 - 12 - y : 667, right: 375, bottom: 667, x: 0, y, toJSON() {} } as DOMRect;
    });
    lastViewer().push(live(snapshot({ version: 1 })));
    const sets = r.calls.setInsets.length;
    top = 395.4; // the courier card appears: the sheet grows
    lastViewer().push(live(snapshot({ version: 2, status: 'courier_assigned', courier: courier() })));
    q<HTMLButtonElement>('button.dt-handle')!.click(); // and the customer opens it
    expect(r.calls.setInsets.length).toBeGreaterThan(sets);
    expect(r.calls.setInsets.at(-1)!.bottom).toBe(Math.round(667 - 395.4));
    expect(r.calls.overview).toBe(0);
    t.destroy();
  });

  it('collapses and expands the bottom sheet with its handle', () => {
    const t = mount(el, opts());
    const handle = q<HTMLButtonElement>('button.dt-handle')!;
    expect(handle.getAttribute('aria-expanded')).toBe('false');
    handle.click();
    expect(handle.getAttribute('aria-expanded')).toBe('true');
    expect(q('.dt-panel')!.dataset.expanded).toBe('true');
    t.destroy();
  });
});

describe('destroy', () => {
  it('mount then destroy 50 times leaves no listeners, no worker and no WebGL context', () => {
    const tracked = trackListeners();
    try {
      vi.stubGlobal('matchMedia', fakeColorScheme(false).matchMedia);
      const before = tracked.listeners();
      for (let i = 0; i < 50; i++) {
        const t = mount(el, opts({ theme: 'auto' }));
        lastViewer().push(live(snapshot({ version: 1 }), BRANDING));
        q<HTMLButtonElement>('button.dt-mode')!.click();
        t.destroy();
        t.destroy(); // twice is harmless
      }
      expect(gpu.contexts).toBe(0);
      expect(gpu.workers).toBe(0);
      expect(viewers.every((v) => !v.running && v.listeners() === 0)).toBe(true);
      expect(tracked.listeners()).toBe(before);
      expect(tracked.observers()).toBe(0);
      expect(el.children).toHaveLength(0);
    } finally {
      tracked.restore();
    }
  });

  it('ignores the viewer after destroy', () => {
    const onStatusChange = vi.fn();
    const t = mount(el, opts({ onStatusChange }));
    const v = lastViewer();
    t.destroy();
    v.push(live(snapshot()));
    expect(onStatusChange).not.toHaveBeenCalled();
  });
});
