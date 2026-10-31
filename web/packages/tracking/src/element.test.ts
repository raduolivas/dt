import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { defineTrackingElement, DtTrackingElement } from './element';
import { gpu, lastRenderer, lastViewer, live, renderers, resetFakes, snapshot, viewers } from './testing/fixtures';

vi.mock('@dt/renderer', async () => {
  const f = await import('./testing/fixtures');
  return { createRenderer: f.createRenderer };
});
vi.mock('@dt/viewer', async () => {
  const f = await import('./testing/fixtures');
  return { createViewer: f.createViewer };
});

beforeEach(() => {
  resetFakes();
  defineTrackingElement();
});
afterEach(() => {
  document.body.replaceChildren();
});

function make(attrs: Record<string, string>): DtTrackingElement {
  const el = document.createElement('dt-tracking') as DtTrackingElement;
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  document.body.appendChild(el);
  return el;
}

describe('<dt-tracking>', () => {
  it('is registered once', () => {
    defineTrackingElement();
    expect(customElements.get('dt-tracking')).toBe(DtTrackingElement);
  });

  it('mounts on connect with its attributes and cleans up on disconnect', () => {
    const el = make({ token: 'lt_a', mode: '2d', theme: 'dark', lang: 'nl' });
    expect(lastViewer().options.token).toBe('lt_a');
    expect(lastRenderer().options).toMatchObject({ mode: '2d', theme: 'dark' });
    expect(el.shadowRoot!.querySelector('.dt-root')).not.toBeNull();
    expect(el.shadowRoot!.querySelector('button.dt-mode')!.getAttribute('aria-label')).toBe('Toon de kaart in 3D'); // in 2D, it offers 3D
    el.remove();
    expect(gpu.contexts).toBe(0);
    expect(lastViewer().running).toBe(false);
    expect(el.shadowRoot!.childNodes).toHaveLength(0);
  });

  it('waits for a token', () => {
    const el = make({});
    expect(viewers).toHaveLength(0);
    el.setAttribute('token', 'lt_b');
    expect(lastViewer().options.token).toBe('lt_b');
  });

  it('observes mode and theme without remounting, and remounts on token or lang', () => {
    const el = make({ token: 'lt_a', theme: 'light' });
    el.setAttribute('mode', '2d');
    el.setAttribute('theme', 'dark');
    expect(renderers).toHaveLength(1);
    expect(lastRenderer().calls.setMode).toEqual(['2d']);
    expect(lastRenderer().calls.setTheme.at(-1)).toBe('dark');
    const first = lastViewer();
    el.setAttribute('token', 'lt_c');
    expect(first.running).toBe(false);
    expect(lastViewer().options.token).toBe('lt_c');
    expect(gpu.contexts).toBe(1);
    // the remount keeps the mode and theme
    expect(lastRenderer().options).toMatchObject({ mode: '2d', theme: 'dark' });
    el.setAttribute('lang', 'nl');
    expect(viewers).toHaveLength(3);
  });

  it('dispatches ready, statuschange, etachange, delivered and error as CustomEvents', () => {
    const el = make({ token: 'lt_a' });
    const seen: { type: string; detail: unknown }[] = [];
    for (const type of ['ready', 'statuschange', 'etachange', 'delivered', 'error'])
      el.addEventListener(type, (e) => seen.push({ type, detail: (e as CustomEvent).detail }));
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    lastViewer().push(live(snapshot({ version: 1 })));
    lastViewer().push(live(snapshot({ version: 2, status: 'delivered' })));
    lastViewer().fail({ code: 'resync_failed' });
    expect(seen).toEqual([
      { type: 'ready', detail: null },
      { type: 'statuschange', detail: { status: 'confirmed' } },
      { type: 'etachange', detail: { seconds: null, window: null } },
      { type: 'statuschange', detail: { status: 'delivered' } },
      { type: 'delivered', detail: null },
      { type: 'error', detail: { code: 'resync_failed' } },
    ]);
    vi.restoreAllMocks();
  });

  it('exposes setMode, setTheme and destroy', () => {
    const el = make({ token: 'lt_a' });
    el.setMode('2d');
    expect(lastRenderer().calls.setMode).toEqual(['2d']);
    el.setTheme('dark');
    expect(lastRenderer().calls.setTheme.at(-1)).toBe('dark');
    el.destroy();
    expect(gpu.contexts).toBe(0);
    expect(el.shadowRoot!.querySelector('canvas')).toBeNull();
  });
});
