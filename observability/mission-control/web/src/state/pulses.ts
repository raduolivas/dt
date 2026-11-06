import type { McEvent } from '../api/types';

export type PulseColor = 'ok' | 'warn' | 'fail';

export interface Pulse {
  id: string;
  wire: string; // "source>target", the same key the backend uses for bundles
  color: PulseColor;
  at: number; // ms since epoch
  simulated: boolean;
  /** Present only on a bundle pulse: how many observed events it stands for. */
  count?: number;
  eventId?: number;
}

/** More than this many events on one wire inside one window become a single bundle pulse. */
export const BUNDLE_THRESHOLD = 20;
export const WINDOW_MS = 1000;

/** One observed request or message is one pulse (spec 6.3). Logs, health and state changes are not traffic. */
const PULSE_KINDS = new Set(['request', 'message_published', 'message_consumed']);

const WARN = new Set(['slow', 'retried', 'dropped', 'rejected']);

export function colorOf(outcome: string): PulseColor {
  if (outcome === 'failed') return 'fail';
  return WARN.has(outcome) ? 'warn' : 'ok';
}

const RANK: Record<PulseColor, number> = { ok: 0, warn: 1, fail: 2 };

/**
 * Pulses for the events observed in the last second (WINDOW_MS) before now.
 * Pulses are never invented: only events with a source and a target produce
 * one. A wire with more than BUNDLE_THRESHOLD events in the window yields one
 * bundle pulse {count}, coloured by its worst outcome.
 */
export function toPulses(events: McEvent[], now: number): Pulse[] {
  const byWire = new Map<string, { e: McEvent; at: number }[]>();
  for (const e of events) {
    if (!PULSE_KINDS.has(e.kind) || !e.source || !e.target) continue;
    const at = Date.parse(e.occurredAt);
    if (!Number.isFinite(at) || at > now || at <= now - WINDOW_MS) continue;
    const wire = `${e.source}>${e.target}`;
    const list = byWire.get(wire);
    if (list) list.push({ e, at });
    else byWire.set(wire, [{ e, at }]);
  }
  const out: Pulse[] = [];
  for (const [wire, list] of byWire) {
    if (list.length > BUNDLE_THRESHOLD) {
      let worst: PulseColor = 'ok';
      let latest = 0;
      let sim = false;
      for (const { e, at } of list) {
        const c = colorOf(e.outcome);
        if (RANK[c] > RANK[worst]) worst = c;
        latest = Math.max(latest, at);
        sim ||= e.origin === 'simulated';
      }
      out.push({ id: `bundle:${wire}:${latest}`, wire, color: worst, at: latest, simulated: sim, count: list.length });
      continue;
    }
    for (const { e, at } of list) {
      out.push({ id: `e:${e.id}`, wire, color: colorOf(e.outcome), at, simulated: e.origin === 'simulated', eventId: e.id });
    }
  }
  return out.sort((a, b) => a.at - b.at);
}
