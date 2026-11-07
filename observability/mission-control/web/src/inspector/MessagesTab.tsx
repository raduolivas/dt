import { useEffect, useMemo, useState } from 'react';
import { api, type Client } from '../api/client';
import type { EventResponse, McEvent } from '../api/types';
import { fromSpan } from '../scene/model';
import { store, useStore } from '../state/store';
import { OpenIn } from './OpenIn';
import { atOrBefore, belongsTo, fmtTime, message, useCursor } from './util';

const MESSAGE_KINDS = new Set(['request', 'message_published', 'message_consumed']);
const LIST_MAX = 100;

function EventDetail({ id, component, client }: { id: string; component: string; client: Client }) {
  const buffered = useStore((s) => s.events.find((e) => String(e.id) === id) ?? null);
  const cursor = useCursor();
  const [got, setGot] = useState<{ id: string; res: EventResponse | null; error: string | null } | null>(null);
  const res = got?.id === id ? got.res : null;
  const error = got?.id === id ? got.error : null;
  useEffect(() => {
    let alive = true;
    client.event(Number(id)).then(
      (r) => alive && setGot({ id, res: r, error: null }),
      (e: unknown) => alive && setGot({ id, res: null, error: message(e) }),
    );
    return () => {
      alive = false;
    };
  }, [client, id]);

  const e: McEvent | null = res?.event ?? buffered;
  const logs = useMemo(() => atOrBefore(res?.logs ?? [], cursor), [res, cursor]);
  const dur = e?.attrs?.duration_ms;
  return (
    <div className="mc-event">
      {component ? (
        <button type="button" onClick={() => store.select({ kind: 'component', id: component })}>
          ← messages of {component}
        </button>
      ) : null}
      {error !== null ? <p className="mc-placeholder" role="alert">Event unavailable: {error}</p> : null}
      {e ? (
        <>
          <dl className="mc-kv">
            <dt>time</dt><dd>{fmtTime(e.occurredAt)}</dd>
            <dt>duration</dt><dd>{typeof dur === 'number' ? `${dur} ms` : 'n/a'}</dd>
            <dt>outcome</dt><dd>{e.outcome}</dd>
            <dt>trace id</dt><dd>{e.traceId || 'none'}</dd>
            <dt>from → to</dt><dd>{e.source || '?'} → {e.target || '?'}</dd>
            {e.reason ? (<><dt>reason</dt><dd>{e.reason}</dd></>) : null}
          </dl>
          {!fromSpan(e) ? <p className="mc-muted">linked by delivery id, not by trace</p> : null}
        </>
      ) : error === null ? (
        <p className="mc-muted">Loading…</p>
      ) : null}
      <h3 className="mc-h">Log lines</h3>
      {logs.length === 0 ? <p className="mc-muted">No log lines for this request.</p> : null}
      <ol className="mc-logs-list">
        {logs.map((l) => (
          <li key={l.id} className="mc-logline">
            <span className="mc-muted">{fmtTime(l.occurredAt)}</span>{' '}
            <span className="mc-sev">{typeof l.attrs?.severity === 'string' ? l.attrs.severity : ''}</span>{' '}
            <span>{typeof l.attrs?.body === 'string' ? l.attrs.body : l.reason}</span>
          </li>
        ))}
      </ol>
      <OpenIn links={res?.links ?? []} />
    </div>
  );
}

/** Requests and messages in and out of the component (this order when one is selected), or one selected event with its log lines. */
export function MessagesTab({ id, client = api }: { id: string; client?: Client }) {
  const selection = useStore((s) => s.selection);
  const events = useStore((s) => s.events);
  const cursor = useCursor();
  const list = useMemo(
    () =>
      atOrBefore(events, cursor)
        .filter((e) => MESSAGE_KINDS.has(e.kind) && (belongsTo(e.source, id) || belongsTo(e.target, id)))
        .slice(-LIST_MAX)
        .reverse(),
    [events, cursor, id],
  );
  if (selection?.kind === 'event') return <EventDetail id={selection.id} component={id} client={client} />;
  return (
    <div className="mc-messages">
      {list.length === 0 ? <p className="mc-muted">No messages seen.</p> : null}
      <ul className="mc-recent">
        {list.map((e) => (
          <li key={e.id}>
            <button type="button" onClick={() => store.select({ kind: 'event', id: String(e.id) })}>
              <span className="mc-muted">{fmtTime(e.occurredAt)}</span> {e.source} → {e.target} <span>{e.outcome}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
