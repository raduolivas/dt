import type { TrackingSnapshot } from '@dt/contract';

export type ServerMessage =
  | { type: 'snapshot'; data: TrackingSnapshot }
  | { type: 'revoked' }
  | { type: 'ping' | 'status' };

export interface StreamHandlers {
  onOpen(): void;
  onMessage(m: ServerMessage): void;
  /** `code` is the WebSocket close code, or the equivalent for SSE (4401 for 401, 1013 for 429/503), else null. */
  onClose(info: { code: number | null; opened: boolean }): void;
}

export interface StreamConn {
  close(): void;
  /** Swap the token on an open stream. A no-op for SSE, which reconnects with the new token. */
  sendToken(token: string): void;
}

function parse(text: string): ServerMessage | null {
  try {
    const m = JSON.parse(text) as { type?: unknown; data?: { version?: unknown } };
    if (!m || typeof m.type !== 'string') return null;
    if (m.type === 'snapshot') {
      return typeof m.data?.version === 'number' ? (m as ServerMessage) : null;
    }
    if (m.type === 'revoked' || m.type === 'ping' || m.type === 'status') return m as ServerMessage;
    return null;
  } catch {
    return null;
  }
}

export function openWebSocket(
  url: string,
  WS: typeof WebSocket,
  token: string,
  h: StreamHandlers,
): StreamConn {
  const ws = new WS(url);
  let opened = false;
  let done = false;
  const finish = (code: number | null) => {
    if (done) return;
    done = true;
    h.onClose({ code, opened });
  };
  ws.onopen = () => {
    opened = true;
    ws.send(JSON.stringify({ type: 'auth', token }));
    h.onOpen();
  };
  ws.onmessage = (ev) => {
    const m = typeof ev.data === 'string' ? parse(ev.data) : null;
    if (m) h.onMessage(m);
  };
  ws.onerror = () => finish(null);
  ws.onclose = (ev) => finish(ev.code);
  return {
    close() {
      done = true;
      ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null;
      ws.close();
    },
    sendToken(t) {
      if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'auth', token: t }));
    },
  };
}

/** SSE over fetch: the token goes in a header, never in the URL. */
export function openSse(url: string, f: typeof fetch, token: string, h: StreamHandlers): StreamConn {
  const ctl = new AbortController();
  let opened = false;
  let done = false;
  const finish = (code: number | null) => {
    if (done) return;
    done = true;
    h.onClose({ code, opened });
  };
  void (async () => {
    try {
      const res = await f(url, {
        headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
        signal: ctl.signal,
      });
      if (!res.ok || !res.body) {
        finish(res.status === 401 ? 4401 : res.status === 429 || res.status === 503 ? 1013 : null);
        return;
      }
      opened = true;
      h.onOpen();
      const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
      let buf = '';
      for (;;) {
        const { value, done: end } = await reader.read();
        if (end) break;
        buf += value.replace(/\r\n?/g, '\n');
        let i: number;
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i);
          buf = buf.slice(i + 2);
          const data = block
            .split('\n')
            .filter((l) => l.startsWith('data:'))
            .map((l) => l.slice(5).replace(/^ /, ''))
            .join('\n');
          const m = data ? parse(data) : null;
          if (m) h.onMessage(m);
        }
      }
      finish(null);
    } catch {
      finish(null);
    }
  })();
  return {
    close() {
      done = true;
      ctl.abort();
    },
    sendToken() {},
  };
}
