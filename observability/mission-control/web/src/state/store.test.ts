import { describe, expect, it } from 'vitest';
import type { McEvent } from '../api/types';
import { mkEvent } from '../test-utils';
import { MAX_EVENTS, createStore } from './store';

describe('store', () => {
  it('resetStream clears what the old database sent and lowers the resume id', () => {
    const s = createStore();
    s.selectOrder('del_1');
    s.addEvents([mkEvent(800), mkEvent(801)]);
    s.addFinding(mkEvent(802, { kind: 'detector' }));
    s.addBundle({ wire: 'a>b', count: 3, outcome: 'ok' }, 801);
    s.addGap({ from: 1, to: 2 });
    s.setTimelineGaps([{ kind: 'mc_down', from: '2026-01-01T00:00:00Z', to: '2026-01-01T00:01:00Z' }]);
    s.setPanelFindings([mkEvent(803, { kind: 'detector' })]);
    s.select({ kind: 'event', id: '801' });
    s.noteStreamId(802);
    const epoch = s.getState().streamEpoch;
    s.resetStream(3);
    const st = s.getState();
    expect(st.orderId).toBe('del_1');
    expect(st.events).toEqual([]);
    expect(st.findings).toEqual([]);
    expect(st.bundles).toEqual([]);
    expect(st.gaps).toEqual([]);
    expect(st.timelineGaps).toEqual([]);
    expect(st.panelFindings).toEqual([]);
    expect(st.selection).toBeNull();
    expect(st.lastEventId).toBe(3);
    expect(st.streamEpoch).toBe(epoch + 1);
    // ids restarted: an id the old buffer held is new again
    s.addEvents([mkEvent(1), mkEvent(801)]);
    expect(s.getState().events.map((e) => e.id)).toEqual([1, 801]);
    s.addFinding(mkEvent(802, { kind: 'detector' }));
    expect(s.getState().findings).toHaveLength(1);
  });

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

  it('bundles keep a local sequence and their stream id', () => {
    const s = createStore();
    s.addBundle({ wire: 'a>b', count: 3, outcome: 'ok' }, 10);
    s.addBundle({ wire: 'a>b', count: 4, outcome: 'ok' }, 10);
    const [x, y] = s.getState().bundles;
    expect(x).toMatchObject({ wire: 'a>b', count: 3, streamId: 10 });
    expect(y.seq).toBeGreaterThan(x.seq);
  });

  it('queueEvents batches stream events into one update per frame', () => {
    const frames: (() => void)[] = [];
    const s = createStore((cb) => frames.push(cb));
    let n = 0;
    s.subscribe(() => n++);
    s.queueEvents([mkEvent(1)]);
    s.queueEvents([mkEvent(2)]);
    s.queueEvents([mkEvent(3)]);
    expect(s.getState().events).toHaveLength(0);
    expect(frames).toHaveLength(1);
    frames[0]();
    expect(s.getState().events.map((e) => e.id)).toEqual([1, 2, 3]);
    expect(n).toBe(1);
    // an order switch drops what was queued for the old order
    s.queueEvents([mkEvent(4)]);
    s.selectOrder('del_1');
    frames[1]();
    expect(s.getState().events).toHaveLength(0);
  });

  it('queued bundles flush with the events, in stream order', () => {
    const frames: (() => void)[] = [];
    const s = createStore((cb) => frames.push(cb));
    s.queueEvents([mkEvent(5)]);
    s.queueBundle({ wire: 'a>b', count: 9, outcome: 'ok' }, 20);
    s.queueEvents([mkEvent(21)]);
    expect(s.getState().bundles).toHaveLength(0);
    expect(frames).toHaveLength(1);
    let snapshots = 0;
    let sawBundleBeforeEvent = false;
    s.subscribe(() => {
      snapshots++;
      const st = s.getState();
      if (st.bundles.length > 0 && !st.events.some((e) => e.id === 5)) sawBundleBeforeEvent = true;
    });
    frames[0]();
    expect(sawBundleBeforeEvent).toBe(false);
    expect(snapshots).toBe(1);
    expect(s.getState().events.map((e) => e.id)).toEqual([5, 21]);
    expect(s.getState().bundles[0]).toMatchObject({ count: 9, streamId: 20 });
  });

  it('a queue that no frame drains (hidden tab) is flushed once it reaches the buffer size', () => {
    const s = createStore(() => {}); // frames never run
    s.queueEvents(Array.from({ length: MAX_EVENTS }, (_, i) => mkEvent(i + 1)));
    expect(s.getState().events).toHaveLength(MAX_EVENTS);
  });
});
