import type { Branding, DeliveryStatus, EtaWindow, TrackingSnapshot } from '@dt/contract';
import { createRenderer, type TileSourceSpec } from '@dt/renderer';
import { createViewer, type ViewerState } from '@dt/viewer';
import { ensureContrast, readableOn } from './branding';
import { Cards, summaryLine } from './cards';
import { pickLang, t, type Lang } from './i18n';
import { computeLayout, type Rect } from './layout';
import { CARD_BG, STYLES } from './styles';

export type Theme = 'light' | 'dark' | 'auto';
export type Mode = '3d' | '2d';

export interface MountOptions {
  /** The link token (the last path segment of the tracking link). Kept in memory only. */
  token: string;
  mode?: Mode;
  /** Unset: the partner branding's theme, else `auto` (follows `prefers-color-scheme`). */
  theme?: Theme;
  /** Unset: from the browser languages (any `nl*` → `nl`, else `en`). */
  lang?: Lang;
  /** API base URL with the version prefix; defaults to the one this build was made for. */
  api?: string;
  /** Map tiles; default as built. */
  tiles?: TileSourceSpec;
  onReady?(): void;
  onStatusChange?(s: DeliveryStatus): void;
  onEtaChange?(e: { seconds: number | null; window: EtaWindow }): void;
  onDelivered?(): void;
  onError?(e: { code: string }): void;
  /** @internal Test seam: the renderer's clock in milliseconds (the end-to-end look tests freeze it). */
  clock?: () => number;
}

export interface Tracking {
  setMode(m: Mode): void;
  setTheme(t: Theme): void;
  destroy(): void;
}

// Defaults baked in at build time per environment (VITE_DT_API, VITE_DT_TILES); without them, the local stack and
// the OpenFreeMap tiles used in development. Each is read as `import.meta.env.VITE_DT_*` (the cast is erased), so Vite
// replaces it with a literal and the minifier drops the unused fallback: a staging build carries no localhost URL
// (scripts/web-staging-build.sh checks that).
type BuildEnv = Record<string, string | undefined>;
export const DEFAULT_API = (import.meta.env as BuildEnv).VITE_DT_API || 'http://localhost:8080/v1';
const BUILD_TILES = (import.meta.env as BuildEnv).VITE_DT_TILES;
export const DEFAULT_TILES: TileSourceSpec = BUILD_TILES
  ? { kind: 'pmtiles', url: BUILD_TILES }
  : { kind: 'zxy', tilejson: 'https://tiles.openfreemap.org/planet' };

const CHEVRON =
  '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M6 15l6-6 6 6"/></svg>';

function browserLang(): Lang {
  const nav = globalThis.navigator;
  return pickLang(nav?.languages?.length ? nav.languages : nav?.language ? [nav.language] : []);
}

/** Mounts the tracking view in a Shadow DOM inside `el` (on a new child, so `el` can be mounted on again). */
export function mount(el: HTMLElement, o: MountOptions): Tracking {
  const host = document.createElement('div');
  // inline, so the partner page's CSS for `div` cannot resize or hide the host
  host.style.cssText = 'all: initial; display: block; position: relative; width: 100%; height: 100%;';
  el.appendChild(host);
  const t = createTracking(host.attachShadow({ mode: 'open' }), o);
  return {
    setMode: t.setMode,
    setTheme: t.setTheme,
    destroy() {
      t.destroy();
      host.remove();
    },
  };
}

/** @internal The tracking view in `root` (the `mount` host's or `<dt-tracking>`'s shadow root); `destroy` empties it. */
export function createTracking(root: ShadowRoot, o: MountOptions): Tracking {
  const lang = o.lang ?? browserLang();
  let mode: Mode = o.mode ?? '3d';
  let themePref: Theme | undefined = o.theme;
  let branding: Branding | null = null;
  let state: ViewerState = { kind: 'connecting' };
  let receivedAt = Date.now();
  let version = -1;
  let expanded = false;
  let alive = true;

  // ---- DOM ----
  const style = document.createElement('style');
  style.textContent = STYLES;
  const rootEl = document.createElement('div');
  rootEl.className = 'dt-root';
  rootEl.setAttribute('role', 'region');
  rootEl.setAttribute('aria-label', t('region', lang));
  rootEl.lang = lang;
  const mapEl = document.createElement('div');
  mapEl.className = 'dt-map';
  const modeBtn = document.createElement('button');
  modeBtn.type = 'button';
  modeBtn.className = 'dt-mode';
  const panel = document.createElement('section');
  panel.className = 'dt-panel';
  const handle = document.createElement('button');
  handle.type = 'button';
  handle.className = 'dt-handle';
  const grip = document.createElement('span');
  grip.className = 'dt-grip';
  const lineText = document.createElement('span');
  lineText.className = 'dt-line-text';
  const chevron = document.createElement('span');
  chevron.className = 'dt-chevron';
  chevron.setAttribute('aria-hidden', 'true');
  chevron.innerHTML = CHEVRON;
  handle.append(grip, lineText, chevron);
  const cards = new Cards();
  panel.append(handle, cards.el);
  // status changes for screen readers (the cards themselves are replaced as they change)
  const announcer = document.createElement('p');
  announcer.className = 'dt-sr';
  announcer.setAttribute('aria-live', 'polite');
  rootEl.append(mapEl, modeBtn, panel, announcer);
  root.append(style, rootEl);

  // ---- theme ----
  const scheme = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)') : null;
  const resolved = (): 'light' | 'dark' => {
    const want = themePref ?? branding?.theme ?? 'auto';
    return want === 'auto' ? (scheme?.matches ? 'dark' : 'light') : want;
  };
  let theme = resolved();
  const applyAccent = () => {
    const primary = branding?.primary_color;
    if (primary && /^#[0-9a-f]{6}$/i.test(primary)) {
      // text and accents on the cards keep AA contrast: a darker (or, at night, lighter) shade when needed
      const accent = ensureContrast(primary, CARD_BG[theme]);
      rootEl.style.setProperty('--accent', accent);
      rootEl.style.setProperty('--on-accent', readableOn(accent));
    } else {
      rootEl.style.removeProperty('--accent');
      rootEl.style.removeProperty('--on-accent');
    }
  };
  const applyTheme = () => {
    const next = resolved();
    rootEl.dataset.theme = next;
    if (next !== theme) {
      theme = next;
      renderer.setTheme(next);
    }
    applyAccent();
  };
  rootEl.dataset.theme = theme;
  const onScheme = () => applyTheme();
  scheme?.addEventListener('change', onScheme);

  // ---- renderer ----
  const renderer = createRenderer(mapEl, {
    mode,
    theme,
    tiles: o.tiles ?? DEFAULT_TILES,
    labels: { home: t('yourAddress', lang) },
    clock: o.clock,
  });
  let accentSent: string | null = null;

  const showMode = () => {
    const other = mode === '3d' ? '2d' : '3d';
    modeBtn.textContent = other.toUpperCase();
    modeBtn.setAttribute('aria-label', t(other === '2d' ? 'show2d' : 'show3d', lang));
    modeBtn.title = modeBtn.getAttribute('aria-label')!;
  };
  const setMode = (m: Mode) => {
    if (!alive || (m !== '3d' && m !== '2d')) return;
    mode = m;
    renderer.setMode(m);
    showMode();
  };
  const onModeClick = () => setMode(mode === '3d' ? '2d' : '3d');
  modeBtn.addEventListener('click', onModeClick);
  showMode();

  // ---- layout ----
  const box = (e: HTMLElement): Rect | null => {
    const r = e.getBoundingClientRect();
    const base = rootEl.getBoundingClientRect();
    return r.width > 0 && r.height > 0 ? { left: r.left - base.left, top: r.top - base.top, width: r.width, height: r.height } : null;
  };
  let lastInsets = '';
  const relayout = () => {
    if (!alive) return;
    const width = rootEl.clientWidth || 375;
    const height = rootEl.clientHeight || 667;
    const pre = computeLayout({ width, height, expanded });
    // the kind decides the CSS; the browser then lays the panel out and its box gives the exact insets
    if (rootEl.dataset.layout !== pre.kind) rootEl.dataset.layout = pre.kind;
    rootEl.style.setProperty('--side-w', `${pre.sideWidth}px`);
    const collapsible = pre.kind !== 'side' || width < 900;
    panel.toggleAttribute('data-collapsible', collapsible);
    const l = computeLayout({ width, height, expanded, panel: box(panel) });
    // whole pixels: sub-pixel reflows are not a change (the renderer also ignores small ones and reframes itself)
    const i = l.insets;
    const insets = { top: Math.round(i.top), right: Math.round(i.right), bottom: Math.round(i.bottom), left: Math.round(i.left) };
    const key = JSON.stringify(insets);
    if (key !== lastInsets) {
      lastInsets = key;
      renderer.setInsets(insets);
    }
  };
  const showExpanded = () => {
    panel.dataset.expanded = String(expanded);
    handle.setAttribute('aria-expanded', String(expanded));
    handle.setAttribute('aria-label', t(expanded ? 'hideDetails' : 'showDetails', lang));
  };
  const onHandle = () => {
    expanded = !expanded;
    showExpanded();
    relayout();
  };
  handle.addEventListener('click', onHandle);
  showExpanded();
  const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(() => relayout()) : null;
  observer?.observe(rootEl);
  observer?.observe(panel);

  // ---- state and events ----
  let ready = false;
  let lastStatus: DeliveryStatus | null = null;
  let lastEta: string | null = null;
  let delivered = false;
  let lastSummary = '';

  const render = () => {
    cards.update({ state, lang, now: receivedAt, branding });
    const line = summaryLine({ state, lang, now: receivedAt });
    lineText.textContent = line;
    if (line !== lastSummary) {
      lastSummary = line;
      announcer.textContent = line;
    }
    relayout();
  };

  const emitFor = (s: TrackingSnapshot) => {
    if (s.status !== lastStatus) {
      lastStatus = s.status;
      o.onStatusChange?.(s.status);
    }
    const eta = JSON.stringify([s.eta_seconds, s.eta_window]);
    if (eta !== lastEta) {
      lastEta = eta;
      o.onEtaChange?.({ seconds: s.eta_seconds, window: s.eta_window });
    }
    if (s.status === 'delivered' && !delivered) {
      delivered = true;
      o.onDelivered?.();
    }
  };

  const onState = (s: ViewerState) => {
    if (!alive) return;
    state = s;
    if (s.kind === 'live') {
      if (s.snapshot.version !== version) {
        version = s.snapshot.version;
        receivedAt = Date.now();
      }
      if (s.branding !== branding) {
        branding = s.branding;
        applyTheme();
      }
      const accent = branding?.primary_color ?? null;
      if (accent !== accentSent) {
        accentSent = accent;
        renderer.setAccent(accent);
      }
    }
    // the cards first: their size gives the insets the renderer frames this snapshot with
    render();
    if (s.kind === 'live') renderer.update(s.snapshot);
    if (!ready && (s.kind === 'live' || s.kind === 'inactive')) {
      ready = true;
      o.onReady?.();
    }
    if (s.kind === 'live') emitFor(s.snapshot);
  };

  const onError = (e: { code: string; detail?: string }) => {
    if (!alive) return;
    // details go to the console and the error event only; the customer sees calm cards
    console.warn(`dt-tracking: ${e.code}`, e.detail ?? '');
    o.onError?.({ code: e.code });
  };

  let viewer: ReturnType<typeof createViewer> | null = null;
  const offs: (() => void)[] = [];
  if (o.token) {
    viewer = createViewer({ api: o.api ?? DEFAULT_API, token: o.token });
    offs.push(viewer.onState(onState), viewer.onError(onError));
    viewer.start();
  } else {
    onState({ kind: 'inactive', last: null });
    o.onError?.({ code: 'missing_token' });
  }
  if (state.kind === 'connecting') render();

  return {
    setMode,
    setTheme(next) {
      if (!alive || !['light', 'dark', 'auto'].includes(next)) return;
      themePref = next;
      applyTheme();
    },
    destroy() {
      if (!alive) return;
      alive = false;
      viewer?.stop();
      offs.forEach((off) => off());
      scheme?.removeEventListener('change', onScheme);
      modeBtn.removeEventListener('click', onModeClick);
      handle.removeEventListener('click', onHandle);
      observer?.disconnect();
      renderer.destroy();
      root.replaceChildren();
    },
  };
}
