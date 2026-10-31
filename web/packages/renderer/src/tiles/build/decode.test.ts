import { describe, expect, it } from 'vitest';
import { fixtureTiles } from '../fixture';
import { decodeTile, type BuildResult } from './decode';
import { buildBuildings, toyHeight } from './buildings';
import { tilePalette } from './common';

const origin = { lat: 52.3702, lng: 4.8952 };

let cached: Promise<BuildResult[]> | undefined;
function decodeAll(): Promise<BuildResult[]> {
  cached ??= decodeAllUncached();
  return cached;
}

async function decodeAllUncached(): Promise<BuildResult[]> {
  const tiles = await fixtureTiles();
  return tiles.map((t) => decodeTile({ tile: { z: t.z, x: t.x, y: t.y }, data: t.data, origin }));
}

describe('decodeTile', { timeout: 30_000 }, () => {
  it('finds roads, buildings, water and parks in the fixture', async () => {
    const all = await decodeAll();
    const sum = (f: (r: BuildResult) => number) => all.reduce((s, r) => s + f(r), 0);
    expect(sum((r) => r.layers.roads.indices.length)).toBeGreaterThan(0);
    expect(sum((r) => r.layers.buildings.indices.length)).toBeGreaterThan(0);
    expect(sum((r) => r.layers.water.indices.length)).toBeGreaterThan(0);
    expect(sum((r) => r.layers.parks.indices.length)).toBeGreaterThan(0);
    expect(sum((r) => r.layers.parks.trees.length)).toBeGreaterThan(0);
  });

  it('is deterministic: the same tile gives identical buffers (same tints)', async () => {
    const tiles = await fixtureTiles();
    const t = tiles[0]!;
    const run = () => decodeTile({ tile: { z: t.z, x: t.x, y: t.y }, data: t.data, origin });
    const a = run();
    const b = run();
    expect(Array.from(a.layers.buildings.colors)).toEqual(Array.from(b.layers.buildings.colors));
    expect(Array.from(a.layers.parks.trees)).toEqual(Array.from(b.layers.parks.trees));
  });

  it('outputs transferable typed arrays with consistent sizes', async () => {
    const all = await decodeAll();
    for (const r of all) {
      for (const layer of [r.layers.roads, r.layers.buildings, r.layers.water, r.layers.parks, r.layers.landuse]) {
        expect(layer.positions).toBeInstanceOf(Float32Array);
        expect(layer.normals).toBeInstanceOf(Float32Array);
        expect(layer.colors).toBeInstanceOf(Float32Array);
        expect(layer.indices).toBeInstanceOf(Uint32Array);
        expect(layer.normals.length).toBe(layer.positions.length);
        expect(layer.colors.length).toBe(layer.positions.length);
        const nv = layer.positions.length / 3;
        let max = -1;
        for (const i of layer.indices) if (i > max) max = i;
        expect(max).toBeLessThan(nv);
        expect(layer.positions.buffer).toBeInstanceOf(ArrayBuffer);
      }
    }
    const structured = structuredClone(all[0]!, { transfer: [all[0]!.layers.roads.positions.buffer] });
    expect(structured.layers.roads.positions).toBeInstanceOf(Float32Array);
  });

  it('places geometry in scene metres around the origin', async () => {
    const all = await decodeAll();
    for (const r of all) {
      let max = 0;
      for (let i = 0; i < r.layers.roads.positions.length; i += 3) max = Math.max(max, Math.abs(r.layers.roads.positions[i]!));
      expect(max).toBeLessThan(20000);
    }
  });
});

describe('buildings', () => {
  // A single square footprint 100x100 tile units, extent 4096, at z14 (about 36 m on a side).
  const square = [
    [
      { x: 0, y: 0 },
      { x: 100, y: 0 },
      { x: 100, y: 100 },
      { x: 0, y: 100 },
      { x: 0, y: 0 },
    ],
  ];
  const proj = { project: (px: number, py: number): [number, number] => [px * 0.37, py * 0.37] };
  const palette = tilePalette();

  // Wall vertices only (horizontal normals): the roof rim and rooftop units sit a little above the roof.
  const wallYs = (b: { positions: Float32Array; normals: Float32Array }) =>
    Array.from({ length: b.positions.length / 3 }, (_, i) => i)
      .filter((i) => Math.abs(b.normals[i * 3 + 1]!) < 1e-6)
      .map((i) => b.positions[i * 3 + 1]!);

  it('defaults the height to 9 m and respects render_min_height', () => {
    const ys = wallYs(buildBuildings([{ id: 1, props: {}, rings: square }], proj, palette));
    expect(Math.max(...ys)).toBeCloseTo(9);
    expect(Math.min(...ys)).toBeCloseTo(0);
    const ys2 = wallYs(buildBuildings([{ id: 1, props: { render_height: 20, render_min_height: 12 }, rings: square }], proj, palette));
    const top = toyHeight(20);
    expect(Math.max(...ys2)).toBeCloseTo(top);
    expect(Math.min(...ys2)).toBeCloseTo((12 * top) / 20); // the base scales with the toy height
  });

  it('soft caps tall buildings so the city stays toy sized', () => {
    expect(toyHeight(9)).toBe(9);
    expect(toyHeight(20)).toBeGreaterThan(13);
    expect(toyHeight(20)).toBeLessThan(16);
    expect(toyHeight(44)).toBeLessThan(21);
    expect(toyHeight(44)).toBeGreaterThan(toyHeight(20));
    expect(toyHeight(300)).toBeLessThan(22.01);
    const ys = wallYs(buildBuildings([{ id: 1, props: { render_height: 120 }, rings: square }], proj, palette));
    expect(Math.max(...ys)).toBeLessThan(22.01);
  });

  it('records one footprint per polygon with its vertex range', () => {
    const b = buildBuildings([{ id: 1, props: {}, rings: square }], proj, palette);
    expect(b.footprints.info.length).toBe(4);
    expect(b.footprints.info[0]).toBeCloseTo(9);
    expect(b.footprints.info[2]).toBe(0);
    expect(b.footprints.info[3]).toBe(b.positions.length / 3);
    expect(b.footprints.rings.length).toBe(4);
    expect(b.uvs.length).toBe((b.positions.length / 3) * 2);
  });

  it('keeps every building in its own vertex range, so hiding one hides only it', () => {
    const shifted = square.map((r) => r.map((p) => ({ x: p.x + 300, y: p.y })));
    const b = buildBuildings(
      [
        { id: 1, props: {}, rings: square },
        { id: 2, props: { render_height: 30 }, rings: shifted },
      ],
      proj,
      palette,
    );
    const info = b.footprints.info;
    expect(info.length).toBe(8);
    const ranges = [0, 1].map((k) => [info[k * 4 + 2]!, info[k * 4 + 2]! + info[k * 4 + 3]!] as const);
    expect(ranges[0]![0]).toBe(0);
    expect(ranges[0]![1]).toBe(ranges[1]![0]); // contiguous, back to back
    expect(ranges[1]![1]).toBe(b.positions.length / 3); // nothing emitted outside a building's range
    const owner = (v: number) => (v < ranges[0]![1] ? 0 : 1);
    for (let t = 0; t < b.indices.length; t += 3) {
      const o = owner(b.indices[t]!);
      expect(owner(b.indices[t + 1]!)).toBe(o);
      expect(owner(b.indices[t + 2]!)).toBe(o); // no triangle spans two buildings
    }
    // Every vertex of building 1 lies on its own footprint's side of the tile (x ≥ 300 units * 0.37).
    for (let v = ranges[1]![0]; v < ranges[1]![1]; v++) expect(b.positions[v * 3]!).toBeGreaterThan(300 * 0.37 - 1);
  });

  it('gives the same building the same tint in two runs', () => {
    const f = { id: 42, props: {}, rings: square };
    const a = buildBuildings([f], proj, palette);
    const b = buildBuildings([f], proj, palette);
    expect(Array.from(a.colors)).toEqual(Array.from(b.colors));
  });

  it('faces walls outward and roofs up', () => {
    const b = buildBuildings([{ id: 3, props: {}, rings: square }], proj, palette);
    for (let t = 0; t < b.indices.length; t += 3) {
      const [i, j, k] = [b.indices[t]!, b.indices[t + 1]!, b.indices[t + 2]!];
      const p = (n: number) => [b.positions[n * 3]!, b.positions[n * 3 + 1]!, b.positions[n * 3 + 2]!];
      const [a, bb, c] = [p(i), p(j), p(k)];
      const u = [bb[0]! - a[0]!, bb[1]! - a[1]!, bb[2]! - a[2]!];
      const v = [c[0]! - a[0]!, c[1]! - a[1]!, c[2]! - a[2]!];
      const n = [u[1]! * v[2]! - u[2]! * v[1]!, u[2]! * v[0]! - u[0]! * v[2]!, u[0]! * v[1]! - u[1]! * v[0]!];
      const vn = [b.normals[i * 3]!, b.normals[i * 3 + 1]!, b.normals[i * 3 + 2]!];
      expect(n[0]! * vn[0]! + n[1]! * vn[1]! + n[2]! * vn[2]!).toBeGreaterThan(0);
    }
  });
});
