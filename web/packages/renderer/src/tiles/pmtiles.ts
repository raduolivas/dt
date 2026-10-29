import { PMTiles, type Source } from 'pmtiles';
import { MAX_TILE_ZOOM, type TileSource } from './source';

/** Opens a PMTiles archive (a single file read with HTTP range requests). `source` is for tests. */
export async function openPmtiles(url: string, opts: { source?: Source } = {}): Promise<TileSource> {
  const archive = opts.source ? new PMTiles(opts.source) : new PMTiles(url);
  const header = await archive.getHeader();
  return {
    zoom: Math.min(header.maxZoom, MAX_TILE_ZOOM),
    async get(z, x, y, signal) {
      const r = await archive.getZxy(z, x, y, signal);
      return r ? r.data : null;
    },
  };
}
