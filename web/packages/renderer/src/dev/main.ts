import './style.css';
import { createRenderer, type TileSourceSpec } from '../index';
import { DeliverySimulator } from './simulator';
import { GRID_WORLD, amsterdamWorld, type DestinationPreset } from './world';
import { Hud, courierLabel } from './ui';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

// ?tiles=fixture (default: the committed Amsterdam PMTiles), ?tiles=openfreemap, ?tiles=grid (the procedural city),
// or ?pmtiles=<url> for any OpenMapTiles PMTiles archive covering the Amsterdam route.
const query = new URLSearchParams(location.search);
const tilesParam = query.get('tiles') ?? 'fixture';
const tiles: TileSourceSpec | undefined = query.get('pmtiles')
  ? { kind: 'pmtiles', url: query.get('pmtiles')! }
  : tilesParam === 'openfreemap'
    ? { kind: 'zxy', tilejson: 'https://tiles.openfreemap.org/planet' }
    : tilesParam === 'grid'
      ? undefined
      : { kind: 'pmtiles', url: new URL('amsterdam-z14.pmtiles', location.href).href };
$('attribution').hidden = !tiles;

const sim = new DeliverySimulator(tiles ? amsterdamWorld() : GRID_WORLD);
const sceneEl = $('scene');
const scene = createRenderer(sceneEl, {
  tiles,
  onTiles: (t) => {
    // Read by the screenshot / budget scripts.
    document.body.dataset.tiles = JSON.stringify({ ...t, ms: Math.round(t.ms) });
    console.info(`[dt] first tiles: ${t.tiles} tiles, ${t.bytes} bytes, ${Math.round(t.ms)} ms`);
  },
  onFrameStats: (f) => (document.body.dataset.frame = JSON.stringify({ fps: Math.round(f.fps), drawCalls: f.drawCalls })),
});
const hud = new Hud();
const courierTag = () => sceneEl.querySelector<HTMLElement>('.scene-label.courier');
const devState = () => ({ running: sim.isRunning, phase: sim.currentStatus, elapsedSec: sim.elapsedSec });

// engine -> view + HUD. Swap `sim` for any other snapshot source (WebSocket, polling...) and nothing else changes.
sim.subscribe((snap) => {
  scene.update(snap);
  const tag = courierTag();
  if (tag) tag.textContent = courierLabel(snap, sim.currentStatus);
  hud.update(snap, devState());
});

const refresh = () => hud.update(sim.snapshot(), devState());

// The cards cover part of the map: tell the renderer, so its framing keeps store, home and courier visible.
const hudInsets = () => {
  const W = innerWidth;
  const H = innerHeight;
  const i = { top: 0, right: 0, bottom: 0, left: 0 };
  for (const el of document.querySelectorAll<HTMLElement>('.topbar, .stats, .order, .toolbar, .stepper')) {
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    if (r.height > H * 0.35 && r.right > W - 24 && r.width < W * 0.5) i.right = Math.max(i.right, W - r.left);
    else if (r.top + r.height / 2 < H / 2) i.top = Math.max(i.top, r.bottom);
    else i.bottom = Math.max(i.bottom, H - r.top);
  }
  return i;
};
const syncInsets = () => scene.setInsets(hudInsets());
syncInsets();
addEventListener('resize', syncInsets);

// --- toolbar --------------------------------------------------------------
const runOrPause = () => {
  if (sim.currentStatus === 'delivered') {
    sim.reset();
    sim.start();
  } else if (sim.isRunning) {
    sim.pause();
  } else {
    sim.start();
  }
  refresh();
};
$('btn-run').addEventListener('click', runOrPause);
$('btn-reset').addEventListener('click', () => {
  sim.reset();
  refresh();
});
const followBtn = $('btn-follow');
let following = false; // the first view frames store, home and courier
followBtn.addEventListener('click', () => {
  following = !following;
  scene.setFollow(following);
  followBtn.classList.toggle('on', following);
});
$('btn-overview').addEventListener('click', () => {
  scene.overview();
  following = false;
  followBtn.classList.remove('on');
});
const params = $('params');
$('btn-params').addEventListener('click', () => (params.hidden = !params.hidden));
$('params-close').addEventListener('click', () => (params.hidden = true));
window.addEventListener('keydown', (e) => {
  if (e.code === 'Space' && !(e.target instanceof HTMLInputElement)) {
    e.preventDefault();
    runOrPause();
  }
});

// --- parameters -----------------------------------------------------------
const bindRange = (id: string, label: string, fmt: (v: number) => string, apply: (v: number) => void) => {
  const input = $<HTMLInputElement>(id);
  const out = $(label);
  const sync = () => {
    out.textContent = fmt(+input.value);
    apply(+input.value);
  };
  input.addEventListener('input', sync);
  sync();
};
bindRange('p-speed', 'v-speed', (v) => `${v} km/h`, (v) => sim.setParams({ courierSpeedKmh: v }));
bindRange('p-prep', 'v-prep', (v) => `${v} s`, (v) => sim.setParams({ prepTimeSec: v }));
bindRange('p-scale', 'v-scale', (v) => `${v}×`, (v) => sim.setParams({ timeScale: v }));
// ?speed=<n> overrides the simulation speed (scripts and screenshots), ?dest=near|medium|far picks the destination.
if (query.get('speed')) sim.setParams({ timeScale: Number(query.get('speed')) });
const destParam = query.get('dest') as DestinationPreset | null;
if (destParam && ['near', 'medium', 'far'].includes(destParam)) {
  sim.setParams({ destination: destParam });
  sim.reset();
}

const destButtons = Array.from($('p-dest').querySelectorAll<HTMLButtonElement>('button'));
if (destParam) destButtons.forEach((o) => o.classList.toggle('on', o.dataset.v === destParam));
destButtons.forEach((b) =>
  b.addEventListener('click', () => {
    const v = b.dataset.v as DestinationPreset;
    destButtons.forEach((o) => o.classList.toggle('on', o === b));
    sim.setParams({ destination: v });
    sim.reset();
    refresh();
  }),
);

const modeButtons = Array.from($('p-model').querySelectorAll<HTMLButtonElement>('button'));
modeButtons.forEach((b) =>
  b.addEventListener('click', () => {
    modeButtons.forEach((o) => o.classList.toggle('on', o === b));
    sim.setParams({ model: b.dataset.v as 'direct' | 'end_to_end' });
    sim.reset();
    refresh();
  }),
);
const signalBtn = $('btn-signal');
signalBtn.addEventListener('click', () => {
  const live = sim.courierSignal === 'live';
  sim.setSignal(live ? 'lost' : 'live');
  signalBtn.textContent = live ? 'Restore signal' : 'Drop signal';
  refresh();
});

// ?t=<seconds> starts the order and jumps that far into it (screenshots of a given moment); ?pause keeps it there.
if (query.get('t')) {
  sim.advance(Number(query.get('t')));
  if (query.has('pause')) sim.pause();
  refresh();
}
if (query.has('follow')) followBtn.click();

// --- simulator loop (the renderer runs its own frame loop) ------------------
let last = performance.now();
const loop = (now: number) => {
  const dt = (now - last) / 1000;
  last = now;
  sim.update(dt);
  requestAnimationFrame(loop);
};
requestAnimationFrame(loop);
