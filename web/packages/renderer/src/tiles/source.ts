/** Where the map tiles come from. Absent means the procedural grid city (dev page only). */
export type TileSourceSpec = { kind: 'pmtiles'; url: string } | { kind: 'zxy'; tilejson: string };
