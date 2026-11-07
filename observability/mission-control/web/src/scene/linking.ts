// Logs <-> pulses (spec 6.4). Pure: the Logs tab, the Messages tab and the scene all go through these.
import type { McEvent } from '../api/types';
import type { Selection } from '../state/store';
import type { SceneModel } from './model';

const PULSE_KINDS = new Set(['request', 'message_published', 'message_consumed']);

/**
 * The event a log line points at: the request or message of the same span
 * (same trace id and span id) when it is in the buffer, else the log line's own event.
 */
export function logEventId(log: McEvent, events: readonly McEvent[]): number {
  if (log.traceId && log.spanId) {
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i];
      if (PULSE_KINDS.has(e.kind) && e.traceId === log.traceId && e.spanId === log.spanId) return e.id;
    }
  }
  return log.id;
}

/** What a click on a pulse selects: its event (a bundle: the newest event it bundles). Nothing when it has none (a server bundle). */
export function pulseSelection(item: { eventId?: number }): Selection {
  return item.eventId === undefined ? null : { kind: 'event', id: String(item.eventId) };
}

/** The scene wire to highlight for a selected event, or null (not an event, not in the buffer, not drawn in this view). */
export function highlightWire(model: SceneModel, events: readonly McEvent[], sel: Selection): string | null {
  if (!sel || sel.kind !== 'event') return null;
  const e = events.find((x) => x.id === Number(sel.id));
  return e ? (model.placeEvent(e)?.wire ?? null) : null;
}
