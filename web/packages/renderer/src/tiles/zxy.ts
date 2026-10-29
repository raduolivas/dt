import { MAX_TILE_ZOOM, type TileSource } from './source';

interface TileJSON {
  tiles?: string[];
  maxzoom?: number;
}

export function tileUrl(template: string, z: number, x: number, y: number): string {
  return template.replace('{z}', String(z)).replace('{x}', String(x)).replace('{y}', String(y));
}

async function gunzipIfNeeded(buf: ArrayBuffer): Promise<ArrayBuffer> {
  const h = new Uint8Array(buf, 0, Math.min(2, buf.byteLength));
  if (h[0] !== 0x1f || h[1] !== 0x8b) return buf;
  const stream = new Blob([buf]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).arrayBuffer();
}

/** Opens a z/x/y vector tile endpoint described by a TileJSON (for example OpenFreeMap's planet). */
export async function openZxy(tilejson: string, opts: { fetch?: typeof fetch } = {}): Promise<TileSource> {
  const doFetch = opts.fetch ?? fetch.bind(globalThis);
  const res = await doFetch(tilejson);
  if (!res.ok) throw new Error(`tilejson ${tilejson}: ${res.status}`);
  const meta = (await res.json()) as TileJSON;
  const template = meta.tiles?.[0];
  if (!template) throw new Error(`tilejson ${tilejson}: no tiles template`);
  return {
    zoom: Math.min(meta.maxzoom ?? MAX_TILE_ZOOM, MAX_TILE_ZOOM),
    async get(z, x, y, signal) {
      const r = await doFetch(tileUrl(template, z, x, y), { signal });
      if (r.status === 404 || r.status === 204) return null;
      if (!r.ok) throw new Error(`tile ${z}/${x}/${y}: ${r.status}`);
      return gunzipIfNeeded(await r.arrayBuffer());
    },
  };
}
