import type { McEvent } from '../api/types';

/**
 * Replay is a view over what the store already holds: the events that had
 * happened by cursor time t (epoch ms, by occurredAt). It reads nothing from
 * and sends nothing to anywhere; there is no write path to DT behind it.
 * The input is not modified. Events with an unreadable time are left out.
 */
export function eventsAt(buffer: readonly McEvent[], t: number): McEvent[] {
  return buffer.filter((e) => Date.parse(e.occurredAt) <= t);
}
