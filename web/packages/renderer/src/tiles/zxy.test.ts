import { describe, expect, it } from 'vitest';
import { openZxy, tileUrl } from './zxy';

const tilejson = {
  tiles: ['https://tiles.example/planet/20260101/{z}/{x}/{y}.pbf'],
  minzoom: 0,
  maxzoom: 14,
};

describe('zxy', () => {
  it('fills the z/x/y template', () => {
    expect(tileUrl(tilejson.tiles[0]!, 14, 8420, 5400)).toBe('https://tiles.example/planet/20260101/14/8420/5400.pbf');
  });

  it('opens a TileJSON, caps the zoom and fetches tiles', async () => {
    const calls: string[] = [];
    const fetchStub = (async (input: RequestInfo | URL) => {
      const url = String(input);
      calls.push(url);
      if (url.endsWith('planet')) return new Response(JSON.stringify(tilejson), { status: 200 });
      if (url.includes('/404/')) return new Response('', { status: 404 });
      return new Response(new Uint8Array([1, 2, 3]), { status: 200 });
    }) as typeof fetch;
    const s = await openZxy('https://tiles.example/planet', { fetch: fetchStub });
    expect(s.zoom).toBe(14);
    const buf = await s.get(14, 1, 2);
    expect(new Uint8Array(buf!)).toEqual(new Uint8Array([1, 2, 3]));
    expect(calls[1]).toBe('https://tiles.example/planet/20260101/14/1/2.pbf');
    expect(await s.get(14, 404, 2)).toBeNull();
  });

  it('rejects a TileJSON without tiles', async () => {
    const fetchStub = (async () => new Response('{}', { status: 200 })) as typeof fetch;
    await expect(openZxy('https://x/planet', { fetch: fetchStub })).rejects.toThrow();
  });
});
