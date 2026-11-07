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
   * wire to the outside world, when it has exactly one: ungated, so callers pass
   * only events placeEvent accepted.
   */
  place(source: string, target: string, attribute?: boolean): Placement | null;
  /** place() for one event, with the R30 / R33 gates on attribution. */
  placeEvent(e: McEvent): Placement | null;
}

const PULSE_KINDS = new Set(['request', 'message_published', 'message_consumed']);

/** A span-derived event: FromSpan always records span_name and a trace id; NATS envelope events have neither. */
export function fromSpan(e: McEvent): boolean {
  return !!e.traceId && typeof e.attrs?.span_name === 'string';
}

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
  const entry = new Map<string, Placement & { push: boolean }>();
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
    entry.set(part, { wire: sw.id, forward: sw.b === toward, attributed: true, push: w.channel === 'ws_sse' });
  }
  const strip = (p: Placement & { push: boolean }): Placement => ({ wire: p.wire, forward: p.forward, attributed: true });

  const place = (source: string, target: string, attribute = false): Placement | null => {
    if (attribute && source && source === target) {
      const p = entry.get(source);
      return p ? strip(p) : null;
    }
    const a = rootOf(source);
    const b = rootOf(target);
    if (!a || !b || a === b) return null;
    const sw = pair.get(`${a}>${b}`);
    return sw ? { wire: sw.id, forward: sw.a === a, attributed: false } : null;
  };

  /**
   * Gated attribution of an event that happened at one part:
   * R30: a request observed only as a gateway API server span goes on that part's wire from the outside actor.
   * R33: a tracking.viewer_fanout span with tracking.viewers > 0 goes out to the viewer; any other hub event
   * (a fanout to nobody, a subscribe request) draws nothing.
   */
  const placeEvent = (e: McEvent): Placement | null => {
    if (!e.source || e.source !== e.target) return place(e.source, e.target);
    const p = entry.get(e.source);
    if (!p || e.kind !== 'request') return null;
    if (p.push) {
      const viewers = Number(e.attrs?.['tracking.viewers']);
      if (e.attrs?.span_name !== 'tracking.viewer_fanout' || !(viewers > 0)) return null;
    }
    return strip(p);
  };

  return {
    cubes,
    wires,
    rootOf,
    place,
    placeEvent,
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
    const p = m.placeEvent(e);
    if (!p) continue;
    touched.add(p.wire);
    if (p.attributed) {
      // the outside party took part in the order, but it was not observed: touched, not active
      const sw = m.wires.find((w) => w.id === p.wire);
      if (sw) touched.add(sw.a).add(sw.b);
    }
    // measured = a traced hop (the event is a span); envelope events and attributions are inferred
    const ev = evidence.get(p.wire) ?? { measured: 0, inferred: 0 };
    if (!p.attributed && fromSpan(e)) ev.measured++;
    else ev.inferred++;
    evidence.set(p.wire, ev);
  }
  return { activity, evidence, touched };
}
