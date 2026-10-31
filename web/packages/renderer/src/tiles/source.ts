import { openPmtiles } from './pmtiles';
import { openZxy } from './zxy';

/** Where the map tiles come from. Absent means the procedural grid city (dev page only). */
export type TileSourceSpec = { kind: 'pmtiles'; url: string } | { kind: 'zxy'; tilejson: string };

/** A source of raw (already decompressed) OpenMapTiles MVT tiles. */
export interface TileSource {
  /** The zoom level the renderer loads (the archive's maxzoom, capped at 14). */
  zoom: number;
  /** The tile bytes, or null when the tile does not exist. */
  get(z: number, x: number, y: number, signal?: AbortSignal): Promise<ArrayBuffer | null>;
}

/** The highest zoom the renderer uses: buildings are complete there and tiles stay small. */
export const MAX_TILE_ZOOM = 14;

export function openTileSource(spec: TileSourceSpec): Promise<TileSource> {
  return spec.kind === 'pmtiles' ? openPmtiles(spec.url) : openZxy(spec.tilejson);
}
