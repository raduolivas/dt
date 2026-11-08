import type { McEvent } from '../api/types';
import { useStore } from '../state/store';

/** HH:MM:SS.mmm (UTC) of an ISO time; the raw string when it does not parse. */
export function fmtTime(iso: string): string {
  const t = Date.parse(iso);
  return Number.isFinite(t) ? new Date(t).toISOString().slice(11, 23) : iso;
}

/** The replay cursor (epoch ms) while replaying, else null. In replay the tabs show only what is at or before it (R34). */
export function useCursor(): number | null {
  return useStore((s) => (s.mode === 'replay' ? s.replayAt : null));
}

export function atOrBefore<T extends McEvent>(list: readonly T[], cursor: number | null): T[] {
  return cursor === null ? [...list] : list.filter((e) => Date.parse(e.occurredAt) <= cursor);
}

/** Is `part` the component `id` or one of its parts? */
export const belongsTo = (part: string, id: string) => part === id || part.startsWith(id + '.');

export function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
