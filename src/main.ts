import './style.css';
import { DeliverySimulator } from './engine/simulator';
import type { DestinationPreset } from './engine/types';
import { CityScene } from './view/CityScene';
import { Hud, courierLabel } from './ui';

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const sim = new DeliverySimulator();
const scene = new CityScene($('scene'));
const hud = new Hud();

scene.setDestination(sim.params.destination);

// engine -> view + HUD. Swap `sim` for any other TrackingSource (WebSocket, polling...) and nothing else changes.
sim.subscribe((snap) => {
  scene.onSnapshot(snap);
  scene.setCourierLabel(courierLabel(snap));
  hud.update(snap, sim.isRunning);
});

const refresh = () => hud.update(sim.snapshot(), sim.isRunning);

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
followBtn.addEventListener('click', () => {
  scene.setFollow(!scene.isFollowing);
  followBtn.classList.toggle('on', scene.isFollowing);
});
$('btn-overview').addEventListener('click', () => {
  scene.overview();
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
    scene.setDestination(v);
    sim.reset();
    refresh();
  }),
);

// --- main loop ------------------------------------------------------------
let last = performance.now();
const loop = (now: number) => {
  const dt = (now - last) / 1000;
  last = now;
  sim.update(dt);
  scene.frame(dt);
  requestAnimationFrame(loop);
};
requestAnimationFrame(loop);
