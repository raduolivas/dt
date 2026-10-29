import { VectorTile } from '@mapbox/vector-tile';
import { PbfReader } from 'pbf';
import type { LatLng } from '@dt/contract';
import { GeoProjection } from '../../engine/geo';
import type { TileXY } from '../corridor';
import { buildParks, buildLanduse, buildWater, quayTrees, type AreaBuffers, type ParkBuffers } from './areas';
import { buildBuildings, type BuildingBuffers, type FootprintBuffers } from './buildings';
import { insideRing, makeProjector, paletteFor, tileBounds, type Feature, type TileTheme } from './common';
import { buildRoads, type RoadBuffers } from './roads';

export type { TileTheme } from './common';
export type { AreaBuffers, ParkBuffers, BuildingBuffers, FootprintBuffers, RoadBuffers };

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
  /** The tile's raised block slab plus land use tints. */
  landuse: AreaBuffers;
  footprints: FootprintBuffers;
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

// Green land use (pitch and playground are tinted in the land use layer instead: lawns with no trees).
const PARK_LANDUSE = new Set(['park', 'garden', 'cemetery', 'recreation_ground', 'village_green']);
const GREEN_LANDCOVER = new Set(['grass', 'wood', 'forest', 'scrub']);
// The `park` layer also has protected areas (in Amsterdam the whole UNESCO canal ring): only real parks are green.
const PARKS = new Set(['park']);

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
  const park = read('park', (f) => PARKS.has(String(f.props.class ?? '')));
  const landcover = read('landcover', (f) => GREEN_LANDCOVER.has(String(f.props.class ?? '')));
  const landuseGreen = read('landuse', (f) => PARK_LANDUSE.has(String(f.props.class ?? '')));
  const landuse = read('landuse', (f) => !PARK_LANDUSE.has(String(f.props.class ?? '')));

  const bounds = tileBounds(proj(4096), 4096);
  const parkLayers = [park, landcover, landuseGreen].map((l) => buildParks(l.feats, proj(l.extent), palette, bounds));
  const { footprints, ...buildingMesh } = buildBuildings(buildings.feats, proj(buildings.extent), palette);
  const parks = mergeParks(parkLayers);
  const quay = quayTrees(water.feats, proj(water.extent), bounds, buildingTest(footprints));
  if (quay.length > 0) parks.trees = concat(parks.trees, quay);
  return {
    tile: req.tile,
    layers: {
      roads: buildRoads(roads.feats, proj(roads.extent), palette, bounds),
      buildings: buildingMesh,
      water: buildWater(water.feats, proj(water.extent), palette, bounds),
      parks,
      landuse: buildLanduse(landuse.feats, proj(landuse.extent), palette, bounds),
      footprints,
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

function concat(a: Float32Array, b: number[]): Float32Array {
  const out = new Float32Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

/** "Is (x, z) inside a building, or within 2 m of one?" over a 20 m grid of footprint boxes. */
function buildingTest(f: FootprintBuffers): (x: number, z: number) => boolean {
  const CELL = 20;
  const PAD = 2;
  const grid = new Map<string, number[]>();
  const rings: [number, number][][] = [];
  for (let r = 0; r < f.rings.length; r += 4) {
    if (f.rings[r + 3]) continue;
    const first = f.rings[r]!;
    const n = f.rings[r + 1]!;
    const ring: [number, number][] = [];
    let minX = Infinity;
    let minZ = Infinity;
    let maxX = -Infinity;
    let maxZ = -Infinity;
    for (let i = first; i < first + n; i++) {
      const x = f.coords[i * 2]!;
      const z = f.coords[i * 2 + 1]!;
      ring.push([x, z]);
      minX = Math.min(minX, x);
      minZ = Math.min(minZ, z);
      maxX = Math.max(maxX, x);
      maxZ = Math.max(maxZ, z);
    }
    const id = rings.length;
    rings.push(ring);
    for (let cx = Math.floor((minX - PAD) / CELL); cx <= Math.floor((maxX + PAD) / CELL); cx++) {
      for (let cz = Math.floor((minZ - PAD) / CELL); cz <= Math.floor((maxZ + PAD) / CELL); cz++) {
        const k = `${cx},${cz}`;
        const list = grid.get(k);
        if (list) list.push(id);
        else grid.set(k, [id]);
      }
    }
  }
  return (x, z) => {
    const list = grid.get(`${Math.floor(x / CELL)},${Math.floor(z / CELL)}`);
    if (!list) return false;
    for (const id of list) {
      const ring = rings[id]!;
      if (insideRing(ring, x, z)) return true;
      for (const [dx, dz] of [[PAD, 0], [-PAD, 0], [0, PAD], [0, -PAD]] as const) if (insideRing(ring, x + dx, z + dz)) return true;
    }
    return false;
  };
}
