import type { StreamMessage } from './types';

export interface StreamOptions {
  fetchImpl?: typeof fetch;
  /** Resume point for the first connection. */
  lastEventId?: number;
  /** Wait between reconnects; defaults to setTimeout. */
  sleep?: (ms: number) => Promise<void>;
  onStatus?: (s: 'connecting' | 'open' | 'closed') => void;
}

export interface StreamHandle {
  close(): void;
}

export const BACKOFF_START_MS = 500;
export const BACKOFF_MAX_MS = 10_000;

const TYPES = new Set(['event', 'detector', 'gap', 'bundle']);

/** One parsed SSE frame. Comment lines (": keep-alive") are dropped. */
interface Frame {
  id: string | null;
  event: string;
  data: string;
}

/** Splits an SSE byte stream into frames; keeps partial input between chunks. */
export class FrameParser {
  private buf = '';
  push(chunk: string): Frame[] {
    this.buf += chunk;
    const out: Frame[] = [];
    for (;;) {
      const m = /\r?\n\r?\n/.exec(this.buf);
      if (!m) break;
      const raw = this.buf.slice(0, m.index);
      this.buf = this.buf.slice(m.index + m[0].length);
      const f: Frame = { id: null, event: 'message', data: '' };
      const data: string[] = [];
      for (const line of raw.split(/\r?\n/)) {
        if (line === '' || line.startsWith(':')) continue;
        const i = line.indexOf(':');
        const field = i < 0 ? line : line.slice(0, i);
        const value = i < 0 ? '' : line.slice(i + 1).replace(/^ /, '');
        if (field === 'id') f.id = value;
        else if (field === 'event') f.event = value;
        else if (field === 'data') data.push(value);
      }
      f.data = data.join('\n');
      if (data.length > 0 || f.id !== null) out.push(f);
    }
    return out;
  }
}

/**
 * Opens the live stream (all events, or one delivery's). It reconnects for as
 * long as it is open, sends Last-Event-ID (the newest id received) on every
 * reconnect, and backs off 0.5 s, 1 s, 2 s ... capped at 10 s; the delay
 * resets once a message arrives.
 */
export function openStream(
  deliveryId: string | undefined,
  onMsg: (m: StreamMessage) => void,
  opts: StreamOptions = {},
): StreamHandle {
  const doFetch = opts.fetchImpl ?? ((...a: Parameters<typeof fetch>) => fetch(...a));
  const sleep = opts.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const ctl = new AbortController();
  let last = opts.lastEventId ?? null;
  let delay = BACKOFF_START_MS;
  const url = '/api/stream' + (deliveryId ? `?delivery_id=${encodeURIComponent(deliveryId)}` : '');

  async function once(): Promise<void> {
    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    if (last !== null) headers['Last-Event-ID'] = String(last);
    opts.onStatus?.('connecting');
    const res = await doFetch(url, { headers, signal: ctl.signal, cache: 'no-store' });
    if (!res.ok || !res.body) throw new Error(`stream: HTTP ${res.status}`);
    opts.onStatus?.('open');
    const parser = new FrameParser();
    const dec = new TextDecoder();
    const reader = res.body.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      for (const f of parser.push(dec.decode(value, { stream: true }))) {
        const id = f.id === null ? NaN : Number(f.id);
        if (Number.isFinite(id)) last = id;
        if (!TYPES.has(f.event)) continue;
        let data: unknown;
        try {
          data = JSON.parse(f.data);
        } catch {
          continue; // a malformed frame is skipped, never fatal
        }
        delay = BACKOFF_START_MS;
        onMsg({ type: f.event, id, data } as StreamMessage);
      }
    }
  }

  void (async () => {
    while (!ctl.signal.aborted) {
      try {
        await once();
      } catch {
        // fall through to the back-off
      }
      if (ctl.signal.aborted) break;
      opts.onStatus?.('closed');
      const wait = delay;
      delay = Math.min(delay * 2, BACKOFF_MAX_MS);
      await sleep(wait);
    }
  })();

  return { close: () => ctl.abort() };
}
