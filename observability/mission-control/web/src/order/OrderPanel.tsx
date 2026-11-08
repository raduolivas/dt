import { useEffect, useMemo, useState } from 'react';
import { api, type Client } from '../api/client';
import { simApi, type SimClient, type SimRun } from '../api/sim';
import type { McEvent, OrderResponse, StatusStep, SystemResponse } from '../api/types';
import { store, useStore } from '../state/store';
import { age } from '../scene/visualState';
import { stepsAt, useActiveFindings } from './atTime';
import { hitLabel, openOrder } from './Search';

const POLL_MS = 5000;
const MARK: Record<StatusStep['state'], string> = { done: '✓', warning: '▲', pending: '○' };

/** Loads once, then again every POLL_MS while the stream is live. */
function usePolled<T>(load: () => Promise<T>, key: string): { data: T | null; error: string | null } {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const live = useStore((s) => s.mode === 'live');
  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const run = async () => {
      try {
        const d = await load();
        if (alive) {
          setData(d);
          setError(null);
        }
      } catch (e) {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      }
      if (alive && live) timer = setTimeout(run, POLL_MS);
    };
    void run();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, live]);
  return { data, error };
}

/** One finding: the hop (the finding's target), rule and reason, severity chip, the order, and its age. */
function Finding({ f }: { f: McEvent }) {
  const rule = typeof f.attrs?.rule === 'string' ? f.attrs.rule : '';
  const what = [rule, f.reason].filter(Boolean).join(' · ');
  const order = f.orderRef || (f.deliveryId ? `${f.deliveryId.slice(0, 12)}…` : '');
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), POLL_MS);
    return () => clearInterval(t);
  }, []);
  const ageMs = now - Date.parse(f.occurredAt);
  return (
    <li className="mc-finding">
      <strong>{f.target || f.source}</strong> <span className={`mc-chip mc-chip-${f.outcome}`}>{f.outcome}</span>
      {what ? <div>{what}</div> : null}
      <div>
        {f.deliveryId ? <button type="button" onClick={() => openOrder(f.deliveryId)}>{order}</button> : <span className="mc-muted">system-wide</span>}{' '}
        <time className="mc-muted" dateTime={f.occurredAt} title={f.occurredAt}>{Number.isFinite(ageMs) ? `${age(ageMs)} ago` : f.occurredAt}</time>
      </div>
    </li>
  );
}

const BREAKS: { n: number; label: string }[] = [
  { n: 1, label: 'Phone stops' },
  { n: 2, label: 'Invalid token' },
  { n: 3, label: 'Rate limit' },
  { n: 4, label: 'Reassigned' },
];

/** Break buttons for a simulated order. Rendered only when /api/sim answers (simulation mode) and knows this delivery. */
function SimBreaks({ deliveryId, sim }: { deliveryId: string; sim: SimClient }) {
  const [run, setRun] = useState<SimRun | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const refresh = () => setTick((t) => t + 1);
  useEffect(() => {
    let alive = true;
    void sim.status().then((runs) => {
      if (alive) setRun(runs?.find((r) => r.delivery_id === deliveryId) ?? null);
    });
    return () => {
      alive = false;
    };
  }, [deliveryId, sim, tick]);
  if (!run) return null;
  const act = async (f: () => Promise<void>) => {
    try {
      await f();
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    refresh();
  };
  return (
    <section className="mc-sim" aria-label="Break this simulated order">
      <h3 className="mc-h">Break it</h3>
      <div className="mc-sim-buttons">
        {BREAKS.map((b) => (
          <button key={b.n} type="button" aria-pressed={run.scenario === b.n} disabled={b.n === 4 && run.picked_up} title={b.n === 4 && run.picked_up ? "DT reassigns only before pickup: use make mc-break SCENARIO=4" : undefined} onClick={() => act(() => sim.breakRun(deliveryId, b.n))}>
            {b.n}. {b.label}
          </button>
        ))}
        <button type="button" disabled={run.scenario === 0} onClick={() => act(() => sim.heal(deliveryId))}>Heal</button>
      </div>
      <p className="mc-muted">
        5. Redis down: <code>make mc-break SCENARIO=5</code> (heal: add <code>HEAL=1</code>)<br />
        6. OSRM stopped: <code>make mc-break SCENARIO=6</code> (heal: add <code>HEAL=1</code>)
      </p>
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

function OrderView({ id, client, sim }: { id: string; client: Client; sim: SimClient }) {
  const { data, error } = usePolled<OrderResponse>(() => client.order(id), id);
  useEffect(() => {
    store.setPanelFindings(data?.detectors ?? []);
  }, [data]);
  const detectors = useActiveFindings();
  // In replay the panel shows what was known at the cursor (R34); events are read only then.
  const cursor = useStore((s) => (s.mode === 'replay' ? s.replayAt : null));
  const events = useStore((s) => (s.mode === 'replay' ? s.events : null));
  const steps = useMemo(
    () => (data && cursor !== null ? stepsAt(data.statusSteps, events ?? [], detectors, cursor) : (data?.statusSteps ?? [])),
    [data, cursor, events, detectors],
  );

  if (!data) {
    return (
      <p className="mc-placeholder" role="status">
        {error ? `Order unavailable: ${error}` : 'Loading order…'}
      </p>
    );
  }
  const o = data.order;
  return (
    <div className="mc-order">
      <button type="button" onClick={() => store.selectOrder(null)}>← System view</button>
      <h2 className="mc-h">
        {o.orderRef || o.deliveryId}
        {o.origin === 'simulated' ? <span className="mc-badge mc-badge-sim">SIMULATED</span> : null}
      </h2>
      <dl className="mc-kv">
        <dt>tenant</dt><dd>{o.tenantId}</dd>
        <dt>environment</dt><dd>{o.environment}</dd>
        <dt>delivery</dt><dd>{o.deliveryId}</dd>
      </dl>
      <h3 className="mc-h">Status</h3>
      <ol className="mc-steps">
        {steps.map((s) => (
          <li key={s.status} className={`mc-step is-${s.state}`}>
            {MARK[s.state] ?? '○'} {s.status}
            {s.at ? <time className="mc-muted"> {s.at}</time> : null}
          </li>
        ))}
      </ol>
      <h3 className="mc-h">Detectors</h3>
      {detectors.length === 0 ? <p className="mc-muted">No findings for this order.</p> : <ul className="mc-findings">{detectors.map((f) => <Finding key={f.id} f={f} />)}</ul>}
      {o.origin === 'simulated' && cursor === null ? <SimBreaks deliveryId={o.deliveryId} sim={sim} /> : null}
    </div>
  );
}

function SystemView({ client }: { client: Client }) {
  const { data, error } = usePolled<SystemResponse>(() => client.system(), 'system');
  useEffect(() => {
    store.setPanelFindings(data?.warnings ?? []);
  }, [data]);
  const warnings = useActiveFindings();
  if (!data) {
    return (
      <p className="mc-placeholder" role="status">
        {error ? `System view unavailable: ${error}` : 'Loading…'}
      </p>
    );
  }
  return (
    <div className="mc-system">
      <h2 className="mc-h">System</h2>
      <h3 className="mc-h">Traffic</h3>
      {data.traffic.length === 0 ? (
        <p className="mc-muted">No traffic observed yet.</p>
      ) : (
        <table className="mc-traffic">
          <thead><tr><th>wire</th><th>count</th><th>failed</th><th>p95</th></tr></thead>
          <tbody>
            {data.traffic.map((t) => (
              <tr key={t.wire}>
                <td>{t.wire}</td><td>{t.count}</td><td>{t.failed}</td><td>{Math.round(t.p95Ms)} ms</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="mc-h">Recent orders</h3>
      {data.recentOrders.length === 0 ? <p className="mc-muted">None yet.</p> : (
        <ul className="mc-recent">
          {data.recentOrders.map((h) => (
            <li key={`${h.tenantId}|${h.environment}|${h.deliveryId}`}>
              <button type="button" onClick={() => openOrder(h.deliveryId)}>
                {h.orderRef || h.deliveryId} <span className="mc-muted">{hitLabel(h)}</span>
                {h.origin === 'simulated' ? <span className="mc-badge mc-badge-sim">SIMULATED</span> : null}
              </button>
            </li>
          ))}
        </ul>
      )}
      <h3 className="mc-h">Active warnings</h3>
      {warnings.length === 0 ? <p className="mc-muted">No active warnings.</p> : <ul className="mc-findings">{warnings.map((f) => <Finding key={f.id} f={f} />)}</ul>}
    </div>
  );
}

/** Left panel: the selected order (status steps, detectors), or the system view when none is selected. */
export function LeftPanel({ client = api, sim = simApi }: { client?: Client; sim?: SimClient }) {
  const orderId = useStore((s) => s.orderId);
  return orderId ? <OrderView id={orderId} client={client} sim={sim} /> : <SystemView client={client} />;
}
