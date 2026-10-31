import { describe, expect, it } from 'vitest';
import { FIXTURE_PATH, FileSource } from './fixture';
import { openPmtiles } from './pmtiles';

describe('pmtiles source', () => {
  it('reads a decompressed z14 tile from the fixture', async () => {
    const s = await openPmtiles(FIXTURE_PATH, { source: new FileSource(FIXTURE_PATH) });
    expect(s.zoom).toBe(14);
    // Centre of the fixture bbox.
    const n = 2 ** 14;
    const x = Math.floor(((4.895 + 180) / 360) * n);
    const y = Math.floor(((1 - Math.asinh(Math.tan((52.365 * Math.PI) / 180)) / Math.PI) / 2) * n);
    const buf = await s.get(14, x, y);
    expect(buf).not.toBeNull();
    expect(buf!.byteLength).toBeGreaterThan(100);
    // Not gzip any more.
    const head = new Uint8Array(buf!, 0, 2);
    expect(head[0] === 0x1f && head[1] === 0x8b).toBe(false);
  });

  it('returns null for a tile that is not in the archive', async () => {
    const s = await openPmtiles(FIXTURE_PATH, { source: new FileSource(FIXTURE_PATH) });
    expect(await s.get(14, 0, 0)).toBeNull();
  });
});
