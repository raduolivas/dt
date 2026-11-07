import { act, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useLiveStream } from './App';
import type { Client } from './api/client';
import { useOrderTimeline } from './order/Timeline';
import { store } from './state/store';

const enc = new TextEncoder();
const frame = (id: number) =>
  `id: ${id}\nevent: event\ndata: ${JSON.stringify({ id, kind: 'request', source: 'a', target: 'b', outcome: 'ok', attrs: {} })}\n\n`;

function Probe() {
  useLiveStream();
  return null;
}

async function until(cond: () => boolean) {
  for (let i = 0; i < 200 && !cond(); i++) await new Promise((r) => setTimeout(r, 5));
  expect(cond()).toBe(true);
}

describe('useLiveStream', () => {
  const calls: { url: string; lastId: string | null }[] = [];
  beforeEach(() => {
    store.reset();
    calls.length = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url, lastId: new Headers(init?.headers).get('Last-Event-ID') });
        const signal = init?.signal as AbortSignal;
        return new Response(
          new ReadableStream({
            start(c) {
              if (calls.length === 1) c.enqueue(enc.encode(frame(7)));
              signal.addEventListener('abort', () => c.error(new Error('aborted')));
            },
          }),
        );
      }),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it('returning to live reopens the stream from the last seen id', async () => {
    const { unmount } = render(<Probe />);
    await until(() => store.getState().events.length === 1);
    act(() => store.setMode('paused'));
    act(() => store.setMode('live'));
    await until(() => calls.length === 2);
    expect(calls[0].lastId).toBeNull();
    expect(calls[1].lastId).toBe('7');
    unmount();
  });

  it('keeps the resume id across an order switch', async () => {
    const { unmount } = render(<Probe />);
    await until(() => store.getState().events.length === 1);
    act(() => store.selectOrder('del_9'));
    await until(() => calls.length === 2);
    expect(calls[1].url).toBe('/api/stream?delivery_id=del_9');
    expect(calls[1].lastId).toBe('7');
    unmount();
  });

  it('a reset frame clears the buffer and refetches the open order from the start', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url, lastId: new Headers(init?.headers).get('Last-Event-ID') });
        const signal = init?.signal as AbortSignal;
        return new Response(
          new ReadableStream({
            start(c) {
              if (calls.length === 1) c.enqueue(enc.encode('id: 2\nevent: reset\ndata: {"max":2}\n\n'));
              signal.addEventListener('abort', () => c.error(new Error('aborted')));
            },
          }),
        );
      }),
    );
    const ev = (id: number) => ({ id, kind: 'request', source: 'a', target: 'b', outcome: 'ok', attrs: {} }) as never;
    let fetches = 0;
    const client = {
      allTimeline: vi.fn(async () => {
        fetches++;
        return fetches === 1 ? { events: [ev(900), ev(901)], gaps: [] } : { events: [ev(1), ev(2)], gaps: [] };
      }),
    } as unknown as Client;
    act(() => store.selectOrder('del_1'));
    act(() => store.noteStreamId(901));
    let resetSeen = false;
    const unsub = store.subscribe(() => {
      if (store.getState().lastEventId === 2) resetSeen = true;
    });
    function Both() {
      useOrderTimeline(client);
      useLiveStream();
      return null;
    }
    const { unmount } = render(<Both />);
    await until(() => resetSeen && store.getState().events.map((e) => e.id).join() === '1,2');
    expect(calls[0].lastId).toBe('901');
    expect(fetches).toBe(2);
    expect(store.getState().lastEventId).toBe(2);
    unsub();
    unmount();
  });
});
