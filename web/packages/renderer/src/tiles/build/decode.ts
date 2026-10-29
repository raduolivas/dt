import { VectorTile } from '@mapbox/vector-tile';
import { PbfReader } from 'pbf';
import type { LatLng } from '@dt/contract';
import { GeoProjection } from '../../engine/geo';
import type { TileXY } from '../corridor';
import { buildParks, buildLanduse, buildWater, type AreaBuffers, type ParkBuffers } from './areas';
import { buildBuildings, type BuildingBuffers } from './buildings';
import { makeProjector, paletteFor, type Feature, type TileTheme } from './common';
import { buildRoads, type RoadBuffers } from './roads';

export type { TileTheme } from './common';
export type { AreaBuffers, ParkBuffers, BuildingBuffers, RoadBuffers };

export interface BuildRequest {
  tile: TileXY;
  data: ArrayBuffer;
  /** Scene origin (the pickup); geometry is returned in metres around it. */
  origin: LatLng;
  theme: TileTheme;
}

export interface BuildLayers {
  roads: RoadBuffers;
  buildings: BuildingBuffers;
  water: AreaBuffers;
  parks: ParkBuffers;
  landuse: AreaBuffers;
}

export interface BuildResult {
  tile: TileXY;
  layers: BuildLayers;
}

/** Messages between the renderer and the tile worker. */
export type WorkerIn = { type: 'build'; id: number; tile: TileXY; data: ArrayBuffer; origin: LatLng; theme: TileTheme };
export type WorkerOut =
  | { type: 'built'; id: number; layers: BuildLayers }
  | { type: 'error'; id: number; message: string };

const PARK_LANDUSE = new Set(['park', 'garden', 'pitch', 'playground', 'cemetery', 'recreation_ground', 'village_green']);
const GREEN_LANDCOVER = new Set(['grass', 'wood', 'forest', 'scrub', 'farmland']);

/** Pure: decodes one OpenMapTiles MVT tile into geometry buffers. The worker is a thin adapter around it. */
export function decodeTile(req: BuildRequest): BuildResult {
  const vt = new VectorTile(new PbfReader(req.data));
  const geo = new GeoProjection(req.origin);
  const palette = paletteFor(req.theme);

  const read = (name: string, keep: (f: Feature) => boolean = () => true): { feats: Feature[]; extent: number } => {
    const layer = vt.layers[name];
    const feats: Feature[] = [];
    if (!layer) return { feats, extent: 4096 };
    for (let i = 0; i < layer.length; i++) {
      const vf = layer.feature(i);
      const f: Feature = {
        id: vf.id,
        props: vf.properties as Feature['props'],
        rings: vf.loadGeometry().map((r) => r.map((p) => ({ x: p.x, y: p.y }))),
      };
      if (keep(f)) feats.push(f);
    }
    return { feats, extent: layer.extent };
  };
  const proj = (extent: number) => makeProjector(req.tile, extent, geo);

  const roads = read('transportation');
  const buildings = read('building');
  const water = read('water');
  const park = read('park');
  const landcover = read('landcover', (f) => GREEN_LANDCOVER.has(String(f.props.class ?? '')));
  const landuseGreen = read('landuse', (f) => PARK_LANDUSE.has(String(f.props.class ?? '')));
  const landuse = read('landuse', (f) => !PARK_LANDUSE.has(String(f.props.class ?? '')));

  const parkLayers = [park, landcover, landuseGreen].map((l) => buildParks(l.feats, proj(l.extent), palette));
  return {
    tile: req.tile,
    layers: {
      roads: buildRoads(roads.feats, proj(roads.extent), palette),
      buildings: buildBuildings(buildings.feats, proj(buildings.extent), palette),
      water: buildWater(water.feats, proj(water.extent), palette),
      parks: mergeParks(parkLayers),
      landuse: buildLanduse(landuse.feats, proj(landuse.extent), palette),
    },
  };
}

function mergeParks(parts: ParkBuffers[]): ParkBuffers {
  const cat = <T extends Float32Array | Uint32Array>(arrs: T[], ctor: new (n: number) => T, shift?: number[]): T => {
    const out = new ctor(arrs.reduce((s, a) => s + a.length, 0));
    let o = 0;
    arrs.forEach((a, i) => {
      for (let k = 0; k < a.length; k++) out[o + k] = (shift ? a[k]! + shift[i]! : a[k]!) as T[number];
      o += a.length;
    });
    return out;
  };
  const offsets: number[] = [];
  let v = 0;
  for (const p of parts) {
    offsets.push(v);
    v += p.positions.length / 3;
  }
  return {
    positions: cat(parts.map((p) => p.positions), Float32Array),
    normals: cat(parts.map((p) => p.normals), Float32Array),
    colors: cat(parts.map((p) => p.colors), Float32Array),
    indices: cat(parts.map((p) => p.indices), Uint32Array, offsets),
    trees: cat(parts.map((p) => p.trees), Float32Array),
  };
}

/** Every ArrayBuffer in a result, for the postMessage transfer list. */
export function transferList(r: BuildLayers): ArrayBuffer[] {
  const out: ArrayBuffer[] = [];
  for (const l of Object.values(r)) {
    for (const a of Object.values(l) as ArrayBufferView[]) out.push(a.buffer as ArrayBuffer);
  }
  return out;
}
