// Test helper: a file-backed PMTiles Source for Node, and the committed Amsterdam fixture.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { PMTiles, type RangeResponse, type Source } from 'pmtiles';

export const FIXTURE_PATH = resolve(import.meta.dirname, '../../fixtures/amsterdam-z14.pmtiles');

export class FileSource implements Source {
  private readonly buf: Buffer;
  constructor(private readonly path: string) {
    this.buf = readFileSync(path);
  }
  getKey(): string {
    return this.path;
  }
  async getBytes(offset: number, length: number): Promise<RangeResponse> {
    const b = this.buf.subarray(offset, offset + length);
    return { data: b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer };
  }
}

export function openFixture(): PMTiles {
  return new PMTiles(new FileSource(FIXTURE_PATH));
}

/** All z14 tiles of the fixture, with their raw (decompressed) bytes. */
export async function fixtureTiles(): Promise<{ z: number; x: number; y: number; data: ArrayBuffer }[]> {
  const p = openFixture();
  const h = await p.getHeader();
  const out: { z: number; x: number; y: number; data: ArrayBuffer }[] = [];
  const n = 2 ** 14;
  // Fixture bbox 4.86,52.35,4.93,52.38 spans a handful of tiles; scan a window around it.
  const x0 = Math.floor(((4.86 + 180) / 360) * n) - 1;
  const x1 = Math.floor(((4.93 + 180) / 360) * n) + 1;
  const lat = (la: number) => Math.floor(((1 - Math.asinh(Math.tan((la * Math.PI) / 180)) / Math.PI) / 2) * n);
  for (let x = x0; x <= x1; x++) {
    for (let y = lat(52.38) - 1; y <= lat(52.35) + 1; y++) {
      const r = await p.getZxy(h.maxZoom, x, y);
      if (r) out.push({ z: h.maxZoom, x, y, data: r.data });
    }
  }
  return out;
}
