import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TrackingSnapshot } from '@dt/contract';
import { createViewer } from './client';
import type { ViewerError, ViewerState } from './types';

const API = 'https://api.test/v1';
const LINK = 'LINK-TOKEN-SECRET';
const T0 = Date.parse('2026-10-22T14:00:00Z');

const snap = (version: number) => ({ version, status: 'out_for_delivery' }) as unknown as TrackingSnapshot;
const iso = (ms: number) => new Date(ms).toISOString();
const json = (body: unknown, init: ResponseInit = {}) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' }, ...init });
const session = (token: string, expMs: number, version = 1) =>
  json({ viewer_token: token, expires_at: iso(expMs), snapshot: snap(version), branding: null });

class FakeWS {
  static instances: FakeWS[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  constructor(public url: string) {
    FakeWS.instances.push(this);
  }
  send(s: string) {
    this.sent.push(s);
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  message(m: unknown) {
    this.onmessage?.({ data: JSON.stringify(m) });
  }
  closeWith(code: number) {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}

type Call = { method: string; path: string; auth: string | null; url: string };
function harness(routes: Record<string, () => Response | Promise<Response>>) {
  const calls: Call[] = [];
  const f = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const path = url.replace(API, '');
    const method = init?.method ?? 'GET';
    calls.push({ method, path, url, auth: new Headers(init?.headers).get('Authorization') });
    const r = routes[`${method} ${path}`];
    if (!r) throw new Error(`unrouted ${method} ${path}`);
    return r();
  });
  return { f: f as unknown as typeof fetch, calls };
}

let clock = T0;
function make(routes: Record<string, () => Response | Promise<Response>>) {
  const h = harness(routes);
  const viewer = createViewer({
    api: API,
    token: LINK,
    fetch: h.f,
    WebSocket: FakeWS as unknown as typeof WebSocket,
    now: () => clock,
  });
  const states: ViewerState[] = [];
  const errors: ViewerError[] = [];
  viewer.onState((s) => states.push(s));
  viewer.onError((e) => errors.push(e));
  return { viewer, states, errors, ...h };
}
const last = (s: ViewerState[]) => s[s.length - 1];
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, 'random').mockReturnValue(0.5);
  clock = T0;
  FakeWS.instances = [];
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }));
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('session and websocket', () => {
  it('exchanges the link token, then authenticates the socket with the viewer token as its first message', async () => {
    const { viewer, states, calls } = make({ 'POST /tracking/session': () => session('VT1', T0 + 900_000) });
    viewer.start();
    await tick();
    expect(last(states)).toMatchObject({ kind: 'live', snapshot: { version: 1 }, branding: null });
    expect(FakeWS.instances).toHaveLength(1);
    expect(FakeWS.instances[0]!.url).toBe('wss://api.test/v1/tracking/stream');
    FakeWS.instances[0]!.open();
    expect(FakeWS.instances[0]!.sent).toEqual([JSON.stringify({ type: 'auth', token: 'VT1' })]);
    expect(calls[0]).toMatchObject({ method: 'POST', path: '/tracking/session', auth: null });
  });

  it('falls back to SSE over fetch with a bearer header when the socket fails to open', async () => {
    const enc = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(enc.encode(`event: snapshot\ndata: ${JSON.stringify({ type: 'snapshot', data: snap(2) })}\n\n`));
      },
    });
    const { viewer, states, calls } = make({
      'POST /tracking/session': () => session('VT1', T0 + 900_000),
      'GET /tracking/stream/sse': () => new Response(body, { headers: { 'Content-Type': 'text/event-stream' } }),
      'GET /tracking/snapshot': () => json(snap(1)),
    });
    viewer.start();
    await tick();
    FakeWS.instances[0]!.closeWith(1006);
    await tick();
    const sse = calls.find((c) => c.path === '/tracking/stream/sse');
    expect(sse).toMatchObject({ auth: 'Bearer VT1' });
    expect(sse!.url).not.toContain('VT1');
    expect(last(states)).toMatchObject({ kind: 'live', snapshot: { version: 2 } });
  });
});

describe('503 warming', () => {
  it('is warming while the session answers 503 with Retry-After 1, then live', async () => {
    let n = 0;
    const { viewer, states, calls } = make({
      'POST /tracking/session': () =>
        ++n < 3
          ? new Response('{}', { status: 503, headers: { 'Retry-After': '1' } })
          : session('VT1', T0 + 900_000),
    });
    viewer.start();
    await tick();
    expect(last(states)).toEqual({ kind: 'warming' });
    await tick(1000);
    expect(calls).toHaveLength(2);
    await tick(1000);
    expect(last(states)).toMatchObject({ kind: 'live' });
  });

  it('gives up with inactive after 5 s of 503', async () => {
    const { viewer, states } = make({
      'POST /tracking/session': () => new Response('{}', { status: 503, headers: { 'Retry-After': '1' } }),
    });
    viewer.start();
    for (let i = 0; i < 6; i++) {
      await tick(1000);
      clock += 1000;
    }
    expect(last(states)).toEqual({ kind: 'inactive', last: null });
  });

  it('401 link_invalid is inactive at once', async () => {
    const { viewer, states } = make({ 'POST /tracking/session': () => new Response('{}', { status: 401 }) });
    viewer.start();
    await tick();
    expect(last(states)).toEqual({ kind: 'inactive', last: null });
  });
});

describe('snapshots', () => {
  it('ignores a snapshot with an equal or lower version', async () => {
    const { viewer, states } = make({ 'POST /tracking/session': () => session('VT1', T0 + 900_000, 5) });
    viewer.start();
    await tick();
    const ws = FakeWS.instances[0]!;
    ws.open();
    const before = states.length;
    ws.message({ type: 'snapshot', data: snap(5) });
    ws.message({ type: 'snapshot', data: snap(4) });
    expect(states).toHaveLength(before);
    ws.message({ type: 'snapshot', data: snap(6) });
    expect(last(states)).toMatchObject({ kind: 'live', snapshot: { version: 6 } });
  });

  it('revoked moves to inactive with the last snapshot', async () => {
    const { viewer, states } = make({ 'POST /tracking/session': () => session('VT1', T0 + 900_000, 5) });
    viewer.start();
    await tick();
    const ws = FakeWS.instances[0]!;
    ws.open();
    ws.message({ type: 'ping' });
    ws.message({ type: 'revoked' });
    expect(last(states)).toEqual({ kind: 'inactive', last: snap(5) });
  });

  it('close code 4401 is inactive and does not reconnect', async () => {
    const { viewer, states } = make({ 'POST /tracking/session': () => session('VT1', T0 + 900_000) });
    viewer.start();
    await tick();
    FakeWS.instances[0]!.open();
    FakeWS.instances[0]!.closeWith(4401);
    await tick(60_000);
    expect(last(states)).toMatchObject({ kind: 'inactive' });
    expect(FakeWS.instances).toHaveLength(1);
  });

  it('close code 1012 reconnects with backoff and resyncs from the snapshot endpoint', async () => {
    const { viewer, states, calls } = make({
      'POST /tracking/session': () => session('VT1', T0 + 900_000, 1),
      'GET /tracking/snapshot': () => json(snap(9)),
    });
    viewer.start();
    await tick();
    FakeWS.instances[0]!.open();
    FakeWS.instances[0]!.closeWith(1012);
    await tick(500);
    expect(FakeWS.instances).toHaveLength(2);
    FakeWS.instances[1]!.open();
    await tick();
    const resyncs = calls.filter((c) => c.path === '/tracking/snapshot');
    expect(resyncs).toHaveLength(2); // one per socket open
    expect(resyncs[1]!.auth).toBe('Bearer VT1');
    expect(last(states)).toMatchObject({ kind: 'live', snapshot: { version: 9 } });
  });

  it('backs off exponentially up to 30 s with full jitter', async () => {
    const { viewer } = make({
      'POST /tracking/session': () => session('VT1', T0 + 90_000_000),
      'GET /tracking/snapshot': () => json(snap(1)),
    });
    viewer.start();
    await tick();
    FakeWS.instances[0]!.open();
    FakeWS.instances[0]!.closeWith(1013);
    await tick(249);
    expect(FakeWS.instances).toHaveLength(1); // 0.5 * 500 ms
    await tick(2);
    expect(FakeWS.instances).toHaveLength(2);
    FakeWS.instances[1]!.open();
    FakeWS.instances[1]!.closeWith(1013);
    await tick(499);
    expect(FakeWS.instances).toHaveLength(2); // 0.5 * 1000 ms
    await tick(2);
    expect(FakeWS.instances).toHaveLength(3);
  });
});

describe('refresh', () => {
  it('refreshes 120 s before expiry and sends the new token on the open socket', async () => {
    const { viewer, calls } = make({
      'POST /tracking/session': () => session('VT1', T0 + 900_000),
      'POST /tracking/session/refresh': () => json({ viewer_token: 'VT2', expires_at: iso(T0 + 1_800_000) }),
    });
    viewer.start();
    await tick();
    const ws = FakeWS.instances[0]!;
    ws.open();
    await tick(779_000);
    expect(calls.some((c) => c.path === '/tracking/session/refresh')).toBe(false);
    clock += 781_000;
    await tick(2_000);
    expect(calls.find((c) => c.path === '/tracking/session/refresh')!.auth).toBe('Bearer VT1');
    expect(ws.sent.at(-1)).toBe(JSON.stringify({ type: 'auth', token: 'VT2' }));
  });

  it('refresh 401 falls back to a new session from the in-memory link token', async () => {
    let sessions = 0;
    const { viewer, states, calls } = make({
      'GET /tracking/snapshot': () => json(snap(1)),
      'POST /tracking/session': () => session(`VT${++sessions}`, clock + 900_000),
      'POST /tracking/session/refresh': () => new Response('{}', { status: 401 }),
    });
    viewer.start();
    await tick();
    FakeWS.instances[0]!.open();
    clock += 781_000;
    await tick(781_000);
    expect(calls.filter((c) => c.path === '/tracking/session')).toHaveLength(2);
    expect(last(states)).toMatchObject({ kind: 'live' });
  });

  it('resumes after the token expired while hidden', async () => {
    let sessions = 0;
    const { viewer, states, errors, calls } = make({
      'POST /tracking/session': () => session(`VT${++sessions}`, clock + 900_000, sessions),
      'POST /tracking/session/refresh': () => new Response('{}', { status: 401 }),
      'GET /tracking/snapshot': () => json(snap(10)),
    });
    viewer.start();
    await tick();
    const ws = FakeWS.instances[0]!;
    ws.open();
    clock += 2_000_000; // hidden for a long time; timers were throttled
    document.dispatchEvent(new Event('visibilitychange'));
    await tick();
    expect(calls.filter((c) => c.path === '/tracking/session')).toHaveLength(2);
    expect(calls.filter((c) => c.path === '/tracking/snapshot').at(-1)!.auth).toBe('Bearer VT2');
    expect(ws.sent.at(-1)).toBe(JSON.stringify({ type: 'auth', token: 'VT2' }));
    expect(last(states)).toMatchObject({ kind: 'live', snapshot: { version: 10 } });
    expect(errors).toEqual([]);
    viewer.stop();
  });

  it('a revoked link found on resume is inactive', async () => {
    let n = 0;
    const { viewer, states } = make({
      'POST /tracking/session': () => (++n === 1 ? session('VT1', T0 + 900_000) : new Response('{}', { status: 401 })),
      'POST /tracking/session/refresh': () => new Response('{}', { status: 401 }),
    });
    viewer.start();
    await tick();
    FakeWS.instances[0]!.open();
    clock += 2_000_000;
    document.dispatchEvent(new Event('visibilitychange'));
    await tick();
    expect(last(states)).toMatchObject({ kind: 'inactive' });
  });
});

describe('link token secrecy', () => {
  it('never reaches storage, an error payload, or the console', async () => {
    const storage = { setItem: vi.fn(), getItem: vi.fn() };
    vi.stubGlobal('localStorage', storage);
    vi.stubGlobal('sessionStorage', storage);
    const spies = (['log', 'warn', 'error', 'info', 'debug'] as const).map((k) => vi.spyOn(console, k));
    let n = 0;
    const { viewer, errors } = make({
      'POST /tracking/session': () => (++n === 1 ? new Response('boom', { status: 500 }) : session('VT1', T0 + 900_000)),
    });
    viewer.start();
    await tick(2_000);
    expect(errors.length).toBeGreaterThan(0);
    expect(JSON.stringify(errors)).not.toContain(LINK);
    expect(storage.setItem).not.toHaveBeenCalled();
    for (const s of spies) expect(s).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
