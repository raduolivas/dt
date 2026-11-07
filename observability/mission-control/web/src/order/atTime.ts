import { useMemo } from 'react';
import type { McEvent, StatusStep } from '../api/types';
import { useStore } from '../state/store';

const MAIN_END = 'delivered';
const ENDED = new Set(['delivered', 'cancelled', 'failed']);
const LOST = 'tracking.courier_signal_lost';
const RESTORED = 'tracking.courier_signal_restored';

const ms = (iso: string | null) => (iso ? Date.parse(iso) : NaN);
/** A stored finding names its rule in attrs.rule (the backend's LatestFinding); source is the fallback. */
const ruleOf = (f: McEvent) => (typeof f.attrs?.rule === 'string' && f.attrs.rule ? f.attrs.rule : f.source);
/** The identity the backend clears by: rule, hop, delivery and reason. */
const identOf = (f: McEvent) => `${ruleOf(f)}|${f.target}|${f.deliveryId}|${f.reason}`;
const eventType = (e: McEvent) => (typeof e.attrs?.event_type === 'string' ? e.attrs.event_type : '');

/**
 * Newest accepted fix at or before t, mirroring the server's acceptedFixSQL on
 * the events in the buffer: an ok tracking.apply state change whose trace has
 * an accepted-fixes tracking.ingest request and no dropped log for the delivery.
 */
function lastAcceptedFixAt(events: readonly McEvent[], t: number): number {
  const ingested = new Set<string>();
  const dropped = new Set<string>();
  for (const e of events) {
    if (!e.traceId) continue;
    if (e.kind === 'request' && e.attrs?.span_name === 'tracking.ingest' && Number(e.attrs['tracking.fixes.accepted']) > 0) ingested.add(e.traceId);
    if (e.kind === 'log' && e.outcome === 'dropped') dropped.add(`${e.traceId}|${e.deliveryId}`);
  }
  let best = -Infinity;
  for (const e of events) {
    if (e.kind !== 'state_change' || e.outcome !== 'ok' || e.attrs?.span_name !== 'tracking.apply' || !e.traceId) continue;
    if (!ingested.has(e.traceId) || dropped.has(`${e.traceId}|${e.deliveryId}`)) continue;
    const at = ms(e.occurredAt);
    if (at <= t && at > best) best = at;
  }
  return best;
}

/**
 * Status steps as known at the replay cursor t (R34). Steps carry the time
 * they were reached: one reached after t is pending again, and cancelled /
 * failed show only once reached. The warning is recomputed at t with the
 * server's rule (R14 amended): the newest reached main step is a warning only
 * while the newest CourierSignalLost / CourierSignalRestored at or before t is
 * a loss, or the newest courier_stale finding at or before t is not cleared and
 * is newer than the last accepted fix at or before t; never once the delivery
 * ended. Lost / restored / fixes come from the events in the buffer.
 */
export function stepsAt(steps: StatusStep[], events: readonly McEvent[], findings: readonly McEvent[], t: number): StatusStep[] {
  const out: StatusStep[] = [];
  let newest = -1;
  let ended = false;
  for (const s of steps) {
    const at = ms(s.at);
    const reached = s.state !== 'pending' && Number.isFinite(at) && at <= t;
    if (!reached) {
      if (!ENDED.has(s.status) || s.status === MAIN_END) out.push({ status: s.status, at: null, state: 'pending' });
      continue;
    }
    if (ENDED.has(s.status)) ended = true;
    else newest = out.length;
    out.push({ status: s.status, at: s.at, state: 'done' });
  }

  let lostAt = -Infinity;
  let restoredAt = -Infinity;
  for (const e of events) {
    const at = ms(e.occurredAt);
    if (!(at <= t)) continue;
    const ty = eventType(e);
    if (ty === LOST && at > lostAt) lostAt = at;
    if (ty === RESTORED && at > restoredAt) restoredAt = at;
  }
  let warn = lostAt > restoredAt;
  if (!warn) {
    const stale = activeFindings(findings, t).filter((f) => ruleOf(f) === 'courier_stale');
    const newestStale = stale.reduce((m, f) => Math.max(m, ms(f.occurredAt)), -Infinity);
    if (newestStale > -Infinity && newestStale > lastAcceptedFixAt(events, t)) warn = true;
  }
  if (warn && newest >= 0 && !ended) out[newest] = { ...out[newest], state: 'warning' };
  return out;
}

/**
 * The findings that are active: occurred at or before t (when a replay cursor
 * is given), and not cleared, meaning no later "cleared" finding with the same
 * identity (rule, hop, delivery, reason) at or before t. "cleared" entries themselves are not shown.
 */
export function activeFindings(findings: readonly McEvent[], t: number | null): McEvent[] {
  const seen = t === null ? [...findings] : findings.filter((f) => ms(f.occurredAt) <= t);
  const clearedAt = new Map<string, number>();
  for (const f of seen) {
    if (f.outcome !== 'cleared') continue;
    const at = ms(f.occurredAt);
    if (at > (clearedAt.get(identOf(f)) ?? -Infinity)) clearedAt.set(identOf(f), at);
  }
  return seen.filter((f) => f.outcome !== 'cleared' && !(ms(f.occurredAt) <= (clearedAt.get(identOf(f)) ?? -Infinity)));
}

/** What the left panel and the top-bar count both show: loaded plus streamed findings, as of the replay cursor in replay. */
export function useActiveFindings(): McEvent[] {
  const loaded = useStore((s) => s.panelFindings);
  const streamed = useStore((s) => s.findings);
  const cursor = useStore((s) => (s.mode === 'replay' ? s.replayAt : null));
  return useMemo(() => {
    const m = new Map<number, McEvent>();
    for (const f of [...loaded, ...streamed]) m.set(f.id, f);
    return activeFindings([...m.values()].sort((a, b) => a.id - b.id), cursor);
  }, [loaded, streamed, cursor]);
}
