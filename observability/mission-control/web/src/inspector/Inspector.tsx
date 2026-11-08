import { useEffect, useMemo, useState } from 'react';
import { api, type Client } from '../api/client';
import type { ComponentResponse } from '../api/types';
import { store, useStore } from '../state/store';
import { HealthTab } from './HealthTab';
import { LogsTab } from './LogsTab';
import { MessagesTab } from './MessagesTab';
import { OverviewTab } from './OverviewTab';
import { atOrBefore, belongsTo, fmtTime, message, useCursor } from './util';

const TABS = ['Overview', 'Logs', 'Messages', 'Health'] as const;
type Tab = (typeof TABS)[number];
const POLL_MS = 5000;

function useComponent(client: Client, id: string, deliveryId: string | null) {
  const live = useStore((s) => s.mode === 'live');
  const [data, setData] = useState<ComponentResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!id) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const run = async () => {
      try {
        const d = await client.component(id, deliveryId ?? undefined);
        if (alive) {
          setData(d);
          setError(null);
        }
      } catch (e) {
        if (alive) setError(message(e));
      }
      if (alive && live) timer = setTimeout(run, POLL_MS);
    };
    void run();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [client, id, deliveryId, live]);
  // data of another component (just switched) is not shown while the new one loads
  return { data: data && data.component.id === id ? data : null, error };
}

function WirePanel({ id }: { id: string }) {
  const events = useStore((s) => s.events);
  const cursor = useCursor();
  const [a, b] = id.split('>');
  const list = useMemo(
    () =>
      atOrBefore(events, cursor)
        .filter((e) => (belongsTo(e.source, a) && belongsTo(e.target, b)) || (belongsTo(e.source, b) && belongsTo(e.target, a)))
        .slice(-100)
        .reverse(),
    [events, cursor, a, b],
  );
  return (
    <div className="mc-wire-panel">
      <dl className="mc-kv"><dt>wire</dt><dd>{a} ↔ {b}</dd></dl>
      <h3 className="mc-h">Messages</h3>
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

/** The right panel (spec 6.4): Overview, Logs, Messages and Health of the selected component, a wire's messages, or one event. Read-only. */
export function Inspector({ client = api }: { client?: Client }) {
  const selection = useStore((s) => s.selection);
  const orderId = useStore((s) => s.orderId);
  const events = useStore((s) => s.events);
  const [tab, setTab] = useState<Tab>('Overview');
  const [lastComponent, setLastComponent] = useState('');
  const [prevSel, setPrevSel] = useState('');

  // The component an event belongs to: where it happened (its source); else the one last looked at.
  let comp = '';
  if (selection?.kind === 'component') comp = selection.id;
  else if (selection?.kind === 'event') {
    const e = events.find((x) => String(x.id) === selection.id);
    comp = e?.source || lastComponent;
  }
  if (comp && comp !== lastComponent) setLastComponent(comp);
  // A new selection picks the tab: an event shows its messages, a component its overview.
  const selKey = selection ? `${selection.kind}:${selection.id}` : '';
  if (selKey !== prevSel) {
    setPrevSel(selKey);
    if (selection?.kind === 'event') setTab('Messages');
    else if (selection?.kind === 'component') setTab('Overview');
  }

  const { data, error } = useComponent(client, selection?.kind === 'wire' ? '' : comp, orderId);

  if (!selection) return <p className="mc-placeholder">Select a component, a wire or a pulse.</p>;
  if (selection.kind === 'wire') return <WirePanel id={selection.id} />;
  return (
    <div className="mc-inspector">
      <h2 className="mc-h">{data?.component.label ?? (comp || 'event')}</h2>
      <div role="tablist" aria-label="Inspector" className="mc-tabs">
        {TABS.map((t) => (
          <button key={t} type="button" role="tab" aria-selected={tab === t} className={tab === t ? 'is-on' : ''} onClick={() => setTab(t)}>
            {t}
          </button>
        ))}
      </div>
      <div role="tabpanel" aria-label={tab}>
        {!comp && tab !== 'Messages' ? (
          <p className="mc-muted">Select a component for this tab.</p>
        ) : tab === 'Overview' ? (
          <OverviewTab data={data} error={error} />
        ) : tab === 'Logs' ? (
          <LogsTab id={comp} client={client} />
        ) : tab === 'Messages' ? (
          <MessagesTab id={comp} client={client} />
        ) : error !== null ? (
          <p className="mc-placeholder" role="alert">Health unavailable: {error}</p>
        ) : data ? (
          <>
            <HealthTab health={data.health} />
            <p className="mc-muted">Current state, not the state at the replay time.</p>
          </>
        ) : (
          <p className="mc-muted">Loading…</p>
        )}
      </div>
    </div>
  );
}
