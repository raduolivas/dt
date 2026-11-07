import type { ComponentResponse } from '../api/types';
import { store } from '../state/store';
import { OpenIn } from './OpenIn';
import { atOrBefore, fmtTime, useCursor } from './util';

/** What the component is, how it stands, and its last activity. */
export function OverviewTab({ data, error }: { data: ComponentResponse | null; error: string | null }) {
  const cursor = useCursor();
  if (error !== null) return <p className="mc-placeholder" role="alert">Component unavailable: {error}</p>;
  if (!data) return <p className="mc-muted">Loading…</p>;
  const c = data.component;
  const recent = atOrBefore(data.recent, cursor); // newest first
  const last = recent[0];
  return (
    <div className="mc-overview">
      <dl className="mc-kv">
        <dt>name</dt><dd>{c.label}</dd>
        <dt>id</dt><dd>{c.id}</dd>
        <dt>zone</dt><dd>{c.zone}</dd>
        {c.parent ? (<><dt>part of</dt><dd>{c.parent}</dd></>) : null}
        <dt>state</dt>
        <dd>
          {c.status === 'not_built' ? 'not built yet' : !c.instrumented ? 'not instrumented' : data.health.liveness.state === 'not_probed' ? 'health not probed (mTLS)' : `liveness ${data.health.liveness.state}`}
        </dd>
        <dt>last activity</dt>
        <dd>{last ? `${fmtTime(last.occurredAt)} · ${last.kind} · ${last.outcome}` : 'none seen'}</dd>
      </dl>
      <h3 className="mc-h">Recent</h3>
      <ul className="mc-recent">
        {recent.slice(0, 10).map((e) => (
          <li key={e.id}>
            <button type="button" onClick={() => store.select({ kind: 'event', id: String(e.id) })}>
              <span className="mc-muted">{fmtTime(e.occurredAt)}</span> {e.kind} {e.source}
              {e.target ? ` → ${e.target}` : ''} <span>{e.outcome}</span>
            </button>
          </li>
        ))}
      </ul>
      <OpenIn links={data.links} />
    </div>
  );
}
