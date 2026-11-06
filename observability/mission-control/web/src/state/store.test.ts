import { describe, expect, it } from 'vitest';
import type { McEvent } from '../api/types';
import { mkEvent } from '../test-utils';
import { MAX_EVENTS, createStore } from './store';

describe('store', () => {
  it('storeCapsBuffer', () => {
    const s = createStore();
    const evs: McEvent[] = [];
    for (let i = 1; i <= MAX_EVENTS + 500; i++) evs.push(mkEvent(i));
    s.addEvents(evs);
    const got = s.getState().events;
    expect(got).toHaveLength(MAX_EVENTS);
    expect(got[0].id).toBe(501); // oldest dropped
    expect(got[got.length - 1].id).toBe(MAX_EVENTS + 500);
    s.addEvents([mkEvent(MAX_EVENTS + 501)]);
    expect(s.getState().events).toHaveLength(MAX_EVENTS);
  });

  it('keeps events ordered by id and ignores duplicates', () => {
    const s = createStore();
    s.addEvents([mkEvent(3), mkEvent(1)]);
    s.addEvents([mkEvent(2), mkEvent(3)]);
    expect(s.getState().events.map((e) => e.id)).toEqual([1, 2, 3]);
  });

  it('routes detector findings to findings, not the event buffer', () => {
    const s = createStore();
    s.addFinding(mkEvent(9, { kind: 'detector' }));
    s.addFinding(mkEvent(9, { kind: 'detector' }));
    expect(s.getState().findings).toHaveLength(1);
    expect(s.getState().events).toHaveLength(0);
  });

  it('holds mode, order, selection, gaps, panel flags and reduced motion; notifies subscribers', () => {
    const s = createStore();
    let n = 0;
    const off = s.subscribe(() => n++);
    s.setMode('paused');
    s.selectOrder('del_1');
    s.select({ kind: 'wire', id: 'a>b' });
    s.addGap({ from: 1, to: 4 });
    s.togglePanel('left');
    s.setReducedMotion(true);
    off();
    s.setMode('live');
    const st = s.getState();
    expect(st.orderId).toBe('del_1');
    expect(st.selection).toEqual({ kind: 'wire', id: 'a>b' });
    expect(st.gaps).toEqual([{ from: 1, to: 4 }]);
    expect(st.collapsed.left).toBe(true);
    expect(st.reducedMotion).toBe(true);
    expect(n).toBe(6);
  });

  it('selecting another order clears the buffer, selection and findings', () => {
    const s = createStore();
    s.addEvents([mkEvent(1)]);
    s.select({ kind: 'event', id: '1' });
    s.selectOrder('del_2');
    expect(s.getState().events).toHaveLength(0);
    expect(s.getState().selection).toBeNull();
  });
});
