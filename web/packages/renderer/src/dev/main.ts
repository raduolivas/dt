import './style.css';
import { createRenderer } from '../index';
import { DeliverySimulator } from './simulator';
import type { DestinationPreset } from './world';
import { Hud, courierLabel } from './ui';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const sim = new DeliverySimulator();
const sceneEl = $('scene');
const scene = createRenderer(sceneEl, {});
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
let following = true;
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

const destButtons = Array.from($('p-dest').querySelectorAll<HTMLButtonElement>('button'));
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

// --- simulator loop (the renderer runs its own frame loop) ------------------
let last = performance.now();
const loop = (now: number) => {
  const dt = (now - last) / 1000;
  last = now;
  sim.update(dt);
  requestAnimationFrame(loop);
};
requestAnimationFrame(loop);
