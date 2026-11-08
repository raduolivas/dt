import { useEffect, useMemo, useRef, useState } from 'react';
import { api, type Client } from '../api/client';
import type { McEvent } from '../api/types';
import { store, useStore } from '../state/store';
import { atOrBefore, fmtTime, message, useCursor } from './util';

const PAGE = 500;
const KEEP = 1000; // also the backend's tail cap
const FOLLOW_MS = 2000;
const PULSE_KINDS = new Set(['request', 'message_published', 'message_consumed']);

/**
 * Log lines of a component (spec 6.4): this order by default (needs a selected order),
 * a toggle to all of the component, warn+ and follow (auto-scroll, new lines appended while live).
 * Clicking a line selects its event, which highlights its pulse and wire.
 */
export function LogsTab({ id, client = api }: { id: string; client?: Client }) {
  const orderId = useStore((s) => s.orderId);
  const live = useStore((s) => s.mode === 'live');
  const events = useStore((s) => s.events);
  const selection = useStore((s) => s.selection);
  const cursor = useCursor();
  const [scope, setScope] = useState<'order' | 'component'>('order');
  const [warn, setWarn] = useState(false);
  const [follow, setFollow] = useState(true);
  const [lines, setLines] = useState<McEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const box = useRef<HTMLOListElement>(null);

  const perOrder = orderId !== null && scope === 'order';
  const deliveryId = perOrder ? (orderId ?? undefined) : undefined;
  const level = warn ? 'warn' : undefined;

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last = 0;
    const append = (fresh: McEvent[], reset: boolean) =>
      setLines((prev) => {
        const seen = new Set(reset ? [] : prev.map((l) => l.id));
        const merged = (reset ? [] : prev).concat(fresh.filter((l) => !seen.has(l.id)));
        return merged.length > KEEP ? merged.slice(merged.length - KEEP) : merged;
      });
    const load = async () => {
      try {
        // the newest KEEP lines (oldest first); follow continues after the newest id
        const r = await client.componentLogs(id, { deliveryId, level, tail: KEEP });
        if (!alive) return;
        last = r.logs.length ? r.logs[r.logs.length - 1].id : 0;
        append(r.logs, true);
        setError(null);
      } catch (e) {
        if (alive) {
          setLines([]);
          setError(message(e));
        }
      }
      if (alive && live && follow) timer = setTimeout(poll, FOLLOW_MS);
    };
    const poll = async () => {
      try {
        const r = await client.componentLogs(id, { deliveryId, level, after: last, limit: PAGE });
        if (!alive) return;
        if (r.logs.length) {
          last = r.logs[r.logs.length - 1].id;
          append(r.logs, false);
        }
        setError(null);
      } catch (e) {
        if (alive) setError(message(e));
      }
      if (alive) timer = setTimeout(poll, FOLLOW_MS);
    };
    void load();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [client, id, deliveryId, level, live, follow]);

  // Replay: only lines at or before the cursor (R34).
  const shown = useMemo(() => atOrBefore(lines, cursor), [lines, cursor]);
  useEffect(() => {
    if (follow && live && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [shown.length, follow, live]);

  const selectedId = selection?.kind === 'event' ? Number(selection.id) : null;
  // trace:span -> the pulse event of that span, built once per buffer change (not per line per render)
  const eventOf = useMemo(() => {
    const m = new Map<string, number>();
    for (const e of events) if (PULSE_KINDS.has(e.kind) && e.traceId && e.spanId) m.set(`${e.traceId}:${e.spanId}`, e.id);
    return m;
  }, [events]);
  const idOf = (l: McEvent) => (l.traceId && l.spanId ? eventOf.get(`${l.traceId}:${l.spanId}`) : undefined) ?? l.id;
  const service = id.split('.')[0];

  return (
    <div className="mc-logs">
      <div className="mc-logs-bar">
        {orderId !== null ? (
          <span role="group" aria-label="Scope">
            <button type="button" aria-pressed={perOrder} onClick={() => setScope('order')}>this order</button>
            <button type="button" aria-pressed={!perOrder} onClick={() => setScope('component')}>all of component</button>
          </span>
        ) : (
          <span className="mc-muted">all of component (no order selected)</span>
        )}
        <label><input type="checkbox" checked={warn} onChange={(e) => setWarn(e.target.checked)} /> warn+ only</label>
        <label><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> follow</label>
      </div>
      <p className="mc-muted">logs of service {service}</p>
      {error !== null ? <p className="mc-placeholder" role="alert">Logs unavailable: {error}</p> : null}
      {error === null && shown.length === 0 ? <p className="mc-muted">No log lines.</p> : null}
      <ol className="mc-logs-list" ref={box}>
        {shown.map((l) => {
          const sev = typeof l.attrs?.severity === 'string' ? l.attrs.severity : '';
          const body = typeof l.attrs?.body === 'string' ? l.attrs.body : l.reason;
          const sel = selectedId !== null && idOf(l) === selectedId;
          return (
            <li key={l.id}>
              <button
                type="button"
                className={`mc-logline sev-${sev.toLowerCase()}${sel ? ' is-selected' : ''}`}
                aria-pressed={sel}
                onClick={() => store.select({ kind: 'event', id: String(idOf(l)) })}
              >
                <span className="mc-muted">{fmtTime(l.occurredAt)}</span> <span className="mc-sev">{sev}</span> <span>{body}</span>
              </button>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
