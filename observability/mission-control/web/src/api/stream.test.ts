import { describe, expect, it } from 'vitest';
import { openStream } from './stream';
import type { StreamMessage } from './types';

const enc = new TextEncoder();

function body(chunks: string[], hold = false): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(c) {
      for (const ch of chunks) c.enqueue(enc.encode(ch));
      if (!hold) c.close();
    },
  });
}

const ev = (id: number) =>
  JSON.stringify({ id, kind: 'span', source: 'gateway', target: 'tracking.ingest', outcome: 'ok', attrs: {} });

function until(cond: () => boolean): Promise<void> {
  return new Promise((resolve, reject) => {
    const t0 = Date.now();
    const tick = () => {
      if (cond()) return resolve();
      if (Date.now() - t0 > 2000) return reject(new Error('timeout'));
      setTimeout(tick, 2);
    };
    tick();
  });
}

describe('openStream', () => {
  it('passes a reset frame on and resumes from its (lower) id', async () => {
    const calls: (string | null)[] = [];
    const fetchImpl = (async (_url: string, init?: RequestInit) => {
      calls.push(new Headers(init?.headers).get('Last-Event-ID'));
      if (calls.length === 1) {
        return new Response(body([`id: 900\nevent: event\ndata: ${ev(900)}\n\n`, 'id: 3\nevent: reset\ndata: {"max":3}\n\n']), { status: 200 });
      }
      return new Response(body([], true), { status: 200 });
    }) as unknown as typeof fetch;
    const got: StreamMessage[] = [];
    const h = openStream(undefined, (m) => got.push(m), { fetchImpl, sleep: async () => {} });
    await until(() => calls.length === 2);
    h.close();
    expect(got.map((m) => [m.type, m.id])).toEqual([['event', 900], ['reset', 3]]);
    expect(got[1].data).toEqual({ max: 3 });
    expect(calls[1]).toBe('3');
  });

  it('streamResumesWithLastEventId', async () => {
    const calls: { url: string; lastId: string | null }[] = [];
    const fetchImpl = (async (url: string, init?: RequestInit) => {
      const h = new Headers(init?.headers);
      calls.push({ url, lastId: h.get('Last-Event-ID') });
      if (calls.length === 1) {
        return new Response(body([': connected\n\n', `id: 5\nevent: event\ndata: ${ev(5)}\n\n`, `id: 6\nevent: event\ndata: ${ev(6)}\n\n`]), { status: 200 });
      }
      return new Response(body([`id: 7\nevent: event\ndata: ${ev(7)}\n\n`], true), { status: 200 });
    }) as unknown as typeof fetch;
    const got: StreamMessage[] = [];
    const h = openStream('del_1', (m) => got.push(m), { fetchImpl, sleep: async () => {} });
    await until(() => got.length === 3);
    h.close();
    expect(calls[0].url).toBe('/api/stream?delivery_id=del_1');
    expect(calls[0].lastId).toBeNull();
    expect(calls[1].lastId).toBe('6');
    expect(got.map((m) => m.id)).toEqual([5, 6, 7]);
  });

  it('parses gap, bundle and detector messages and ignores keep-alive comments', async () => {
    const fetchImpl = (async () =>
      new Response(
        body([
          ': keep-alive\n\n',
          'id: 10\nevent: gap\ndata: {"from":3,"to":9}\n\n',
          'id: 11\nevent: bundle\ndata: {"wire":"a>b","count":42,"outcome":"ok"}\n\n',
          `id: 12\nevent: detector\ndata: ${ev(12)}\n\n`,
        ], true),
      )) as unknown as typeof fetch;
    const got: StreamMessage[] = [];
    const h = openStream(undefined, (m) => got.push(m), { fetchImpl });
    await until(() => got.length === 3);
    h.close();
    expect(got.map((m) => m.type)).toEqual(['gap', 'bundle', 'detector']);
    expect(got[1].data).toEqual({ wire: 'a>b', count: 42, outcome: 'ok' });
  });

  it('reassembles a message split across chunks and skips bad JSON', async () => {
    const full = `id: 2\nevent: event\ndata: ${ev(2)}\n\n`;
    const fetchImpl = (async () =>
      new Response(body(['id: 1\nevent: event\ndata: {nope\n\n', full.slice(0, 20), full.slice(20)], true))) as unknown as typeof fetch;
    const got: StreamMessage[] = [];
    const h = openStream(undefined, (m) => got.push(m), { fetchImpl });
    await until(() => got.length === 1);
    h.close();
    expect(got[0].id).toBe(2);
  });

  it('backs off exponentially, capped at 10 s, and resets after a message', async () => {
    const waits: number[] = [];
    let n = 0;
    const fetchImpl = (async () => {
      n++;
      if (n === 8) return new Response(body([`id: 1\nevent: event\ndata: ${ev(1)}\n\n`]));
      if (n === 9) return new Response(body([], true));
      throw new Error('down');
    }) as unknown as typeof fetch;
    const got: StreamMessage[] = [];
    const h = openStream(undefined, (m) => got.push(m), {
      fetchImpl,
      sleep: async (ms) => {
        waits.push(ms);
      },
    });
    await until(() => n >= 9);
    h.close();
    expect(waits.slice(0, 7)).toEqual([500, 1000, 2000, 4000, 8000, 10000, 10000]);
    expect(waits[7]).toBe(500); // reset once a message arrived
  });

  it('close stops reconnecting', async () => {
    let n = 0;
    const fetchImpl = (async () => {
      n++;
      throw new Error('down');
    }) as unknown as typeof fetch;
    const h = openStream(undefined, () => {}, { fetchImpl, sleep: () => new Promise((r) => setTimeout(r, 5)) });
    await until(() => n >= 2);
    h.close();
    const at = n;
    await new Promise((r) => setTimeout(r, 40));
    expect(n).toBeLessThanOrEqual(at + 1);
  });

  it('resets the backoff when a connection opens, even with no data frame', async () => {
    const waits: number[] = [];
    let n = 0;
    const fetchImpl = (async () => {
      n++;
      if (n <= 3) throw new Error('down');
      if (n === 4) return new Response(body([': connected\n\n'])); // opens, sends only a comment, ends
      return new Response(body([], true));
    }) as unknown as typeof fetch;
    const h = openStream(undefined, () => {}, { fetchImpl, sleep: async (ms) => void waits.push(ms) });
    await until(() => n >= 5);
    h.close();
    expect(waits.slice(0, 4)).toEqual([500, 1000, 2000, 500]);
  });

  it('reconnects when no bytes arrive within the idle limit, and keep-alives count as bytes', async () => {
    let n = 0;
    const lastIds: (string | null)[] = [];
    const fetchImpl = (async (_u: string, init?: RequestInit) => {
      n++;
      lastIds.push(new Headers(init?.headers).get('Last-Event-ID'));
      const signal = init?.signal as AbortSignal;
      if (n === 1) {
        // one data frame, then silence: the connection must be dropped by the watchdog
        return new Response(
          new ReadableStream({
            start(c) {
              c.enqueue(enc.encode(`id: 4\nevent: event\ndata: ${ev(4)}\n\n`));
              signal.addEventListener('abort', () => c.error(new Error('aborted')));
            },
          }),
        );
      }
      return new Response(body([], true));
    }) as unknown as typeof fetch;
    const h = openStream(undefined, () => {}, { fetchImpl, sleep: async () => {}, idleMs: 40 });
    await until(() => n >= 2);
    h.close();
    expect(lastIds[1]).toBe('4');
  });

  it('keep-alive comments keep a quiet connection open', async () => {
    let n = 0;
    const fetchImpl = (async (_u: string, init?: RequestInit) => {
      n++;
      const signal = init?.signal as AbortSignal;
      return new Response(
        new ReadableStream({
          start(c) {
            const t = setInterval(() => c.enqueue(enc.encode(': keep-alive\n\n')), 15);
            signal.addEventListener('abort', () => {
              clearInterval(t);
              c.error(new Error('aborted'));
            });
          },
        }),
      );
    }) as unknown as typeof fetch;
    const h = openStream(undefined, () => {}, { fetchImpl, sleep: async () => {}, idleMs: 60 });
    await new Promise((r) => setTimeout(r, 250));
    h.close();
    expect(n).toBe(1);
  });

  it('drops the connection when one frame outgrows the buffer cap', async () => {
    let n = 0;
    const fetchImpl = (async () => {
      n++;
      if (n === 1) return new Response(body(['data: ' + 'x'.repeat(5000)], true)); // never terminated
      return new Response(body([], true));
    }) as unknown as typeof fetch;
    const h = openStream(undefined, () => {}, { fetchImpl, sleep: async () => {}, maxBuffer: 1024 });
    await until(() => n >= 2);
    h.close();
    expect(n).toBeGreaterThanOrEqual(2);
  });
});
