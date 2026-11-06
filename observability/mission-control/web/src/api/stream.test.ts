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
});
