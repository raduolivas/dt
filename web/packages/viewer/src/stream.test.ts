import { describe, expect, it, vi } from 'vitest';
import { openSse, openWebSocket } from './stream';
import type { ServerMessage } from './stream';

const handlers = () => {
  const msgs: ServerMessage[] = [];
  const closes: Array<{ code: number | null; opened: boolean }> = [];
  return { msgs, closes, h: { onOpen: vi.fn(), onMessage: (m: ServerMessage) => msgs.push(m), onClose: (i: { code: number | null; opened: boolean }) => closes.push(i) } };
};
const sseFetch = (chunks: string[], status = 200) =>
  (async () =>
    new Response(
      new ReadableStream<Uint8Array>({
        start(c) {
          for (const ch of chunks) c.enqueue(new TextEncoder().encode(ch));
          c.close();
        },
      }),
      { status },
    )) as unknown as typeof fetch;
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('openSse', () => {
  it('parses events split across chunks, ignores unknown and malformed ones, and reports the end', async () => {
    const { msgs, closes, h } = handlers();
    const snap = JSON.stringify({ type: 'snapshot', data: { version: 3 } });
    openSse('https://x/s', sseFetch([`event: snapshot\r\ndata: ${snap.slice(0, 10)}`, `${snap.slice(10)}\r\n\r\n`, 'event: ping\ndata: {"type":"ping"}\n\n', 'data: not json\n\n', 'data: {"type":"other"}\n\n']), 't', h);
    await flush();
    expect(msgs).toEqual([{ type: 'snapshot', data: { version: 3 } }, { type: 'ping' }]);
    expect(closes).toEqual([{ code: null, opened: true }]);
  });

  it('maps 401 to close code 4401 and 503 to 1013', async () => {
    for (const [status, code] of [[401, 4401], [503, 1013]] as const) {
      const { closes, h } = handlers();
      openSse('https://x/s', sseFetch([], status), 't', h);
      await flush();
      expect(closes).toEqual([{ code, opened: false }]);
    }
  });
});

describe('openWebSocket', () => {
  it('ignores malformed frames and reports one close', () => {
    class WS {
      readyState = 0;
      onopen: (() => void) | null = null;
      onmessage: ((e: { data: unknown }) => void) | null = null;
      onerror: (() => void) | null = null;
      onclose: ((e: { code: number }) => void) | null = null;
      send() {}
      close() {}
    }
    const made: WS[] = [];
    const Ctor = class extends WS { constructor() { super(); made.push(this); } };
    const { msgs, closes, h } = handlers();
    openWebSocket('wss://x', Ctor as unknown as typeof WebSocket, 't', h);
    const sock = made[0]!;
    sock.onmessage!({ data: 'garbage' });
    sock.onmessage!({ data: new ArrayBuffer(1) });
    sock.onmessage!({ data: '{"type":"snapshot","data":{}}' });
    sock.onerror!();
    sock.onclose!({ code: 1006 });
    expect(msgs).toEqual([]);
    expect(closes).toEqual([{ code: null, opened: false }]);
  });
});
