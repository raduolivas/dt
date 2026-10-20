import { describe, expect, it } from 'vitest';
import { GeoProjection, haversineM } from './geo';

const ORIGIN = { lat: 52.3676, lng: 4.9041 };

describe('GeoProjection', () => {
  it('maps the origin to (0, 0)', () => {
    const p = new GeoProjection(ORIGIN).toLocal(ORIGIN);
    expect(p.x).toBeCloseTo(0, 9);
    expect(p.z).toBeCloseTo(0, 9);
  });
  it('round-trips a point 500 m away', () => {
    const proj = new GeoProjection(ORIGIN);
    const ll = proj.toLatLng({ x: 300, z: -400 });
    const back = proj.toLocal(ll);
    expect(back.x).toBeCloseTo(300, 6);
    expect(back.z).toBeCloseTo(-400, 6);
  });
  it('treats north as negative z', () => {
    const p = new GeoProjection(ORIGIN).toLocal({ lat: ORIGIN.lat + 0.001, lng: ORIGIN.lng });
    expect(p.z).toBeCloseTo(-111.319, 2);
  });
});

describe('haversineM', () => {
  it('measures 0.001 deg of latitude as about 111.32 m', () => {
    expect(haversineM(ORIGIN, { lat: ORIGIN.lat + 0.001, lng: ORIGIN.lng })).toBeCloseTo(111.319, 2);
  });
});
