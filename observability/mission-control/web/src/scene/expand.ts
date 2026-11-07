// The expanded view of one component: its working parts (topology Parent) on their own platform, with the
// wires between them. Pure; the scene renders it with the same World as the main view.
import type { ComponentWithState, TopologyResponse } from '../api/types';
import type { Vec3 } from './camera';
import { CUBE_H, PLATFORM_H, type Layout, type Zone } from './layout';
import type { Placement, SceneModel, SceneWire } from './model';

export interface ExpandedView { model: SceneModel; layout: Layout; component: ComponentWithState }

/** Where the expanded platform sits in the world: far from the main platforms, so the camera tween travels to it. */
export const EXPAND_ORIGIN: Vec3 = [0, 0, -80];
const STEP = 4.6;
const COLS = 3;
const PAD = 2.4;

export function buildExpanded(t: TopologyResponse, id: string): ExpandedView | null {
  const component = t.components.find((c) => c.id === id);
  const parts = t.components.filter((c) => c.parent === id);
  if (!component || parts.length === 0) return null;
  const isPart = new Set(parts.map((p) => p.id));

  const wires: SceneWire[] = [];
  const pair = new Map<string, SceneWire>();
  for (const w of t.wires) {
    if (!isPart.has(w.from) || !isPart.has(w.to) || w.from === w.to) continue;
    let sw = pair.get(`${w.from}>${w.to}`);
    if (!sw) {
      sw = { id: `${w.from}>${w.to}`, a: w.from, b: w.to, channels: [] };
      wires.push(sw);
      pair.set(`${w.from}>${w.to}`, sw);
      pair.set(`${w.to}>${w.from}`, sw);
    }
    if (!sw.channels.includes(w.channel)) sw.channels.push(w.channel);
  }

  const place = (source: string, target: string): Placement | null => {
    if (!source || source === target) return null;
    const sw = pair.get(`${source}>${target}`);
    return sw ? { wire: sw.id, forward: sw.a === source, attributed: false } : null;
  };
  const model: SceneModel = {
    cubes: parts,
    wires,
    rootOf: (pid) => (isPart.has(pid) ? pid : null),
    place,
    placeEvent: (e) => place(e.source, e.target),
  };

  const cols = Math.min(COLS, parts.length);
  const rows = Math.ceil(parts.length / COLS);
  const w = cols * STEP + PAD;
  const d = rows * STEP + PAD;
  const cubes = new Map<string, Vec3>();
  parts.forEach((p, i) => {
    const c = i % COLS;
    const r = Math.floor(i / COLS);
    cubes.set(p.id, [EXPAND_ORIGIN[0] + (c - (cols - 1) / 2) * STEP, CUBE_H / 2, EXPAND_ORIGIN[2] + (r - (rows - 1) / 2) * STEP]);
  });
  const layout: Layout = {
    platforms: [{ zone: component.zone as Zone, center: [EXPAND_ORIGIN[0], -PLATFORM_H / 2, EXPAND_ORIGIN[2]], size: [w, d] }],
    cubes,
    bounds: {
      min: [EXPAND_ORIGIN[0] - w / 2, -PLATFORM_H, EXPAND_ORIGIN[2] - d / 2],
      max: [EXPAND_ORIGIN[0] + w / 2, CUBE_H + 2.5, EXPAND_ORIGIN[2] + d / 2],
    },
  };
  return { model, layout, component };
}
