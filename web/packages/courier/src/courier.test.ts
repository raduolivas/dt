import { IDBFactory } from 'fake-indexeddb';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createCourier, CourierError } from './index';
import type { CourierNotice, CourierOptions } from './index';
import { ACCESS_MS, delivery, fakeApi, fakeGeolocation, json, memoryStorage, problem, T0, tick } from './testkit';

let api: ReturnType<typeof fakeApi>;
let geo: ReturnType<typeof fakeGeolocation>;
let storage: ReturnType<typeof memoryStorage>;
let idb: IDBFactory;

beforeEach(() => {
  // fake-indexeddb schedules with setImmediate and microtasks: leave those real
  vi.useFakeTimers({ now: T0, toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  api = fakeApi();
  geo = fakeGeolocation();
  storage = memoryStorage();
  idb = new IDBFactory();
});
afterEach(() => vi.useRealTimers());

const make = (over: Partial<CourierOptions> = {}) =>
  createCourier({
    api: 'https://api.test/v1',
    linkCode: 'code-1',
    storage,
    fetch: api.fetch,
    geolocation: geo.geo,
    now: Date.now,
    indexedDB: idb,
    ...over,
  });

/** Start a courier and let its first poll land, so GPS is running when there is a delivery. */
async function started(over: Partial<CourierOptions> = {}) {
  const c = make(over);
  await c.start();
  await tick(0);
  return c;
}
const notices = (c: ReturnType<typeof make>) => {
  const out: CourierNotice[] = [];
  c.on('notice', (n) => out.push(n));
  return out;
};

describe('sampling', () => {
  it('drops inaccurate fixes', async () => {
    await started();
    geo.fix(51);
    geo.fix(50);
    await tick(3000);
    const sent = api.paths('/courier/locations');
    expect(sent).toHaveLength(1);
    expect(sent[0].body.fixes).toHaveLength(1);
    expect(sent[0].body.fixes[0].accuracy_m).toBe(50);
  });

  it('takes the fix time from the position, not the clock', async () => {
    await started();
    geo.fix(10, T0 - 4000);
    await tick(3000);
    expect(api.paths('/courier/locations')[0].body.fixes[0].t).toBe(new Date(T0 - 4000).toISOString());
  });

  it('batches every 3 s or at 50', async () => {
    await started();
    geo.fix(10);
    geo.fix(10);
    await tick(2999);
    expect(api.paths('/courier/locations')).toHaveLength(0);
    await tick(1);
    expect(api.paths('/courier/locations')).toHaveLength(1);
    expect(api.paths('/courier/locations')[0].body.fixes).toHaveLength(2);

    for (let i = 0; i < 50; i++) geo.fix(10);
    await tick(0); // no timer needed: the 50th fix sends
    expect(api.paths('/courier/locations')).toHaveLength(2);
    expect(api.paths('/courier/locations')[1].body.fixes).toHaveLength(50);
  });

  it('no watchPosition before start()', async () => {
    const c = make();
    await tick(60_000);
    expect(geo.watches).toBe(0);
    expect(api.calls).toHaveLength(0);
    await c.start();
    await tick(0);
    expect(geo.watches).toBe(1);
  });

  it('asks for permission once at start with an empty list, and never watches or sends', async () => {
    api.deliveries = [];
    const c = make();
    expect(c.state.gps).toBe('off');
    await c.start();
    expect(geo.asks).toBe(1);
    expect(c.state.gps).toBe('off'); // allowed: asking is over and nothing is being collected
    await tick(60_000);
    expect(geo.watches).toBe(0);
    expect(c.state.buffered).toBe(0);
    expect(api.paths('/courier/locations')).toHaveLength(0);
  });

  it('shows asking while the prompt is open, and denied when it is refused', async () => {
    api.deliveries = [];
    geo.askAnswer = 'denied';
    const c = make();
    const seen: string[] = [];
    c.on('state', (s) => seen.push(s.gps));
    await c.start();
    expect(seen.filter((g, i) => g !== seen[i - 1])).toEqual(['off', 'asking', 'denied']);
    expect(c.state.gps).toBe('denied');
  });

  it('the permission fix is never buffered or sent', async () => {
    await started(); // a delivery exists: the watch runs, but the one-off reading must not reach the server
    geo.fix(10, Date.now(), 52.37);
    await tick(3000);
    const fixes = api.paths('/courier/locations').flatMap((c) => c.body.fixes);
    expect(fixes).toHaveLength(1);
    expect(fixes[0].lat).toBe(52.37); // not the permission reading (lat 1)
    expect(geo.asks).toBe(1);
  });

  it('stops watching when deliveries empty and resumes when one appears', async () => {
    const c = await started();
    expect(geo.watching).toBe(true);
    api.deliveries = [];
    await tick(10_000);
    expect(c.deliveries).toHaveLength(0);
    expect(geo.watching).toBe(false);
    expect(c.state.gps).toBe('off');

    api.deliveries = [delivery('del_2')];
    await tick(19_999);
    expect(geo.watching).toBe(false); // an empty list is checked every 20 s
    await tick(1);
    expect(geo.watching).toBe(true);
    expect(geo.watches).toBe(2);
  });

  it('polls every 10 s while there is work', async () => {
    await started();
    await tick(30_000);
    expect(api.paths('/courier/deliveries')).toHaveLength(4); // at start, then every 10 s
  });

  it('reports a denied permission', async () => {
    const c = await started();
    geo.deny();
    expect(c.state.gps).toBe('denied');
  });
});

describe('session end', () => {
  it('stops on 401 and discards buffer', async () => {
    api.offline = false;
    const c = await started();
    const seen = notices(c);
    api.offline = true;
    geo.fix(10);
    geo.fix(10);
    await tick(3000);
    expect(c.state.buffered).toBe(2);
    expect(c.state.network).toBe('offline');
    const pending = c.report('del_1', 'at_pickup').catch((e) => e);

    api.offline = false;
    api.handle('POST /courier/deliveries/:id/events', () => problem(401, 'session_ended'));
    await tick(3000);

    expect(c.state.session).toBe('ended');
    expect(c.state.buffered).toBe(0);
    expect(c.state.pendingTaps).toBe(0);
    expect(c.state.gps).toBe('off');
    expect(geo.watching).toBe(false);
    expect(seen).toEqual([{ kind: 'ended' }]);
    expect(storage.data.size).toBe(0);
    expect(c.deliveries).toHaveLength(0);
    expect(await pending).toBeInstanceOf(CourierError);

    const before = api.calls.length;
    await tick(120_000);
    expect(api.calls.length).toBe(before); // no retry loop
    expect(api.paths('/courier/locations')).toHaveLength(0); // the fixes were never sent
  });

  it('reports expired when a 401 comes after the session expiry', async () => {
    api.sessionEnd = T0 + ACCESS_MS * 2;
    const c = await started();
    api.handle('GET /courier/deliveries', () => problem(401, 'unauthenticated'));
    vi.setSystemTime(T0 + ACCESS_MS * 2 + 1000);
    await tick(10_000);
    expect(c.state.session).toBe('expired');
  });

  it('end() calls session/end, clears storage, stops GPS', async () => {
    const c = await started();
    expect(storage.data.size).toBe(1);
    await c.end();
    const end = api.paths('/courier/session/end');
    expect(end).toHaveLength(1);
    expect(end[0].auth).toBe('Bearer access-1');
    expect(storage.data.size).toBe(0);
    expect(geo.watching).toBe(false);
    expect(c.state.session).toBe('ended');
    const before = api.calls.length;
    await tick(60_000);
    expect(api.calls.length).toBe(before);
  });

  it('end() still clears everything when the network is down', async () => {
    const c = await started();
    api.offline = true;
    await c.end();
    expect(storage.data.size).toBe(0);
    expect(c.state.session).toBe('ended');
  });
});

describe('link code', () => {
  it('rejects start() with the problem code for a used link', async () => {
    api.handle('POST /courier/token', () => problem(401, 'link_code_used'));
    const c = make();
    await expect(c.start()).rejects.toMatchObject({ code: 'link_code_used' });
    expect(c.state.session).toBe('ended');
    expect(geo.watches).toBe(0);
  });

  it('rejects start() for an invalid link', async () => {
    api.handle('POST /courier/token', () => problem(401, 'link_code_invalid'));
    await expect(make().start()).rejects.toMatchObject({ code: 'link_code_invalid' });
  });

  it('reload resumes from storage without exchanging the code again', async () => {
    const first = await started();
    expect(api.calls.filter((c) => c.body?.grant_type === 'link_code')).toHaveLength(1);
    // the tab dies; the page opens the same address again
    void first;
    const second = await started();
    expect(api.calls.filter((c) => c.body?.grant_type === 'link_code')).toHaveLength(1);
    expect(second.state.session).toBe('active');
    expect(api.paths('/courier/deliveries').at(-1)!.auth).toBe('Bearer access-1');
  });

  it('a reload after a refresh resumes with the rotated pair', async () => {
    await started();
    await tick(ACCESS_MS - 60_000);
    expect(api.calls.filter((c) => c.body?.grant_type === 'refresh_token')).toHaveLength(1);
    const second = await started();
    expect(api.calls.filter((c) => c.body?.grant_type === 'link_code')).toHaveLength(1);
    expect(second.state.session).toBe('active');
    expect(api.calls.filter((c) => c.body?.grant_type === 'refresh_token')).toHaveLength(1);
  });

  it('exchanges a different code even when storage holds an older shift', async () => {
    await started();
    await started({ linkCode: 'code-2' });
    expect(api.calls.filter((c) => c.body?.grant_type === 'link_code').map((c) => c.body.code)).toEqual([
      'code-1',
      'code-2',
    ]);
  });

  it('resumes with no code or tokens given, and fails with no_session when storage is empty', async () => {
    await started();
    const c = await started({ linkCode: undefined });
    expect(c.state.session).toBe('active');
    storage.data.clear();
    await expect(make({ linkCode: undefined }).start()).rejects.toMatchObject({ code: 'no_session' });
  });

  it('SDK partners start from a given pair', async () => {
    const tokens = api.tokens();
    const c = await started({ linkCode: undefined, tokens });
    expect(api.calls.some((x) => x.path === '/courier/token')).toBe(false);
    expect(api.paths('/courier/deliveries')[0].auth).toBe(`Bearer ${tokens.access_token}`);
    expect(c.state.session).toBe('active');
  });
});

describe('token refresh', () => {
  it('refreshes 60 s before the access token expires', async () => {
    await started();
    await tick(ACCESS_MS - 61_000);
    expect(api.calls.filter((c) => c.body?.grant_type === 'refresh_token')).toHaveLength(0);
    await tick(1000);
    const r = api.calls.filter((c) => c.body?.grant_type === 'refresh_token');
    expect(r).toHaveLength(1);
    expect(r[0].body.refresh_token).toBe('refresh-1');
    expect(JSON.parse(storage.data.get('dt.courier.session')!).tokens.refresh_token).toBe('refresh-2');
  });

  it('concurrent refresh triggers make one refresh call', async () => {
    const c = await started();
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    api.handle('POST /courier/token', async () => {
      await gate;
      return json(200, api.tokens());
    });
    // the timer fires, and at the same moment the page comes back to the foreground and a poll wants a token
    await tick(ACCESS_MS - 60_000);
    document.dispatchEvent(new Event('visibilitychange'));
    await tick(10_000);
    expect(api.calls.filter((x) => x.body?.grant_type === 'refresh_token')).toHaveLength(1);
    release();
    await tick(0);
    expect(api.calls.filter((x) => x.body?.grant_type === 'refresh_token')).toHaveLength(1);
    await tick(10_000);
    expect(api.paths('/courier/deliveries').at(-1)!.auth).toBe('Bearer access-2');
    expect(c.state.session).toBe('active');
  });

  it('a network error during refresh retries the same refresh token', async () => {
    const c = await started();
    let n = 0;
    api.handle('POST /courier/token', () => {
      n++;
      throw new TypeError('network');
    });
    await tick(ACCESS_MS - 60_000);
    await tick(1000);
    const refreshes = api.calls.filter((x) => x.body?.grant_type === 'refresh_token');
    expect(n).toBe(1);
    expect(refreshes.length).toBeGreaterThanOrEqual(2);
    expect(new Set(refreshes.map((x) => x.body.refresh_token))).toEqual(new Set(['refresh-1']));
    expect(c.state.session).toBe('active');
  });

  it('a 401 on refresh ends the session', async () => {
    const c = await started();
    const seen = notices(c);
    api.handle('POST /courier/token', () => problem(401, 'unauthenticated'));
    await tick(ACCESS_MS - 60_000);
    expect(c.state.session).toBe('ended');
    expect(seen).toEqual([{ kind: 'ended' }]);
    expect(geo.watching).toBe(false);
  });
});

describe('status taps', () => {
  it('sends a tap and resolves with the outcome', async () => {
    const c = await started();
    const r = await c.report('del_1', 'at_pickup');
    expect(r.outcome).toBe('applied');
    expect(r.delivery?.status).toBe('at_pickup');
    const ev = api.paths('/courier/deliveries/del_1/events');
    expect(ev).toHaveLength(1);
    expect(ev[0].body).toMatchObject({ type: 'at_pickup', occurred_at: new Date(T0).toISOString() });
    expect(typeof ev[0].body.event_id).toBe('string');
    expect(c.state.pendingTaps).toBe(0);
  });

  it('offline buffer drops fixes older than 10 min but keeps taps', async () => {
    const c = await started();
    api.offline = true;
    geo.fix(10, Date.now());
    const tap = c.report('del_1', 'at_pickup');
    await tick(5 * 60_000);
    geo.fix(10, Date.now());
    expect(c.state.buffered).toBe(2);
    await tick(6 * 60_000); // the first fix is now 11 min old
    expect(c.state.buffered).toBe(1);
    expect(c.state.pendingTaps).toBe(1);

    api.offline = false;
    await tick(3000);
    expect((await tap).outcome).toBe('applied');
    expect(api.paths('/courier/locations')[0].body.fixes).toHaveLength(1);
    expect(c.state.buffered).toBe(0);
  });

  it('taps replay in order before fixes when back online', async () => {
    const c = await started();
    api.offline = true;
    geo.fix(10);
    const a = c.report('del_1', 'at_pickup');
    const b = c.report('del_1', 'pickup_confirmed');
    await tick(3000);
    api.calls.length = 0;
    api.offline = false;
    await tick(3000);
    await Promise.all([a, b]);
    const order = api.calls.filter((x) => x.path !== '/courier/deliveries').map((x) => x.body?.type ?? x.path);
    expect(order).toEqual(['at_pickup', 'pickup_confirmed', '/courier/locations']);
  });

  it('same event_id on retry; second answer duplicate resolves applied-once', async () => {
    const c = await started();
    // the server gets the first request and applies it, but the answer is lost on the way back
    api.handle('POST /courier/deliveries/:id/events', () => {
      throw new TypeError('network');
    });
    let resolved = 0;
    const p = c.report('del_1', 'delivered').then((r) => {
      resolved++;
      return r;
    });
    await tick(0);
    expect(c.state.pendingTaps).toBe(1);
    expect(resolved).toBe(0);

    api.handle('POST /courier/deliveries/:id/events', () =>
      json(200, { outcome: 'duplicate', delivery: delivery('del_1', { status: 'delivered', next_actions: [] }) }),
    );
    await tick(3000);
    const r = await p;
    expect(r.outcome).toBe('duplicate');
    expect(r.delivery?.status).toBe('delivered');
    const ids = api.paths('/courier/deliveries/del_1/events').map((x) => x.body.event_id);
    expect(ids).toHaveLength(2);
    expect(ids[0]).toBe(ids[1]);
    await tick(10_000);
    expect(resolved).toBe(1);
    expect(api.paths('/courier/deliveries/del_1/events')).toHaveLength(2);
  });

  it('a double tap makes one event', async () => {
    const c = await started();
    api.offline = true;
    const a = c.report('del_1', 'at_pickup');
    const b = c.report('del_1', 'at_pickup');
    expect(c.state.pendingTaps).toBe(1);
    api.offline = false;
    await tick(3000);
    expect((await a).outcome).toBe('applied');
    expect((await b).outcome).toBe('applied');
    expect(api.paths('/courier/deliveries/del_1/events')).toHaveLength(1);
  });

  it('404 on tap → not_yours + reassigned notice', async () => {
    const c = await started();
    const seen = notices(c);
    api.handle('POST /courier/deliveries/:id/events', () => problem(404, 'not_found'));
    api.deliveries = [];
    const r = await c.report('del_1', 'at_pickup');
    expect(r).toEqual({ outcome: 'not_yours' });
    expect(seen).toEqual([{ kind: 'reassigned', deliveryId: 'del_1' }]);
    expect(c.state.pendingTaps).toBe(0);
    await tick(0);
    expect(c.deliveries).toHaveLength(0); // the poll that follows removes the card
    expect(c.state.session).toBe('active');
  });

  it('a refused tap does not block the ones behind it', async () => {
    const c = await started();
    api.offline = true;
    const bad = c.report('del_1', 'failed', { reason: 'x' }).catch((e) => e);
    const good = c.report('del_1', 'at_pickup');
    api.offline = false;
    api.handle('POST /courier/deliveries/:id/events', () => problem(400, 'validation_failed'));
    await tick(3000);
    expect(await bad).toBeInstanceOf(CourierError);
    expect((await good).outcome).toBe('applied');
  });

  it('an invalid transition resolves conflict', async () => {
    const c = await started();
    api.handle('POST /courier/deliveries/:id/events', () => problem(409, 'invalid_transition'));
    expect((await c.report('del_1', 'delivered')).outcome).toBe('conflict');
  });

  it('survives a reload: an unsent tap goes out from the buffer', async () => {
    const first = await started();
    api.offline = true;
    void first.report('del_1', 'at_pickup');
    await tick(0);
    expect(first.state.pendingTaps).toBe(1);
    await idbSettle();

    api.offline = false;
    await started();
    await tick(0);
    expect(api.paths('/courier/deliveries/del_1/events')).toHaveLength(1);
  });
});

describe('rate limits', () => {
  it('honours Retry-After on locations', async () => {
    await started();
    api.handle('POST /courier/locations', () => problem(429, 'rate_limited', { 'Retry-After': '20' }));
    geo.fix(10);
    await tick(3000);
    expect(api.paths('/courier/locations')).toHaveLength(1);
    await tick(15_000); // 18 s in: still inside the 20 s the server asked for
    expect(api.paths('/courier/locations')).toHaveLength(1);
    await tick(6000);
    expect(api.paths('/courier/locations')).toHaveLength(2);
  });
});

/** Let the fire-and-forget IndexedDB writes land (the first courier's store is not reachable from the test). */
const idbSettle = async () => {
  for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r));
};

describe('review fixes', () => {
  it('a refused tap with nobody waiting (restored after a reload) raises a rejected notice', async () => {
    const first = await started();
    api.offline = true;
    void first.report('del_1', 'delivered');
    await tick(0);
    await idbSettle();

    api.offline = false;
    api.handle('POST /courier/deliveries/:id/events', () => problem(409, 'invalid_transition'));
    const second = make();
    const seen = notices(second);
    await second.start();
    await tick(0);
    expect(seen).toEqual([{ kind: 'rejected', deliveryId: 'del_1', type: 'delivered' }]);
    expect(second.state.pendingTaps).toBe(0);
  });

  it('a 400 on a restored tap also raises rejected', async () => {
    const first = await started();
    api.offline = true;
    void first.report('del_1', 'failed', { reason: 'x' });
    await tick(0);
    await idbSettle();
    api.offline = false;
    api.handle('POST /courier/deliveries/:id/events', () => problem(400, 'validation_failed'));
    const second = make();
    const seen = notices(second);
    await second.start();
    await tick(0);
    expect(seen).toEqual([{ kind: 'rejected', deliveryId: 'del_1', type: 'failed' }]);
  });

  it('end() does not wait for the network once the access token has expired', async () => {
    const c = await started();
    api.offline = true;
    await tick(ACCESS_MS + 60_000); // the refresh loop is retrying and the token is dead
    expect(c.state.network).toBe('offline');
    await c.end(); // would hang on the refresh before the fix
    expect(c.state.session).toBe('ended');
    expect(storage.data.size).toBe(0);
    expect(geo.watching).toBe(false);
  });

  it('end() gives up on a stalled farewell after a few seconds', async () => {
    const c = await started();
    api.handle('POST /courier/session/end', () => new Promise<Response>(() => {}));
    const done = c.end();
    await tick(5000);
    await done;
    expect(c.state.session).toBe('ended');
    expect(storage.data.size).toBe(0);
  });

  it.each([
    [429, 'rate_limited'],
    [503, 'unavailable'],
  ])('a %i on the code exchange leaves start() retryable', async (status, code) => {
    api.handle('POST /courier/token', () => problem(status, code, { 'Retry-After': '1' }));
    const c = make();
    await expect(c.start()).rejects.toBeInstanceOf(CourierError);
    expect(c.state.session).toBe('starting');
    await c.start(); // the same instance, a second try
    expect(c.state.session).toBe('active');
  });

  it('leftover fixes and taps from an older shift are dropped when a new code is exchanged', async () => {
    let tabDead = false; // the old tab is gone: its timers must not send anything
    const first = await started({ fetch: ((...a) => (tabDead ? Promise.reject(new TypeError('x')) : api.fetch(...a))) as typeof fetch });
    api.offline = true;
    geo.fix(10);
    void first.report('del_1', 'at_pickup');
    await tick(0);
    await idbSettle();
    tabDead = true;
    api.offline = false;
    api.calls.length = 0;

    const second = await started({ linkCode: 'code-2' });
    await tick(3000);
    expect(second.state.buffered).toBe(0);
    expect(second.state.pendingTaps).toBe(0);
    expect(api.paths('/courier/deliveries/del_1/events')).toHaveLength(0);
    expect(api.paths('/courier/locations')).toHaveLength(0);
  });

  it('stores a fingerprint of the code and of the first refresh token, not the secret', async () => {
    await started();
    let rec = JSON.parse(storage.data.get('dt.courier.session')!);
    expect(rec.origin).toMatch(/^[0-9a-f]{64}$/);
    expect(storage.data.get('dt.courier.session')).not.toContain('code-1');

    storage.data.clear();
    const tokens = api.tokens();
    await started({ linkCode: undefined, tokens });
    rec = JSON.parse(storage.data.get('dt.courier.session')!);
    expect(rec.origin).toMatch(/^[0-9a-f]{64}$/);
    expect(rec.origin).not.toBe(tokens.refresh_token);
    // and the same pair given again after a reload still resumes instead of reusing the old token
    const before = api.calls.length;
    await started({ linkCode: undefined, tokens });
    expect(api.calls.slice(before).filter((c) => c.path === '/courier/token')).toHaveLength(0);
  });

  it('report() before start() rejects with not_started', async () => {
    const c = make();
    await expect(c.report('del_1', 'at_pickup')).rejects.toMatchObject({ code: 'not_started' });
    expect(api.calls).toHaveLength(0);
  });
});
