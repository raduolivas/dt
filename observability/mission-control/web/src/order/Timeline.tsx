import { useEffect, useMemo, useRef } from 'react';
import { api, type Client } from '../api/client';
import type { McEvent, TimelineGap } from '../api/types';
import { PULSE_HEX, pulseColor } from '../scene/visualState';
import { store, useStore } from '../state/store';

export const GAP_LABEL: Record<TimelineGap['kind'], string> = {
  mc_down: 'Mission Control was not running',
  no_trace: 'no trace data',
};

/**
 * Fetches the selected order's whole timeline (from after=0, every page) into
 * the store; again after a stream reset (streamEpoch), which emptied the buffer.
 */
export function useOrderTimeline(client: Client = api): void {
  const orderId = useStore((s) => s.orderId);
  const epoch = useStore((s) => s.streamEpoch);
  useEffect(() => {
    if (!orderId) return;
    let alive = true;
    client
      .allTimeline(orderId)
      .then((t) => {
        if (!alive || store.getState().orderId !== orderId || store.getState().streamEpoch !== epoch) return;
        store.addEvents(t.events);
        store.setTimelineGaps(t.gaps);
      })
      .catch(() => {
        // the left panel reports order errors; the timeline stays with what the stream delivers
      });
    return () => {
      alive = false;
    };
  }, [orderId, client, epoch]);
}

const at = (e: McEvent) => Date.parse(e.occurredAt);

/** Bottom timeline: one tick per event coloured by outcome, hatched gap bands, drag to replay. */
export function Timeline() {
  const events = useStore((s) => s.events);
  const gaps = useStore((s) => s.timelineGaps);
  const mode = useStore((s) => s.mode);
  const cursor = useStore((s) => s.replayAt);
  const dragging = useRef(false);

  const range = useMemo(() => {
    const ts = events.map(at);
    for (const g of gaps) ts.push(Date.parse(g.from), Date.parse(g.to));
    const ok = ts.filter(Number.isFinite);
    if (ok.length === 0) return null;
    const min = Math.min(...ok);
    return { min, span: Math.max(1, Math.max(...ok) - min) };
  }, [events, gaps]);

  if (!range) return <p className="mc-placeholder">No events yet.</p>;
  const pct = (t: number) => `${(((t - range.min) / range.span) * 100).toFixed(3)}%`;

  const seek = (clientX: number, el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    const f = r.width > 0 ? Math.min(1, Math.max(0, (clientX - r.left) / r.width)) : 0;
    store.setMode('replay');
    store.setReplayAt(Math.round(range.min + f * range.span));
  };

  return (
    <div className="mc-timeline">
      {mode === 'replay' && cursor !== null ? (
        <div className="mc-replay-banner" role="status">
          REPLAY {new Date(cursor).toISOString()}
          <button type="button" onClick={() => store.setMode('live')}>Back to live</button>
        </div>
      ) : null}
      <div
        className="mc-track"
        aria-label="Timeline: drag to replay"
        onPointerDown={(e) => {
          dragging.current = true;
          e.currentTarget.setPointerCapture?.(e.pointerId);
          seek(e.clientX, e.currentTarget);
        }}
        onPointerMove={(e) => {
          if (dragging.current) seek(e.clientX, e.currentTarget);
        }}
        onPointerUp={() => {
          dragging.current = false;
        }}
        onPointerCancel={() => {
          dragging.current = false;
        }}
      >
        {gaps.map((g) => {
          const from = Date.parse(g.from);
          const to = Date.parse(g.to);
          if (!Number.isFinite(from) || !Number.isFinite(to)) return null;
          return (
            <div
              key={`${g.kind}|${g.from}|${g.to}`}
              className={`mc-gap is-${g.kind}`}
              style={{ left: pct(from), width: `${(((to - from) / range.span) * 100).toFixed(3)}%` }}
              title={GAP_LABEL[g.kind]}
            >
              <span>{GAP_LABEL[g.kind]}</span>
            </div>
          );
        })}
        {events.map((e) => {
          const t = at(e);
          if (!Number.isFinite(t)) return null;
          const c = pulseColor(e.outcome);
          const future = mode === 'replay' && cursor !== null && t > cursor;
          return (
            <div
              key={e.id}
              className={`mc-tick is-${c}${e.origin === 'simulated' ? ' is-sim' : ''}${future ? ' is-future' : ''}`}
              style={{ left: pct(t), background: PULSE_HEX[c] }}
            />
          );
        })}
        {mode === 'replay' && cursor !== null ? <div className="mc-cursor" style={{ left: pct(cursor) }} /> : null}
      </div>
    </div>
  );
}
