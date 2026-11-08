// Component, wire and pulse looks (spec 6.3). Pure: no three.js, so it is unit tested.
import type { ComponentWithState, McEvent, Wire } from '../api/types';
import { colorOf, type PulseColor } from '../state/pulses';
import { theme } from '../theme';

export type VisualState = 'active' | 'idle' | 'not_instrumented' | 'stale' | 'warning' | 'unhealthy' | 'not_built';
export const VISUAL_STATES: VisualState[] = ['active', 'idle', 'not_instrumented', 'stale', 'warning', 'unhealthy', 'not_built'];

/** Outline style: solid, dashed (not instrumented), dotted (not built), hatched (stale / no data). */
export type Dash = 'solid' | 'dashed' | 'dotted' | 'hatched';
export interface Look { color: string; dash: Dash; opacity: number }

/** Every state has its own {color, dash, opacity}; cyan (the accent) is reserved for "lit". */
export const STATE_LOOK: Record<VisualState, Look> = {
  active: { color: theme.accent, dash: 'solid', opacity: 1 }, // lit, its LED breathes
  idle: { color: '#5b8f9c', dash: 'solid', opacity: 0.9 }, // lit, still
  not_instrumented: { color: '#8593a3', dash: 'dashed', opacity: 0.75 },
  stale: { color: '#64707d', dash: 'hatched', opacity: 0.45 }, // faded, with an age label
  warning: { color: theme.warn, dash: 'solid', opacity: 1 },
  unhealthy: { color: theme.fail, dash: 'solid', opacity: 1 },
  not_built: { color: '#3c4654', dash: 'dotted', opacity: 0.35 }, // "not built yet"
};

export const STATE_CAPTION: Record<VisualState, string> = {
  active: 'active',
  idle: 'idle',
  not_instrumented: 'not instrumented',
  stale: 'stale',
  warning: 'warning',
  unhealthy: 'unhealthy',
  not_built: 'not built yet',
};

/** Same windows as the backend (handlers.go activeWithin / idleWithin). */
export const ACTIVE_WITHIN_MS = 30_000;
export const IDLE_WITHIN_MS = 5 * 60_000;

/**
 * Services whose health Mission Control cannot probe (mTLS-only, ruling R22;
 * mirrors probedServices in backend/internal/api/handlers.go). Their state
 * comes from activity only and is never red; the label says "health not probed".
 */
export const HEALTH_NOT_PROBED = new Set(['deliveries', 'accounts']);

/** Kinds that are not activity (as the backend's Activity query). */
const NOT_ACTIVITY = new Set(['health', 'detector', 'mc_self']);

/** Whole-id prefix match: "tracking" owns "tracking.ingest", not "trackingx". */
export function owns(componentId: string, id: string): boolean {
  return id === componentId || id.startsWith(componentId + '.');
}

/** The state of a cube from the backend's state plus the newest activity (ms) the browser has seen. */
export function stateFrom(c: ComponentWithState, lastActivity: number | null, now: number): VisualState {
  if (c.status === 'not_built' || c.state === 'not_built') return 'not_built';
  const notProbed = HEALTH_NOT_PROBED.has(c.id);
  const last = lastSeenOf(c, lastActivity);
  // Dashed grey only while nothing is seen there: once a span names it (say a pgx rollout), it lights.
  if ((!c.instrumented || c.state === 'not_instrumented') && last === null) return 'not_instrumented';
  if (c.state === 'unreachable' && !notProbed) return 'unhealthy';
  if (c.state === 'warning') return 'warning';
  // R22: without a health probe, quiet is not stale: lit by activity, else idle (the caption says how quiet).
  if (last === null) return notProbed ? 'idle' : 'stale';
  const age = now - last;
  if (age <= ACTIVE_WITHIN_MS) return 'active';
  if (age <= IDLE_WITHIN_MS || notProbed) return 'idle';
  return 'stale';
}

export function lastSeenOf(c: ComponentWithState, lastActivity: number | null): number | null {
  const backend = c.lastSeen ? Date.parse(c.lastSeen) : NaN;
  const vals = [lastActivity ?? NaN, backend].filter(Number.isFinite);
  return vals.length ? Math.max(...vals) : null;
}

export function visualState(c: ComponentWithState, events: readonly McEvent[], now: number): VisualState {
  let last: number | null = null;
  for (const e of events) {
    if (NOT_ACTIVITY.has(e.kind)) continue;
    if (!owns(c.id, e.source) && !(e.target && owns(c.id, e.target))) continue;
    const t = Date.parse(e.observedAt);
    if (Number.isFinite(t) && (last === null || t > last)) last = t;
  }
  return stateFrom(c, last, now);
}

export function isActivity(e: McEvent): boolean {
  return !NOT_ACTIVITY.has(e.kind);
}

/** "12s", "4m", "3h": the age label for idle and stale cubes. */
export function age(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

/** The caption under a cube's name: its state, an age where it helps, and R22's "health not probed". */
export function caption(c: ComponentWithState, s: VisualState, lastActivity: number | null, now: number): string[] {
  const last = lastSeenOf(c, lastActivity);
  let line = STATE_CAPTION[s];
  if (s === 'stale') line += last === null ? ' · no data' : ` · ${age(now - last)} ago`;
  if (s === 'idle') line += last === null ? ' · no traffic seen' : ` · ${age(now - last)} ago`;
  const out = [line];
  if (HEALTH_NOT_PROBED.has(c.id)) out.push('health not probed');
  return out;
}

// ---- wires -------------------------------------------------------------------

/**
 * measured: at least one pulse on the wire came from a span (a traced hop);
 * inferred (dotted): its evidence is only NATS envelope events (joined by ids
 * and timing) or attributed arrivals (R30, R33); quiet: no traffic seen.
 */
export type WireLook = 'measured' | 'inferred' | 'quiet';
export interface Evidence { measured: number; inferred: number }

export const WIRE_LOOK: Record<WireLook, { color: string; dash: 'solid' | 'dotted'; opacity: number }> = {
  measured: { color: theme.accent, dash: 'solid', opacity: 0.75 },
  inferred: { color: theme.accent, dash: 'dotted', opacity: 0.6 },
  quiet: { color: '#2b3b4c', dash: 'solid', opacity: 0.55 },
};

export function wireStyle(wire: Pick<Wire, 'id'>, evidence: ReadonlyMap<string, Evidence>): WireLook {
  const ev = evidence.get(wire.id);
  if (!ev) return 'quiet';
  if (ev.measured > 0) return 'measured';
  return ev.inferred > 0 ? 'inferred' : 'quiet';
}

// ---- pulses ------------------------------------------------------------------

/** Traffic light: green ok; amber slow, retried, dropped or rejected; red failed; grey unknown (R32). */
export function pulseColor(outcome: string): PulseColor {
  return colorOf(outcome);
}

export const PULSE_HEX: Record<PulseColor, string> = { ok: theme.ok, warn: theme.warn, fail: theme.fail, unknown: theme.unknown };
