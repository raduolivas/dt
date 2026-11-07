// The main scene's model: top-level cubes and the wires between them, projected
// from the topology's working-part wires (Task 12 expands the parts). Pure.
import type { ComponentWithState, McEvent, TopologyResponse, Wire } from '../api/types';
import { isActivity, type Evidence } from './visualState';

/** One curve per cube pair; traffic in both directions shares it. id = "a>b" in the first topology direction. */
export interface SceneWire { id: string; a: string; b: string; channels: Wire['channel'][] }

/**
 * Where an event travels in the scene. forward = from a to b. attributed = the
 * event was observed only where it arrived (an entry part), and drawn on that
 * part's single outside wire: the caller side is not measured.
 */
export interface Placement { wire: string; forward: boolean; attributed: boolean }

export interface SceneModel {
  cubes: ComponentWithState[];
  wires: SceneWire[];
  /** The top-level cube an id belongs to, or null for ids the topology does not know. */
  rootOf(id: string): string | null;
  /**
   * The scene wire between source and target, or null (same cube, no such wire,
   * unknown id: nothing is drawn, nothing invented). With attribute=true, an event
   * that happened at one part (source === target) is drawn on that part's only
   * wire to the outside world, when it has exactly one.
   */
  place(source: string, target: string, attribute?: boolean): Placement | null;
}

const PULSE_KINDS = new Set(['request', 'message_published', 'message_consumed']);

export function buildModel(t: TopologyResponse): SceneModel {
  const byId = new Map(t.components.map((c) => [c.id, c]));
  const roots = new Map<string, string | null>();
  const rootOf = (id: string): string | null => {
    if (!id) return null;
    const hit = roots.get(id);
    if (hit !== undefined) return hit;
    let c = byId.get(id);
    for (let i = 0; c && c.parent && i < 16; i++) c = byId.get(c.parent);
    const r = c && !c.parent ? c.id : null;
    roots.set(id, r);
    return r;
  };

  const cubes = t.components.filter((c) => !c.parent);
  const wires: SceneWire[] = [];
  const pair = new Map<string, SceneWire>(); // "a>b" and "b>a" -> wire
  for (const w of t.wires) {
    const a = rootOf(w.from);
    const b = rootOf(w.to);
    if (!a || !b || a === b) continue;
    let sw = pair.get(`${a}>${b}`);
    if (!sw) {
      sw = { id: `${a}>${b}`, a, b, channels: [] };
      wires.push(sw);
      pair.set(`${a}>${b}`, sw);
      pair.set(`${b}>${a}`, sw);
    }
    if (!sw.channels.includes(w.channel)) sw.channels.push(w.channel);
  }

  // Entry parts: a part with exactly one wire to an outside-zone cube.
  const entry = new Map<string, Placement>();
  const outsideWires = new Map<string, Wire[]>();
  for (const w of t.wires) {
    for (const [part, other] of [[w.to, w.from], [w.from, w.to]] as const) {
      if (byId.get(other)?.zone !== 'outside' || byId.get(part)?.zone === 'outside') continue;
      const l = outsideWires.get(part) ?? [];
      l.push(w);
      outsideWires.set(part, l);
    }
  }
  for (const [part, ws] of outsideWires) {
    if (ws.length !== 1) continue;
    const w = ws[0];
    const outside = byId.get(w.from)?.zone === 'outside' ? w.from : w.to;
    const sw = pair.get(`${rootOf(outside)}>${rootOf(part)}`);
    if (!sw) continue;
    // A request arrives from outside; WS/SSE is the hub pushing to the outside party.
    const toward = w.channel === 'ws_sse' ? rootOf(outside) : rootOf(part);
    entry.set(part, { wire: sw.id, forward: sw.b === toward, attributed: true });
  }

  return {
    cubes,
    wires,
    rootOf,
    place(source, target, attribute = false) {
      if (attribute && source && source === target) return entry.get(source) ?? null;
      const a = rootOf(source);
      const b = rootOf(target);
      if (!a || !b || a === b) return null;
      const sw = pair.get(`${a}>${b}`);
      return sw ? { wire: sw.id, forward: sw.a === a, attributed: false } : null;
    },
  };
}

export interface Derived {
  /** cube id -> newest observedAt (ms) of an event touching it */
  activity: Map<string, number>;
  /** scene wire id -> how its traffic was linked */
  evidence: Map<string, Evidence>;
  /** cube and scene wire ids the events touched (for dimming with an order searched) */
  touched: Set<string>;
}

/** One pass over the buffer: activity per cube, evidence per wire, touched parts. */
export function derive(m: SceneModel, events: readonly McEvent[]): Derived {
  const activity = new Map<string, number>();
  const evidence = new Map<string, Evidence>();
  const touched = new Set<string>();
  for (const e of events) {
    if (!isActivity(e)) continue;
    const t = Date.parse(e.observedAt);
    for (const id of [e.source, e.target]) {
      const r = m.rootOf(id);
      if (!r) continue;
      touched.add(r);
      if (Number.isFinite(t) && t > (activity.get(r) ?? -Infinity)) activity.set(r, t);
    }
    if (!PULSE_KINDS.has(e.kind) || !e.source || !e.target) continue;
    const p = m.place(e.source, e.target, true);
    if (!p) continue;
    touched.add(p.wire);
    if (p.attributed) {
      // the outside party at the other end of an attributed arrival acted too
      const sw = m.wires.find((w) => w.id === p.wire);
      for (const r of sw ? [sw.a, sw.b] : []) {
        touched.add(r);
        if (Number.isFinite(t) && t > (activity.get(r) ?? -Infinity)) activity.set(r, t);
      }
    }
    const ev = evidence.get(p.wire) ?? { measured: 0, inferred: 0 };
    if (p.attributed || !e.traceId) ev.inferred++;
    else ev.measured++;
    evidence.set(p.wire, ev);
  }
  return { activity, evidence, touched };
}
