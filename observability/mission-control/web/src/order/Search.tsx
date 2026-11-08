import { useState, type FormEvent } from 'react';
import { api, type Client } from '../api/client';
import type { OrderHit } from '../api/types';
import { store } from '../state/store';

/** An order is selected by its delivery id; every field shown here is partner-supplied text. */
export function openOrder(deliveryId: string): void {
  store.selectOrder(deliveryId);
}

export function hitLabel(h: OrderHit): string {
  return `${h.tenantId} · ${h.environment} · ${h.createdAt}`;
}

/**
 * Order search (spec 6.1). One hit opens the order; several (the same order
 * ref in more than one tenant or environment) show a picker, never a guess.
 */
export function Search({ client = api }: { client?: Client }) {
  const [q, setQ] = useState('');
  const [hits, setHits] = useState<OrderHit[] | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    const term = q.trim();
    setHits(null);
    setMsg(null);
    if (term === '') return;
    try {
      const found = await client.searchOrders(term);
      if (found.length === 0) setMsg('No order found');
      else if (found.length === 1) openOrder(found[0].deliveryId);
      else setHits(found);
    } catch (e) {
      setMsg(`Search failed: ${e instanceof Error ? e.message : String(e)}`);
    }
  };
  const pick = (h: OrderHit) => {
    setHits(null);
    openOrder(h.deliveryId);
  };

  return (
    <form className="mc-searchbox" role="search" onSubmit={(e) => void submit(e)}>
      <input
        className="mc-search"
        type="search"
        placeholder="Order number or delivery id"
        aria-label="Search orders"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {msg ? <span className="mc-search-msg" role="status">{msg}</span> : null}
      {hits ? (
        <ul className="mc-picker" role="listbox" aria-label="Several orders match">
          {hits.map((h) => (
            <li
              key={`${h.tenantId}|${h.environment}|${h.deliveryId}`}
              role="option"
              aria-selected={false}
              tabIndex={0}
              onClick={() => pick(h)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  pick(h);
                }
              }}
            >
              <span className="mc-picker-ref">{h.orderRef || h.deliveryId}</span> {hitLabel(h)}
              {h.origin === 'simulated' ? <span className="mc-badge mc-badge-sim">SIMULATED</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
    </form>
  );
}
